---
title: "Viewer 独立 Task、分层 Context 与统一 Decision 实施计划"
goal: "以相互独立、可结束的 Task 替代 Branch/DAG 工作线，并提供 Task 内 Turn 摘要、跨 Task 摘要和统一人工 Decision 回复中心。"
architecture: "Chat 是项目容器，Task 是消息、session、草稿和自动化的唯一工作边界；历史按『已结束 Task 摘要 → 当前 Task 较久 Turn 摘要 → 当前 Task 最近 Turn 可见原文』分层构建。Decision 是持久化人工输入请求，回复复用原 Task/Role 的普通 dispatch；Hindsight 继续作为按 Chat 隔离、跨重置保留的长期记忆层。"
tech_stack: "Go/GORM/SQLite、现有 Bus RPC 与 chat dispatch、Vue 3/TypeScript/Pinia、现有 llm:_:complete、Vitest、Python 黑盒 smoke"
estimated_time: "按 7 个阶段实施；每阶段先补失败测试，再实现并运行局部检查，最后执行完整标准检查。"
header: "For Hermes: 按任务顺序直接实现，不委派代码实现；小步修改、逐步验证。未经用户明确要求，不 commit、不重启 Viewer、不启动前端 dev server。"
---

# Viewer 独立 Task、分层 Context 与统一 Decision 实施计划

## 1. 定稿语义

### 1.1 产品模型

- **Chat**：项目/长期容器，拥有 workspace、Roles、Task 集合和完整历史。
- **Task**：Chat 内相互独立的工作单元。每条消息、Turn、provider session、草稿、后台活动和自动化都唯一属于一个 Task。
- **Turn**：一次普通派发；`turn.ended` 只表示本次运行结束，不表示 Task 完成。
- **Session**：按 `(chat_id, task_id, role_id)` 续接。不同 Task 不共享 session，不继承原文，不建立父子/fork/merge 关系。
- **Hindsight Memory**：Chat 级长期记忆，不属于单个 Task 时间线。同一 Chat 的所有新旧 Task 有意共享同一 bank，重置 SQLite 历史时继续保留；不同 Chat 使用不同 bank，互不检索。
- **Decision**：某个 Turn 向人提出的持久化问题；人的回复成为原 Task 中下一条普通用户消息，并续接原 Role/session。

新建 Chat 时自动创建一个名为「主线」的普通 Task 作为初始激活目标。它只是引导用的第一个 Task，**没有特殊合并或归档语义，也可以 ended**。所有 Task 使用同一套规则。

**本次升级不迁移任何 Chat history**：只保留 Chat 容器和设置，为每个现有 Chat 创建一个全新 ID 的默认 open Task。旧 Branch/Task、Message、Turn、Block、Summary、Session、Draft、Queue、Decision、ContextBuild、旧 Automation 和 Loop 运行数据均不进入新库；Loop 插件本身也全部删除；旧数据库只作为离线备份保留，新运行时不得读取。

### 1.2 Task 生命周期

- `open`：可查看、激活、输入、排队、运行自动化。
- `ended`：完整历史仍可查看和分页，但禁止 dispatch、queued edit、草稿写入、continue、programmatic dispatch 和 Decision 回复。只读指用户消息/执行历史；ended 后仍允许重命名和重试摘要，不允许重新 pin。
- ended 是人工显式动作；正常 `turn-ended`、生成摘要或 Decision 出现都不得自动结束 Task。
- ended 前必须没有 running/queued/Agent post-turn background；否则返回 `task_busy`，不隐式取消工作。title、Turn Summary、Task Summary 等 Viewer LLM worker 不属于 Task busy，也不点亮 Side panel 的 Agent running 状态。
- ended 时取消该 Task 尚未回复的 Decision；已经 dispatching 或已产生排队/运行工作的 Task 仍按 `task_busy` 拒绝 ended。
- 第一版不提供 reopen、merge、fork、archive、DAG 图和跨 Task 联合视图。需要继续时新建 Task，通过已结束 Task 摘要承接上下文。
- Task 结束后立即只读，final Task Summary 异步生成；摘要失败不阻塞 ended，也不要求重新打开 Task，可单独重试。

### 1.3 Task-first 导航、管理与单任务阅读

- **Side panel 从 Chat 列表改为 Task 列表**：只显示当前 Active open Task、running open Tasks 和 pinned open Tasks。条目标题是 Task title，Chat 名称作为次级来源信息；running+pinned 重叠时只显示一次。当前 open Task 即使未 pin、未 running 也临时显示，其他 Task 到 Tasks 页查找。
- Task ended 后立即从 Side panel 移除并清除 pinned；主页面可继续显示该 ended Task 的只读历史，但 Side panel 不再把它列为活动工作。旧 Chat.Pinned 作为导航设置复制到该 Chat 新建的默认 Task.Pinned，不参考任何旧 Branch/Task 活动。
- **主页面直接显示一个 Task**：标题采用 `Chat / Task` 层级，主体只有该 Task 的 messages/blocks/Decisions/Composer。每个 Chat view instance 只有一个 Active Task；Side panel 跟随默认视图，副本 Pane 仍有自己的 Active Task。
- 删除主线/Branch tabs、底部 open-task tags、「全部」、多选、merge/archive 和 Chat 内嵌 Task list，释放纵向空间。切换 Task 就是切换整个主页面内容，不再对 Chat 消息做 Branch 过滤。
- **新增独立 Tasks 页**追踪所有 open/ended/running/pinned Task，支持 Chat 筛选、名称/摘要搜索、状态筛选、最后活动、Summary/Decision 状态和 pin/end/retry 操作。桌面使用每页 50 条的服务端游标分页表格，手机使用分页卡片；点击行回到主页面打开 Task，不在 Tasks 页内嵌消息时间线。
- Chat 仍是 workspace、Roles、路由和 Task 分组的项目容器，其配置留在 Chat Settings；日常导航和输入不再以 Chat/Branch 为工作单元。

#### 海量历史与滚动

- Side panel 和 Tasks 页只读取 Task 元数据及短 summary preview，不加载 Task messages；完整 Summary 在选中 Task 后按需读取。
- 主消息流沿用每页约 50 条、常驻 DOM 约 300 条、`before/after + (created_at,id)` 游标和 messages/blocks 同范围加载；不因为 Task 随时间积累海量消息就全量读取。
- 新增 `history:around(task_id, created_at, message_id)`，每个 `(view_instance, task)` 保存轻量阅读锚点。再次打开时恢复锚点前后消息；无锚点才加载最新页，「跳到最新」是显式动作。
- 加载更早、DOM 驱逐和 Task 切换都保持视口锚定；新消息不自动滚动，detached 时只显示新消息数量/跳到最新。非 Active Task 的流式内容只更新元数据并使缓存失效，不在浏览器无限缓冲。
- 不采用消息流内 Task Accordion 或同向双层滚动。手机 Side panel 使用 Drawer，Tasks 页是独立页面，主消息区始终只有一个纵向滚动上下文。
- 消息缓存、分页、实时帧归属、草稿、等待回复和 optimistic send 全部按 `(view_instance, task)` 隔离；保留时间交错消息/活动、紧凑样式和禁止自动滚动。

