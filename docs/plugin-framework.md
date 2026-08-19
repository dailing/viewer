# Viewer Plugin Framework 设计文档

> 状态：**草案 v0.39**（2026-08-19）。本文档是架构决策的唯一权威来源，逐节评审、迭代定稿。只记录已决定的内容，不记录决策过程。**线路级协议规范见 `docs/plugin-protocol.md`（Phase 0，冻结后写码）。**
> v0.39 变更：**§8.4 阶段 B 落地 + 插件管理面定稿**——①前端资产管道实现：RPC `gateway:_:assets:push` 支持 one-shot 与三段式 chunked（`begin`/`file`/`commit`，超帧文件以 `append:true` 分片续传），资产 id 绑定调用方 origin（插件只能发布自己的 bundle）；内容寻址库存 `<data>/plugin-assets/<id>/<hash>/`（保留最近 3 代、`entry.json` sidecar、启动时从磁盘重建），gateway 同源 serve `/plugins/<id>/assets/<hash>/<file>`（immutable 缓存）；全量映射发 `plugins:_:assets` mailbox；`gateway:_:assets:remove {id}` 删除资产。②shell 动态加载：`index.html` 静态 import map 把 `vue`/`pinia` 钉到稳定 vendor entry（`src/externals/*.ts`，与 app bundle 同一次 Rollup 构建、共享 chunk = 同一单例）；loader 订阅 assets mailbox 对账：新 id `import(url)`、hash 变即热重载（先 deactivate 再 import 新 URL，无页面刷新）、条目消失即逻辑卸载（注销组件/Dock provider，已开 pane 回落 unknown-type placeholder）；import 失败按 hash 冷却 30s。shell 额外识别伴随样式表：assets 文件列表含 `frontend.css` 时 import 前自动注入 `<link>`、卸载时移除（插件构建用 Vite library mode + `cssCodeSplit:false`）。③**插件管理 = supervisor 扩展 + 默认核心前端插件 `plugin-manager`**（singleton pane，§8.9 版式）：RPC `supervisor:_:list/upsert/delete/start/stop`；registry entry 增加 `command`（自定义启动命令行，相对路径相对插件目录解析；`--kernel-ws` 恒追加）、`name`、`autostart`（**默认 false，手动启动**）；失败策略定为**连续崩溃重试 3 次后转 `broken` 待手动启动**，稳定运行超过 60s 重置计数（旧 60s 窗口内 5 次熔断语义废弃）；delete 顺带 `assets:remove` 完成前端热卸载。④manifest 展示标准：hello manifest 可选 `name`/`icon`（bootstrap-icons class）/`description`，经 `plugins:_:list` 原文透传（Raw passthrough，无协议变更），供管理面板与 shell placeholder 使用。⑤SDK：`sdk/go` `busclient.PushAssets`、`sdk/python` `push_assets`（+ `Plugin.push_assets`）、`sdk/ts` `plugin.ts`（`definePlugin` + ctx/Dock/PaneChrome 类型，与 shell 结构化兼容）；inbox-RPC 回应助手（`Object`/`Cancelled`/`Respond`/`RespondError`）下沉到 `sdk/go/busclient`（`internal/plugins/pluginrpc` 变为同名委托），外部 Go 插件不再依赖 internal 包。开发者手册见 `docs/plugin-development.md`。
> v0.38 变更：**RPC no_route fail-fast + 丢帧可追查（§5.3）**——broker 对零订阅者的请求帧（payload 带 `_reply_to` + `_corr`）立即合成 `no_route` 错误响应回 `_reply_to`，调用方秒级失败（此前干等 30s 客户端超时，重启竞态下 blocks:list 等 RPC 必然中招）；普通事件零订阅者仍静默丢弃。慢消费者队列丢弃带 `_corr` 的帧逐条 WARN（带 corr）。inbox 约定本身不变，broker 仍无 RPC 路由子系统。
> v0.37 变更：**ACP 权限请求自动批准 + 一次性新会话开关 + 构建并重启**——①viewer 的 ACP 客户端（`internal/acp`）现在应答 agent→client 请求：`session/request_permission` 一律自动批准（优先选 `allow_always`，否则第一个 allow 类选项；viewer 无审批 UI，用户裁决为全部放行），其它未实现方法立即回 `-32601` 避免 agent 干等超时；②`chat:_:dispatch` 增加一次性字段 `force_new_session`（A.7）：该消息的所有选中 role 跳过内存 runtime 复用与 `role_sessions` 恢复，强制新建 agent session（旧 session 常驻不回收），前端 composer 加一次性图标开关（发出后自动复位）；③gateway 增加 `POST /api/admin/build-restart`（C4）：后台运行 `web/build-release.sh`（构建期间服务不中断），成功才走 v0.34 优雅重启路径，失败只记日志（`/tmp/viewerd-build.log`）不动运行中的二进制；dock 设置菜单加「构建并重启」按钮（先等网关下线再等恢复后刷新页面）。
> v0.36 变更：**catalog 协议发现改按需触发（废除 v0.35 的 30 分钟周期刷新）**——`CatalogCache.StartLoop` 改 `StartOnce`：启动时一次性后台发现，无 ticker；chat 新增扇出 RPC `chat:_:agent-catalog-refresh`（并行调用各 agent 插件的 `<plugin>:_:catalog-refresh`，成功响应直接并入聚合 catalog，按 `chat:_:agent-catalog` 同构返回，失败插件保留旧值）；前端 chat-manager 的 Roles/Routes 面板打开时调用该 RPC——修改 provider/模型配置后重开面板即得最新清单。
> v0.34 变更：**gateway admin restart 定稿（C4）**——http-gateway 暴露 `POST /api/admin/restart`（单二进制优雅自重启）：spawn 同参数新进程（追加 `--wait-pid <self>`、Setsid 脱离、日志转 `/tmp/viewerd.log`）+ 500ms 后 SIGTERM 自关；关闭路径排空 running turns（≤10s）后退出，新进程等旧 pid 消失（≤30s）才 bind 端口——无交接缺口、无端口竞争；spawn 失败保持旧进程运行并返回 500。dev 单二进制由此获得与 deploy/supervisor 等价的开发重启路径。
> v0.33 变更：**仓库结构定稿（Go 主线落地）**——`next/`（Python 参考实现）与 `next-go/` 目录删除：Go 主线（`cmd/`、`internal/`、`web/`、`examples/`、`scripts/`）上移至仓库根，单一 Go module（`viewer`）自根构建；SDK 按语言归入 `sdk/`（`sdk/go` = protocol 帧类型 + busclient，`sdk/python` = Python BusClient/Plugin，`sdk/ts` = @viewer/bus-sdk）；前端在 `frontend/`。测试即规格：`scripts/smoke_*.py`（Python SDK 黑盒）+ `sdk/ts` vitest 直接跑 Go kernel binary（`VIEWER_KERNEL_BIN` 可覆盖）。
> v0.32 变更：**chat 前端会话缓存（A.7）**——ChatPane 卸载后已加载历史存模块级缓存（LRU 24 聊天；每聊天 messages ≤2000 / blocks ≤4000，超限丢最老半页并重置上翻游标）；重开聊天秒渲染缓存 + 只拉增量：`chats:list` include_messages 增加 `after`/`after_id`（复合游标 `(created_at, id)` **含边界**，升序返回 + `has_more`，边界行重拉以便前端用最终文本替换可能仍在流式的缓存副本），`blocks:list` 取缓存内最大 `occurred_at` 为 after；合并按 id 去重/替换（增量行严格更新），工作区/roles 同增量刷新，聊天删除时逐出缓存；重复打开无新消息 ≈ 16 KiB（chats 列表 + workspace + 空增量），已加载老页零流量。
> v0.31 变更：**chat 历史懒加载（A.7）**——对齐生产版"最新优先 + 向上翻页"：`chats:list` include_messages 支持 `before`/`before_id`/`limit`（复合游标 `(created_at, id)` 严格小于，返回 `has_more`），`blocks:list` 支持 `after`/`before` 时间窗 [after, before) ms（按已加载消息跨度分窗取块，零缺口零重复）；前端首屏只取最新一页 + 覆盖时间窗的 blocks 并滚到底，滚动到顶加载更早页（插入后按 DOM 高度差恢复滚动位置，顶部显示加载/尽头指示），激活/聊天变更改 merge-refresh 不重置分页。
> v0.30 变更：**chat 消息时间线渲染**（A.7）——blocks 数据面上总线（RPC `chat:_:blocks:list` + 事件 `chat:{id}:block`），前端一 turn 一盒、文本段 markdown（KaTeX/hljs 行号/mermaid）+ 工具活动折叠行严格按时间交错、盒顶 info 条，样式经 `--markdown-*`/`--syntax-*` CSS 变量主题可定制。
> v0.28 变更：**viewer.voice 插件契约定稿**（A.8）——语音输入走总线：RPC `voice:_:start`/`cancel` + publish `voice:{rec}:chunk`（base64 音频）/`voice:{rec}:stop` + 事件 `voice:{rec}:event`（ready/processing/partial/committed/final/error）；后端插件只做外部 voice-service 的 WS relay（C1 `plugins.viewer-voice.*` 注入 service_ws/model/language，内嵌 ASR 后端不移植）；前端新增无 pane 的 `voice` 插件（voiceStore + VoiceInputButton 移植自生产版），chat composer 直接引用。
> v0.27 变更：**dock overlay 展开 + 设置入口 + 管理面板版式统一**——①dock hover 展开改为 overlay（右侧 workspace 不被压缩 reflow），悬停延迟可配（默认 500ms，localStorage 持久化）；②dock 底部总线连接指示移除，原位换设置按钮；③管理面板统一 master-detail 版式（§8.9）：左窄 list 只显名字 + 固定新建按钮，删除/pin 等动作收进右栏 configuration；④路由编辑器改版：候选每行一条、agent/provider/model 为可点击文本（非 select 下拉样式）、候选间分割线中央"+"插入、拖拽排序取代上下移按钮、内嵌 parameters 框移除改为右栏底部整体 JSON 预览。
> v0.26 变更：**dock 自动展开**——默认纯图标窄条，hover 持续 ≥500ms 展开显示每个条目名字，移出即收回；无开关、不持久化（§8.8）。
> v0.25 变更：**pane chrome 注册机制**——移植老版 paneToolbar：插件经 ctx 注册 title/status/actions/controls，由 shell 的 pane title bar 统一渲染，插件不再自渲染标题栏；chat dock 实例列表收窄为 pinned ∪ 已开（§8.8）。
> v0.24 变更：**chat 体验与 shell 语义定稿**——①agent 契约加 `turn_id` 贯穿（`start`/`prompt` 携带、`event`/`turn-ended` echo，chat 按 turn_id 解复用，删 session→turn 映射）；②idle reap 否决（agent 子进程常驻不自动回收，用户随时重开网页须看到原状）；③shell 两条行为定稿（§8.7）：`openInstance` 不再覆盖已占用 pane（已开→聚焦；有空 pane→用之；否则自动 split，默认垂直）；dock singleton 条目引入 **pin**（pinned 常驻，可切换）；④chat 前端拆为 `chat`（ChatPane + 实例 dock 列表）与 `chat-manager`（singleton 管理面板：聊天/Roles/路由三 tab）两个前端插件，后端 `viewer.chat` 单插件不变；⑤roles/routing policies 从 C1 迁入 chat 插件 DB（领域数据归 source-of-truth 插件），C1 只留插件级配置（agents 映射/LLM router/预算）。
> v0.23 变更：**agent 实现拆为独立 headless 插件族**——`viewer.agent-hermes`（ACP）/ `viewer.agent-codex`（app-server）/ `viewer.agent-opencode`（ACP，新建）为无 UI 单实例服务插件，统一总线契约：RPC `start`（`target={agent,provider,model,parameters}` opaque 透传 + cwd + session_id?）/ `prompt`（立即 ack，turn 异步）/ `cancel`；事件 `event`（seq + kind + raw_json + 已解析 block 同帧）/ `turn-ended`；retained mailbox `catalog` 公布 agent-provider-model 清单。chat 瘦身为纯编排（roles/routing/relay/DB/summaries），经 C1 `plugins.viewer-chat.agents` 映射发现 agent 插件并聚合 catalog；**profile = routing policy**（既有模型不造新概念）：role 挂 policy，candidates = 有序三元组参数包，turn 开始按序解析首个 enabled 且在线者，`auto_failover` 接回失败按序尝试语义；`role.provider/model` 降为迁移输入。opencode 由"不实现"（v0.22）转为新建（ACP 第二租户）。
> v0.22 变更：**chat 数据面与 provider 面定稿**——①原始事件全量保留：ACP/codex 两个 driver 的每条 session update 原文落 `turn_events`（append-only，per-turn seq），解析结果单独存 `message_blocks`（归一化 kind：agent_text/thinking/tool_call/tool_result/file_change/command/other），`messages` 表仍是用户可见文本视图；②接力定案：插件内顺序执行即最终形态，worker 队列/lease/failover 永久废弃；③codex provider 仅 app-server（原生协议库 `internal/codexserver/`），旧 codex-acp 路径不移植；④历史迁移把生产版 `super_workspace_messages.raw_json` 幂等迁入 `turn_events`。
> v0.21 变更：**file-service 增加目录列表能力**——新增 RPC `file:_:list`：输入 `{path}`，输出 `{path, entries[]}`，entry 字段对齐生产版 FileEntry（name/path/type(file\|directory\|symlink\|other)/size/mtime/mime/is_dir/is_symlink/link_target），目录优先 + name 字典序排序，一次性全量返回不分页，隐藏文件过滤归 file-service 插件配置。用途：viewer.files 文件树（A.5）的唯一取数通道。
> v0.20 变更：**分发形态定稿**——核心集（内核 + core plugins + 前端）用 **Go** 实现为**单一静态二进制**（前端经 `go:embed` 内嵌），第三方/外挂插件仍以独立进程连总线、语言无关（松耦合不变）；**数据库访问一律使用 ORM**（Go 侧定 GORM + 纯 Go SQLite 驱动 modernc.org/sqlite，保持 CGO 关闭与交叉编译能力），禁止裸 SQL；现有 Python 栈（`next/`）转为协议参考实现，`next-go/` 为新主线（§17）。
> v0.3 变更：插件 I/O 改为 **slots/emits 固定契约**，bindings 只存 slot→source 映射（删除 action）；**内核纯化为消息系统**，config/instance store/file/gateway 降为 core plugins；传输层定为 **WebSocket 单一栈**。
> v0.4 变更：§8 扩写为完整的**前端插件机制**（四层结构、instance 挂载流程、两阶段加载：build-time 懒加载 → 运行时 ESM + import map）；前端加载问题从待决议中勾掉。
> v0.5 变更：新增 **§14 插件包格式与开发流程**（目录包、双 SDK、external 加载、standalone attach 调试模式）；后续章节顺延。
> v0.6 变更：**动态注册定稿**——后端注册 = `hello` 握手；前端 = 事件驱动运行时加载（§8.6）：订阅 `plugins:list` mailbox → dynamic import → activate。
> v0.7 变更：**hello 内联完整 manifest**；前端资产改走**内容寻址资产库**（注册时复制/上传，shell 只认 gateway 同源 URL）；远程插件路径预留（§14.6）。
> v0.8 变更：**前端资产统一走 WS push**：所有插件 hello 后经总线 RPC push bundle 给 gateway（本地/远程同一机制，gateway 不再读插件目录）；内核只路由；资产是 §6.2 by-reference 原则的刻意例外。
> v0.9 变更：新增**附录 A 现状功能清单与插件映射**——全部 29 个后端模块、53 条路由、10 个 viewer、6 个 sidebar 面板逐一归档到具体插件，含 slots/emits 候选与迁移要点。
> v0.10 变更：新增 **bus-inspector 调试插件**（附录 A.10）与 broker **monitor 订阅**原语（§5.3）；排期提前至 Phase 2。
> v0.11 变更：**订阅完全开放**（删除 monitor 特权与 capability 声明）——任何插件可订阅任意 channel 含 RPC 帧与 `>` 全量，无权限层；框架不管插件依赖（作者声明、用户安装）；内核发布插件生命周期事件；broker 定位明确为 **NATS Core + replay window**。
> v0.12 变更：**砍掉 broker 事件历史**（replay window 引入即废）——broker 只保留 mailbox 最新值；历史归属 source-of-truth 插件（chat DB / terminal PTY ring / bus-inspector ring）；重连重开走 §8.3 "RPC 快照 + live delta"；定位修正为 **NATS Core + retained mailbox**。
> v0.13 变更：**instance config 更名 instance state**（状态驱动行为，与 C1 全局 configuration 解歧义）；**插件后端语言无关化**——启动 ABI 定为固定 cmdline 契约（可执行 `backend/run --kernel-ws ...`），manifest 删 `process.command`，Python SDK 降为便利而非必需。
> v0.14 变更：**runtime instance 双模式**（manifest `instancing: shared | per-instance`，chat 选 per-instance——**每 chat 一个进程**，worker 概念删除）；状态改**四层**（manifest / plugin config / instance state / view state）；启动 ABI 加 `--instance-id` + 恢复契约；§16-4（内核重启→存活重连）、§16-9（状态清理由插件负责）敲定。
> v0.15 变更：**instancing 双模式从框架删除**（v0.14 引入即废）——进程↔instance 映射下沉为插件内部实现；manifest 删 `instancing` 字段、ABI 回到单参数；supervisor 每插件只拉起一个 `backend/run`；插件可自行 spawn 子进程各自 hello（可选 `instance_id` 信息字段）；instance 唯一性/选举/恢复均为插件侧模式，框架零介入。
> v0.16 变更：**supervisor 下放为 core plugin**（`viewer.supervisor`/C0，内核只剩 broker + 连接注册表 + 唯一 autostart）；**RPC 定为 pub/sub 之上的 inbox 约定**（broker 无 RPC 路由子系统，帧类型收敛为 5 种）；§16-3（错误/超时/cancel 全是约定层）、§16-12（管理 RPC 归 C0）敲定。

