# Viewer Plugin Protocol Specification

> 状态：**草案 v0.2**（2026-08-13，**未冻结**）。本文档是插件协议在线路级（wire-level）的唯一权威来源；架构层决策见 `docs/plugin-framework.md`（§5/§6/§9）。Phase 0 要求：**本规范评审冻结后才写码**。
> v0.2 变更：channel field 语法允许点号（`[a-z0-9][a-z0-9_.-]*`）——agent 插件族 id（如 `viewer.agent-hermes`）含点号并进入 channel 第一段（framework v0.23）。
>
> 范围：单机 localhost。多机 federation 暂缓（framework §16-11）。不向后兼容（§12）。

## 1. 总览

- 协议 = **一条 WebSocket 上的 5 种 JSON 帧**：`hello` / `publish` / `set` / `subscribe` / `unsubscribe`。没有第 6 种帧；RPC、错误通知、生命周期全部建立在这 5 种帧与 channel 约定之上。
- 内核（Kernel）是唯一的消息路由者：publish 路由 + mailbox retained 当前值 + 连接注册表。内核不含任何功能逻辑。
- 本规范面向四种实现者：内核、后端插件（任何语言）、gateway（C4 core plugin）、SDK（Python/TS 及未来其他语言）。

## 2. 连接拓扑

内核监听 `ws://127.0.0.1:<port>/ws`。所有组件与内核之间**只有这一种连接**。

| 连接 | 方向 | 说明 |
|---|---|---|
| plugin backend → kernel | 插件主动连 | 每个插件进程（含插件自行 spawn 的子进程）一条 WS，各自 `hello` |
| supervisor (C0) → kernel | 插件主动连 | supervisor 自己也是插件连接；内核唯一 autostart 的进程是它 |
| gateway (C4) → kernel | 插件主动连 | gateway 是普通插件连接 |
| browser → gateway | 浏览器主动连 | **唯一一条多路复用 WS**；gateway 做协议翻译（§11），浏览器不直连内核 |
| plugin ↔ plugin | **不存在直连** | 一切插件间流量经内核路由（含 RPC，§8） |

组件身份只在 `hello` 时建立；连接断开即注销（§9）。

## 3. 传输层

- WebSocket（RFC 6455）**文本帧**，每帧一个完整 JSON object（UTF-8）。无二进制帧；二进制效率不足时未来整体升级 MessagePack，协议语义不变。
- 心跳复用 WebSocket 内建 ping/pong：内核每 **30s** 发 ping，连续 **2 次**未收到 pong（≈60s）即关闭连接。连接存活检测是 lifecycle 事件的来源，不做应用层心跳帧。
- 帧大小上限：默认 **1 MiB**；例外 channel（目前仅 `gateway:_:assets:push`，见 framework §6.2）内核配置放宽至 **64 MiB**。超限帧 → 丢弃 + 错误通知（§10），连接保持。
- TCP 背压内建；应用层背压见 §10。

## 4. Channel 语法与匹配

### 4.1 语法

```
channel := field (":" field)*
field   := [a-z0-9][a-z0-9_.-]*
```

- **前三层语义固定**：`plugin:instance:message`；第四层起插件内部自由 grouping（如 `gateway:_:assets:push`）。
- 无 instance 概念的插件级 channel 用**保留实例名 `_`**（如 `plugins:_:list`、`config:_:get`）。
- **`_` 前缀 = 框架保留命名空间**：`_inbox:*`（RPC 回复通道，§8）、`_conn:{conn}:error`（协议错误通知 mailbox，§10）、`_` 实例占位符。插件不得占用 `_` 开头的 channel 段。
- `plugins:*` 为内核/supervisor 命名空间（连接注册表与生命周期，§9）；其余第一段归对应插件所有。

### 4.2 匹配算法

pattern 语法与 channel 相同，另加两个通配符：`*`（单字段通配，必须占满整个字段，可出现在任意层）与 `>`（匹配总线全部，**只允许作为整个 pattern 单独出现**）。

```
match(pattern, channel):
    if pattern == ">":            return true
    pf := split(pattern, ":")
    cf := split(channel, ":")
    if len(pf) > len(cf):         return false
    for i in 0 .. len(pf)-1:
        if pf[i] == "*":          continue
        if pf[i] != cf[i]:        return false
    return true                   # 前缀规则：pattern 未指定的尾部字段默认全匹配
```

例：`chat:42` 匹配 `chat:42:message`、`chat:42:turn-completed:x`；`chat:*:status` 匹配所有 chat instance 的 status；`chat:42:message:x` 不匹配 `chat:42:message`（pattern 比 channel 长）。