### 1.4 分层 History Builder 与 Chat 长期记忆

仅在新 session 或显式 `force_new_session` 时构建完整分层上下文，顺序如下：

1. **项目层**：当前 Chat 中最近最多 50 个、且具有 completed final summary 的其他 ended Task，每个 Task 一条摘要，按结束时间从旧到新排列。
2. **当前 Task 较久 Turn 层**：当前 Task 中除最近 N 个已结束 Turn 外的已完成 Turn Summary，按时间从旧到新排列。
3. **当前 Task 最近原文层**：最近 N 个已结束 Turn 的可见 user/assistant message 原文，按实际时间排列；默认 N=3，做成后端常量/配置，不做首版 UI 设置。
4. **Chat 长期记忆层**：沿用现有 `buildHindsightRecallSection`，只 recall 当前 Chat ID 对应的 Hindsight bank；可包含重置前和其他 Task 沉淀的长期记忆。它是独立于消息时间线的 candidate evidence，不是旧 Message 原文恢复。

最近原文明确排除：

- thinking；
- tool_call、tool_result；
- command、file_change；
- 内部 routing/provider/session 信息；
- Decision marker 内容。

普通 `Message` 已是可见文本来源，因此直接读取 messages 即可，不从 `message_blocks` 重建原文。最近 N 个**已结束** Turn 形成原文时间窗口；窗口内尚未形成 Turn 的 orphan user message 也按时间插入，queued/running 的未完成 Turn 暂不纳入，窗口外 orphan 没有摘要时记 coverage omission。Turn Summary 的生成输入仍可使用工具/命令结果来概括工作事实，但不得把原始工具输出放进最近原文层。

约束与降级：

- open Task 的原文、Turn Summary 和滚动 Task Summary 不注入其他 Task；Task 时间线层跨 Task 自动共享的只有 ended Task 的 completed final Summary。Chat 级 Hindsight memory 是有意的长期记忆例外，可在同一 Chat 的 Task 间共享，但不得跨 Chat。
- 显式 `@message` 引用仍是用户主动跨 Task/Chat 引用原文的例外。
- 最近 50 条和现有字符/字节预算共同生效，不为凑满 50 条撑爆 prompt；实际纳入和省略项写入 `ContextBuild.coverage`。
- pending/failed Task Summary 不用原文替代；pending/failed Turn Summary 也不把较久原文偷渡进 prompt，只记录 coverage omission，并允许重试摘要。
- resumed session 不重复注入整份分层上下文；Role 切换桥只补当前 Task 中该 Role 尚未见过的摘要/可见活动。
- 其他 Task 新完成的摘要在下一次新 session 时纳入，不打断当前 provider session。
- raw dispatch 维持现有语义：跳过公共 prompt、分层 context、Role 切换桥和 Decision post-prompt；session 续接规则不变。
- Hindsight 沿用 `hindsightBankID(prefix, chatID)`，不引入 history generation，不清空、不重建、不换 bank。`recall_enabled`、retain、预算和降级行为保持现状；同一 Chat 重置后的新 Task 可 recall 旧 memory，其他 Chat 的 bank 不参与请求。raw dispatch 也维持现有 Hindsight recall 行为。

### 1.5 Task Title 与身份

- `Task.ID` 是全局唯一主键，所有消息、session、Decision、pin、RPC 和跳转都只使用 ID。`Task.Title` **允许在同一 Chat 或不同 Chat 中重复**，不得对 `(chat_id,title)` 加唯一约束，也不得用 title 解析目标。
- 新 Task 的首条用户 query 到达时，不等待 LLM：将首尾空白和连续换行规范化后，取**前 20 个 Unicode 字符**作为临时 title；空 query 使用「未命名 Task」。默认「主线」只是可自动替换的占位 title。
- 同时以第一条 query 为唯一输入，异步调用 Viewer 的 `llm:_:complete`（当前 active model）生成简短、同语言、单行 title。title 生成与 Agent dispatch 并行，不阻塞首 Turn、session 或消息落库。
- LLM 结果去掉包裹引号、压缩换行并限制 40 个 Unicode 字符；空结果、超时或失败保留 query 前缀，不影响 Task 使用。成功后只更新一次，不随后续 Turn 反复改名。
- Task 保存 `title_source=default|query|llm|manual` 和 `title_revision`。创建时显式命名或用户之后改名都标记 manual；异步结果仅在 Task 仍为预期 query/default source 且 revision 未变化时生效，迟到响应不得覆盖手动 title。
- 空 Task 可先显示「未命名 Task」；首个 query 只触发一次自动标题任务，重复事件/重启通过 Task 状态和 `title_query_message_id` 去重；使用原始可见 query，不包含 @引用展开、公共 prompt 或 post-prompt。

### 1.6 Task Summary

- 每个非取消 Turn 完成后继续异步生成 Turn Summary。Turn Summary 完成后触发 Task Summary 滚动更新：**以上一条 Task Summary + 尚未覆盖的新 Turn Summaries** 为输入，不必等 Task ended，也不重新读取全部海量消息。
- Task Summary worker 按 Task 串行并 coalesce 连续 Turn，保存精确 `source_turn_ids` 集合和递增 `summary_revision`，不使用无法表达失败空洞的单一高水位。新输入按 Turn 时间排序，过期 revision 的结果丢弃；失败保留上一版可用摘要，下次连同未覆盖 Turn 一起重试。
- 运行中的摘要用于主页面和 Tasks 页预览，但**不注入其他 Task**。Task ended 后立即只读，将摘要行标记为 `final=true,status=pending`，再由同一串行 worker 用滚动摘要文本和剩余 Turn Summaries 生成最终版本。
- 只有 `status=completed AND final=true` 的 ended Task Summary 才进入其他 Task 的项目层 context。ended 时的旧滚动摘要若缺少最后 Turn，不得冒充 final；失败不阻塞 ended，也不要求 reopen，可单独重试。
- Task Summary 使用与用户相同语言，覆盖目标、当前结果/最终结果、关键决定、产物/文件、未决事项；长度沿用现有 summary 预算。保存 provider/model、summary_revision、status/final、error、created/updated 和精确覆盖范围，超预算时显式记录遗漏。
- completed Turn Summary 采用 append-only：retry 只处理 missing/failed，不能静默改写已覆盖内容，否则滚动摘要会重复追加新旧两版。本次没有 legacy Task 或旧摘要回填；所有摘要只从升级后的新 Turn 产生，新 ended Task 自动生成 final summary，missing/failed 可显式 retry。