> v0.17 变更：**Event vs State 发布准则定稿**（§5.6）——state（当前事实）走 mailbox `set` **完整自包含值**（replace 语义，broker 删除 delta 合并），消费者订阅即得、永不需为此发 RPC；event（追加流）走普通 publish 无留存，历史一律走生产者 source of truth + **显式分页 RPC 快照**。

> v0.18 变更：**协议三件套定稿**——channel 语法（§5.2：冒号分隔、前三层固定 `plugin:instance:message`、`*` 单字段通配 + 前缀隐式全匹配 + `>` 全量）；payload **partial 校验**（envelope/hello 等元信息强校验，payload 本体任意 JSON 不校验）；版本策略定为**不向后兼容**（严格相等 + 一次性迁移，§16-13）。§16-1/16-2/16-13 敲定，16-11（多机）暂缓。

> v0.19 变更：**Phase 0 开工**——§16 已敲定条目全部移出待决议清单（剩 16-5/16-6/16-10 + 16-11 暂缓）；新建 **`docs/plugin-protocol.md`**（线路级协议规范草案 v0.1，未冻结）：5 帧 schema、channel 匹配算法、mailbox 原子交接、RPC inbox 契约、close codes、交换时序图。

## 1. 背景与目标

- Viewer 演进为**微内核插件化 workbench**：
  - **内核 = 纯消息系统**：broker（publish 路由 + mailbox）+ 连接注册表 + 唯一 autostart（拉起 supervisor 插件）。不含任何功能逻辑与监督职能（§9）。
  - 一切功能都是**插件**，后端插件运行在**独立进程**；config store、instance store、file service、HTTP gateway 等基础能力也是插件（core plugins，必启）。
  - 所有交互——插件间、插件与内核服务、前端与后端——**全部经过同一条消息总线、同一种传输（WebSocket）**。
- 远期愿景：用户所有项目搬进一个 page；系统同时是一套 **event-based 自动化系统**；保留多机通信的扩展路径。
- 非目标：多用户、权限体系、第三方不可信插件市场、**插件依赖管理**（插件间依赖由作者在插件文档声明、用户自行安装，框架零介入，§14.5）。

## 2. 参照系

| 参照系 | 学什么 |
|---|---|
| **Qt signals & slots** | 插件 I/O 模型：slot（固定 handler 的输入点）/ signal（固定输出点）在代码中声明，connect（= binding）是数据（§7） |
| **Node-RED** | 同上：port + wire；wire 可序列化、可视化 |
| **i3 / sway** | 微内核 + IPC 的 workbench 形态 |
| **NATS** | subject 层级、通配、request/reply 语义对照系；远期多机平移目标 |
| **DBus** | service/signal/method ≈ 我们的 RPC/event/mailbox |
| **VS Code** | contribution points + `activate(ctx)` |
| **Home Assistant** | states + events 双轨、trigger-condition-action 自动化 |

**信任模型**：插件均为自写、全信任。进程隔离的目的是存活期管理与架构统一性，不是安全沙箱。

## 3. 总体架构

```
┌── Kernel 进程（纯消息系统）─────────────────────┐
│  Broker: publish 路由 + mailbox(retained)       │
│  连接注册表: plugins:_:list + lifecycle 事件    │
│  唯一 autostart: 拉起 viewer.supervisor 进程     │
└───┬─────────────────────────────────────────────┘
    │ WebSocket，统一帧协议（§5/§6）
┌───┴──────────────────────────────────────────┐
│ Core plugins（必启，协议上与普通插件无异）      │
│  supervisor │ config-store │ instance-store   │
│  file-service │ http-gateway │ automation(后期)│
├───────────────────────────────────────────────┤
│ 功能插件（进程）：chat runtime │ terminal │ git │ …│
└───────────────────────────────────────────────┘
    http-gateway │ 一条 WebSocket（多路复用全部 channel）
┌───┴─────────────────────────────────────────────┐
│ 浏览器 display 层（本身是插件）                    │
│ 薄 bootstrap → layout 插件 + 各插件前端模块        │
│ 按 instance dispatch 消息到对应组件（§8）           │
└─────────────────────────────────────────────────┘
```

**消息流闭环示例（terminal）**：PTY 出字符 → terminal 插件从固定 emit 点 `publish terminal:3:output`(delta) → kernel 路由 → gateway（代浏览器订阅）→ WS → 前端 dispatch 到 instance 组件。前端输入反向走 RPC：`request terminal:3:input` → kernel → terminal 插件固定 slot handler。

## 4. Message System：内核自建 broker

协议语义对齐 NATS（subject 层级 + 通配 + request/reply），保留远期平移能力。现在不引入外部 MQ：单用户 localhost 下 asyncio broker 是几百行，envelope 护栏（§5.4）完全自控。

broker 形态概括为 **NATS Core + retained mailbox**：语义对齐 NATS Core（其本身无状态），加一个 NATS Core 没有的 retained mailbox（每 channel 仅存**最新值**）。**broker 不保存事件历史**：mailbox 只有最新一条，event 纯瞬态。历史一律归属 source-of-truth 插件（chat → 插件 DB、terminal → PTY ring buffer、调试 → bus-inspector ring）；断连重开按 §5.6 准则重同步：state 重订阅 mailbox 即得当前值，历史走显式分页 RPC 快照 + live event。NATS JetStream 级别的持久流不需要。

两个一等原则：

- **订阅完全开放**（§5.3）：任何插件可订阅任意 channel pattern，无权限层；跨插件联动是一等用法。
- **生命周期事件上总线**：内核发布 `plugins:{id}:lifecycle`（activated / deactivated / crashed / restarted；instance 段即插件 id）与 `plugins:_:list` mailbox；gateway 等插件也发布自己的激活事件——统计、记录、user feedback 等消费方随意挂。

## 5. 消息协议

### 5.1 三个原语

| 原语 | 语义 | 用途 |
|---|---|---|
| **Event**（pub/sub） | 瞬态，"发生了一件事" | 触发器；`chat:42:turn-completed`、`terminal:3:output` |
| **Mailbox**（retained state） | 驻留"当前值"，新订阅者立即收到 | `chat:_:active`、`terminal:3:status` |
| **RPC**（request/reply） | id 关联的请求/响应——**pub/sub 之上的 inbox 约定，非 broker 机制**（§5.3） | 命令式调用、服务查询（`config:_:get` 等）、前端输入 |

### 5.2 Channel 与 envelope

```
channel:   plugin:instance:message[:group...]
payload:   一律 JSON
envelope:  { channel, value, ts, origin: {plugin, instance}, trace_id, depth }
```

- **固定三层**：field 1 = plugin 名，field 2 = instance 名，field 3 = message 名；第四层起插件内部自由 grouping（如 `gateway:_:assets:push`）。无 instance 概念的插件级 channel 用保留实例名 `_`（`plugins:_:list`、`chat:_:active`）；`_` 前缀为框架保留命名空间（`_inbox:*` = RPC 回复通道）。
- **通配匹配**：`*` = 单字段通配，可出现在任意层（`chat:*:status`）；pattern 字段数少于 channel 字段数时，**未指定的尾部字段默认全匹配**（前缀规则——`chat:42` 即该 instance 下的全部消息）；`>` = 匹配总线所有流量（零字段前缀的显式写法，bus-inspector 用）。
- **校验（partial）**：envelope 与 hello（内联 manifest）等协议元信息**强校验**（Pydantic / JSON Schema 皆可，实现自定）；payload 本体 = 任意 JSON **不校验**——slot/emit 声明的 payload 类型为标注性（文档与工具用途），不做运行时强校验。

### 5.3 帧类型

