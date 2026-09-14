# 聊天分支、DAG 历史与 Context Builder 实施方案

日期：2026-09-14。状态：供实现 Agent 执行的设计方案；本次只写计划，不修改产品代码。

本方案取代 `.hermes/plans/2026-09-12_055133-branch-merge-lineage-graft.md` 和 `2026-09-12_060500-branch-merge-turn-dag.md`。以本次用户要求为准：分支服务于并行工作和上下文隔离；合并后完整历史可供 context builder 使用；未合并的 open branch 在聊天底部分开显示，合并后进入目标时间线并按时间排序。

## 1. 产品约定

1. branch 是有稳定 ID 的上下文工作线，不绑定某个 agent。一个 branch 可以先后交给不同角色。
2. 普通 fork 继承选定历史节点及其祖先，此后父线和兄弟线的新内容不会自动进入该分支。空白分支没有聊天历史父节点，但仍保留 workspace、角色指令等明确的公共配置。
3. A 合入 B = B 的新 head 同时引用 B 和 A 的历史。之后从 B 构建上下文，能枚举 A、B 以及它们已经继承、合并的全部内容，共享祖先只出现一次。
4. A 从普通分支条、open branch 区和已归档列表消失；不生成摘要消息、不 dispatch、不唤醒 agent、不添加“已合并”聊天卡片。
5. “消失”是工作线关闭，不是删除历史。保留内部 branch ID、原始消息、turn、图边及合并记录，用于查询、迁移和排错。
6. 归档只关闭工作入口，不引入合并边，也不撤销已经存在的祖先关系。已有后代仍可读取其继承的历史。
7. 本期主线始终存在，只能作为合并目标；普通分支可以合入主线或另一条 open branch。合并不支持撤销；后续可从历史节点另开分支。
8. 聊天合并不等于 Git 合并：不自动处理文件、worktree 或代码冲突。上下文中的不同结论均保留，由后续 agent 判断。

## 2. 最关键的分层

```text
持久化 DAG + 原始消息/工具记录
          ↓  精确祖先闭包，按 ID 去重
HistorySnapshot（完整可分页读取的历史集合）
          ├── 聊天 UI：按时间展示
          └── Context Builder：摘要、筛选、预算、输出 prompt
```

“能拿到所有内容”保证在 HistorySnapshot 层成立；不承诺有限模型窗口一次容纳全部原文。builder 必须能读取每个可达 turn 的完整持久化内容，并报告哪些以原文、摘要表示，哪些因预算未装入本次 prompt。原始 provider 未提供或 Viewer 未保存的内容不在该保证之内。

自定义 builder 不应反查 branch 元数据拼历史，也不应该只能获得现有预算截断后的字符串。先定义 Go 接口，当前内置 builder 也使用同一接口；外部插件适配可后续增加。

## 3. 当前实现与必须修正的地方

依据本次读取的仓库代码：

| 位置 | 当前职责 | 实施重点 |
| --- | --- | --- |
| `internal/plugins/chat/store.go` | Branch 独立表、Turn 单父 `prev_turn_id`、消息关联 dispatch/turn | 增加图存储、head、快照与覆盖记录 |
| `internal/plugins/chat/lineage.go` | 按 branch 递归 fork/merge，构建 context/bridge | 用节点祖先闭包替换 branch 递归 |
| `internal/plugins/chat/merge.go` | LLM 草稿、merge-confirm、摘要消息与归档 | 改为单步原子合并 |
| `internal/plugins/chat/runtime.go` | 路由、队列、session 复用、turn 生命周期 | 固定输入快照、分支并行、完成后发布 head |
| `internal/plugins/chat/summary.go`、`hindsight.go` | 摘要、chat 级记忆检索 | 摘要覆盖校验、检索的历史范围隔离 |
| `internal/plugins/chat/automation.go` | 自动 dispatch、gate/lease | 和合并共用接入检查，避免关闭分支仍被自动提交 |
| `frontend/src/plugins/chat/ChatPane.vue` | 分支选择、可见性、合并草稿与卡片 | 后端提供成员集合，分支区和合并后时间线 |

现实现存在的设计风险，应通过回归测试落实：