### 1.7 Decision 协议与回复

post-prompt 固定要求：确需用户选择或确认才能继续时，在最终可见回复末尾只放一个**平铺 key-value marker**：

```text
<viewer-decision>
version: 1
kind: choice
question: 接下来按哪个方向继续？
option: 按推荐方案，先修改引言
option: 同时修改引言和摘要
display: buttons
tone: neutral
</viewer-decision>
```

这不是 XML、YAML 或 JSON。外层标签只划分边界，正文采用 Viewer 自定义的单行协议，不使用任何通用序列化解析器。

#### 平铺字段语法

- 正文每个非空行都必须是 `key: value`；key 从该行第一个字符开始，到**第一个 ASCII 冒号**为止。key 只能匹配 `[a-z][a-z0-9_]*`，不允许缩进、空格、点号或连字符。
- 冒号后的剩余内容全部是 value；其中再次出现 `:`、`#`、引号、反斜杠、花括号等都没有特殊含义。仅去掉 value 两端 ASCII 空白，内部内容原样保留。
- key 和标签大小写敏感；全角冒号 `：`不是分隔符。空行/纯空白行忽略，其余无冒号行、空 key、非法 key 都使整个 marker 无效。
- 不支持多行 value、缩进、嵌套对象、转义、引号解义、注释、锚点或任意标签。`question: |` 中的 `|` 也只是普通文本，不是多行语法。
- 语法允许重复 key，但字段 schema 决定其含义：v1 只有 `option` 可重复并按出现顺序形成 list；`version/kind/question/display/tone` 出现两次即无效，即使值相同也不采用 first-wins/last-wins。
- v1 必填 `version: 1`、`kind: choice` 和非空 `question`；`option` 可省略，用户始终可以自由输入。未知 version/kind/key 使 marker 无效。
- `display` 可选 `auto|buttons|list`，默认 `auto`；`tone` 可选 `neutral|warning`，默认 `neutral`。这两个展示字段的值无效时只回退默认值并记录 parse warning，不丢弃核心 Decision。
- 单个 marker 上限 16 KiB，question 上限 2000 个 Unicode 字符，单个 option 上限 500 个 Unicode 字符，最多 8 个 option；空 option、超限或非有效 UTF-8 均无效。
- `display/tone` 只是语义展示提示。禁止 HTML、CSS、组件名、URL、RPC、工具、action、session/task/role 等路由或执行字段；Viewer 始终拥有最终展示和路由权。

#### 边界提取与异常处理

- 只在 Turn 完整结束后解析该 Turn **最后一个 assistant `Message.Text`**；marker 不得跨 Message 拼接，不扫描 thinking、工具输出、command、用户消息或其他隐藏字段。
- 扫描时统一 CRLF/CR 为 LF，同时保留映射到原始 Message.Text 的 byte offset。`<viewer-decision>` 和 `</viewer-decision>` 必须独占一行、从第一列开始、无属性或尾随字符；closing tag 后只允许空白，后面有正文或代码围栏都不解析。
- 使用逐行 scanner，不用跨全文正则。跟踪 fenced code block：代码块内的标签不算 marker；候选标签被未闭合代码围栏覆盖时不解析。
- 每轮最终正文最多允许一个代码块外 marker，且必须位于末尾。多个、嵌套、缺少开闭标签或不合法候选都按普通文本处理；不向前寻找另一个“可能可用”的 marker，不自动补标签、修 key、拼接多行或调用 LLM 修复。
- 核心字段不合法时不产生 Decision，也不隐藏任何原文。只有完整验证通过后，才按原始 Message.Text 的准确 byte 范围隐藏有效 marker；原始 Message 永不篡改。
- Decision 保存 `version/kind/question/options/display/tone/parse_warning`、`source_message_id/marker_start/marker_end` 以及 `chat_id/task_id/source_turn_id/role_id/status/response/resolved_dispatch_id`，并保存请求者 session/provider 供诊断；回复路由由 Viewer 绑定，不能由 marker 指定。
- 原 Task 时间线插入 `decision` MessageBlock，状态随 Decision 更新；有效 marker 在正文展示和 Turn Summary 输入中隐藏。解析结果转成内部强类型 struct，DB/RPC 可使用规范化 JSON，但 Agent 永远不直接写 JSON。
- 非 raw dispatch 每次都附加简短 Decision post-prompt，避免 resumed session 忘记协议。

Decision 状态：`pending → dispatching → resolved`，另有 `dismissed/cancelled`。admission 失败留在 pending 并保留错误，不另造含义不清的 failed 状态。回复规则：

- `resolve` 使用 `decision:<decision_id>` 作 dispatch 幂等键，原子 claim pending Decision。
- 通过后端的同一个 dispatch admission/queue/runtime 路径，向原 `task_id + role_id` 发送用户文本；不重新做 LLM 路由，不新建 Task，不主动 force new session。
- dispatch 被接受后才记 `resolved + resolved_dispatch_id`。只有 admission 前的 queue rejection/校验/存储失败可重试；一旦 admission 成功，后续 provider error、Turn failed/cancelled 都不重新打开 Decision 或自动重发，用户可另发普通消息。重启后根据幂等 receipt/dispatch 状态对账 `dispatching`，不盲目重复发送。
- 原 Task 普通 dispatch 可显式携带 `decision_id` 关联回复；未携带时，只有该 Task 恰好一个 pending Decision 才自动关联，多个 pending 时不得猜测，卡片保持 pending，需用户从卡片选择或 dismiss。关联同样在 admission 成功后完成，不发送第二份消息。
- `resolve/dismiss` RPC 只给前端使用，不注册为 Agent 工具。

### 1.8 Programmatic Task 与 Shell 控制

#### Task API 是一等公共 Bus RPC

前端、内置插件、外部插件和 Go/Python/TS SDK 都使用同一组 API，不新增 HTTP facade，也不给自动调用另建消息/session 通道：

- `tasks:create {chat_id, title?, idempotency_key, causation_id?, correlation_id?} → {task}`。显式 title 标记 manual；省略时使用默认占位，首条用户消息仍按统一 title 规则处理。创建 Task 不打开面板、不启动 Agent。
- `tasks:dispatch {task_id, role_ids?, text, idempotency_key, causation_id?, correlation_id?, ...} → {dispatch_id, task_id, status}`。**与用户在输入框输入并发送完全同义**：持久化一条用户消息，走统一 admission/queue/runtime 并调用 Agent；role_ids 缺省沿用正常路由，返回 queued/running admission 状态，正常发出 turn/message/block/background 事件。