`hello`（插件握手：**内联完整 manifest**、协议版本——**严格相等，不向后兼容**（§16-13）、`managed` 标志、可选 `instance_id` 信息字段——插件 spawn 的子进程标识自己属于哪个 instance，§9）/ `publish` / `set`（mailbox 写）/ `subscribe` / `unsubscribe`（通配）——**仅 5 种**。**心跳复用 WebSocket 内建 ping/pong**（§6）。

**RPC = inbox 约定（broker 无 RPC 路由子系统）**：调用方生成一次性 reply channel `_inbox:{conn}:{corr}` 并订阅 → 请求作为普通 `publish` 发到目标 channel（payload 带 `reply_to` + corr）→ 被叫方把响应作为普通 `publish` 发到 `reply_to` → 调用方按 corr 匹配。超时由调用方 SDK 处理（默认 30s 可配）；取消 = 向目标 channel 发一条带 corr 的取消消息（被叫方自行决定是否响应）；错误分类 = 响应 payload 里的约定字段。SDK 的 `ctx.request()` 封装这全套——`request`/`response`/`error` 不是帧类型。**唯一 broker 介入点（v0.38）**：请求帧（payload 带 `_reply_to` + `_corr`）发布时零订阅者，broker 立即合成 `{ok:false, error:{code:"no_route"}, _corr}` 响应发回 `_reply_to`（fail-fast 兜底重启竞态，非路由——普通事件零订阅者仍静默丢弃）；慢消费者队列丢弃带 `_corr` 的帧时逐条 WARN（带 corr，此前只发 slow_consumer 通知、丢帧不可追查）。

> hello 必须内联完整 manifest 而非路径引用：standalone attach（§14.4）与远程插件（§14.6）场景下内核一侧没有插件的 plugin.json 文件；supervised 插件同样内联，保证注册路径只有一条。manifest 不含启动命令——入口是约定的可执行 `backend/run`（启动 ABI 见 §14.3）。

**订阅完全开放**：任何插件可订阅任意 channel pattern（含 `>` 全量通配），可见总线上所有流量——含其他插件的 event / mailbox 与 RPC 流量（inbox 约定的 publish，同样可见）。**无权限层、无 capability 声明**（localhost 全信任模型，§1）。防回声由消费者自行处理（如 bus-inspector 客户端过滤自身 origin）。**跨插件联动是一等用法**：插件 A 订阅插件 B 的事件/mailbox 即完成联动；订阅了没人发、request 了没人回，都是正常状态（松耦合）。

### 5.4 自动化三护栏

1. **死循环防护**：`trace_id` + `depth`，同 trace 传播超上限（默认 8）丢弃并告警。
2. **origin 标记**：每条事件带 `{plugin, instance}` 来源。
3. **回放风暴**：订阅默认只给当前值（BehaviorSubject 语义）；event 无历史可言，不存在 replay。

### 5.5 Retention 与背压

mailbox 只存**当前值**（无 ring、无历史）；`set` 语义为**整体替换**——producer 每次必须发**完整自包含值**，broker 只做 replace，不做 delta 合并、不做字段级 patch；慢消费者超队列上限丢弃并通知。

### 5.6 Event vs State 发布准则

每条总线消息必须先归类，两类语义不同、取数路径不同：

| | **State（当前事实）** | **Event（追加流）** |
|---|---|---|
| 定义 | 有身份、会被覆盖的"现在是什么"：instance status（idle/running/finished）、online 数、cwd、配置当前值 | 瞬态、只追加的"发生了什么"：chat 新消息、terminal 输出、turn 进度 |
| 发布方式 | mailbox `set` **完整值**（replace） | 普通 `publish`，broker 不留存 |
| 迟到订阅者 | 订阅即得当前值 + 后续完整替换，**永远不需要为此发 RPC** | 无当前值可言 |
| 要历史 | 无历史概念（旧值即被覆盖） | 走生产者的 source of truth（插件 DB / PTY ring），**显式分页 RPC 快照**（如 `before_id` + `limit`），之后收 live event |
| 判别 | 迟到者问"**现在是什么**" → State | 迟到者问"**发生了什么**" → Event |

**禁止**：mailbox 发 partial/delta（破坏自包含，broker 无合并逻辑可依赖）；event payload 依赖上一条才能解读（每条 event 必须独立可解）；消费者"读上一条总线消息"式的隐式取数（要历史必须走显式 RPC 快照）。

## 6. 传输层：WebSocket 单一栈

**决策：plugin↔kernel、gateway↔kernel、browser↔gateway 全部使用 WebSocket。**

| 候选 | 否决/采纳理由 |
|---|---|
| stdio | 仅限内核拉起的 1:1 子进程、不可重连、无远程——只留给 ACP 等外部协议的插件内部消化 |
| UDS/TCP + JSONL | 需自做 stream→帧分包；UDS 无多机路径；TCP 多机可用但与浏览器栈不统一 |
| HTTP | 无推送、逐次握手开销——仅保留为 gateway 的 by-reference 数据面（§6.2） |
| **WebSocket** ✅ | 消息帧天然带边界（免分包）；wss+TLS 即多机路径；浏览器原生（本来就必须用）；ping/pong 内建做连接存活检测（lifecycle 事件来源）；TCP 背压内建；Python/JS 库成熟 |

二进制效率不足时升级 MessagePack over WS binary frame，协议语义不变。

### 6.1 前端只剩一条 WebSocket

现状（SSE + 每 terminal 一条 WS + voice WS）收编为**一条多路复用 WS**。gateway 是协议翻译器：浏览器帧 ↔ 内核帧。

### 6.2 控制面走消息，数据面走引用

小 payload（delta、状态、事件）→ 总线；大字节（文件内容、PDF、图片、下载）→ 消息只带引用（path + token），gateway 用 HTTP 流式吐字节。禁止 base64 文件过总线。

**刻意例外：插件前端 bundle**。所有插件 hello 后经总线 RPC（`gateway:_:assets:push`）把 bundle 字节（base64，通常几 MB，每次注册一次）push 给 gateway——本地/远程统一一套机制（§14.3），这是该例外的全部理由。broker 对资产 RPC 放宽帧上限（如 64MB；注意 `websockets` 库默认 `max_size=1MB` 需调），普通流量保持小上限。

## 7. 插件 I/O 契约：slots / emits / bindings

### 7.1 固定契约（代码写死）

插件在 manifest + 代码中固定声明：

- **slots**：输入点。每个 slot 有名字、payload 类型、对应的**固定 handler 函数**。事件到达 slot → 调用该函数，不可配置。
- **emits**：输出点。固定 channel（可按 instance id 参数化，如 `terminal:{id}:output`）+ payload 类型 + 固定发送位置。

### 7.2 bindings（instance state 里的数据）

**Instance state**（instance 级持久化状态，重开恢复）：同一插件代码 + 同一 slot，不同 instance state → 不同 behavior——状态驱动行为。命名刻意避开 configuration：**configuration 专指插件级配置**（C1 管，改动影响全部 instance，如 chat 插件的 roles/agents 列表），instance state 是单个实例的持久化状态（C2/插件 DB 管，如某 chat 的 roles、cwd、session ids）；bindings 存在 instance state 里。

**状态四层**：manifest（code，静态）/ **plugin config**（插件级，C1）/ **instance state**（instance 级，C2 + 插件 DB）/ view state（前端瞬时，F6）。

**只存 slot → source channel 的映射**，不含 action：

```json
{ "bindings": { "cwd": "chat:cwd-changed" } }
```

**CWD 示例**：chat 插件的固定 emit 点在被激活时发出 CWD 事件；fs-tree 插件声明 `cwd` slot（固定 handler `setCwd()`）；binding 把 slot 接到该 source → 事件到达 → `setCwd()` 被调用。

### 7.3 可选行为

某 slot 若允许多种行为，则代码中预定义 A/B/C 处理分支，instance state 选择其一；代码本身不变。

### 7.4 类型校验

slot/emits 声明 payload 类型；hello 握手与 binding 物化时校验 source 与 slot 类型匹配，不匹配拒绝并告警。（payload 本体不校验——partial 校验见 §5.2。）

### 7.5 自动化

自动化引擎是一个 core plugin：规则 = 它的 instance state，格式同为 slot binding（source → 引擎的 trigger slot）；condition/action 逻辑在引擎内部固定实现。

## 8. View / 前端插件机制

### 8.1 基本原则

- **plugin view = 前端 JS 模块 + 后端 runtime 进程**。总线上传组件的 state / props / delta / event。
- **渲染只在浏览器**；后端（含内核）不产生 HTML。前端无进程隔离。
- **不用 iframe 做插件容器**（破坏布局拖拽/主题/跨插件交互），不用 div 里手动跑 DOM——用 Vue 组件实例作为多实例隔离容器。iframe 仅保留给外部项目页面和不可信 HTML（HtmlViewer 模式）。
- http-gateway 插件化不影响前端加载：gateway 的职责就是 serve 前端静态资源；浏览器拿到资源后的插件组装是浏览器内部事务。

### 8.2 浏览器内四层

```
① Bootstrap（极薄静态页）：连单条 WS → 拉 manifest → 加载 layout 插件 → 启动 plugin runtime
② Display Shell（plugin runtime）：registries（组件/sidebar/commands/设置页）+ ctx 工厂 + 消息 dispatch
③ Layout 插件（可替换）：递归 split；pane node = instance {type, instanceId, state}
④ 插件前端模块：每个插件一个目录，导出 activate(ctx)
```

### 8.3 Instance 挂载生命周期

1. Layout 渲染 pane node `{type, instanceId}`；
2. Shell 通用 `PluginPaneHost` 查组件注册表 → lazy loader → `defineAsyncComponent` 挂载；
3. 注入 instance 作用域 ctx：`{bus, instanceState, layout, storage, render, input, instanceId}`；
4. `activate`：物化 bindings（slot→source 翻译成 `bus.watch`）+ 按 §5.6 分两类取数——state 订阅 mailbox 即得当前值；需要历史时 RPC 拉显式分页快照 + 之后收 live event——然后渲染；
5. **消息 dispatch**：gateway 推来的消息由 shell bus client 按 channel 路由到该 instance 的 slot handler；
6. pane 关闭：ctx 记录的所有订阅/定时器**自动注销**，插件不写清理代码也不泄漏。

同一 `type` 可挂 N 个 pane，Vue 组件实例 + 各自 ctx 作用域天然隔离——"plugin 是 class，pane 是 instance"的前端实现。

### 8.4 插件代码加载：两阶段

- **阶段 A（当前）**：插件在 repo 内 `frontend/src/plugins/<id>/index.ts`，Vite `import.meta.glob` 自动发现 + code-split 独立 chunk，首次用到才 `import()` 下载。一次 build 全出、类型检查完整、Vue/Pinia 天然单例。
- **阶段 B（外部插件，机制见 §14）**：插件以 Vite library mode 构建为独立 ESM bundle，gateway 在 `/plugins/<id>/assets/` serve；shell 注入 **import map** 把 `vue`/`pinia` 等共享依赖钉成 shell 单例（双 Vue 拷贝 = 响应式损坏，这是阶段 B 的核心坑），再 `import(url)` 加载，走同一 `activate(ctx)` 入口。阶段 A 插件目录原样平移，接口不变。
- **否决**：Module Federation（过重，阶段 B 用 import map 即可）；iframe 插件容器（见 §8.1）。

### 8.5 简单插件的 JSON 声明式渲染

后期可选捷径，非主路径。

### 8.6 动态注册与运行时加载

**后端注册 = `hello` 握手**：插件进程连上内核 WS → 发 `hello`（manifest、版本、slots/emits/channel 前缀）→ 内核校验登记、开始路由；断开 = 注销。内核 config 的插件列表只是自动发现的路径清单，真正的注册永远发生在运行时。

**前端注册 = 事件驱动运行时加载**（无需刷新页面）：

```
① 内核维护 mailbox: plugins:_:list（全部插件的 manifest/状态）
② Shell 自 bootstrap 订阅该 mailbox
③ 新插件 hello 成功 → mailbox 变化 → Shell：
   1. 取 bundle URL /plugins/<id>/assets/<hash>/frontend.js（gateway 同源）
   2. import(url)（原生 dynamic import）
   3. 拿到 definePlugin 模块 → activate(ctx)
   4. 注册 components/sidebar/commands/设置页进 shell registries
④ 此后该 type 的 pane 可挂载
```

**shell 只认 gateway 同源 URL，从不引用插件文件路径**；bundle 字节来自本地 dist 复制还是远程上传（§14.6），对 shell 不可见。

关键规则：