- `lineageTurns` 按 branch ID 标记 visited，却可能沿不同路径带不同 cutoff 到达同一 branch；不能用它作为未来精确 DAG 的权威。
- 按消息时间判断“while you were away”，会漏掉刚合并进来但时间较老的源分支消息；同角色在另一 session 的工作也不能按 role ID 一概排除。
- 用所有 turn 的最新摘要时间裁掉 raw tail，可能漏掉另一条分支更早但尚未被摘要覆盖的消息。
- Hindsight 当前按 chat bank 检索。仅修正 SQL lineage，仍不足以保证分支隔离。
- 旧 plan 从“某线所有 turn + 时间 cutoff”起步，不是真正固定的图快照；按 branch 可达集合过滤 UI 也会把 fork 点之后的消息带进来。

## 4. 图模型：历史节点与执行 turn 分离

建议增加 `history_nodes`、`history_edges`、`line_heads`，保留现有 `turns` 作为真实 agent 执行记录。不要把 fork/merge marker 伪装成 agent turn，避免污染运行状态、摘要任务、统计和 loop。

### 4.1 存储契约

| 表 | 必要字段及约束 |
| --- | --- |
| `history_nodes` | `id`、`chat_id`、`kind`（turn/fork/merge/join）、`origin_branch_id`、可空唯一 `turn_id`、`created_at`、chat 内单调 `seq` |
| `history_edges` | `child_id`、`parent_id` 联合唯一，`kind`（sequence/fork/merge/join）；正反向索引 |
| `line_heads` | `(chat_id, branch_id)` 联合唯一、`head_node_id`、`revision`；主线仍用空 branch ID，必须有独立 head 行 |
| `branches` 扩展 | 显式 `state=open/archived/merged`、`merged_at`、`merged_into_branch_id`、`merge_node_id`；旧字段暂保留兼容 |
| dispatch 输入快照 | dispatch/批次 ID、branch ID、接入时 head、revision；队列记录与实际执行快照分别保存 |
| context 构建记录 | session/dispatch ID、snapshot head、builder 版本、原文/摘要覆盖与未装入集合（可归一化成关联表） |

边统一表示“子节点依赖父节点”。同 chat、无自环；新图只允许新节点引用已经提交的节点，因此自然无环。ID 和 seq 是稳定排序/诊断依据，时间戳不承担可达性判断。

逻辑 head 更新、节点与边写入必须在同一事务；用 revision CAS 防止丢更新。branch 生命周期是操作权限的权威，图边是历史可见性的权威，两者在同一事务维护。

### 4.2 各类节点

- **turn**：引用一个真实执行 turn，只在该 turn 终止、最终消息持久化后进入已发布历史。正常完成、失败、取消均可保留已有内容及终态。
- **fork**：普通 fork 指向选定节点；fresh fork 无父节点。空 branch 也立即有 head，未发第一条消息时即可读取继承历史。
- **merge**：父节点为目标旧 head 与各源 head；它成为目标新 head，本身没有聊天正文。
- **join**：同一工作批次多个 agent 并发结束后汇集结果，避免最后完成的一个覆盖其他结果；不作为聊天消息显示。

turn 的原始 branch 归属不因合并而改写。UI 的显示归属来自目标 head 的可达集合。

### 4.3 快照与遍历

`Snapshot(head)` = 从给定 head 沿父边访问到的所有节点，按节点 ID 去重，投影出真实 turns 和对应消息。不能从整条 branch 的所有 turn 开始扫描，也不能事后按时间推断“当时可见”。

fork 到历史 turn 是从该节点的精确祖先开分支，不包含其后发生的 merge；从“当前分支状态”开分支则选当前 head（可能是 merge/join marker）。UI 应区分这两个入口。

例：B 在 t1 开出 C，t2 才把 A 合入 B。B 的当前快照包含 A；C 不包含 A。即使 A 的消息早于 t1，也不能穿透到 C。

只读遍历发现悬空边、跨 chat 边或环时返回明确错误，不能静默生成残缺上下文。大图使用批量查询/递归 CTE、稳定分页和索引，避免逐节点查库及一次返回完整 blocks。

## 5. 并行与提交规则

