# 分支合并重定义为血统融合（merge = lineage graft）Plan

**Goal**：把 chat 分支合并从「LLM 摘要 + 发消息 + 归档」重定义为「纯血统融合」：源分支彻底消失，其全部 turn 并入目标线的 agent context 与 UI history；不产生任何摘要消息、不调用 LLM、不进已归档列表。

**Architecture**：合并只在 `branches` 行上打一个血统链接（`merged_into_branch_id` + 新列 `merged_at`），复用已存在的 lineage 递归（`lineageTurns` 已经会把 merged 分支的 turn 并集进目标线 context）；UI 时间线用同一链接做前端可见性并集。归档（archive，by the way 模式）语义不变，与合并是两种不同的终态。

**Tech Stack**：Go（chat 插件）、Vue/TS（ChatPane）、sqlite（gorm 自动迁移）。

---

## 现状（为什么现在的实现是错的）

当前 merge 流程（framework v0.61–0.66，`internal/plugins/chat/merge.go`）：
1. `branches:merge` → LLM 起草摘要（慢、是用户这次"两次合并没成功"的痛点）
2. `branches:merge-confirm` → 把摘要**作为一条 user 消息 dispatch 进目标线**（触发一轮真实 agent turn）
3. 源分支打 `archived_at`，留在「已归档」列表里可只读查看

用户定义的正确语义：
- A 合并进 B 后 **A 就没有了**——分支条没有、已归档列表也没有、没有这个 tag
- 走 B 的 **history**（UI）和 **context**（agent）都能看到被合并进来的 A 的内容
- **不生成摘要消息、不给 agent 发任何东西**

关键事实：context 并集**已经实现**——`lineage.go:36-46` 的 `lineageTurns` 递归walk `branchesMergedInto`，新 context 建造（`buildLineContext`/`buildLineBridge`）都基于它。缺的是：①合并动作本身不发消息不打归档；②UI 时间线的可见性并集。

## 目标语义

分支行的两种终态，互斥：
- **归档**（`archived_at != NULL AND merged_into_branch_id = ''`）：by the way 隐藏；内容不进任何线的 context/history；留在「已归档」只读列表。语义不变。
- **合并**（`merged_at != NULL`）：从分支条和「已归档」彻底消失；其全部 turn 递归并入目标线的 context（已有机制）与 UI 时间线（新增前端并集）；拒绝 dispatch/fork/delete（同归档）。

合并后目标线后续新 turn 的 context 自动包含源分支内容（lineage 递归天然传递：源分支自己 merged-in 的分支、fork 祖先一并带入）。源分支在合并**之后**不可能再有新 turn（dispatch 被拒绝），因此无需 cutoff 概念，`MergedThroughTurnID`/`MergeMessageID` 字段废弃。

## 改动清单

### 后端（`internal/plugins/chat/`）

1. **`store.go`**
   - `Branch` 新增 `MergedAt *int64`（gorm sqlite 自动加列 `merged_at`）。
   - `branchesMergedInto`（:706）谓词改为 `chat_id = ? AND merged_into_branch_id = ? AND merged_at IS NOT NULL`——去掉 `archived_at IS NOT NULL AND merge_message_id != ''`（新合并没有这两个字段；归档行 `merged_at` 为 NULL 自然排除）。
   - 启动迁移（或一次性 SQL）：`UPDATE branches SET merged_at = archived_at WHERE merge_message_id != '' AND merged_at IS NULL`——今天已合并的两条（loop design / empty session）血统链接保留。
   - `Branch.retired()` helper = `archived() || merged()`，供 dispatch/fork/delete 守卫统一调用。