1. **import map 不随注册变化**：共享依赖（vue/pinia/SDK）bootstrap 时一次注入；插件 bundle 只 import 共享依赖 + 相对资产。
2. **热更新靠 URL hash**：`import()` 对同 URL 返回缓存模块；reload = `deactivate` 旧模块 → 以新 hash URL 重新 `import()` → `activate`。重新 build 后 URL 变化，天然 cache-bust。
3. **逻辑卸载，物理不卸载**：ES module 无法从浏览器真正移除；`deactivate` 注销订阅与 registry 条目、instance 组件 unmount 或转 placeholder，旧代码留内存无害，页面刷新自清。
4. **未知 type 的 pane**：插件未加载时 `PluginPaneHost` 显示 placeholder 并排队，插件注册后自动补挂。
5. **时序**：前端模块加载不等待后端 hello 完成；组件照常挂载，RPC 失败由 ctx 统一重试/"connecting"态；插件状态本身是 mailbox 数据，UI 直接绑定。

静态与动态汇于一处：registries 只有一套，in-repo core plugins bootstrap 静态注册（build-time chunk），external plugins 走本节动态流程，插件作者无感知。

### 8.7 Dock 与 pane 打开语义（v0.24 定稿）

- **`openInstance` 打开语义**：目标 instance 已开 → 聚焦所在 pane；当前 active pane 为空 → 用之；存在其他空 pane → 用第一个空 pane；都没有 → 对 active pane 自动 split（默认垂直即左右分，新 pane 在右侧）并放入。**任何已占用 pane 的内容不被静默替换。**
- **Dock singleton 条目 = pin 制**：singleton provider（bus-inspector、chat-manager 等）默认 **pinned**——图标常驻 dock，点击 = 打开或聚焦；用户可 unpin（hover 上的 pin 切换），unpin 后回到"开着才显示"。pin 状态属 view state（F6），当前实现持久化在浏览器 localStorage，按 provider type 键控。
- **Dock 分区契约不变**：一个前端插件贡献一个 DockProvider（一个 dock 分区 + instances 列表）；一个插件可注册任意数量 pane 组件类型。需要第二个 dock 分区时拆第二个前端插件（如 chat / chat-manager），shell 契约零改动。

### 8.8 Pane chrome 注册（v0.25 定稿）

- **单一 title bar**：pane 标题栏只有 shell 渲染的一条（图标 + 标题 + 状态 + 插件 actions/controls + 标准布局按钮 refresh/split/close）。**插件禁止自渲染标题栏**；插件自定义标题/按钮一律走注册。
- **注册 API**：`ctx.setChrome({title?, status?, statusClass?, actions?, controls?})`，按 instance uid（`paneType:instanceId`）键控；ctx dispose 时自动清除。actions = `{id, title, icon?, label?, active?, variant?, run()}`；controls = `select`（options + onChange）或 `chips`（只读条目）——类型移植自老版 `stores/paneToolbar.ts`，语义不变。
- **标题回退**：未注册时 shell 沿用自动标题（provider title + instance 标识）。
- **dock 实例过滤**：instance 型 DockProvider 的 dock 列表 = **pinned ∪ 当前已开**（与 singleton unpin 后"开着才显示"同一条语义）；完整列表永远在管理界面（chat → chat-manager 聊天 tab）。
- **dock 自动展开（v0.26 引入，v0.27 修订）**：dock 默认纯图标窄条；hover 持续超过阈值自动展开，每个条目显示名字（图标 + 文本）；指针移出即收回。**v0.27 修订两点**：①展开改 **overlay**——展开面板覆盖在 workspace 之上，dock 的 flex 占位不变，右侧 pane 布局不因展开/收回而 reflow；②悬停延迟**可配**（默认 500ms，0 = 立即），入口为 dock 底部设置按钮，值持久化于 localStorage（view state，F6）。

### 8.9 管理面板版式约定（v0.27 定稿）

- **统一 master-detail**：管理型面板（chat-manager 三个 tab 为首例）一律**左窄 list + 右 configuration** 两栏，各 tab 共用同一套版式与视觉风格。list 条目**只显示名字**——无图标/星标前缀、无路径、无摘要行；list 顶部或底部固定一个"新建"按钮。
- **动作归右栏**：list 条目上不放任何按钮；删除、pin 等破坏性或有副作用的动作一律在右栏 configuration 区（与保存并列）。
- **dock 底部 = 设置入口**：总线连接状态指示从 dock 移除，原位为设置按钮（齿轮）；设置项含"悬停展开延迟（ms）"，持久化于 localStorage（view state，F6）。
- **路由编辑器**：policy 的 candidates 每候选一行；agent/provider/model 渲染为"浅色小 label + 可点击文本"（不使用 select 下拉箭头样式），点击文本弹出选项菜单；相邻候选间的分割线轻微高亮、中央置小"+"按钮 = 在该位置插入新候选（列表末尾同样有一条"+"分割线用于追加）；候选排序用**拖拽**（drag & drop），不设上下移按钮；候选行不再内嵌 parameters JSON 框——右栏最底部用一个等宽框展示当前 policy 的完整 JSON 预览（只读，随可视化编辑实时更新）。

## 9. 进程模型与监督

- **内核只剩 message system + 唯一 autostart**：WS 端点、broker（publish 路由 + mailbox retained）、连接注册表（`plugins:_:list` mailbox + lifecycle 事件，WS 端点的自然副产品）。内核唯一的进程职责：拉起 `viewer.supervisor` 进程并在其崩溃时重生——bootstrap 的最小代价；除此之外内核无任何监督职能。
- **Boot 序列**：内核起 WS → 拉起 `viewer.supervisor` → supervisor hello → 读插件注册表 → 拉起 C1-C4 与全部功能插件。
- **所有后端插件 = 独立进程**，由 core plugin `viewer.supervisor` 拉起并监督（`hello` 握手 + 消费 lifecycle 事件/本地 SIGCHLD + 指数退避重启 + crash-loop 熔断 + per-plugin 日志）。supervisor 管别的插件用的全是现成原语——它是"插件可 spawn 子进程"（下条）的第一个用户。插件管理 RPC（install/reload/enable）也归它（§16-12）。
- **框架不区分 instancing 模式**：进程↔instance 的映射（单进程托管多 instance / 每 instance 一进程 / 混合）是**插件内部实现**，manifest 不声明、supervisor 不感知。supervisor 插件对每个插件只拉起一个 `backend/run`，职责到此为止。
- **插件可自行 spawn 子进程**（插件侧模式，如 chat 每 chat 一进程）：子进程用继承的 `--kernel-ws` 地址各自连内核、各自 hello（可带可选 `instance_id` 信息字段，用于 `plugins:_:list` 展示与 channel 归属）；内核眼里它们只是更多连接。子进程的生命周期（何时拉起、闲置回收、崩溃重启）由父插件用本地 PID 管理，框架零介入。
- **instance 唯一性/选举是插件侧模式**：本地 PID/线程管理，或经总线发 command 消息探测（无回应即无同 instance 在跑）——用开放订阅与 RPC 现成原语拼（§5.3）。
- **worker 概念删除**：chat 插件的 per-chat 子进程即 worker（插件侧实现）——Viewer/浏览器关闭不影响；旧 worker 的 DB 任务队列 + lease + pid handover 整套废弃。
- **DB 并发**：插件按数据归属自行保证（如每 chat 的行只由对应子进程写，原子 insert 即可）；插件级 DB 用 WAL 支持并发读。
- **core plugins** 是必启插件（内核启动序列的一部分），协议上与普通插件无异。
- 跨进程通信**只有一种协议**：内核总线的 WS 帧。既有模式的归宿：

| 旧模式 | 归宿 |
|---|---|
| DB 任务队列 + lease | **删除**（per-instance 进程取代 worker，见上） |
| pid + leadership handover | 沉淀为 supervisor 插件统一策略 |
| stdio JSON-RPC | 仅用于插件内部对接外部 agent（hermes/codex ACP） |
| HTTP + SSE | 合并进 http-gateway 插件 + 单条 WS |

- **内核重启：插件进程存活重连**（已定）：内核重启不杀插件进程（尤其不杀跑了一半 turn 的 instance 进程）；插件检测断连后自动重连重 hello。崩溃/关闭语义不变。
- 外部项目（infod/badminton）= 外部服务，由薄 adapter 插件桥接进总线。

## 10. Services 清单

### 10.1 内核（仅一个 + autostart）

| # | Service |
|---|---|
| K1 | Message Bus（§5：publish 路由 + mailbox + 护栏 + 连接注册表）+ 唯一 autostart（拉起 viewer.supervisor，§9） |

### 10.2 Core plugins（必启，经总线 RPC 消费）

| # | Plugin | 职责 |
|---|---|---|
| C0 | viewer.supervisor | 拉起/心跳/重启/熔断/日志全部插件进程；插件管理 RPC（install/reload/enable）归属（§9） |
| C1 | config-store | `config:_:get/set` 等 RPC channel；`plugins.<id>.*` namespace |
| C2 | instance-store | instance state CRUD（§7.2 数据落点）；自由 JSON，schema 归插件 |
| C3 | file-service | resolve/read/hash/raw/list：引用签发 + 目录列表（v0.21，收紧程度待决议 §16-5） |
| C4 | http-gateway | 单 WS 翻译器 + by-reference 数据面 + serve 前端静态资源 + `POST /api/admin/restart`（优雅自重启，v0.34）+ `POST /api/admin/build-restart`（后台 build 成功后自重启，v0.37） |
| C5 | automation-engine | 后期（§7.5） |

### 10.3 前端 display 层（插件前端模块 `activate(ctx)`）

| # | Service |
|---|---|
| F0 | Bus client（单 WS 上的 subscribe/request/set） |
| F1 | Registry: 组件（instance.type → component） |
| F2 | Registry: sidebar tools / commands / 设置页 sections |
| F3 | Layout 插件 API（openPane/split/close；layout 自身可替换） |
| F4 | Input service（VoiceTextarea + voice 封装） |
| F5 | Render service（markdown/highlight/KaTeX/Mermaid） |
| F6 | Storage（namespaced localStorage） |

### 10.4 跨插件调用（三种，按耦合度）

1. **Bus pub/sub + slot binding**（最松）；2. **RPC**（中，只认 channel 名）；3. **Plugin API export**（最紧，manifest 声明依赖，经 RPC 调对方服务方法）。

## 11. 插件契约（manifest）

```json
{
  "id": "viewer.files",
  "version": "0.1.0",
  "process":  { "restart": "on-failure" },
  "frontend": { "entry": "plugins/files/index.ts" },
  "io": {
    "slots": [{ "name": "cwd", "type": "string", "description": "设置当前工作目录" }],
    "emits": [{ "channel": "files:cwd-changed", "type": "string" }]
  },
  "contributes": {
    "components":   [{ "type": "file-tree", "component": "FilesPanel" }],
    "sidebarTools": [{ "id": "files", "icon": "bi-folder", "title": "Files" }],
    "commands":     [{ "id": "files.refresh", "title": "Files: Refresh" }],
    "fileTypes":    [{ "extensions": [".md"], "preview": "markdown" }],
    "configSection": { "title": "Files", "schema": {} }
  },
  "dependencies": []
}
```

`hello` 握手时内核校验 channel 前缀归属（插件只能 publish/subscribe 合法前缀）。

## 12. 既有代码迁移映射

| 现状 | 归宿 |
|---|---|
| `main.py` 53 路由 | 大部分进 http-gateway 插件（代理总线）；file/config 进对应 core plugin |
| `events.py` SSE hub | 内核 broker + gateway 单 WS |
| `files.py read_config/write_config` | config-store core plugin |
| `terminals.py` | terminal 插件进程 |
| `super_workspace_*.py` runtime/worker | chat runtime 插件进程；handover 经验沉淀为 supervisor 策略 |
| `turn_summary.py` / `llm_client.py` | chat runtime 插件内部，或抽 agent service（§16-6） |
| `viewers/*.vue`、`stores/*`、`VoiceTextarea` | 前端 display 层插件模块与服务 |
| `paneToolbar.ts` | F 层 registry 模式原型，推广 |

## 13. 分期路线

| Phase | 内容 | 风险 |
|---|---|---|
| **0** | **协议规范冻结**：envelope/帧/RPC 错误分类/slots-emits 契约写成独立规范 + 类型定义 | 低，最关键 |
| **1** | 内核 broker + viewer.supervisor；config-store / instance-store / file-service core plugins；http-gateway + 单 WS；前端 bus client | 中 |
| **2** | **terminal 端到端**：第一个功能进程插件，PTY delta 过总线、slot binding 联通、前端组件渲染 | 中高（链路首验） |
| **3** | file viewers 前端模块 + fileTypes 聚合；by-reference 数据面 | 中 |
| **4** | chat runtime 插件（DB 迁移、turn summary、Hindsight）；chat pane 前端模块 | 高 |
| **5** | layout 插件化；automation-engine（常驻 binding 物化） | 中 |
| **6** | 外部项目 adapter；JSON 声明式 view（可选）；多机/NATS 平移（可选） | 按需 |

## 14. 插件包格式与开发流程