第一期采用“跨分支并行，分支内按 dispatch 批次排序”。一个 dispatch 若路由到多个角色，仍可批次内并行，所有角色拿到相同输入 head；下一批等当前批次收束后再启动。这会收紧现有同线不同角色跨 dispatch 重叠的行为，需在队列 UI 与测试中体现。

1. 接入一批任务时，原子保存输入 head；每个角色的 prompt 构建都基于该快照，当前用户输入另行传入。
2. 运行中内容照常流式显示，但不进入其他任务的稳定历史快照。一个角色不能偶然读到另一个尚未完成的工具输出。
3. 每个真实 turn 终止后记录其结果节点，父节点指向该批次输入 head；全部结果收束后，用一个 join（单结果可直接用 turn 节点）更新分支 head。
4. crash recovery 必须将遗留批次恢复或终结为可解释的状态，并幂等发布结果；失败/取消不能使后续队列永远挂住。
5. session 隔离至少包括 chat、branch、role 和现有 provider/runtime 身份；不得复用兄弟分支 session。同一个 provider session 不能同时承载两次 prompt。
6. fork 仅引用已发布节点。未完成批次不能作为稳定 fork 点；等待它完成后再 fork。

合并第一期只允许源与目标均无运行批次、无待执行队列、无能继续提交的自动化任务。loop/lease 活跃时返回可操作的 busy 原因，用户先暂停并清空/完成任务再合并，不自动停止 agent。

dispatch 接入、自动提交与 merge 必须共用并发保护和事务校验；不能只在事务前检查一次 busy。无关分支继续并行运行。

## 6. 单步合并契约

建议沿用 `chat:_:branches:merge`，明确改变其协议语义：

```json
{
  "chat_id": "…",
  "branch_ids": ["A", "C"],
  "target_branch_id": "",
  "expected_revisions": {"": 12, "A": 4, "C": 7},
  "idempotency_key": "…"
}
```

事务内校验同 chat、全部 open、源不含目标、目标主线或合法分支、全部空闲、revision 匹配。空分支可以合并（至少有 fork 节点）；共享历史由图遍历去重。

同一事务写 merge 节点与所有父边、更新目标 head/revision、将源置 merged、保存幂等回执。重试同 key 同参数返回原结果；同 key 不同参数拒绝。任何失败均不产生半合并。

成功返回 merge 节点、目标 head/revision、源 ID 集合；提交后发布一次可供重载的变更通知。断线客户端用 revision 重新拉取权威状态。

旧 `merge-confirm` 明确返回协议升级错误，不再执行摘要 dispatch。删除旧草稿路径；新客户端能力协商失败时提示刷新/升级，避免旧前端把新合并 RPC 当“生成草稿”调用而直接合并。

源 branch 的后代保持 open，仍持有原来的 fork 快照，不随源合并而自动关闭或接收目标未来历史。从合并后目标时间线中的旧 turn 再 fork 允许，只校验节点属于当前目标的可达快照，不因其 origin branch 已 merged 而拒绝。

## 7. Context Builder 契约与 session 补齐

建议抽象以下 Go 层能力（名称可按仓库风格调整，语义必须保持）：

```text
ResolveSnapshot(chatID, branchID | headNodeID) -> snapshot
ListHistory(snapshot, cursor, limit) -> turn/message references
ReadContent(snapshot, references, cursor, limit) -> messages/blocks
BuildContext(snapshot, query, budget, sessionCoverage) -> prompt + coverage
```

所有分页绑定相同 snapshot；消息按 message ID 去重。特别处理当前 user message 的 `turn_id` 实际可能是 dispatch ID：同一输入路由到多个角色时只展示、装入一次。尚未开始执行的排队消息作为 UI 的待处理层，不能提前污染稳定历史。

builder 可选择正文、工具摘要或详细 blocks，但必须保留读取原始持久化内容的能力，不能在 history 层先丢弃工具信息。返回覆盖清单，区分“已作为原文注入”“被某摘要覆盖”“预算未装入”，不能把后者标记为 session 已读。

### 摘要

摘要必须声明覆盖的 turn/message ID 或可验证的集合引用。只有实际被选入 prompt 的摘要才可替代它覆盖的原文；不再用一个全局最新摘要时间排除旧消息。历史摘要缺少精确覆盖时，仅保守认定其明确关联的 turn，并保留未证明覆盖的正文。