约束：

- 所有 API 都重新校验 Chat/Task/Role 和 ended 状态；`tasks:dispatch` 不得绕过 busy、queue、session、title、Hindsight retain、Summary 或 Decision 规则，也不存在只写消息而不调用 Agent 的公共 append API。
- 调用方插件身份由 Bus 连接绑定，不能由 payload 自报。Task 保存 `created_by`，Message/OperationReceipt 保存 `source_plugin/source_event_id/causation_id/correlation_id`；幂等键按 `(caller_plugin, operation, idempotency_key)` 隔离，重试返回原 Task/Message/Dispatch，不重复执行。
- `dispatch` 的文本只表示用户输入，调用方不能伪造 assistant、tool result 或 provider session。外部插件不直接调用 agent provider，而是经 Chat 编排使用统一 Agent 接口。
- 未来 event system 只需让事件消费者调用这些 RPC；本期不实现事件框架、定时器、Webhook 或 Loop 替代品。
- create/dispatch **不直接修改前端 Shell 视图状态**：不切换 Chat/Active Task，不打开/聚焦/关闭面板，不改 InputSession/草稿，不滚动页面，也不调用 Shell 控制 RPC。Shell 只通过普通 Task/Message/Turn 数据事件被动更新；若对应 Task 已打开就刷新内容，否则只更新任务列表/running 状态。

#### Shell/Pane 也复用统一接口

- 浏览器内继续直接使用现有 `ctx.shell.open {paneType,instanceId?,params?,newPane?}` 和 `ctx.shell.panes.list/close/focus/move`；Chat/Task 前端不发明第二套本地控制方式。
- 浏览器 Shell 新增每连接公共 Bus RPC：`shell:<target_conn>:open`、`shell:<target_conn>:panes:list`、`shell:<target_conn>:panes:focus`、`shell:<target_conn>:panes:close`、`shell:<target_conn>:panes:move`，内部只调用上述现有 helpers。调用方从 `plugins:_:list` 中 manifest.id=shell 的条目选择 conn。
- 每个浏览器是独立 `shell` 连接，因此 `target_conn` 必须出现在 RPC channel 中；不得广播到所有浏览器或猜测“当前浏览器”。只有目标连接订阅自己的 channels，目标离线直接 no_route，不用 mailbox 延迟重放旧的开/关面板命令。
- Chat instance adapter 接受 `{chat_id, task_id}`，因此程序化流程可以先 `tasks:create`，再按需 `tasks:dispatch`，最后可选 `shell:<target_conn>:open` 显示该 Chat/Task；也可以只在后台运行而不打开任何面板。

## 2. 明确不做

- 不复用 History DAG、lineage/closure、merge node、source→archive 联合视图。
- 不在 Branch 上再包一层 Task；不保留旧 schema reader、legacy 列、Branch API/字段、前端旧 payload、双写或任何 backward-compatibility shim。
- 不做 MCP Tools、Agent→Agent、A2A、通用 action marker、通用 `emit_event`、新的 HTTP/Webhook 入口或 event-system 框架；只提供未来事件消费者可直接调用的 Task/Shell RPC。
- 不保留或替换 Loop：旧 Loop 后端、前端、注册、文档、smoke、lease/gate 和数据库依赖全部删除。
- 不做后台轮询等待 Decision，不让 Agent 自己批准 Decision。
- 不自动把正常 turn-ended 当 task-ended，不自动滚动，不恢复 Branch/open-task tabs；pinned 只用于 Side panel 的 open Task 导航。
- 不改动当前 file-manager/file-viewer 的未提交工作。

## 3. 实施任务

### Task 1：设计全新 Task schema，并准备最后执行的 Chat-level 设置迁移

**文件**

- 修改：`internal/plugins/chat/models.go`
- 修改：`internal/plugins/chat/store.go`
- 新增：`internal/plugins/chat/task.go`
- 新增：`internal/plugins/chat/chat_level_migration.go`
- 新增：`internal/plugins/chat/task_test.go`
- 新增：`internal/plugins/chat/chat_level_migration_test.go`
- 后续删除不再被引用的 `history_graph.go`、`history_runtime.go`、`history_migration.go`、`history_rpc.go`、`merge.go` 及对应 DAG 测试。

**步骤**

1. 先写全新模型/Chat-level migration 失败测试，固定以下数据；开发、单测和功能黑盒始终先使用新空库，不打开真实旧库：
   - `Task {id, chat_id, title, title_source, title_revision, title_query_message_id, created_by, pinned, created_at, updated_at, ended_at}`；每次重置都生成全新 ID，title 非唯一；
   - `TaskSummary {task_id, text, source_turn_ids, summary_revision, provider, model, status, final, error, timestamps}`；
   - `Turn/Message/MessageBlock/TurnSummary` 增加 indexed `task_id`；
   - `RoleSession` 以 `(chat_id, task_id, role_id)` 为键；
   - Draft 以 `(input_instance_id, task_id)` 为键；
   - `ContextBuild` 以 Task 为归属，移除 head-node 语义；
   - Message 增加 `task_id/source_plugin/source_event_id/causation_id/correlation_id`；`OperationReceipt` 以 `(caller_plugin, operation, idempotency_key)` 幂等 create/dispatch，不恢复 Loop lease/gate。