### 14.1 插件包 = 一个目录（通常就是一个 git repo）

```
my-plugin/
├── plugin.json          # manifest（§11）
├── backend/             # 后端进程代码，语言不限；入口 = 可执行 backend/run
├── requirements.txt     # Python 插件用（自带 uv venv）；其他语言换各自的依赖声明
├── frontend/            # Vue SFC + TS 源码
└── dist/                # 构建产物 frontend.js + sourcemap，经 WS push 给 gateway（§14.3）
```

### 14.2 SDK 两个半边

- **后端 `viewer-plugin-sdk`（Python）**：`Plugin` 基类 + `@slot` 装饰器 + `emit/request/set` 助手；`plugin.run()` 解析固定 cmdline 参数（§14.3 启动 ABI）连内核、完成 `hello`、分发帧到 slot handler。**SDK 是便利不是必需**——任何语言只要能连 WS、实现 §5 协议即可（§14.3）。
- **前端 `@viewer/plugin-sdk`（TS）**：`definePlugin({ components, activate, deactivate })` + ctx 类型；Vite library mode 构建，`vue`/`pinia`/SDK externalize。
- `viewer-plugin-template` 模板 repo 作为起点，含构建脚本与示例 slot/emit。

### 14.3 加载与安装（external plugin）

- 内核 config 维护插件注册表：条目指向 `~/.view/plugins/<id>/`（安装态）或**任意外部路径**（开发态，直接指向工作目录）。
- **启动 ABI（固定 cmdline 契约，语言无关）**：插件后端入口 = 可执行文件 `backend/run`（Python entry / shell 脚本 / 编译二进制皆可）；supervisor 插件用固定参数拉起：`backend/run --kernel-ws ws://127.0.0.1:<port>`，参数集仅此一项、全框架统一，后续按需追加遇到再加。插件 spawn 自己子进程时传什么参数（如 chat 插件给 per-chat 子进程传 `--instance-id`）是**插件内部 ABI**，框架不约定（§9）。supervisor 插件与被拉起的插件进程之间只约定这一组参数 + §5 协议。
- **前端资产管道（WS push + 内容寻址）**：所有插件（本地 supervised / standalone attach / 远程）hello 后经总线 RPC `gateway:_:assets:push` 主动 push 自己的 bundle 字节（§6.2 例外）；**gateway 不读任何插件目录**。gateway 存进内容寻址资产库 `~/.view/plugin-assets/<id>/<content-hash>/`，并维护 **`plugins:_:assets` mailbox**（id → url/hash）；shell 拿到的 URL 是 `/plugins/<id>/assets/<hash>/frontend.js`（与内核的 `plugins:_:list` 状态合并使用）。URL hash = 内容 hash，热更新 cache-bust 自动成立。SDK 在 hello 后自动 push，dev watch 模式下 dist 变化自动重推。
- 安装/卸载/启用/禁用/重载：经总线 RPC（如 `plugins:_:install/reload`）。
- In-repo core plugins 走 §8.4 阶段 A，不经此机制。

### 14.4 开发流程

1. clone 模板；`uv venv && uv pip install -r requirements.txt`；`npm install`。
2. 后端两种模式随时切换：
   - **standalone attach（调试首选）**：`backend/run --kernel-ws ws://127.0.0.1:<port>`（参数与 supervisor 插件注入的完全相同，只是手动传）——插件是 IDE 里的普通进程，pdb/py-spy/日志随便用，改完重启即可。WS（而非 stdio）传输使此模式成立。
   - **supervised**：路径注册给 supervisor 插件，由它拉起，崩溃自动重启，stdout/stderr 落 per-plugin 日志。
3. 前端：`npm run dev`（`vite build --watch` + sourcemap）→ 浏览器刷新对应 pane；Vue devtools 正常可用。
4. 联调：浏览器开插件 pane，全链路（组件 → WS → kernel → 插件进程）实时可见；消息用 bus-inspector 插件（A.10）排查。
5. 测试：SDK 提供 mock kernel/bus 做单元测试；e2e 用隔离实例模式。

### 14.5 版本与依赖

- 插件自带 venv（uv 创建），依赖与主项目完全隔离。
- SDK 版本与内核协议版本在 `hello` 握手校验，不兼容拒绝并提示。
- **插件间依赖不由框架管理**：作者在插件文档中声明依赖（如"消费 X 插件的 Y channel"），用户自行安装启用；开放订阅（§5.3）下依赖缺失只是"没有消息进来"，不会报错。
- 插件更新 = git pull + 重建前端 + `plugins:reload`。

### 14.6 远程插件（预留，延后实现）

总线层与资产层均已天然支持（wss；资产 WS push 与本地同一机制，§14.3）。相对本地插件的全部增量只剩：

1. **认证**：wss 握手带 token；
2. **`managed: false`**：hello 标志；远程插件自行启动，内核不 spawn、不重启，只做心跳观察与断连注销。

复杂度评估：低，不动架构。内核间 federation（多机多内核）是更大的变体，见 §16-11。

## 15. 风险与对策

1. **一切异步化的调试成本**——trace_id 全链路 + bus-inspector 插件（A.10）。
2. **协议一次要设计对**——Phase 0 独立规范文档，先冻结再写码。
3. **分布式状态排错难**——mailbox "当前状态总览"调试视图（经 gateway 展示）。
4. **工程量大**——每 Phase 端到端可跑；旧系统并行存活直到对应 Phase 切换（strangler 模式）。
5. **自动化死循环/风暴**——§5.4 三护栏第一天内置。
6. **范围控制**——框架只长当前插件需要的能力。

## 16. 待决议问题

> 已敲定条目已全部移出本节（决议正文见对应章节，历史见 §18）。

5. file-service 收紧程度：插件经它读写文件（可控、可审计）vs 插件直接摸文件系统（现状、自由）。
6. Agent service：core plugin 还是 chat runtime 插件的内部能力。 → **已定（v0.23）**：皆非——agent 实现拆为独立 headless 功能插件族 `viewer.agent-*`（无 UI 单实例服务插件，统一总线契约，见 A.7）；chat 只经契约消费，不认识任何 provider 实现。
10. 插件前端 TS 类型与后端 envelope 类型的单一来源（schema 生成？）。
11. 多机场景的内互联结（**暂缓**，v0.18）：当前只考虑单机 localhost；多机 federation 暂不考虑，NATS 平移路径保留（§4），触发条件以后再说。

## 17. 分发形态与 Go 主线

- **核心集 = 单一静态二进制**：内核 + 全部 core plugins（supervisor / config-store / instance-store / file-service / http-gateway / bus-inspector / terminal）+ 前端（`go:embed` 内嵌 dist）用 **Go** 实现为单个二进制，交叉编译分发（无运行时依赖、无 venv、RSS 一个数量级下降）。
- **进程模型**：核心集从「每插件一进程」收进**单进程 goroutine 插件**（编译期 registry 注册，panic 隔离到插件粒度）；内部通信复用同一线路协议（进程内 transport），总线观察者无法区分。外挂/第三方插件**维持独立进程**连总线，语言无关（v0.13 启动 ABI + wire protocol 保证），不打包进二进制。
- **数据库访问一律 ORM**，禁止裸 SQL：Go 侧定 **GORM**（AutoMigrate 管 schema 演进）；SQLite 驱动用 **modernc.org/sqlite**（纯 Go，CGO 关闭，交叉编译不受损）。
- **仓库结构定稿（v0.33）**：Python 参考实现（`next/`）与 `next-go/` 目录已删除；Go 主线（`cmd/`、`internal/`、`web/`、`scripts/`）上移至仓库根，单一 Go module（`viewer`）自根构建；SDK 按语言归入 `sdk/{go,python,ts}`；前端在 `frontend/`。测试即规格：`scripts/smoke_*.py`（Python SDK 黑盒）+ `sdk/ts` vitest 直接验证 Go 内核（`VIEWER_KERNEL_BIN` 可覆盖）。
- 迁移顺序：内核 → terminal → gateway（+embed 前端）→ supervisor / config-store / instance-store / file-service / inspector → 前端适配 → chat（最重，附录 A.7 原有排期不变）。

## 18. 修订记录

- **v0.1**（2026-08-12）：初版。三层结构、Event+Mailbox 双原语、"逻辑隔离默认"进程模型。
- **v0.2**（2026-08-12）：微内核化；RPC 升为一等原语；传输统一（单 WS）；by-reference 数据面；渲染只在浏览器、view 为前端模块+后端 runtime。
- **v0.3**（2026-08-12）：插件 I/O 固定契约（slots/emits，bindings 只存 slot→source 映射，删除 action）；内核纯化（config/instance-store/file/gateway 降为 core plugins，内核仅 broker+supervisor）；传输层定为 WebSocket 单一栈；自动化引擎定位为 core plugin。
- **v0.4**（2026-08-12）：前端插件机制定稿——浏览器内四层、instance 挂载生命周期（PluginPaneHost + 懒加载组件 + instance ctx + 自动清理）、两阶段加载（A: build-time `import.meta.glob` + code-split 懒加载；B: 运行时 ESM + import map 共享依赖单例）；否决 Module Federation 与 iframe 插件容器。
- **v0.5**（2026-08-12）：新增 §14 插件包格式与开发流程——目录包（plugin.json + backend + frontend + dist）、双 SDK（Python/TS）+ 模板 repo、external 加载（注册表路径 + gateway 资产挂载 + import map）、standalone attach 调试模式（WS 传输的红利）、插件自 venv 与 hello 版本校验。
- **v0.6**（2026-08-12）：动态注册定稿——后端注册即 `hello` 握手（断开即注销）；前端事件驱动运行时加载（§8.6）：订阅 `plugins:list` mailbox → `import(url)` → `activate(ctx)`；热更新靠 bundle URL hash；逻辑卸载物理不卸载；未知 type pane 排队补挂。
- **v0.7**（2026-08-12）：`hello` 内联完整 manifest（standalone attach / 远程场景下内核无 plugin.json 文件，注册路径统一为一条）；前端资产改走内容寻址资产库（注册时复制进 `~/.view/plugin-assets/<id>/<hash>/`，shell 只认 gateway 同源 URL）；新增 §14.6 远程插件预留（增量仅 wss 认证 + 资产上传 + `managed:false`）。
- **v0.8**（2026-08-12）：前端资产统一为 **WS push**——所有插件 hello 后经总线 RPC `gateway:assets:push` 主动 push bundle（base64，几 MB 一次），gateway 不读插件目录，本地/远程/standalone 同一机制；内核只路由不碰资产；资产定为 §6.2 by-reference 原则的刻意例外（broker 对资产 RPC 放宽帧上限）；gateway 维护 `plugins:assets` mailbox；远程插件增量缩减为认证 + `managed:false` 两项。
- **v0.9**（2026-08-12）：新增附录 A 现状功能清单与插件映射（迁移作战地图）。
- **v0.10**（2026-08-12）：新增 bus-inspector 调试插件（A.10）：broker **monitor 订阅**特权原语（§5.3，manifest capability `bus:monitor`，捕获含 RPC 在内的全部帧、排除自身防回声）、默认 5000 条 ring buffer、filter（channel glob/类型/origin/trace/payload 文本）、暂停不清流、不自动滚动、超量降采样；排期 Phase 2 与 terminal 同批。
- **v0.11**（2026-08-12）：**订阅完全开放**——删除 monitor 特权与 `bus:monitor` capability（v0.10 引入即废），任何插件可订阅任意 channel pattern 含 RPC 帧与 `>` 全量，无权限层；防回声改消费者客户端过滤；跨插件联动定为一等用法；框架不做插件依赖管理（§2 non-goal + §14.5，作者声明用户安装）；内核发布插件生命周期事件 `plugins:{id}:lifecycle`（§4.1）；broker 定位明确为 **NATS Core + replay window**（每 channel/mailbox 保留有限历史的有状态便利，不上 JetStream 级持久流）。
- **v0.12**（2026-08-12）：**砍掉 broker 事件历史**（replay window 引入即废）——逐 use case 检查后每个场景都有更好归属：迟到状态→mailbox 最新值；重连/重开→§8.3 "RPC 快照 + live delta"；chat 历史→插件 DB；terminal 回滚→PTY ring；调试→bus-inspector ring。broker 退回极简（路由 + mailbox 最新值表），无 retention 策略/配置面/内存增长；定位修正为 **NATS Core + retained mailbox**；§16-7 待决议勾掉。
- **v0.13**（2026-08-12）：**instance config → instance state**：状态驱动行为（同代码同 slot，不同 state 不同 behavior）；configuration 专指 C1 全局配置，三层定为 manifest / instance state（C2）/ view state（F6）。**插件后端语言无关化**：启动 ABI = 固定 cmdline 契约——入口为可执行 `backend/run`，supervisor 注入固定参数 `--kernel-ws ws://...`（当前仅此一项，按需再扩）；manifest 删 `process.command`（§11）；Python SDK 降为便利非必需，任何语言实现 §5 协议即可；standalone attach 用同一组参数手动传（§14.4）。
- **v0.14**（2026-08-12）：**runtime instancing 双模式**（§9，manifest `process.instancing`）：`shared`（默认，轻量插件单进程托管）/ `per-instance`（每 runtime instance 一进程；chat 采用——**每 chat 一进程**，崩溃隔离到 chat 粒度，channel 天然 per-process）。**worker 概念删除**：instance 进程即 worker（按需拉起、turn 进行中永不闲置杀、完成且闲置超时回收、Viewer 关闭不影响）；旧 DB 任务队列 + lease + pid handover 整套废弃；DB 并发按数据归属解决（每 instance 的数据只由自己的进程写）。状态三层改**四层**：manifest / plugin config（C1，插件级）/ instance state（C2+插件 DB，instance 级）/ view state（F6）。启动 ABI 加 `--instance-id` + **恢复契约**（凭 id 加载 state 恢复 cwd/sessions/续跑 turn）；cmdline 参数全框架统一。§16-4 定**存活重连**、§16-9 定**插件负责状态清理**——勾掉两条待决议。
- **v0.15**（2026-08-12）：**instancing 双模式从框架删除**（v0.14 引入即废）——框架不区分进程↔instance 映射，下沉为插件内部实现：supervisor 每插件只拉起一个 `backend/run`（ABI 回到单参数 `--kernel-ws`）；manifest 删 `instancing` 字段；插件可自行 spawn 子进程各自连内核 hello（hello 加可选 `instance_id` 信息字段，§5.3），子进程生命周期/传参/恢复均为插件内部 ABI；instance 唯一性/选举为插件侧模式（本地 PID 管理或总线 command 探测）；worker 删除、状态四层、§16-4/§16-9 结论保留（重新归类为 chat 插件内部设计）。
- **v0.16**（2026-08-12）：**supervisor 下放为 core plugin** `viewer.supervisor`（C0）——内核只剩 WS 端点 + broker（publish 路由 + mailbox retained）+ 连接注册表（`plugins:list` + lifecycle 事件）+ 唯一 autostart（拉起并重生 supervisor 一个进程，bootstrap 最小代价）；boot 序列：内核 → supervisor → C1-C4 + 功能插件；插件管理 RPC 归属 C0（§16-12 敲定）。**RPC 定为 inbox 约定**（§5.3）：broker 无 RPC 路由子系统，请求/响应/取消/超时/错误全是 publish 之上的约定（`_inbox:{conn}:{corr}` reply channel），帧类型收敛为 5 种（hello/publish/set/subscribe/unsubscribe）；§16-3 敲定（超时 client-side 30s 可配、错误=payload 约定字段、cancel=带 corr 普通消息）。K2 从内核 Services 清单删除，内核只剩 K1。