## 5. Envelope 与帧格式

### 5.1 公共约定

- 帧 = JSON object，必有 `type` 字段（5 种取值之一）。未知 `type`、JSON 解析失败、schema 校验失败 → 按 §10 处理。
- **校验是 partial 的**（framework §16-2）：帧结构与 `hello`（含内联 manifest）**强校验**（Pydantic / JSON Schema，实现自定）；`value`（payload 本体）= **任意 JSON，不校验**。payload 内以 `_` 前缀的 key 保留给协议约定（§8），插件自定义 payload 不得使用 `_` 前缀 key。
- 时间戳 `ts`：unix 毫秒，**内核在收帧时打戳**，发送方携带的 `ts` 一律被覆盖。
- 来源 `origin`：`{plugin, instance}`，**内核按连接的 hello 身份打戳**，发送方携带的一律被覆盖（防伪造）。插件未声明 `instance_id` 时 `origin.instance = "_"`。

### 5.2 hello（连接后第一帧，client → kernel）

```json
{
  "type": "hello",
  "protocol_version": 1,
  "conn": "uuid-by-client",
  "manifest": { "id": "chat", "version": "...", "slots": {}, "emits": {} },
  "managed": true,
  "instance_id": "chat-42"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `protocol_version` | ✓ | 整数。**严格相等**，不等 → close 4003（§10），不向后兼容 |
| `conn` | ✓ | 客户端生成的连接 id（uuid4）。用于 `_inbox:{conn}:{corr}` 与 `_conn:{conn}:error`；重连必须生成新值 |
| `manifest` | ✓ | **内联完整 manifest**（内核一侧可能没有插件的 plugin.json 文件）。manifest 不含启动命令 |
| `managed` | ✓ | `true` = supervisor 拉起；`false` = standalone attach / 远程（预留） |
| `instance_id` | 可选 | 信息字段：插件 spawn 的子进程标识所属 instance，用于 `plugins:_:list` 展示与 channel 归属 |

- 连接上**第一帧必须是 hello**，否则 close 4001；hello 校验失败 close 4002。
- **hello 成功无显式 ack**：连接保持打开即成功；可观察性经 `plugins:_:list` mailbox 与 lifecycle 事件（§9）。SDK 等待注册完成的推荐做法：订阅 `plugins:_:list` 并观察自身条目出现（等价于一次同步屏障）。
- hello 之后的重发 hello → 错误通知（§10），忽略。

### 5.3 publish（双向）

```json
{
  "type": "publish",
  "channel": "chat:42:message",
  "value": {},
  "trace_id": "optional-uuid",
  "depth": 0
}
```

- 瞬态事件，broker 不留存。`trace_id`/`depth` 见护栏 §9.2。
- 投递给订阅者时内核补齐 `ts` 与 `origin`（§5.1）。订阅者收到的帧与发送帧同型（`publish`），可据此区分 event 与 mailbox 更新。

### 5.4 set（双向）

```json
{ "type": "set", "channel": "terminal:3:status", "value": { "state": "running" } }
```

- 写 mailbox。**整体替换语义**：`value` 必须是**完整自包含的当前值**；broker 只做 replace，无 delta 合并、无字段级 patch（framework §5.5/§5.6）。禁止 partial/delta。
- 投递规则同 publish（内核补齐 `ts`/`origin`）；订阅者收到 `set` 帧即知是 retained state 更新。

### 5.5 subscribe / unsubscribe（client → kernel）

```json
{ "type": "subscribe",   "pattern": "chat:42" }
{ "type": "unsubscribe", "pattern": "chat:42" }
```

- `pattern` 按 §4.2 校验；非法 pattern → 错误通知（§10），不生效。
- 无 ack。同一连接重复 subscribe 同一 pattern 幂等；unsubscribe 精确匹配 pattern 字符串。
- 连接断开时其全部订阅自动注销。

## 6. Mailbox 语义

- 每 channel 只 retained **最新一条** `set`（无 ring、无历史）。event（publish）无 retained 概念。
- **订阅交接（原子）**：subscribe 生效时，内核先把所有匹配 pattern 的 retained 当前值逐个以 `set` 帧投递，再进入 live 流。retained 投递与 live 投递由 broker 单线程串行化，**不丢不重**——这是"订阅即得当前值"的实现保证，消费者不需要为 state 发 RPC（framework §5.6）。
- **Event vs State 的归类是发布方的义务**（framework §5.6）：State 走 `set` 完整值；Event 走 `publish`；历史查询走 RPC 显式分页快照。
- 插件断开不影响其已写入的 retained 值（值驻留在 broker）；消费者可自行把"生产者离线"标注为 stale（经 `plugins:_:list` 观察生产者在线状态）。

## 7. 连接注册表与生命周期

- 内核维护 mailbox **`plugins:_:list`**：value = 数组，每项 `{id, instance_id, manifest, managed, conn, connected_at}`。任何连接变化即整体 replace。
- 生命周期事件 channel **`plugins:{id}:lifecycle`**（instance 段 = 插件 id）：
  - 内核发布：`activated`（hello 成功）、`deactivated`（连接断开——断开即注销）。
  - supervisor 插件发布：`crashed`、`restarted`（只有它能区分崩溃与正常退出，framework §9）。
- 插件断开后重连：新 `conn`、重新 hello、SDK 自动重放全部 subscribe。
- **内核重启**（framework §16-4）：插件进程**存活重连**——内核关闭时以 close 4009 通知（§10），插件检测到断连后指数退避重连并重新 hello；SDK 负责重放订阅，state 经 §6 交接自动恢复。

## 8. RPC（inbox 约定）

RPC 不是帧类型，是 `publish` 之上的约定（framework §5.3）。跨语言 SDK 必须实现同一套约定以保证互操作：

1. 调用方取 `corr`（uuid4），构造 reply channel **`_inbox:{conn}:{corr}`** 并 subscribe；
2. 请求 = 普通 `publish` 到目标 channel，`value` 含保留 key：
   ```json
   { "_reply_to": "_inbox:9f3c:ab12", "_corr": "ab12", "path": "/foo", "limit": 50 }
   ```
3. 被叫方处理后把响应 = 普通 `publish` 到 `_reply_to`：
   ```json
   { "_corr": "ab12", "ok": true,  "result": {} }
   { "_corr": "ab12", "ok": false, "error": { "code": "not_found", "message": "..." } }
   ```
4. 调用方按 `_corr` 匹配后 unsubscribe 该 inbox channel。

- **超时**：client-side，SDK 默认 **30s** 可配；超时后丢弃迟到响应。
- **取消**：调用方向目标 channel 再发一条 `{ "_corr": "ab12", "_cancel": true }`；被叫方自行决定是否响应/终止。
- **错误分类**：响应 payload 的 `error.code` 是被叫方自己的约定，协议层不分类、不设专门错误 channel（framework §16-3）。
- 请求了没人回、订阅了没人发都是正常状态（松耦合）。
- 分页快照类 RPC 的请求参数必须显式（如 `before_id` + `limit`），禁止隐式取数（framework §5.6）。

## 9. 护栏

1. **死循环防护**：由因果触发的再发布必须携带同一 `trace_id` 且 `depth + 1`；`depth ≥ 8` → 内核丢弃 + 错误通知 + 内核日志。新起因果链的发布不带 `trace_id`（depth 视为 0）。
2. **origin 标记**：内核打戳（§5.1），每条投递帧必带。
3. **回放风暴**：不存在——event 无历史，mailbox 只给当前值（§6）。
4. **防回声**：订阅完全开放（framework §5.3），消费者可能收到自己发的消息；由消费者自行按 `origin` 过滤（如 bus-inspector 排除自身）。

## 10. 错误与背压

### 10.1 协议错误通知（不新增帧类型）

- 注册后发生的协议错误（非法帧、非法 pattern、超帧大小、depth 超限、慢消费者丢弃）→ 内核写入该连接的错误 mailbox **`_conn:{conn}:error`**（value = `{code, message, ts, detail?}`，整体 replace，连续错误计数并入 `detail.count`）。SDK 应自动订阅并表面化（日志/回调）。
- hello 之前/hello 期间的致命错误无法依赖通道 → 用 **WS close frame** 拒绝：

| close code | 含义 |
|---|---|
| 4000 | malformed frame（持续违规时内核可主动断开） |
| 4001 | 首帧不是 hello |
| 4002 | hello schema 校验失败 |
| 4003 | protocol_version 不匹配（严格相等，不向后兼容） |
| 4009 | 内核即将关闭（插件应进入存活重连流程，§7） |

close reason 携带 JSON：`{"code": 4003, "message": "protocol version mismatch: got 2, want 1"}`。

### 10.2 背压

- 每连接出站队列上限 **1000 帧**；超限时**丢弃新帧**并节流发送错误通知（`slow_consumer`，含累计丢弃数）。event 与 set 同策略（mailbox 语义下丢中间 set 无害——下一个 set 仍是完整值）。
- 入站不节流；帧大小上限见 §3。

## 11. 浏览器面（gateway 翻译）

- 浏览器与 gateway 之间是**同一套 5 帧 JSON**（一条多路复用 WS）；gateway 是协议翻译器与订阅代理（framework §6.1）。
- gateway 以自己身份（plugin id `gateway`）对内核持有连接，代浏览器 subscribe/publish；浏览器来源的流量 origin 为 `gateway`（浏览器无插件身份，前端 instance 归属由 gateway 在 channel/payload 层表达）。
- 浏览器帧的大小/错误策略与内核侧一致；gateway 侧的浏览器连接管理（鉴权、断连）是 gateway 插件内部设计，不在本规范范围。

## 12. 版本与兼容

- `PROTOCOL_VERSION = 1`。协商 = **严格相等**：不等即 close 4003，**不向后兼容**（aggressive development 阶段，framework §16-13）。
- 任何帧结构、channel 语义、保留命名空间的变更 → bump `PROTOCOL_VERSION`；存储/schema 变更走一次性迁移，迁移不了丢弃重来可接受。

## 13. 交换时序

### 13.1 插件启动（supervised）

```
kernel                 supervisor (C0)              plugin              gateway/shell
  │ autostart spawn ──▶ │                              │                    │
  │ ◀── hello(C0) ───── │                              │                    │
  │ set plugins:_:list ─┼──────────────────────────────┼───────────────────▶│ (shell 观察)
  │                     │ spawn backend/run            │                    │
  │                     │   --kernel-ws ws://... ─────▶│                    │
  │ ◀───────────────────┼────────── hello(plugin) ─────│                    │
  │ set plugins:_:list; publish plugins:{id}:lifecycle "activated" ────────▶│
  │                     │                              │ push assets (RPC)─▶│ → shell import+activate