2. **不提供 backward compatibility**：新 runtime 不读取旧 schema，不对旧内容表做 ALTER、回填或 Branch→Task 映射，不保留旧列、旧 RPC 字段、兼容 shim 或双写。旧库只允许最后的一次性 Chat-level migration 以只读方式打开；真实迁移必须等 Task 1–7 和完整验证全部完成后执行。
3. Chat-level migration 用新 models 在同级临时文件创建全新空库，只复制明确 allowlist 的设置：Chat.ID、Name、Type、Pinned、Root、CommonPrompt、MemberRoleIDs、RoleRoutingOverrides、CreatedAt/UpdatedAt，以及 Roles、RoutingPolicies、default routing policy 和有效 active_chat_id。外部 config store 中的 workspace、summary、Hindsight 和 LLM 配置保持不动；旧 provider/session 列不复制。
4. 每个保留的 Chat 只创建一个全新 Task ID、title「主线」、`title_source=default` 的 open Task；`Task.Pinned = 旧 Chat.Pinned`。没有旧 Branch ID 复用，也没有 ended/merged/archive Task。新 Chat 后续同样自动创建一个默认 Task，首个 query 再按 title 规则生成临时/LLM title。
5. 新库的 Messages、Turns、TurnEvents、Blocks、TurnSummaries、TaskSummaries、RoleSessions、Drafts、Queues、OperationReceipts、Decisions 和 ContextBuilds 均为空，不创建任何 DAG 或 Loop lease/gate 表；不创建新的 `loop.sqlite3`，旧 Loop/Iteration 不读取、不恢复。
6. Hindsight 完全沿用原 `prefix + chatID` bank：重置不删除旧 memory、不创建新 generation、不改变 bank ID。新 Task 的 retain/recall 继续写入和读取同一 Chat bank；新 retain metadata 可补充 `task_id` 作来源诊断，但不得据此过滤掉无 task_id 的旧 memory。
7. 校验 `task_count == chat_count`、每 Chat 恰好一个默认 open Task、所有内容表计数为零、Chat 设置字段逐项相等。通过后原子替换 Chat 库，旧 Loop 数据库只留在离线备份中。新库写入 `chat_schema_version=1`；启动时先检查版本，已有新库绝不再次重置，之后新增的消息/Task 必须跨重启保留。失败只放弃临时文件并保留旧库与备份。
8. 新 schema 只包含 Task-first 表，不 AutoMigrate Branch/HistoryNode/HistoryEdge/LineHead，也不调用 `migrateHistory()`。Chat 不保留 legacy Title/Provider/ProviderProfile/ProviderSessionID/CWD 列；Turn 不保留 BranchID/PrevTurnID/BatchID 等 DAG 字段。删除 Chat 时级联删除其新 Task 及派生数据。
9. Chat-level migration 是独立的一次性升级步骤，不属于新 runtime 的持久依赖。开发阶段只用临时 fixture 验证；真实旧库在所有功能完成后备份并迁移一次，随后旧库退出运行。

**验收**

```bash
go test ./internal/plugins/chat -run 'TestTask|TestChatLevelMigration' -count=1
```

- 用含旧消息和 Branch 的 fixture 重置后，Chat ID/名称/Root/prompt/Roles/routing 设置不变，每 Chat 只有一个全新 ID 的默认 open Task，旧内容计数为零。
- 旧 Branch/Task/Message ID 不出现在新库；不加载 Loop 也不创建 loop 数据库，Hindsight bank ID 重置前后完全相同，旧 memory 未被删除。
- 新建两个 Task 可以使用完全相同 title，所有关联仍按唯一 ID 正确隔离。
- 重置过程没有 LLM 调用；首次重置后写入新消息再重启，不会再次清空或重建默认 Task。

### Task 2：把 dispatch、session、queue、draft、实时事件和历史查询切到 Task

**文件**

- 修改：`internal/plugins/chat/chat.go`
- 修改：`internal/plugins/chat/runtime.go`
- 修改：`internal/plugins/chat/dispatch.go`
- 新增：`internal/plugins/chat/task_api.go`（统一 create/dispatch 与 OperationReceipt）
- 修改：`internal/plugins/chat/store.go`
- 简化：`internal/plugins/chat/view_filter.go`（可更名为 `task_history.go`）
- 修改：`internal/plugins/chat/chat_test.go`；删除 Loop 专用 `automation_test.go`
- 新增：`internal/plugins/chat/task_runtime_test.go`
- 新增：`internal/plugins/chat/task_api_test.go`

**步骤**

1. 将 RPC 的 `branch_id` 全面改为 `task_id`：原 `dispatch` 清洁重命名为 `tasks:dispatch`，并统一 `chats:list`、`blocks:list`、draft、queued update/cancel、continue 和后台列表；所有目标解析只使用唯一 Task ID。
2. 新增 `tasks:create/list/patch/end/pin` 和 `task-summary:retry`。Tasks 页使用筛选/搜索/游标分页；Side panel 由前端把默认视图的 `current_task_id` 显式传给 `tasks:list {scope:"sidebar",current_task_id}`，后端合并 current/running/pinned open Task 并去重。Active Task 仍是前端 view instance 状态，不在后端按 title 推断。
3. runtime/session/busy/queue/background key 全部包含 Task；同 Chat 不同 Task 可并行，同 Task 同 Role 保持串行。
4. dispatch 在持久化用户消息前重新读取 Task，ended 时返回 `task_ended`；programmatic dispatch 与自动重试也走同一 guard。Task 的首个用户 query 在 intake 时唯一标记，供临时 title 和异步 title worker 使用。
5. `chats:list` 和 `blocks:list` 只查询显式 Task，before/after 游标同时带 `task_id`；新增 around 查询及对应 messages/blocks 同范围响应。实时 turn/message/block/background 都携带 `task_id`。
6. continue 只允许同一 Task 的源 Turn；不能跨 Task 续接 session。
7. ended 使用与现有 merge guard 同等的 running/queued/Agent background 检查，但不把 Viewer title/summary worker 算作 busy；成功 ended 清除 pinned、发布 Side panel 移除事件，将摘要置为 `final=true,status=pending` 并交给同一串行 worker。
8. 删除旧 Branch RPC、merge RPC 和 Loop 专用 automation lease/gate，不保留双写或兼容 shim。
9. 实现公共 `tasks:create/dispatch`：dispatch 与输入框发送共用完整 intake→message→admission 路径；UI 与外部插件共用这些 RPC。create/dispatch 不执行任何 Shell 视图操作，前端只消费正常数据事件。
10. 从 Bus 连接绑定 caller identity，写入 Task/Message 来源和因果字段；OperationReceipt 按 caller+operation+key 去重，跨插件同 key 不冲突，同插件重试返回原结果。

**验收**

```bash
go test ./internal/plugins/chat -run 'TestTaskRuntime|TestTaskAPI|TestDispatch|TestQueue|TestDraft' -count=1
```

- Task A 运行不阻塞 Task B；Task A 的消息/实时帧不会进入 Task B。
- 同一 Task 的 Role/session 可续接，不同 Task 即使 Role 相同也新建 session。
- ended 后所有写入路径统一失败，历史分页仍正常；programmatic 与 UI 写入都返回 task_ended，title/summary worker 不阻塞 ended。
- 外部调用 dispatch 与 UI 发送产生同一种 Message/Turn/session/queue 结果。相同 caller/key 重试不重复，不同 caller 同 key 不冲突；调用前后 active Chat/Task、pane、草稿和滚动位置均不被后端主动改变。

### Task 3：实现三层 Context、Task Title 和滚动 Summary worker

**文件**

- 重写：`internal/plugins/chat/context.go`
- 修改：`internal/plugins/chat/summary.go`
- 修改：`internal/plugins/chat/runtime.go`
- 修改：`internal/plugins/chat/hindsight.go`（保持 bank 不变，补 Task 来源元数据与注释）
- 新增：`internal/plugins/chat/task_title.go`
- 新增：`internal/plugins/chat/task_summary.go`
- 新增：`internal/plugins/chat/task_title_test.go`
- 新增：`internal/plugins/chat/context_task_test.go`
- 新增：`internal/plugins/chat/task_summary_test.go`