- **v0.17**（2026-08-12）：**Event vs State 发布准则定稿**（§5.6）——每条总线消息先归类：State（"现在是什么"：instance status / online / cwd / 配置当前值）走 mailbox `set` **完整自包含值**，订阅即得当前值 + 后续替换，消费者永不需为 state 发 RPC；Event（"发生了什么"：chat 消息 / terminal 输出 / turn 进度）走普通 publish 无留存，历史一律走生产者 source of truth + **显式分页 RPC 快照**（`before_id`+`limit`）+ live event。三条禁止：mailbox 发 partial/delta；event 依赖上一条才能解读；"读上一条总线消息"式隐式取数。§5.5 `set` 语义定为整体替换，**broker 删除 delta 合并**（replace 是唯一动作，broker 再简化）；§4 重同步表述、§8.3 挂载取数、附录 A.4 terminal 存储同步更新。
- **v0.18**（2026-08-12）：**协议三件套定稿**——§16-1 channel 语法（§5.2）：冒号分隔、**前三层语义固定** `plugin:instance:message`、第四层起自由 grouping；`*` 单字段通配（任意层）+ **pattern 前缀隐式全匹配**（pattern 字段数少于 channel 时尾部默认全匹配）+ `>` 匹配总线全部；插件级无 instance 的 channel 用保留实例名 `_`，`_` 前缀为框架保留命名空间（`_inbox:*`）；正文 channel 写法统一（`plugins:_:list`、`plugins:_:assets`、`config:_:get/set`、`gateway:_:assets:push`、`chat:_:active` 等）。§16-2 payload **partial 校验**：envelope/hello（内联 manifest）等协议元信息强校验（Pydantic / JSON Schema 皆可），payload 本体任意 JSON 不校验，slot/emit 类型声明为标注性。§16-3 补充确认：错误不需要专门 channel/订阅机制。§16-11 多机**暂缓**（当前只考虑单机 localhost）。§16-13 版本策略定**不向后兼容**：hello 协议版本严格相等、不等即拒连；schema/存储变更走一次性迁移，丢了重来可接受。待决议剩余 3 条（16-5 file-service 收紧、16-6 agent service 归属、16-10 类型单一来源）。
- **v0.19**（2026-08-12）：**Phase 0 开工**——§16 待决议清单清理（已敲定条目移出，剩 16-5 file-service 收紧 / 16-6 agent service 归属 / 16-10 类型单一来源 + 16-11 多机暂缓）；新建 **`docs/plugin-protocol.md`** 线路级协议规范草案 v0.1（**未冻结**，评审后冻结再写码）：连接拓扑、5 帧 JSON schema（hello 含 client 生成 `conn`、成功无 ack、失败用 WS close code 4001/4002/4003/4009）、channel 匹配算法形式化（前缀隐式全匹配）、mailbox replace-only + 订阅原子交接、RPC inbox payload 契约（`_reply_to`/`_corr`/`ok`/`error`/`_cancel`、30s 超时）、`_conn:{conn}:error` 协议错误通知 mailbox、背压（出站队列 1000 丢新帧）、心跳 ping/pong 30s×2、五张组件交换时序图。
- **v0.20**（2026-08-13）：**分发形态定稿（§17）**——核心集（内核 + core plugins + 前端 embed）用 Go 实现为单一静态二进制；外挂插件维持独立进程连总线、语言无关；核心集进程模型改为单进程 goroutine 插件（编译期 registry、panic 隔离到插件粒度）；数据库访问定 ORM（GORM + modernc.org/sqlite 纯 Go 驱动），禁止裸 SQL；Python 栈转协议参考实现，`next-go/` 为新主线，测试套件作迁移期验收标准。
- **v0.21**（2026-08-13）：**file-service 目录列表定稿**——新增 RPC `file:_:list`：输入 `{path}`（与 resolve 同一解析语义），输出 `{path, entries[]}`；entry 字段对齐生产版 FileEntry（name/path/type(file|directory|symlink|other)/size/mtime/mime/is_dir/is_symlink/link_target）；排序 = 目录优先 + name 字典序；一次性全量返回不分页；隐藏文件过滤归 file-service 插件配置。用途：viewer.files 文件树（A.5）的唯一取数通道。
- **v0.22**（2026-08-13）：**chat 数据面与 provider 面定稿**——数据面三层：`turn_events`（append-only raw 帧全量，per-turn seq，过滤前落库）→ `message_blocks`（归一化解析块单独存，event_id 回指）→ `messages`（用户可见文本视图）；接力 = 插件内顺序执行即最终形态；provider = hermes + codex-app-server 唯二（codex-acp 不移植，opencode 暂不实现）；历史迁移 `super_workspace_messages.raw_json` 幂等迁入 `turn_events`。
- **v0.23**（2026-08-13）：**agent 实现拆为独立 headless 插件族**——`viewer.agent-hermes`（ACP）/ `viewer.agent-codex`（app-server）/ `viewer.agent-opencode`（ACP，新建）；统一契约：RPC `start`/`prompt`（ack 异步）/`cancel` + 事件 `event`（seq/kind/raw_json/block 同帧）/`turn-ended` + retained mailbox `catalog`；chat 瘦身为纯编排，经 C1 `plugins.viewer-chat.agents` 映射发现并聚合 catalog；profile = routing policy（既有模型），role 挂 policy，turn 开始按序解析 candidates，`auto_failover` 接回；`role.provider/model` 降为迁移输入；opencode 转为新建（ACP 第二租户）；§16-6 敲定。
- **v0.25**（2026-08-14）：**pane chrome 注册机制**（§8.8）——移植老版 paneToolbar 到插件契约：`ctx.setChrome` 注册 title/status/actions/controls，shell title bar 统一渲染，插件禁自渲染标题栏；ChatPane 拆除内部 header 改用 chrome（标题 = chat 名，config 按钮 → chat-manager）。chat dock 实例收窄为 pinned ∪ 已开。
- **v0.26**（2026-08-14）：**dock 自动展开**（§8.8）——默认纯图标窄条，hover ≥500ms 展开显示条目名，移出收回；无开关不持久化。
- **v0.27**（2026-08-14）：**dock overlay 展开 + 设置入口 + 管理面板版式统一**（§8.8/§8.9）——dock 展开改 overlay（右侧 workspace 不再 reflow），悬停延迟可配（localStorage）；dock 底部连接指示移除、原位换设置按钮；管理面板统一 master-detail（左窄 list 只显名字 + 固定新建按钮，动作归右栏 configuration）；路由编辑器去 select 化（label + 可点击文本弹菜单、分割线中央"+"插入、拖拽排序、底部整体 JSON 预览）。
- **v0.28**（2026-08-14）：**viewer.voice 契约定稿**（A.8）——音频经总线传输（base64 chunk publish）、文字经总线事件回传（ready/partial/final…）；后端插件只做外部 voice-service 的 WS relay（C1 注入 service_ws/model/language；内嵌 ASR 后端不移植）；前端 voice 插件无 pane（store + 按钮），chat composer 直接 import 引用；录音安全上限 10 分钟。
- **v0.29**（2026-08-14）：**voice 前端降级**（A.8）——后端 voice 插件缺席时（`plugins:_:list` 探测），composer 麦克风按钮置灰禁用，chat 其余功能不受影响；总线重连后重新探测。
- **v0.38**（2026-08-17）：**RPC no_route fail-fast + 丢帧可追查**（§5.3）——请求帧零订阅者时 broker 合成 `no_route` 错误响应立即回 `_reply_to`（此前调用方干等 30s 客户端超时；重启竞态兜底，非路由子系统，普通事件仍静默丢弃）；慢消费者丢弃带 `_corr` 帧逐条 WARN。
- **v0.34**（2026-08-15）：**gateway admin restart 定稿**（§10 C4）——http-gateway 暴露 `POST /api/admin/restart`：单二进制优雅自重启（spawn 同参数新进程 + `--wait-pid <self>` 等旧 pid 消失再 bind + Setsid + 日志转 `/tmp/viewerd.log`，500ms 后 SIGTERM 自关走排空路径）；spawn 失败返回 500 且旧进程继续运行；`--wait-pid` 上限 30s。dev 单二进制获得与 deploy/supervisor 等价的开发重启路径。
- **v0.35**（2026-08-16）：**agent catalog 协议发现 + 缓存刷新**——hermes/opencode catalog 从 config.yaml 扫描/硬编码改为 ACP 协议枚举（hermes `session/new.models` = SessionModelState `provider:model`，opencode `session/new.configOptions` 的 select 型 `model` 项 = `provider/model`），codex 沿用 app-server `model/list`；统一 `agentdriver.CatalogCache`（启动静态 fallback + 后台即时/30 分钟周期刷新 + 失败保留旧值 + 发现成功重发 retained mailbox）；新增 `<agent-plugin>:_:catalog-refresh` RPC；opencode `start` 经 `session/set_config_option` 落地模型强制（agent 校验失败即 start 失败）；C1 catalog override 语义不变。
- **v0.37**（2026-08-16）：**ACP 权限自动批准 + 一次性新会话开关 + 构建并重启**——①ACP 客户端（`internal/acp`）应答 agent→client 请求：`session/request_permission` 一律自动批准（优先 `allow_always`，否则第一个 allow 类选项；viewer 无审批 UI，用户裁决全部放行），未实现方法立即回 `-32601`（此前一律无应答，agent 侧干等 60s 超时）；②`chat:_:dispatch`（A.7 send-message）增加一次性 `force_new_session`：跳过内存 runtime 复用与 `role_sessions` 恢复强制新建 agent session（旧 session 常驻不回收，v0.24 裁决不变），前端 composer 加一次性图标开关（发出自动复位）；③C4 增加 `POST /api/admin/build-restart`：后台运行 `web/build-release.sh`（构建期服务不中断），成功才走 v0.34 优雅重启，失败只记 `/tmp/viewerd-build.log`；dock 设置菜单加「构建并重启」按钮（两阶段轮询：先等下线再等恢复）。
- **v0.36**（2026-08-16）：**catalog 发现改按需（v0.35 的 30 分钟周期刷新废除）**——`CatalogCache.StartLoop` 改 `StartOnce`（启动一次性后台发现，无 ticker）；chat 新增 `chat:_:agent-catalog-refresh` RPC：并行扇出各 agent 插件的 `<plugin>:_:catalog-refresh`，成功响应并入聚合 catalog 后按 `agent-catalog` 同构返回，失败保留旧值；前端 chat-manager Roles/Routes 面板 load 时调用该 RPC（打开面板即触发一次协议发现，修改配置后及时生效）。
- **v0.33**（2026-08-15）：**仓库结构定稿（Go 主线落地）**——Python 参考实现（`next/`）与 `next-go/` 目录删除；Go 主线（`cmd/`、`internal/`、`web/`、`examples/`、`scripts/`）上移至仓库根，单一 Go module（`viewer`）自根构建；SDK 按语言归入 `sdk/{go,python,ts}`；前端在 `frontend/`。测试即规格：`scripts/smoke_*.py`（Python SDK 黑盒）+ `sdk/ts` vitest 直接验证 Go 内核（`VIEWER_KERNEL_BIN` 可覆盖）。
- **v0.32**（2026-08-15）：**chat 前端会话缓存定稿**（A.7）——ChatPane 卸载后已加载历史存模块级缓存（LRU 24 聊天，每聊天 messages ≤2000 / blocks ≤4000，超限丢最老半页重置上翻游标）；重开秒渲染 + 增量合并：`chats:list` include_messages 加 `after`/`after_id`（复合游标含边界，边界行重拉以最终文本替换流式中的缓存副本，升序 + `has_more` 前端循环取完），blocks 按缓存内最大 `occurred_at` 增量取；按 id 去重/替换，聊天删除逐出缓存；重复打开无新消息 ≈16 KiB，已加载老页零流量。
- **v0.31**（2026-08-15）：**chat 历史懒加载定稿**（A.7）——对齐生产版最新优先 + 向上翻页：`chats:list` include_messages 加 `before`/`before_id`/`limit`（复合游标 `(created_at, id)` 严格小于 + `has_more`），`blocks:list` 加 `after`/`before` 时间窗（按已加载消息跨度分窗，零缺口零重复）；前端首屏只加载最新一页 + 覆盖窗 blocks 并滚到底，滚动到顶触发更早页（DOM 高度差恢复滚动位置，顶部显示加载/尽头指示），激活/聊天变更事件改 merge-refresh（不重置已加载分页、不跳滚动）。
- **v0.30**（2026-08-15）：**chat 消息时间线渲染定稿**（A.7）——blocks 数据面上总线：RPC `chat:_:blocks:list` + 事件 `chat:{id}:block`（块落库即推）；前端一 turn 一盒、消息文本段与工具活动块严格按时间交错，文本段 markdown 渲染（markdown-it + KaTeX + hljs 行号 + mermaid），工具块折叠 activity 行，盒顶 info 条；样式经 `--markdown-*`/`--syntax-*` CSS 变量主题可定制（亮/暗内置，localStorage 持久化）。
- **v0.24**（2026-08-14）：**chat 体验与 shell 语义定稿**——①agent 契约加 `turn_id` 贯穿：`start`/`prompt` payload 携带，`event`/`turn-ended` 帧 echo，chat 按 turn_id 解复用（删 session→turn 映射，同 session 连续 turn 消歧）；②**idle reap 否决**：agent 子进程常驻不自动回收（用户随时重开网页须看到原状；常用 chat 个位数，开销可忽略）；③shell 行为定稿（新增 §8.7）：`openInstance` 不再覆盖已占用 pane（聚焦/空 pane/自动 split 三级），dock singleton 条目改 pin 制（默认 pinned 常驻，可切换）；④chat 前端拆为 `chat` + `chat-manager` 两个前端插件（后者 = singleton 三 tab 管理面板：聊天/Roles/路由），后端 `viewer.chat` 保持单插件；⑤roles/routing policies 从 C1 config-store 迁入 chat 插件 DB（GORM 表，对齐生产版 `super_workspace_roles`/routing 模型），C1 收缩为纯插件级配置。