### 合并后的旧 session

首期建议：目标线合并成功后，标记其已有 session 在下次运行前需要重建；下次发送启动新 session，从新的完整 snapshot 构建上下文。这是默认实现策略，合并本身不启动 session。所有目标角色都适用，source sessions 不搬到目标线。

这样无需依赖旧 session 的最后活动时间来猜测已知内容。若未来优化为增量 bridge，必须按实际 session 的 coverage 集合求差，并对摘要变化、重试、compaction 和预算未装入项做处理；不能用时间戳或同 role 过滤代替。session 无法确认完整接收时，不提前提交 coverage。

### 检索隔离

Hindsight/其他记忆检索结果只有在其来源能证明属于 snapshot 可达消息集合时才可注入。没有来源 ID、混合来源无法拆分或旧记忆无法验证时，首期跳过该结果；必要时停用该快照的外部 recall，保留 SQLite 历史构建。不要把 chat bank 的全局检索结果直接接到隔离上下文后面。

## 8. 聊天页面

首期将用户的“下面分开显示”落实为：保留聊天底部的分支区，每条 open branch 独立入口、运行/排队状态与发送目标；选择后查看该线时间线。保留已有多选联合查看能力，不新增所有分支同时展开的多列聊天布局。

- 主线和各 open branch 保持独立。切换查看不会改变图，也不会隐式合并。
- 普通 fork 显示其继承历史和自身后续消息；共享历史在单个视图里只显示一次。
- 多选联合查看只做可达 turn/message ID 的并集；发送仍必须明确指定唯一目标，不能靠选择顺序猜测。
- 合并操作明确选择目标，并用一次确认说明哪些工作线将关闭；无摘要编辑步骤。
- 合并成功移除源入口，切换到目标。目标时间线把所有可达消息按 `(created_at, message_id)` 升序混排；turn 内 blocks 保持原来的协议顺序，不按接收时钟重排。
- DAG 负责因果关系，时间排序只负责阅读顺序；agent 执行卡片可按 turn 开始时间定位。相同时间使用稳定 ID 打破平局。
- 不显示 merge/fork/join 聊天 box、来源 tag、已合并卡片或额外分隔行。内部来源元数据保留，供未来可选图检查器使用。
- 合并导致较早消息插入当前时间线时，以 message ID 保持滚动锚点，不强制跳底。
- localStorage 恢复到已 merged 的分支时，解析到最终 open 合并目标；目标已归档则回主线。多级合并需支持传递解析。

前端不要自己写第二套 branch 递归。新增分页 `history` 查询返回 snapshot、消息与 cursor；已有增量 blocks 流继续使用，流式内容作为稳定历史上方的运行态覆盖层。

可另增 `branches:graph` 返回节点/边元数据供调试及未来 DAG 视图，必须分页或支持 revision 增量；不得要求聊天首屏一次下载全图或所有 blocks。

## 9. 旧数据迁移

不能简单把每条 `prev_turn_id` 原样变成新权威图：它可能连接 session 的上一 turn，而不是完整工作线 head。迁移必须重建分支事件序列，并验证覆盖范围。

1. 先只读扫描、统计和备份数据库；输出有界迁移报告。实现版本化、幂等迁移，失败回滚，保留旧列和原消息。
2. 依据 turn 所属线、fork 点、已知合并锚点及稳定时间/ID 重建历史；旧记录无法还原真实并发输入快照时，明确标记 legacy 推断，不伪称精确因果。
3. 为每条旧线创建 head；空分支创建 fork 节点。主线也显式创建 head 记录。
4. 已有摘要合并按 `merge_message_id`、`merged_through_turn_id` 等证据，在历史合并位置插入 merge 节点，并让目标后续节点引用它；不能只在今天的最终 head 补一个 merge，破坏旧 fork 的可见范围。
5. 区分普通 archived 与旧 merged。`merged_into_branch_id` 为空本身不能判定是否合并，因为主线 ID 也是空。
6. 旧合并摘要消息属于真实历史，保留为普通历史消息；只去掉旧 UI 卡片。迁移不触发新的 dispatch、摘要或模型调用。
7. 对孤立 user message、无真实 turn 的失败 dispatch、取消队列记录制定逐类映射：已执行/已失败的历史输入不可无声丢弃；必要时增加无 agent 输出的历史输入节点类型，并在协议与测试中明确。纯取消且按旧行为已删除的队列输入不复活。
8. 无法判断合并位置、存在缺失 fork 引用或图环时，报告精确记录 ID；不静默猜测或丢弃。支持修复数据后重新运行迁移。
9. 迁移后验证所有原始记录仍可寻址、预期 fork/merge 集合正确、无环、边不跨 chat、head 有效。完成后切换图读路径，不长期并存两套权威。