**步骤**

1. 用 `buildTaskContext(chatID, taskID, roleID, query)` 替代 DAG bridge/context 的新 session 构建；取消 head-node、lineage 和 merged-through。保留 `buildHindsightRecallSection(chatID, ...)`，只传 Chat ID，不把 bank 改成 Task 级，也不禁止重置前 memory。
2. 项目层查询最近 50 个 ended 且具有 completed final summary 的 Task；当前 Task 层查询最近 3 个已结束 Turn 之前的 Turn Summaries；原文层只查询最近 3 个已结束 Turn 的 user/assistant Messages，并按时间加入窗口内 orphan user messages。
3. 从摘要选择中排除最近原文 Turn，避免同一 Turn 同时以 summary 和 raw 重复出现。
4. 保留现有预算和最终 `capContextBytes`，把 coverage 改为 Task/Turn 两级实际纳入、省略和失败信息。
5. Role 切换桥仅处理当前 Task 自上次活动以来的新增 Turn；不引入其他 open Task。
6. turn 完成路径先调用 Decision 解析，再生成 Turn Summary；Turn Summary 成功后按 Task ID 进入滚动 Task Summary 队列，保证摘要不包含有效 marker 内容。
7. 首个 query intake 同步设置 20 字临时 title，再异步调用 Viewer active LLM；用 `title_query_message_id + title_revision` 做幂等和 stale response guard，手动改名后迟到结果必须丢弃。
8. Task Summary worker 按 Task 串行、coalesce 新 Turn，以前一版摘要和未覆盖 Turn Summaries 滚动更新，使用 `source_turn_ids + summary_revision` 防止乱序覆盖；ended 时直接置 `final=true,status=pending`，完成后才写 `status=completed`。失败保留旧文本和准确 coverage。
9. `turn-summaries:retry` 只接受 missing/failed，已完成返回 `summary_immutable`；`task-summary:retry` 只处理升级后新 Task 的 missing/failed 摘要。不存在 legacy Task 摘要回填、启动扫描或旧消息读取。
10. Hindsight retain 仍写原 Chat bank，可在 metadata 增加 `task_id`；recall 不按 Task 过滤旧 memory。更新“bank 只是可由 SQLite 重建的派生索引”的旧注释：重置后它是独立保留的 Chat 长期记忆，SQLite 消息清零不影响其存续。

**验收**

使用 fake LLM 捕获真实 prompt，断言：

- 有最近 ended Task 的 completed final summary、当前 Task 较久 Turn 摘要、最近 3 个 Turn 可见原文；
- 没有 thinking/tool_call/tool_result/command/file_change；
- 没有其他 open Task 原文，没有 pending/failed/non-final 摘要，没有重复 Turn；
- 超过 50 条或预算时有准确 coverage；
- 普通 resume 不重复完整 context，force-new 会纳入最新 final Task Summary；
- 首 query 临时 title 不等待 LLM，重复 title 可共存，LLM 成功只覆盖 query/default title，手动改名后的迟到响应被丢弃；
- 滚动摘要只读取前一版和新增 Turn Summaries，revision/空洞/失败不丢覆盖范围；completed Turn Summary 不可静默改写，运行中摘要不进入其他 Task，ended 后只有 final completed 版本可共享；
- Hindsight recall 只访问当前 Chat 的原 bank，重置前 memory 在启用 recall 时仍可出现，另一 Chat 的 memory 永不出现。

```bash
go test ./internal/plugins/chat -run 'TestTaskContext|TestTaskTitle|TestTaskSummary|TestTurnSummary' -count=1
```

### Task 4：Task-first Side panel、独立 Tasks 页和单 Task 时间线

**文件**

- 修改：`frontend/src/plugins/chat/types.ts`
- 修改：`frontend/src/plugins/chat/index.ts`
- 修改：`frontend/src/plugins/chat/ChatPane.vue`
- 新增：`frontend/src/plugins/tasks/index.ts`
- 新增：`frontend/src/plugins/tasks/TasksPane.vue`
- 新增：`frontend/src/plugins/tasks/TasksTable.vue`
- 新增：`frontend/src/plugins/tasks/taskNavigation.ts`
- 新增：`frontend/src/plugins/chat/taskViewState.ts`
- 修改：`frontend/src/plugins/chat/chatCache.ts`
- 修改：`frontend/src/plugins/chat/inputDispatch.ts`
- 修改：`frontend/src/plugins/chat/queuedMessageEdit.ts`
- 修改：`frontend/src/plugins/chat/ComposerBox.vue`
- 修改：`frontend/src/stores/inputSessionState.ts`
- 修改：`frontend/src/stores/inputSessions.ts`
- 修改：`frontend/src/plugins/chat/viewInstances.ts`
- 新增测试：`frontend/src/plugins/tasks/tasks.test.ts`
- 新增测试：`frontend/src/plugins/chat/taskViewState.test.ts`、`inputDispatch.test.ts`

**步骤**

1. 把所有前端 `branchId/branches/activeBranchIds/name` 工作线语义改为 `taskId/tasks/activeTaskId/title/pinned`；所有 key、RPC 和导航只使用 Task ID，title 重复不作为异常。
2. Side panel provider 从 Chat entries 改为 current/running/pinned open Task entries，Task title 为主、Chat 为次级信息，合并去重；running 状态由实时帧更新，ended/pin 变化立即移除，不加载消息。当前 open Task 未 pin/running 时临时插入。Tasks/Decisions 通过显式 `createDockActions` 提供全局入口和 badge，不依赖 singleton 自动 Dock 规则。
3. ChatPane 只渲染 Active Task，顶部显示 `Chat / Task`；删除全部/Branch/open-task tags、merge/archive 和内嵌 Task list。每个 view instance 持久化自己的 `activeTaskId`，Side panel 只跟随默认视图。
4. Tasks 页实现 Chat/status/running/pinned/summary/decision 筛选、搜索和每页 50 条游标分页；桌面 table、手机 cards 共用同一份元数据，点击行打开默认 Chat view 并设置 Task ID，不展开消息；行操作提供 pin/end 和 missing/failed summary 的 retry。
5. 消息缓存键、订阅归属和 RPC generation 同时包含 Task；实现 before/after/around、每 Task 阅读锚点和有限 DOM。切回 Task 恢复锚点，新消息只提示不自动滚动，非 Active 流式帧使对应缓存失效而非无限 buffer。
6. 切换 Task 时保存旧锚点、flush 旧草稿、取消旧视图待回写请求并加载新 Task；旧 generation 响应不得写入新 Task。
7. Composer、voice、queued edit、force-new、raw、continue 和 optimistic echo 都冻结发送时的 Task ID；慢响应不得把草稿/结果写到后来激活的 Task。
8. ended Task 显示完整只读历史并禁用 Composer，提供新建/Tasks 页入口；ended 后 Side panel 删除该 Task 且不自动跳转或滚动。
9. 保留现有消息/活动交错、紧凑 UI、thinking/tool_result 展示和单一消息滚动上下文，不恢复任何底部 tags。
10. Chat instance adapter 的 `open` 接受 `{chat_id,task_id}`，复用正常 Active Task/view instance 逻辑；本地 `ctx.shell.open` 和外部 `shell:<conn>:open` 都走这里，programmatic create/dispatch 本身不隐式调用 open。
11. 升级 Chat 前端存储 namespace，清除/停用旧 Branch message cache、activeBranchIds、旧阅读锚点、草稿和 optimistic/queued 状态；首次打开只从服务端新默认 Task hydrate，不得从 IndexedDB/localStorage 显示旧消息。Theme、layout、typography 等全局 UI 设置保留。