---

## 附录 A：现状功能清单与插件映射（迁移作战地图）

> 基于 2026-08-12 代码盘点：`backend/app/` 29 个模块、`main.py` 53 条路由、前端 10 个 viewer / 6 个 sidebar 面板 / 9 个 store。**每一个现状功能都有且只有一个归宿。**

### A.1 总览

| 插件 | 性质 | 现状来源（后端） | 现状来源（前端） | 迁移 Phase |
|---|---|---|---|---|
| 内核 K1 + viewer.supervisor（C0） | 内核 + core plugin | `events.py`(概念)、`restart.py`、`main.py` 的 health/admin | — | 1 |
| C1 config-store | core | `config.py`、`models.py` | `ConfigPanel.vue`（壳+sections） | 1 |
| C2 instance-store | core | （新建） | `utils/storage.ts`、`stores/layout.ts` 持久化逻辑 | 1 |
| C3 file-service | core | `files.py`（resolve/read/hash/raw）、`storage.py`、`watcher.py` | — | 1 |
| C4 http-gateway | core | `main.py`（静态资源）、`api/events.ts` 对端 | `api/client.ts`、`api/events.ts` | 1 |
| viewer.terminal | 功能 | `terminals.py`、`process_registry.py` | `TerminalViewer`、`sidebar/TerminalsPanel`、`stores/terminals.ts` | 2 |
| viewer.bus-inspector | 功能（调试） | **新增**（broker monitor 订阅 + ring buffer） | **新增**（消息表 + filter bar） | 2 |
| viewer.files | 功能 | `files.py`（preview_kind/tree/content/upload/delete/site/resolve-link） | 见 A.5 | 3 |
| viewer.git | 功能 | `git_diff.py` | `sidebar/GitPanel`、`DiffViewer` | 3 |
| viewer.chat | 功能 | super_workspace 编排 + 存储（见 A.7；agent 驱动层已拆出，v0.23；**v0.31 历史懒加载分页 + v0.32 会话缓存**） | 见 A.7 | 4 |
| viewer.agent-hermes / agent-codex / agent-opencode | 功能（headless 服务插件，无 UI，v0.23） | `hermes_acp.py` + `acp_*.py` / `codex_app_server*.py` / `opencode_*.py`（新建） | —（无前端） | 4 |
| viewer.voice | 功能(候选 core) | `voice.py` | `VoiceTextarea`、`VoiceInputButton`、`stores/voice.ts` | 2-4 |
| layout/shell | display 层 | — | `Workspace.vue`、`SplitNode`、`stores/layout.ts`、`stores/paneToolbar.ts`、`ViewerPane→PluginPaneHost` | 1（壳）/ 5（插件化） |
| logging/共享 | 库 | `logging.py`、`identity.py` | `utils/paths.ts` | 各 Phase 随用 |

### A.2 内核自带（不插件化）

| 现状 | 归宿 |
|---|---|
| `GET /api/health`、`POST /api/admin/restart|stop`（`restart.py`） | 内核/viewer.supervisor 的 system RPC（`system:health/restart/stop`），gateway 转发 |
| `events.py` SSE hub | 概念并入 K1 broker；`/api/events` 由 gateway 单 WS 取代 |
| `logging.py` | 内核与 SDK 共享日志库 |

### A.3 Core plugins 详单

- **C0 viewer.supervisor**：`restart.py` 的进程管理逻辑 + `main.py` 的插件进程拉起职责 → 独立 core plugin；内核只保留 autostart 它一个进程的逻辑（§9）。
- **C1 config-store**：`config.py` + `models.py`（AppConfig schema）；路由 `GET/PUT /api/config`、`GET/POST /api/config/llm-provider-states(/clear)` → RPC `config:_:get/set` 等。前端 `ConfigPanel.vue` 拆为设置壳 + per-plugin section 贡献点（F2）。
- **C2 instance-store**：新建（§7.2 bindings、instance state 落点）；同时接管 `viewer.layout.v1` 的服务端持久化（若需要跨设备）——view state 仍走 F6 localStorage。
- **C3 file-service**：`files.py` 的 resolve/hash/raw 字节 + `list_directory()` 目录列表（v0.21 新增 `file:_:list` RPC：一次性全量、目录优先排序、entry 对齐 FileEntry 字段）+ `storage.py` + `watcher.py`（目录变更 → 总线事件 `files:_:changed`）。by-reference 数据面（§6.2）的引用签发方。
- **C4 http-gateway**：单 WS 翻译器；serve 前端构建产物与内容寻址资产库（§14.3）；`plugins:_:assets` mailbox 维护者；by-reference HTTP 数据面。

### A.4 viewer.terminal（Phase 2，链路首验）

- 后端：`terminals.py` + `process_registry.py`；路由 `/api/terminals`（CRUD/terminate）+ `/api/terminals/{id}/ws`。
- Instance：每 terminal 一个 instance；**runtime = PTY session（插件进程内），view = TerminalViewer pane**——view/runtime 分离样板（pane 关闭 PTY 不死，重开 reconnect）。
- slots：`input`（键入）、`resize`；emits：`terminal:{id}:output`（字节流 event）、`terminal:{id}:status`（mailbox）。
- 存储：terminal 元数据 → instance-store；PTY 内容 → ring buffer（插件内存，RPC 快照源）；output 走 event（§5.6，不经 mailbox）。
- 迁移要点：per-terminal WS 消失，output 走总线 event；PTY 高频输出验证 §5.5 背压（慢消费者丢弃）策略的第一个真实场景。

### A.5 viewer.files（Phase 3）

- 后端：`files.py` 其余部分；路由 `/api/tree`、`/api/file/meta|content|text-lines|upload|delete|raw|site|resolve-link|resolve-directory-link`。
- 前端：`sidebar/FilesPanel`、`FileSidebar.vue`、`FileTree.vue`、`DirectoryPicker.vue`；**8 个 viewer**：Text / LargeText / Markdown / Csv / Image / Pdf / Html / Unsupported；`stores/files.ts`（目录+外观+主题部分）；`utils/scrollMemory.ts`。
- Instance：file-tree（sidebar 常驻 instance）+ 每预览 pane 一个 instance（type 按 preview kind）。
- slots：`cwd`（§7.2 的 CWD 联动示例）、`open-file`；emits：`files:file-opened`、`files:cwd-changed`。
- 迁移要点：① `preview_kind()` 硬编码映射 → **fileTypes 贡献点**（每 viewer 声明 extensions，registry 聚合）；② `markdownRender.ts`（highlight/KaTeX/Mermaid）上提为 F5 render service 供所有插件复用；③ HtmlViewer 的 `/api/file/site` 代理 + activation shield 模式保留；④ `stores/files.ts` god-store 拆分是本子项的主要工作量。

### A.6 viewer.git（Phase 3）

- 后端：`git_diff.py`；路由 `/api/git/status|diff|stage|revert|commit|push`（6 条）→ 全部变 RPC。
- 前端：`sidebar/GitPanel.vue`、`DiffViewer.vue`（同时注册为 `.diff/.patch` 的 fileType viewer）。
- Instance：git panel（per repo/cwd）+ diff pane。
- slots：`refresh`、`cwd`；emits：`git:status-changed`（配合 C3 watcher）。

### A.7 viewer.chat（Super Workspace，Phase 4，最重）