2. **`merge.go`**
   - `handleBranchesMerge` 重写为单步 RPC：`{chat_id, branch_ids, target_branch_id}` → 校验（chat 存在；每个源存在、属于本 chat、未终态、无 running turn；目标活跃且不在源集合中）→ 逐源打 `MergedIntoBranchID` + `MergedAt` + `UpdatedAt` → `publishBranch(branch, "merged")` → 返回合并后的 branch payload。**无 LLM 调用、无 dispatch、无 cutoff**。
   - 删除：`handleBranchesMergeConfirm`、`buildMergeInput`、`mergeSummarySystemPrompt`/`mergeSummaryUserTemplate`、cutoff 结构；`chat.go` 注销 `branches:merge-confirm` 订阅。
   - dispatch/fork/delete 守卫从 `archived()` 换 `retired()`（`runtime.go:361`、`merge.go:75`、删除路径 :178）。
3. **`lineage.go`**：零改动（`branchesMergedInto` 谓词变更即接入）。

### 前端（`frontend/src/plugins/chat/ChatPane.vue`）

4. **合并操作**：`draftMerge`/`confirmMerge`/`mergeDraft`/`mergeBusy` 草稿对话框（模板 :1914-1924）整体删除，改为一次 `window.confirm`（"把「X」「Y」合并进主线？源分支将消失，其内容并入主线的 history 与 context"）→ 调单步 `branches:merge` → 移除源 tab、`activeTabs` 切到目标线。`mergeCards`（已合并分支卡片，:1789 附近）一并删除——不再有这种消息。
5. **时间线可见性并集**：`turnVisible`（:526）改为——查看线 L 时，可见 turn = L 自己的 + 递归 `merged_into` L 的分支的 turn（从 `branches.value` 的 `merged_into_branch_id` 建树，纯前端计算，无需新 RPC）。来自合并线的 box 加低调来源标记（小字 `自「分支名」合并`，沿用现有无方框低对比度单行样式）。
6. **「已归档」列表**：过滤条件加 `!merged_into_branch_id`，合并行不出现。

### 测试

7. `chat_test.go` 的 merge 测试（:1999-2247）重写：单步合并 → 源行 `merged_at` 落库 + 目标线 `lineageTurns` 并集含源 turn；源 dispatch 拒绝；重复合并拒绝；running 源拒绝；无摘要消息产生（messages 表无新增）。
8. `lineage_test.go`：diamond/传递用例断言语义不变（谓词改后仍过）；补一条归档行不进任何 lineage 的断言。
9. `branch_partition_test.go`：归档用例不变；补归档行 `merged_at IS NULL` 断言。

### 文档

10. `docs/plugin-framework.md` bump v0.70：合并重定义（纯血统融合、单步 RPC、无摘要消息；归档语义不变；`merged_at` 列；废弃 `branches:merge-confirm`/`MergeMessageID`/`MergedThroughTurnID`）。
11. `architecture.md` 同步分支小节。

## 验证

- `gofmt -l . && go build ./... && go test ./...`
- `cd frontend && npx vue-tsc --noEmit && npm run build`
- ad-hoc 真链路脚本（用后删除）：建 chat → 两分支各跑 turn → 合并 → 断言 ①源分支从 branches:list 活跃集消失 ②目标线新 turn 的 context 含源分支内容 ③无新消息产生 ④归档列表不含合并行
- `POST /api/admin/restart`（需用户确认）后用户实测

## 遗留与边界

- 今天已发出的两条「已合并分支」摘要消息（loop design / empty session）已是主线普通消息，无法干净回收，保留；两条分支行经 backfill 后按新语义从「已归档」消失、血统保留。
- `MergedThroughTurnID`/`MergeMessageID` 列保留在表中但不再写入（不删列，避免迁移风险）。

## 待用户确认的开放问题

1. UI 时间线内联显示合并来源 turn（带来源小字标记）——这就是你要的"走 B 的 history 看到 A"吗？还是只要 agent context 可见、UI 不内联？
2. 合并后的分支是否任何地方都不可再见（不做"已合并"只读列表）？（按你"没有这个 tag 了"默认不做）
3. 单步合并不要摘要、不要编辑框，confirm 弹一次就执行——可以吗？
