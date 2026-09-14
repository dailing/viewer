# 分支合并重定义 + turn 级 DAG 物化 Plan（supersedes 2026-09-12_055133）

**Goal**：把 chat 分支建成一个真正可查询、可渲染的 DAG——①合并重定义为纯血统融合（源分支消失、无摘要消息、无 LLM、不归档）；②turn 的父链从单父（`prev_turn_id`）泛化为多父边表，fork/merge 都是图上的一等边，history/context/渲染统一从图推导。

**Architecture**：节点 = turn（含两种 marker 节点）；车道 = branch（独立表、稳定 id，已有）；边 = 新表 `turn_parents(turn_id, parent_turn_id, kind)`，kind ∈ sequence / fork / merge。线的 history 与 agent context = 从该线的 turn 集合沿边做 DAG 祖先遍历（去重、时间排序、cutoff 截断），fork 与 merge 不再特判。

**Tech Stack**：Go（chat 插件）、sqlite（gorm 自动迁移）、Vue/TS（ChatPane）。

---

## 0. 现状（存储回答）

- **分支已是独立 table**：`chat.sqlite3` 的 `branches`——固定 hex `id`（name 只是显示名、可重复），已有 `fork_turn_id`/`parent_branch_id`（出生边）和 `merged_into_branch_id`/`merged_through_turn_id`（合并边字段）。图的骨架已存在。
- **turn 单父链**：`turns.prev_turn_id`（v0.64），加上 `turns.branch_id` 归属。
- 问题：merge 边被"LLM 摘要 + 发消息 + 归档"流程污染（摘要 turn 混进主线、源分支进已归档列表）；父链单父，merge 无法表达；lineage 是 branch 级递归特判（fork 一套、merge 一套）。

## 1. 目标图模型（决策）

### 节点
- **普通 turn**：真实回合，消息挂在 turn_id/dispatch_id 上（不变）。
- **fork marker**：分支创建时写入（`branch_id=新分支, kind='fork'`），parents = [fork 点 turn]（fresh 分支 fork 点为空 → 无 parent）。空分支的 context 由此自然等于 fork 祖先，无特判。
- **merge marker**：合并时写入（`branch_id=目标线, kind='merge'`），parents = [目标线 head（sequence）, 各源线 head（merge）]。**不是消息、不 dispatch、不进 context 正文、UI 不渲染为聊天 box**——纯图节点，同时是"在何处合并进来"的渲染锚点。
- marker 行的 `role_id`/`session_id` 为空、`started_at=ended_at`；`latestLineTurn`/`latestBranchRoleTurn` 等取"最新真实 turn"的查询统一过滤 `kind=''`。

### 边（新表 `turn_parents`）
| kind | 含义 | 写入时机 |
|---|---|---|
| sequence | 同线顺序父（现 prev_turn_id 的泛化） | 每个真实 turn / marker 创建时 |
| fork | 分支首节点 → fork 点 turn | fork marker 创建时 |
| merge | merge marker → 源线 head turn | 合并时（一条 per 源分支） |

backfill（迁移）：`prev_turn_id` → sequence 边；存量分支 fork 点 → 补 fork marker + fork 边；存量已合并分支（loop design / empty session）→ 补 merge marker + merge 边。

### 派生态（不再独立存储或仅做冗余索引）
- "A 已合并进 B" = 存在 B 线上的 merge marker，其 merge 父 = A 的 head。源分支行的 `merged_into_branch_id`/`merged_through_turn_id`/`merged_at` 可在同一事务写入做查询加速，但**图是唯一权威**。
- 合并后的源分支：分支条/已归档列表都不出现；dispatch/fork/delete 拒绝（`retired()` = archived ‖ merged）。
- **归档（archive）语义不变**：by the way 隐藏、不进任何线的图遍历（归档分支不出现在任何 parents 边的可达集里——它的 turn 只被自己的 tab 只读查看）。

### 遍历（单一机制）
`lineageTurns(L, before)` 重写：起点 = 线 L 上 `started_at < before` 的全部 turn（含 marker）∪ {L 的 fork marker}；沿 `turn_parents` 反向 BFS（visited 去重，天然处理 diamond）；按 `(started_at, id)` 排序；cutoff 截断。fork/merge/多父全部消隐在边表里。`buildLineContext`/`buildLineBridge`/前端可见性全部基于这一个遍历。