- 后端（13 个模块）：`super_workspace.py` / `_runtime.py` / `_worker.py` / `_memory.py`、`agent_history.py`、`turn_summary.py`、`llm_client.py`、`inference.py`、`identity.py`、`driver_catalog.py`、`ws_clients.py`，及 ACP 层 `acp_runtime.py` / `acp_sessions.py` / `hermes_acp.py` / `hermes_sessions.py` / `codex_app_server*.py` / `opencode_*.py`。**（v0.23：ACP/agent 驱动层模块归对应 `viewer.agent-*` 插件，chat 只留编排 + 存储 + summaries/Hindsight。）**
- 路由：`/api/super-workspace/*`（~20 条：workspace/chats/roles/routing/runs/events/dispatch/stop）+ `/api/agents/providers|inference-targets` + `/internal/super-workspace/notify` → 全部变 RPC/总线事件。
- 前端：`SuperWorkspacePage.vue`、`SuperWorkspaceChatPane.vue`、`sidebar/ChatsPanel|RolesPanel|RoutesPanel`、`stores/agents|superChatComposer|superChatDispatch|inputSessions.ts`。**（v0.24：next 前端拆为两个插件——`chat` = ChatPane + 聊天实例 dock 列表（实例来自后端 chat 列表，刷新/换设备均重建）；`chat-manager` = singleton 管理面板，单个带 tab 页面三合一：聊天（common_prompt + member roles + root）/ Roles（prompt + policy 绑定 + catalog 三级选择器）/ 路由（有序 candidates + auto_failover）；经 `chat:_:*` RPC 消费同一后端，shell 契约不变，§8.7。）**
- Instance：每 chat 一个 instance。**agent runtime 移出 chat（v0.23）**——agent 实现为独立 headless 插件族 `viewer.agent-hermes`（ACP）/ `viewer.agent-codex`（app-server）/ `viewer.agent-opencode`（ACP，新建）：无 UI、单实例（instance 恒 `_`）、随二进制编译期 registry 启动，插件内部自管 session→子进程池（"per-chat 子进程即 worker"决策不变，所有者从 chat 变为 agent 插件；外挂语言实现的 agent 插件经同一总线契约接入，语言无关）。**统一契约**：RPC `start`（`{cwd, target:{agent,provider,model,parameters}, session_id?, turn_id?}` → `{session_id, resumed}`；`parameters` opaque 透传，agent 插件自行解释）/ `prompt`（`{session_id, turn_id, text}` → 立即 ack，turn 异步执行）/ `cancel`（`{session_id}`）；事件 `<plugin>:_:event`（`{session_id, turn_id, seq, kind, raw_json, block}`——raw 原文与归一化解析块同帧，seq 由 agent 侧 turn 内单调发号）、`<plugin>:_:turn-ended`（`{session_id, turn_id, stop_reason}`）；**`turn_id` 由 chat 生成并贯穿（v0.24）**：`prompt` 必带、`start` 在隐式首 turn 时携带，事件帧逐帧 echo，chat 按 turn_id 解复用（删 session→turn 映射，同 session 连续 turn 消歧）。子进程池常驻，**不做 idle 回收**（v0.24 否决 idle reap）。retained mailbox `<plugin>:_:catalog` 公布 `{agent, providers:[{provider, models[], parameter_schema}]}`。**catalog 来源与更新（v0.35）**：插件经各自 wire 协议枚举，禁止硬编码清单——hermes = ACP `session/new` 的 `models`（SessionModelState，modelId = `provider:model`，仅已认证 provider）、opencode = ACP `session/new` 的 `configOptions`（select 型 `model` 项，value = `provider/model`）、codex = app-server `model/list`；统一 `agentdriver.CatalogCache`：启动先发静态 fallback，随后一次性后台发现（StartOnce），无周期任务（v0.36 废除 30 分钟轮询）；失败保留旧值，发现成功即重发 retained mailbox（订阅方实时可见）；`<plugin>:_:catalog-refresh` RPC 强制刷新并回最新 catalog，chat 的 `chat:_:agent-catalog-refresh` 扇出触发全部 agent 插件（前端 chat-manager Roles/Routes 面板打开时调用——按需发现的唯一入口）；C1 `<plugin>.catalog` override 恒优先于发现值。chat = 纯编排：经 C1 `plugins.viewer-chat.agents`（agent → 插件 id 映射，带默认值可改）发现 agent 插件、聚合 catalog（对前端暴露 `chat:_:agent-catalog` RPC 供 Roles/Routes 面板做候选选择器）；**profile = routing policy**（既有 RoutingPolicy/RoutingCandidate 模型，不造新概念）——role 挂 `routing_policy_id`，candidate = 有序 (agent, provider, model, parameters) 参数包，turn 开始按序解析首个 enabled 且 agent 插件在线（catalog 存在）的 candidate，`auto_failover` 开启时 start/prompt 失败按序试下一个（`max_attempts` 封顶）；`role.provider/model` 直写字段降为迁移输入（自动转 migrated policy，同生产版 `_ensure_role_routing_migration`）。roles/routing/chat-list 面板 = plugin-level 进程的配置视图（plugin config：roles/policies/agents 映射，C1；instance state：某 chat 的 roles/cwd/session ids，C2+插件 DB）。
- slots：`send-message`（v0.37 起支持一次性 `force_new_session: true`——跳过 runtime 复用与 `role_sessions` 恢复，所有选中 role 强制新建 agent session）、`stop`、`blocks:list`（`{chat_id, after?, before?}` → `{blocks[]}` 按 `occurred_at` 排序，`after`/`before` 限定时间窗 [after, before) ms，缺省全量；**v0.31 起时间线按已加载消息跨度分窗取块**）；`chats:list` include_messages 分页（v0.31/v0.32）：`{chat_id, include_messages: true, before?, before_id?, after?, after_id?, limit?}` → `{chats, messages[], has_more}`，游标 = 复合 `(created_at, id)`：`before` 严格小于（v0.31 上翻页），`after` **含边界**（v0.32 增量拉取：重拉边界行本身 + 全部更新行，升序返回，`has_more` 表示还有更新行——前端循环取完；边界行重拉以便用最终文本替换可能仍在流式的缓存副本），消息升序返回；emits：`chat:{id}:turn-completed`、`chat:{id}:message`、`chat:{id}:block`（块落库即推，v0.30）、`chat:_:active`（mailbox，CWD 联动的 source）。
- **消息渲染（v0.30 定稿）**：一 turn 一盒（user 消息独立盒；role turn 一盒），盒顶 info 条 = 角色图标+名 + 时间（+活跃态 spinner）；盒内 segments 严格按 `occurred_at` 交错——`messages` 文本段走 markdown 渲染，非 `agent_text` 块（thinking/tool_call/tool_result/file_change/command/other）渲染为折叠 activity 行（图标+标签+单行摘要+时间，展开见原文/payload）。markdown 管线 = markdown-it + texmath(KaTeX) + hljs（行号）+ mermaid（对齐生产版 `markdownRender.ts`）；样式 = `--markdown-*`/`--syntax-*` CSS 变量主题（亮/暗内置默认随 app theme，自定义存 localStorage `viewer.markdownTheme.v1`）。
- 存储：`agent-history.sqlite3` → **插件自管 DB**（chat_id 行级作用域，既有决策；per-chat 子进程只写自己 chat 的行，原子 insert 无竞争；WAL 支持并发读）；turn summaries 同库；Hindsight = 外部服务经 bus 消费。**数据面三层（v0.22 定稿）**：`turn_events`（append-only，driver 每条 session update 的完整原文 `raw_json` + per-turn `seq`，任何过滤之前落库，落库失败只记日志不阻断 turn）→ `message_blocks`（从 raw 同步派生的归一化解析块，单独存，`event_id` 回指 raw 行，拿不准的 method 进 `other` 不丢）→ `messages`（用户可见文本视图，行为不变）；删 chat 级联三层。roles/routing policies 同库（**v0.24 从 C1 迁入**：GORM 表对齐生产版 `super_workspace_roles` 与 routing policy 模型——role = id/name/description/prompt/cwd/routing_policy_id/session_policy/context 回收阈值；policy = 有序 candidates（agent/provider/model/parameters/enabled）+ auto_failover；启动时一次性迁移 C1 遗留数据，C1 收缩为插件级配置）。
- 迁移要点：**worker 整套删除**（DB 任务队列 + lease + pid handover 废弃，§9）——per-chat 子进程即 worker（插件侧实现），Viewer 关闭 turn 照跑，子进程启动参数与恢复逻辑是 chat 插件内部 ABI；ACP stdio 降级为子进程内部实现；对外只暴露总线契约。session 三元组复用、turn summary 预算制注入（词数近似，不做 token 精确化）等既有行为不变，只换通信外壳。**接力定案（v0.22）**：多 role 接力 = 插件内顺序执行（`runRelay`），即最终形态，不再回到生产版 worker 队列/lease/failover/cooldown。**provider 定案（v0.22）**：`hermes`（ACP stdio）+ `codex-app-server`（`internal/codexserver/` 原生协议库化）唯二；旧 codex-acp 适配器不移植；opencode 暂不实现 → **v0.23 起 opencode 转为新建**（ACP 第二租户，见上方 Instance 条）。历史迁移脚本把生产版 `super_workspace_messages.raw_json` 幂等迁入 `turn_events`（`seq` 取 `event_index`），`message_blocks` 不迁（可从 raw 重解析）。

### A.8 viewer.voice（语音输入，v0.28 定稿）

> 现状对应：生产版 `voice.py`（`/api/voice/ws`，三种后端 relay）+ `VoiceInputButton.vue` / `stores/voice.ts`。新版只保留 **voice-service relay** 一条链路——offline faster-whisper / whisperlivekit 内嵌后端**不移植**，ASR 一律归外部 voice-service 项目。

- **定位**：功能插件（goroutine，编译期注册），被 chat 等前端复用；无 pane、无 dock 条目。
- **总线契约**（三段式；`{rec}` = 每次录音的会话 id，插件在 start 时签发）：
  - RPC `voice:_:start` `{mime_type, llm_refine}` → `{rec_id}`：插件拨 voice-service WS，发送 start（合并 C1 配置 `plugins.viewer-voice.service_ws/model/language`，空值省略走服务端默认），随后发布 `ready` 事件。
  - publish `voice:{rec}:chunk` `{data}`：base64 音频块（MediaRecorder 250ms 一片，约 5–15KB）；插件解码转发为 WS 二进制帧。总线帧为 JSON，音频即 base64 payload（内核无帧长上限，`SetReadLimit(-1)`）。
  - publish `voice:{rec}:stop`：插件转发 stop，继续 relay 直到 final，随后关闭会话。
  - RPC `voice:_:cancel` `{rec_id}`：中止并清理（关 WS、丢弃结果）。
  - emit `voice:{rec}:event` `{type: ready|processing|partial|committed|final|error, text?, message?}`：voice-service 消息的直通归一（语义对齐生产版 `_normalize_voice_service_message`）。
- **会话生命周期**：final / error / cancel 结束；单条录音设安全上限（默认 10 分钟，超时发 error 并清理）——协议安全帽，非 idle reap。
- **并发**：多 rec 并行；ASR 串行化由 voice-service 自身保证（其全局转写锁）。
- **前端**：`src/plugins/voice/`（无 components/dock）：`voiceStore`（per-composer 状态 + 分段合成，移植生产版语义）+ `VoiceInputButton.vue`（mic/hourglass/record/check 状态机不变）；chat 插件 ChatPane composer **直接 import** 引用（`ctx.input` 共享输入机制不建——单一机制优于特判，Stage A 同 bundle 直接 import 即可）。
- **前端降级（v0.29）**：voice 后端插件缺席时（订阅 retained mailbox `plugins:_:list`——非 RPC——清单无 `voice` 即缺席），VoiceInputButton 置灰禁用、tooltip 说明原因；chat 不硬依赖 voice，其余功能不受影响。插件上线/下线经 mailbox 更新自动反映。
- **外部依赖**：voice-service（默认 `ws://127.0.0.1:8765/v1/voice/ws`，Docker，faster-whisper + 可选 LLM refine）维持外部服务。

### A.9 display 层（layout/shell）

- `Workspace.vue`、`SplitNode.vue`、`stores/layout.ts` → **layout 插件**（Phase 1 先作为壳内建，Phase 5 完成插件化、可替换）。
- `ViewerPane.vue` → 通用 `PluginPaneHost`（§8.3）；15 行 v-else-if viewer 选择链消亡，改查 registry。
- `stores/paneToolbar.ts` → F2 registry 直接沿用其模式。
- `ConfigPanel.vue` → 设置壳（shell）+ per-plugin config section 贡献点。

### A.10 viewer.bus-inspector（Phase 2，调试工具，新增）

> 微内核下的刚需调试工具（§15 风险对策第 1 条"broker ring dump 查询"的产品化）。无现状代码对应。

- 后端：**开放订阅 `>`**（§5.3，无特权）捕获总线全部帧（event / mailbox set / RPC request+response / error），客户端过滤自身 origin 防回声；插件内维护 **ring buffer 默认 5000 条**（instance state 可调）。
- slots：`set-filter`（channel glob / 帧类型 / origin / trace_id / payload 文本）、`pause`、`resume`、`clear`；emits：`bus-inspector:{id}:matches`（命中帧流）、`bus-inspector:{id}:stats`（mailbox：速率、dropped 计数）。
- 前端 pane：filter bar + 实时表格（ts/type/channel/origin/trace/size，行展开 payload JSON）+ pause/resume/clear；**新消息追加不自动滚动**，提供"跳到最新"按钮。
- 防刷屏：瞬时流量超阈值时降采样并显示 dropped 计数；自身帧客户端过滤（§5.3）。
- 排期理由：Phase 3 起每个插件的联调都靠它看消息，故与 terminal 同批落地。

### A.11 完整性校验

- 后端 29 模块：上表覆盖 28；`__init__.py` 不计。
- 53 条路由：全部归入 K（3）/ C1（4）/ C3（0，库化）/ C4（静态+WS）/ files（12）/ git（6）/ terminal（6）/ chat（~22）/ voice（1）。
- 前端 10 viewer / 6 sidebar panel / 9 store / 2 api / 4 util：全部归档（inputSessions 归 chat、scrollMemory 归 files、paths/storage 归共享与 F6）。

---

*评审约定：每节讨论定稿后直接修订本文件并更新版本；§16 问题敲定一条勾一条。*