**验收**

```bash
cd frontend
npx vitest run src/plugins/tasks/tasks.test.ts src/plugins/chat/taskViewState.test.ts src/plugins/chat/inputDispatch.test.ts
npx vue-tsc --noEmit
```

覆盖：Side panel current/running/pinned 去重与 ended 移除、重复 title 导航、Tasks 分页筛选、around 锚点恢复、发送中切换、慢草稿回写、queued edit、跨 Task 实时帧、ended 只读和关闭重开恢复 Active Task。

### Task 5：实现 Decision marker、持久化、原 Task 回复和取消

**文件**

- 新增：`internal/plugins/chat/decision.go`
- 新增：`internal/plugins/chat/decision_parser.go`
- 新增：`internal/plugins/chat/decision_test.go`
- 修改：`internal/plugins/chat/models.go`
- 修改：`internal/plugins/chat/store.go`
- 修改：`internal/plugins/chat/runtime.go`
- 修改：`internal/plugins/chat/dispatch.go`

**步骤**

1. 先实现纯函数 `parseDecision(finalText string) (spec, exactSpan, warning, ok)`，不接触 DB/dispatch；逐行 scanner 与字段校验分离，不用正则解析正文，不引入 YAML/XML/JSON dependency。
2. 写 parser table tests：
   - 合法：有/无 option、value 中额外冒号/`#`/引号/反斜杠、CRLF、空行、任意字段顺序、display/tone 缺省或非法时回退；
   - 字段异常：缩进 key、全角冒号、无冒号行、多行 question、未知 key/version/kind、重复 singleton、空 question/option、超长/超项、非 UTF-8；
   - 边界异常：closing 后有正文/代码围栏、标签缩进/属性/尾随字符、多个/嵌套/缺开闭标签、代码块内 marker、未闭合代码围栏、前序坏 marker 不得触发向后/向前恢复；
   - 展示：无效核心 marker 不创建 Decision 且一个字符也不隐藏；有效 marker 只隐藏 exactSpan；CRLF/CR 下 offsets 仍指向原始 Message.Text 的正确 byte 范围；重复 option 保持原顺序，不去重、不互相覆盖。
3. 注册 Decision 模型、索引和唯一 `source_turn_id`，同时保存 `source_message_id` 与原始文本 byte offsets；写入 Decision 与对应 `decision` MessageBlock 后再发布事件，同一 Turn 重放解析不产生第二条。
4. 在 Turn 完成且存在最终 assistant 文本时同步解析；Turn Summary worker 在解析后运行。
5. post-prompt 放在现有 workspace/chat/role prompt 拼接层，给出与 v1 grammar 完全相同的短模板和规则；raw 不注入。
6. 实现 `decisions:list/get/resolve/dismiss`；list 支持 pending/history、cursor 和按 Chat/Task 过滤。
7. resolve 原子 claim，并复用内部普通 dispatch admission；请求只接受 `decision_id + text`，目标从数据库读取。
8. 用 `decision:<id>` 作为 dispatch 幂等键；只对 admission 前失败允许重试，对重启遗留 dispatching 按 receipt/dispatch 对账。admission 后的 provider/Turn 失败保持 resolved，不自动重发。
9. 原 Task 普通 dispatch 按“显式 decision_id → 唯一 pending 才自动关联 → 多 pending 不猜测”处理；Task ended 自动取消 pending Decision，dismiss 只关闭卡片，不向 Agent 发消息。

**验收**

```bash
go test ./internal/plugins/chat -run 'TestDecision' -count=1
```

必须实际验证：marker Turn 正常结束且不等待；Decision 出现在原 Task；回复生成同一 Task/Role 的下一条用户 Turn；provider session 延续；重复点击/重试不会重复 dispatch；多 pending 普通回复不错误关联；admission 后 provider/Turn 失败不重开、不重发；ended 后回复被拒绝。

### Task 6：实现统一 Messages/Decisions Pane

**文件**

- 新增：`frontend/src/plugins/decisions/index.ts`
- 新增：`frontend/src/plugins/decisions/DecisionCenterPane.vue`
- 新增：`frontend/src/plugins/decisions/DecisionCard.vue`
- 新增：`frontend/src/plugins/decisions/decisionInput.ts`
- 新增：`frontend/src/plugins/decisions/types.ts`
- 新增：`frontend/src/plugins/decisions/decisionInput.test.ts`
- 修改：`frontend/src/plugins/chat/ChatPane.vue`，渲染内联 Decision block。

**步骤**

1. 注册一个全局 Messages/Decisions 入口，pending 数量作为 badge；默认按最近请求时间倒序显示 pending，历史 Tab 可看 resolved/dismissed/cancelled。
2. 卡片显示问题、来源 Chat/Task/Role、时间、状态、可展开来源回复和回复输入框。
3. 选项支持一键回答，也支持带入输入框修改后回答；自由文本与选项文本走同一个 resolve RPC。
4. 回复时冻结 decision ID 和文本；成功后显示原 dispatch ID，可点击跳转并激活原 Chat/Task；admission 失败保留文本并允许重试；admission 后的 Turn 失败只更新原 Task，不把卡片改回 pending。
5. 原 Task 内联卡片与全局中心订阅同一 Decision 状态；一处回答，两处同时更新。
6. 不为 Decision Center 创建独立 InputSession 目标或 Agent session；它只是一组临时回复编辑器。

**验收**

```bash
cd frontend
npx vitest run src/plugins/decisions/decisionInput.test.ts
npx vue-tsc --noEmit
```

覆盖选项、修改、自由回复、pending→dispatching→resolved、失败重试、跨面板同步和跳转原 Task。

### Task 7：Programmatic Shell API、删除 Loop、文档与黑盒清理

**文件**