```

### 13.2 State 订阅交接（原子）

```
subscriber                     kernel
  │ subscribe "terminal:3:status" │
  │ ◀── set terminal:3:status (retained 当前值) ── │   ← 先给当前值
  │ ◀── set terminal:3:status (live 更新) ... ──── │   ← 后接 live，不丢不重
```

### 13.3 RPC（成功 / 超时）

```
caller                         kernel                       callee
  │ subscribe _inbox:c1:r9        │                           │
  │ publish config:_:get ────────▶│ ── publish ──────────────▶│
  │   {_reply_to:_inbox:c1:r9,    │                           │ handle
  │    _corr:r9, key:"theme"}     │ ◀── publish _inbox:c1:r9 ─│
  │ ◀──────────────────────────── │   {_corr:r9, ok, result}  │
  │ unsubscribe _inbox:c1:r9      │                           │
  │ （无响应：30s 超时，SDK 报 timeout，丢弃迟到响应）           │
```

### 13.4 浏览器输入（前端 → 插件）

```
browser            gateway                    kernel                plugin (terminal)
  │ publish ───────▶│ translate ─────────────▶ │ ── publish ────────▶│ slot: input
  │  terminal:3:input  (origin=gateway)        │                      │
  │ ◀── publish terminal:3:output（gateway 代订阅后转发）◀──────────── │
```

### 13.5 内核重启（存活重连）

```
kernel                        plugin (SDK)
  │ close 4009 ──────────────▶ │ 进程不死，进入重连循环（指数退避）
  │   …内核重启…                │
  │ ◀──── hello（新 conn）───── │ SDK 自动重放全部 subscribe
  │ ── set（retained 当前值）─▶ │ state 自动恢复（§6 交接）；event 流自然接续
```

## 14. 实现清单（冻结核对表）

- [ ] 5 帧 schema（§5）+ partial 校验（帧/manifest 强校验，value 自由）
- [ ] §4.2 匹配算法（含前缀规则、`*`、`>`）
- [ ] mailbox：replace-only + §6 原子交接
- [ ] hello 流程：版本严格相等、conn 规则、无 ack、close codes（§10.1）
- [ ] `_conn:{conn}:error` 通知 mailbox
- [ ] RPC inbox 约定（`_reply_to`/`_corr`/`ok`/`error`/`_cancel`，30s 超时）
- [ ] trace_id/depth 护栏（上限 8）
- [ ] 出站队列 1000 + 慢消费者丢弃通知
- [ ] 重连重放订阅（SDK 职责）
- [ ] ping/pong 30s×2