## 10. 实施阶段与交付

按顺序交付，阶段之间可 code review；这些是实现任务划分，本次不启动其他 Agent。

1. **契约与存储**：先固定上述 schema、RPC 和输入快照时机，新增图存储/迁移模块（建议 `history_graph.go`、`history_migration.go`）。迁移测试通过后再接运行路径。
2. **执行与历史服务**：runtime 批次隔离、完成发布 head、异常恢复；实现快照遍历及分页内容读取，用它替换 `lineage.go` 的 branch 递归。
3. **Context 正确性**：摘要覆盖、预算报告、检索来源过滤、合并后 session 重建。用 fake agent 捕获实际 prompt 验证，不能只测集合 helper。
4. **合并与 UI**：原子 merge、兼容性拒绝、事件同步、源入口移除、底部分支区、目标时间线、滚动与本地状态恢复。删除 LLM 草稿/confirm/card 路径。
5. **收尾**：更新前后端 types、`docs/plugin-framework.md` 和必要的协议文档、`architecture.md`；文档版本以实施时仓库版本递增，不写死旧 plan 的 v0.70。

实施前检查工作区已有变更。本次发现 `architecture.md` 和 LLM 插件相关文件已有其他工作未提交；不得覆盖、回滚或混入不相关修改。

## 11. 验收标准

| 场景 | 必须满足 |
| --- | --- |
| fork 后两线各自继续 | 分支仅继承 fork 点以前的精确祖先 |
| fresh 分支 | 无聊天历史，公共角色配置仍有效 |
| A、B 并行跑不同 agent | session 不串线、稳定上下文不含兄弟未合并内容 |
| 同批多角色、完成顺序颠倒 | 共用输入快照；所有结果进入 head，无覆盖丢失 |
| A→B，再 B→主线 | 主线完整可达 A/B，多个共享祖先只出现一次 |
| 菱形路径/相同时间戳 | 不遗漏、不重复，不依赖时间窗口猜成员 |
| 源消息很旧，目标 session 很新 | 合并后下一次 prompt 仍获取源内容；预算省略有记录 |
| 一线有新摘要，另一线旧消息未摘要 | 未摘要内容仍进入候选集合 |
| recall 命中兄弟线或混合来源 | 不注入隔离上下文 |
| 合并后从较早 fork 的 C 继续 | C 不追溯吸收之后才发生的合并 |
| 源有 open 后代 / 后来归档祖先 | 后代保持原快照，可继续工作 |
| 合并时 running/queued/loop 接入竞态 | merge 或 dispatch 明确失败，不接收半关闭状态 |
| 双击 merge、断线重试、并发 head 更新 | 一次生效，幂等回执或 revision conflict |
| 一个 dispatch 多角色 / 无输出失败 | 用户输入恰好出现一次，已持久化历史不丢 |
| UI 合并后刷新或重开 pane | 源不出现在 open/archived 区，目标时间顺序正确 |
| 大历史分页与流式更新 | 不重复/跳页，快照固定，滚动锚点稳定 |
| 迁移重复运行/中途失败 | 数据不丢、可回滚重试、旧合并无重复 dispatch |

实现完成运行：

```sh
gofmt -l .
go build ./...
go test ./...
cd frontend && npx vue-tsc --noEmit && npm run build
```

另在仓库根运行 `bash scripts/smoke_all.sh`，新增与 DAG/merge/session 重建相关的黑盒场景。不要启动前端 dev server。服务重启仅在用户明确要求时执行 `POST /api/admin/restart`，不调用 systemctl。

本次计划交付不需要构建、迁移数据库、提交代码或重启服务。