- 删除：`internal/plugins/loop/` 整目录及测试。
- 修改：`internal/pluginapi/registry.go`、`internal/pluginapi/pluginapi_test.go`，移除 Loop 注册。
- 删除：`frontend/src/plugins/loop/` 整目录。
- 修改：`frontend/src/shell/bus.ts`，连接后注册本连接 Shell RPC。
- 新增：`frontend/src/shell/programmatic.ts`、`frontend/src/shell/programmatic.test.ts`。
- 修改：`scripts/smoke_gateway.py`、`scripts/smoke_chat.py`、`scripts/smoke_all.sh`。
- 删除：`scripts/smoke_loop.py`。
- 新增：`scripts/smoke_task_api.py`，用真实外部 Python SDK 调用 create/dispatch。
- 新增：`scripts/smoke_decision.py`；按现有 mock agent 方式增加可控 Decision 回复。
- 新增：`scripts/smoke_chat_level_migration.py`；只在临时目录构造旧库并验证 Chat-level 设置迁移，不接触真实 Viewer 数据。
- 修改：`architecture.md`。
- 修改并 bump：`docs/plugin-framework.md`、`docs/plugin-development.md`。
- 删除：`docs/loop-plugin.md`。
- 用定稿的 Task/Decision/Programmatic API 合同替代 `docs/chat-branch-dag-plan.md`，避免保留互相矛盾的现行设计。

**步骤**

1. 完整删除 Loop 后端、前端、注册项、配置、文档、smoke 和数据文件依赖；不创建空 Loop 替代品，不读取或恢复 `loop.sqlite3`。同步删除只服务 Loop 的 automation lease/gate 和恢复逻辑，普通 programmatic dispatch 不需要它。
2. 在 Shell 连接建立后，按 `plugins:_:list` 中的自身 conn 注册 `shell:<conn>:open/panes:*`；RPC 只调用 `openShellInstance` 和现有 pane helpers。没有目标 conn 不注册默认频道，目标离线自然 no_route，非目标浏览器收不到操作。
3. Chat adapter 处理 `{chat_id,task_id}` 并复用正常实例恢复/激活；测试 create→dispatch→可选 open、focus、close，以及两个 Shell 连接互不误控。面板关闭不影响后台 Task 运行。
4. 删除运行时代码中的 History DAG、merge/closure/source archive 依赖及无用测试，不做 Branch RPC 兼容层。
5. 更新插件框架/开发手册中的 Task create/dispatch、caller identity、OperationReceipt、Shell 每连接 RPC、Chat/InputSession、草稿、队列和实时事件合同，版本号递增，只记录定稿结论。
6. 更新 architecture 的存储、context、session、Hindsight Chat-bank 存续边界、programmatic API、事件和故障定位地图，明确历史重置不删除外部长期记忆。
7. 黑盒覆盖：外部插件 create→dispatch（与 UI 发送同构的一次 Message/Turn）→幂等重试；重复 title Task 按 ID 独立运行、首 query title、滚动/final summary、三层 context、ended 只读、Decision 回复去重，以及 Chat-level 设置迁移后不二次清空。
8. 搜索确认没有现行代码/文档仍把 `branch_id`、`merge`、`archive` 当 Task 机制，也没有 Loop 注册、UI、RPC 或恢复路径；历史提交说明不作为清理对象。

## 4. 完整验证

每个阶段运行对应局部测试；全部完成后运行：

```bash
gofmt -l . && go build ./... && go test ./...
cd frontend && npx vue-tsc --noEmit && npm run build
cd .. && bash scripts/smoke_all.sh
```

标准检查全部通过后，先在隔离临时目录做 Chat-level migration 演练：

1. 构造含旧主线、Branch、Message、Turn、Session、Queue 和旧 Loop 文件的测试数据目录，记录 Chat/Role/routing 设置，备份后以只读旧库运行一次性 migration。
2. 验证 Chat ID 和 allowlist 设置不变，每 Chat 只有一个全新 ID 的默认 open Task；旧消息、Branch、Session、Queue、Summary、Decision 全部为零，Loop 不加载且不创建新 loop 文件，旧内容表不回填。
3. 验证浏览器旧 cache namespace 不再使用；SQLite 新 session 没有任何旧 Message/Task 原文，但 Hindsight 仍使用原 Chat bank，启用 recall 时可使用重置前 memory，其他 Chat bank 不被请求。
4. 写入一条新消息并再次启动隔离实例，验证新消息和新 Task 保留，migration 不会再次执行。

真实 Chat 库的迁移是所有实现与验证完成后的最后一个独立操作：先备份，再由用户明确确认执行；开发过程中不触碰真实旧库，不启动前端 dev server，不自行重启 Viewer。

## 5. 最终人工验收清单

- 升级后原有 Chat 的名称、Root、prompt、Roles 和 routing 设置仍在，但旧消息/Task 不可见；每 Chat 从唯一的新默认 Task 开始，重启不会再次清空。同一 Chat 的旧 Hindsight memory 仍可在新 Task 中 recall，不同 Chat 的 memory 不混用。
- Side panel 只显示 current/running/pinned open Tasks；同一 running+pinned Task 不重复，主页面没有 Branch/open-task tags。
- 新建 Task A/B 后首 query 立即形成短 title，LLM 异步改善；A/B 即使 title 完全相同，点击和输入仍按 ID 分流，手动改名不被迟到 LLM 结果覆盖。
- A/B 可同时运行；各自历史、草稿、queued message 和 Role/session 独立；Tasks 页可分页筛选并跳回任一 Task。
- ended A 后仍可打开全部消息，但不能输入；A 从 Side panel 移除，最终摘要未完成前不进入其他 Task context。
- 新 Task C 的首个新 session 能看到 A 的 completed final Task Summary，但看不到 A/B 的整段原文或运行中摘要。
- C 内超过最近 3 个 Turn 后，新 session 使用较久 Turn Summary + 最近 3 个 Turn 可见原文，原文没有 thinking/tool result；长历史通过 before/after/around 按需加载并恢复阅读锚点。
- Agent 输出 Decision 后 Turn 正常结束；全局 Messages Pane 立即可见。
- 选择选项或修改后回复，原 Task 出现普通用户消息并续接原 Role/session；全局和原 Task 卡片同时变为已回答。
- 重复点击、刷新和重启不会造成重复回复；ended Task 的 pending Decision 已取消。
- 外部插件可用 SDK create 一个 Task，再 dispatch 一条与输入框发送完全同义的消息并调用 Agent；重复 key 不产生重复数据。Shell 仅随正常数据事件刷新，不主动切换视图或面板。
- 调用方可从 plugins:list 选择目标 Shell conn，编程打开并聚焦对应 Chat/Task，随后关闭面板；另一个浏览器不受影响，关面板不停止后台 Task。Loop 不再出现在注册表或 UI。