## 2. 改动清单

### 后端（`internal/plugins/chat/`）
1. **`store.go`**：`TurnParent` 模型 + `turn_parents` 表（联合主键 + 双向索引）；`Turn.Kind` 列；`turn_parents` 写入 helper（`addTurnEdges`）；backfill 迁移；`latestLineTurn`/`latestBranchRoleTurn` 过滤 `kind=''`；`branchesMergedInto` 改为图查询（经 merge 边 join）或读冗余列。
2. **`lineage.go`**：`lineageTurns` 重写为 DAG 遍历（上节）；`prevTurnFor` 改为返回 parents 集合（多父），`beginTurn` 写边。
3. **`merge.go`**：
   - 分支创建（`branches:create`）→ 写 fork marker + fork 边。
   - `handleBranchesMerge` 重写为单步：校验（源活跃、无 running turn；目标活跃且不在源集）→ 同一事务：写 merge marker（目标线）+ sequence/merge 边 + 源分支冗余 merged 列 → `publishBranch(branch, "merged")`。**无 LLM、无 dispatch、无摘要**。
   - 删除 `merge-confirm`/摘要 prompt/cutoff 整套。
4. **图查询 RPC**：`chat:_:branches:graph {chat_id}` → 节点（turn id、kind、branch、时间）+ 边（from/to/kind）——供前端并集计算与未来图渲染。
5. **dispatch/fork/delete 守卫**：`retired()`。

### 前端（`frontend/src/plugins/chat/ChatPane.vue`）
6. 合并操作：删草稿对话框/`merge-confirm`/`mergeCards`；一次 confirm 弹窗 → 单步 `branches:merge` → 移除源 tab、切到目标线。
7. 时间线并集：`turnVisible` 用 `branches:graph`（或 branches payload 内嵌边）计算"查看线 + 图可达线"集合；merge 来源 box 加来源小字；merge marker 渲染为细分隔行（"⇤「A」合并进本线"，低对比度单行样式）。
8. 「已归档」过滤掉合并行。

### 测试
9. merge 测试重写：单步合并 → marker + 边落库；目标线 `lineageTurns` 含源 turn；源 dispatch 拒绝；无新消息；重复合并/running 源拒绝。
10. lineage DAG 测试：fork 祖先、merge 并集、diamond 去重、传递 merge、归档不可达、cutoff。
11. fork marker：fresh 分支 context = 空；fork 分支首发 turn 前 context = fork 祖先。

### 文档
12. `docs/plugin-framework.md` bump v0.70：图模型（节点/边/marker/派生态/遍历）、单步 merge、废弃 `merge-confirm` 与 MergeMessageID/MergedThroughTurnID 写入（列保留不删）。
13. `architecture.md` 同步。

## 3. 验证
- `gofmt -l . && go build ./... && go test ./...`
- `cd frontend && npx vue-tsc --noEmit && npm run build`
- ad-hoc 真链路脚本（用后删）：两分支各跑 turn → 合并 → 断言图边、context 并集、无消息、归档列表干净、`branches:graph` 输出符合预期
- `POST /api/admin/restart`（用户确认后）实测

## 4. 分支条/图渲染（本期 out of scope）
用户尚未定分支条形态，本期不做。数据模型为此提供的保证：每条边有锚点 turn、方向、kind、时间；每条 lane 有出生点（fork marker）与消亡点（merge marker 或归档）；`branches:graph` 一次可取全图。后续无论做 lane 图、commit 图还是"history window 内多分支标记"，都直接读这份数据。

## 5. 待用户确认的开放问题
1. merge marker 在 UI 时间线渲染为一条细分隔行（"「A」合并进本线"）——可接受？（不是消息、不进 context）
2. fork 也写 marker（统一无特判），还是 fork 边留到分支首个真实 turn 再写、空分支走 branch 行 fallback（少一类节点、多一处特判）？plan 默认前者。
3. 一步到位直接上图模型（推荐，避免 branch 级 lineage 改两遍），还是先 branch 级止血再图化？
