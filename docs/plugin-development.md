# Viewer 插件开发与接入指南

> 面向外部插件开发者的实操手册：接口、选项、以及你应该 expect 的行为。
> 架构决策的权威来源是 `docs/plugin-framework.md`（v0.39+）；本文档描述**当前已实现的机制**，两者冲突时以 framework 文档为准并修正本文档。

## 0. 全景

Viewer 是微内核插件系统：内核只做 WebSocket 消息路由（channel 匹配 + mailbox），一切功能都是插件。你的项目作为**外部插件**接入只需两步：

1. **后端**：一个进程，连上内核 WS（默认 `ws://127.0.0.1:8765/ws`），发 `hello` 握手即注册，断开即注销。语言不限。
2. **前端（可选）**：把你的 UI 构建成一个 ESM bundle（入口 `frontend.js`），由后端通过总线 RPC `gateway:_:assets:push` 推送；浏览器 shell 收到后**热加载，无需刷新页面**。

两种进程模式，可以随时混用：

| 模式 | 谁拉起进程 | 适用 |
|---|---|---|
| **standalone attach** | 你自己（IDE、终端、任何方式） | 开发调试；attach/detach 即时生效 |
| **managed（插件管理器）** | Viewer 内置的 plugin manager 按注册条目拉起、监控、重试 | 常驻运行 |

## 1. 插件目录的标准形态

```
my-plugin/
├── plugin.json          # 元信息（见下；系统不强制读取，是给人和工具看的标准形式）
├── backend/
│   └── run              # 可执行入口（固定 ABI，见 §2.1）；语言不限
└── frontend/            # Vue 3 SFC + TS 源码
    └── dist/            # 构建产物：frontend.js（必需）+ frontend.css（可选）
```

`plugin.json` 标准形式：

```json
{
  "id": "my-plugin",
  "version": "0.1.0",
  "name": "My Plugin",
  "icon": "bi-puzzle",
  "description": "一句话描述",
  "slots": { "echo": {} },
  "emits": { "events": {} }
}
```

- `id`：`^[a-z0-9][a-z0-9_.-]*$`，全系统唯一，channel 第一段。
- `name` / `icon`（bootstrap-icons class）/ `description`：展示元数据。插件管理器列表、shell placeholder 都用它。**真正的权威 manifest 是后端 hello 里内联的那份**（SDK 构造参数），请让两者保持一致。
- `slots` / `emits`：标注性声明（别人能调你哪些 RPC、你会发哪些 channel），不做运行时校验。

## 2. 后端契约

### 2.1 启动 ABI（固定，不变）

```
backend/run --kernel-ws ws://127.0.0.1:8765/ws
```

- `--kernel-ws` 由 plugin manager **自动追加**（managed 模式），或你自己写上（standalone）。
- managed 模式下进程带有环境变量 `VIEWER_MANAGED=1`。
- 你应该处理 **SIGTERM** 并优雅退出：停止流程是先 TERM，2 秒宽限后 KILL 整个进程组（你的子进程也会被收掉）。
- stdout/stderr 被追加写入 `<数据目录>/logs/<id>.log`（默认 `~/.local/share/viewer/logs/`）。

### 2.2 hello 握手与注册

连接内核 `/ws` 后第一帧必须是 hello，内联完整 manifest。SDK 会帮你做。你应 expect：

- 注册成功后出现在 `plugins:_:list` mailbox（browser 和所有插件可见）。
- **断开连接 = 立即注销**，你的 channel 全部失路由（调用方收到 `no_route` 错误）。重连由 SDK 自动完成（指数退避），重连后订阅自动恢复。
- 协议版本严格相等，不匹配会被拒连（用配套版本的 SDK 即可，不用关心）。

### 2.3 Channel 与消息

- 命名：`<plugin-id>:<instance>:<message>`，第三层起自由分组（如 `my-plugin:_:events`、`my-plugin:_:state:get`）。插件级（无 instance）用保留实例名 `_`。
- 三种操作：
  - `publish(channel, value)` — 即发即弃事件。无订阅者时静默丢弃。
  - `set(channel, value)` — mailbox（retained 最新值）。新订阅者立即收到当前值。**状态同步请用 mailbox，不要等查询**。
  - `request(channel, payload)` — RPC，本质是带 `_reply_to`/`_corr` 的 publish（inbox 约定）。
- 订阅 pattern 支持精确、前缀隐式全匹配、`*` 单层通配、`>` 全量。
- **限制**：单帧（含信封）1 MiB——大负载请分片或走引用；RPC 默认 30s 超时（可调）；RPC 目标无订阅者时**秒级返回 `no_route` 错误**（不会干等）。

### 2.4 提供 RPC（被调用方）

订阅你的 slot channel，在 handler 里回复。SDK 提供 `Respond`/`RespondError`（inbox 约定封装，非 RPC 帧自动忽略）：

```go
client.Subscribe("my-plugin:_:echo", func(frame busclient.Frame) {
    value, _ := busclient.Object(frame)
    _ = busclient.Respond(client, frame, map[string]any{"echo": value["text"]})
})
```

调用方收到 `{ok: true, result: ...}` 或 `{ok: false, error: {code, message}}`。取消是带 `_cancel: true` 的同 channel 帧（best-effort），`busclient.Cancelled(frame)` 识别。

### 2.5 最小后端（Go）

```go
package main

import (
    "context"
    "flag"
    "os"
    "os/signal"
    "syscall"

    "viewer/sdk/go/busclient"
)

func main() {
    kernelWS := flag.String("kernel-ws", "", "kernel WebSocket URL") // 固定 ABI
    flag.Parse()

    client := busclient.New(*kernelWS, busclient.Manifest{
        ID: "my-plugin", Version: "0.1.0",
        Name: "My Plugin", Icon: "bi-puzzle",
        Slots: map[string]any{"echo": map[string]any{}},
        Emits: map[string]any{},
    })
    client.Subscribe("my-plugin:_:echo", func(f busclient.Frame) {
        v, _ := busclient.Object(f)
        _ = busclient.Respond(client, f, map[string]any{"echo": v["text"]})
    })
    if err := client.Connect(context.Background()); err != nil {
        panic(err)
    }
    // 有前端 UI 时：构建产物推送（见 §3.4）
    // defer 或 on_start 后调用：
    // busclient.PushAssets(context.Background(), client, "frontend/dist",
    //     map[string]any{"name": "My Plugin", "icon": "bi-puzzle"})

    sig := make(chan os.Signal, 1)
    signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
    <-sig
    _ = client.Close()
}
```

`backend/run` 就是编译后的这个二进制（或一个 exec 它的脚本）。

### 2.6 最小后端（Python）

```python
from sdk import Plugin, slot  # sdk/python（加入 PYTHONPATH 或随插件目录携带）

class MyPlugin(Plugin):
    manifest = {"id": "my-plugin", "version": "0.1.0",
                "name": "My Plugin", "icon": "bi-puzzle",
                "slots": {"echo": {}}, "emits": {}}

    async def on_start(self) -> None:
        # 有前端 UI 时推送构建产物（见 §3.4）
        # await self.push_assets("frontend/dist",
        #     manifest={"name": "My Plugin", "icon": "bi-puzzle"})
        pass

    @slot("my-plugin:_:echo")
    async def echo(self, ctx) -> None:
        await ctx.respond({"echo": ctx.frame["value"].get("text")})

MyPlugin().run()  # 解析 --kernel-ws，处理 SIGINT/SIGTERM，自动重连
```

`backend/run`：一个可执行脚本，例如 `#!/bin/sh\nexec uv run python backend/main.py "$@"`。

### 2.7 推送前端资产（有 UI 的插件必做）

hello 之后调用一次即可；SDK 自动选择 one-shot 或分片（超 1 MiB 帧限制）传输：

| SDK | 调用 |
|---|---|
| Go | `busclient.PushAssets(ctx, client, "frontend/dist", manifest)` |
| Python | `await plugin.push_assets("frontend/dist", manifest=...)` 或模块级 `push_assets(client, dir, ...)` |

你应 expect：

- `frontend/dist` 必须含入口 **`frontend.js`**（缺失直接报错）；`frontend.css` 若存在会被 shell 自动注入。
- 推送成功 → `plugins:_:assets` mailbox 出现你的条目 → **所有开着的浏览器页面热加载你的插件**（无刷新）。
- 重新构建后再推一次 = **热重载**：内容 hash 变化 → 新 URL → shell 先 deactivate 旧模块再 import 新模块，打开的 pane 原地更新。
- 资产存于 gateway 内容寻址库（保留最近 3 代），重启 viewer 不丢。
- 资产 id 绑定你的连接身份：你只能发布/覆盖自己的 bundle。

## 3. 前端契约

### 3.1 构建配置（Vite library mode）

插件前端独立构建，**与 viewer 主仓库零耦合**。关键：把 `vue`/`pinia` 外置——shell 的 import map 会把它们解析到 shell 单例（打两份 Vue = 响应式损坏）。

```ts
// vite.config.ts
import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

export default defineConfig({
  plugins: [vue()],
  build: {
    lib: { entry: "src/index.ts", formats: ["es"], fileName: () => "frontend.js" },
    cssCodeSplit: false,          // 样式收进单个 frontend.css，shell 自动注入
    rollupOptions: { external: ["vue", "pinia"] },  // 由 shell import map 提供
  },
});
```

构建产物放进 `frontend/dist/`（`frontend.js` + 可选 `frontend.css`），由后端 push。

### 3.2 模块契约（`src/index.ts`）

默认导出 `definePlugin({...})`。类型来自 `@viewer/bus-sdk` 的 `plugin.ts`（与 shell 结构化兼容，纯类型，无运行时依赖）：

```ts
import { defineAsyncComponent } from "vue";
import { definePlugin } from "@viewer/bus-sdk"; // sdk/ts

export default definePlugin({
  id: "my-plugin",   // 必须与后端 hello manifest 的 id 一致（加载时校验）
  components: {
    // pane type → 组件；component 经 inject("pluginCtx") 拿 pane ctx
    "my-plugin": defineAsyncComponent(() => import("./MainPane.vue")),
  },
  createDockProvider: (ctx) => ({
    type: "my-plugin",        // 与 components 的 key 对应
    icon: "bi-puzzle",
    title: "My Plugin",       // Dock "+" 菜单里的名字
    singleton: true,          // 单实例面板；多实例见 §3.3
    instances: [],
  }),
  // 可选：Dock 底部（pin/设置旁）的全局动作按钮，如 voice-control 的耳机开关。
  // icon/title/active 是 getter，渲染期调用，ref 状态保持响应式。
  createDockActions: (ctx) => [{
    id: "my-plugin-action",
    icon: () => "bi-lightning",
    title: () => "Do something",
    onClick: () => { /* ... */ },
  }],
  activate: (ctx) => { /* 插件级初始化：订阅 mailbox、注册全局行为 */ },
  deactivate: () => { /* 热卸载/热重载前调用；ctx 的订阅会被自动清理 */ },
});
```

shell 对你的模块是**结构化（鸭子类型）校验**：字段名对上即可。加载顺序：components → createDockProvider → createDockActions → activate（所以后三者若共享状态要懒初始化，见 voice-control 的 `ensure()`）。

### 3.3 组件与 ctx

pane 组件在 `setup` 里：

```ts
import { inject } from "vue";
import type { PluginCtx } from "@viewer/bus-sdk";

const ctx = inject<PluginCtx>("pluginCtx");
if (!ctx) throw new Error("requires PluginPaneHost");
```

ctx 提供（pane 级 scope = `<paneType>:<instanceId>`，插件级 instanceId 为 `"_"`）：

| API | 说明 |
|---|---|
| `ctx.bus.subscribe(pattern, handler)` | 订阅；**dispose 时自动退订，不用写清理代码** |
| `ctx.bus.request(channel, payload?)` | RPC，30s 默认超时 |
| `ctx.bus.publish / set / cancel` | 事件 / mailbox / 取消 |
| `ctx.storage.get(key, fallback)` / `set` / `remove` | 同步、按 scope 命名空间的 localStorage（F6 视图状态） |
| `ctx.setChrome({title, status, statusClass, actions, controls})` | 往 shell 标题栏注册内容（按钮/下拉/chips），插件不自绘标题栏 |
| `ctx.onDispose(fn)` | 注册清理回调（pane 关闭 / 插件卸载时调用） |

多实例插件（如 terminal）：`instances` 是**响应式数组**（`ref`/`reactive`），你的 activate 里维护它（订阅后端 mailbox 增量更新）；`create()` 是 Dock "+" 动作。DockInstance 的 `state`（`"running" | "unread" | "error" | "dead"`）驱动状态点颜色。Dock 条目上没有动作按钮——实例的终止/删除走 pane chrome action（`ctx.setChrome`，如 terminal 的终止按钮）或 pin + 关闭剪枝语义（如 files）。

### 3.4 样式约定

- 用 shell 的 CSS 变量：`var(--color-surface)`、`var(--color-text)`、`var(--color-accent)`、`--font-size-ui` 等（见 `frontend/src/styles.css`/主题系统），自动适配用户主题（含亮暗切换）。**不要在自己的根元素上重新声明 `--color-*` 变量**——那会覆盖活动主题。
- 通用元素直接用 shell 的样式 class（`.v-bg-surface` / `.v-fg-muted` / `.v-title` / `.v-panel` 等，完整清单见 plugin-framework §8.10）；状态色/徽标底色用 `color-mix(in srgb, var(--color-*) N%, transparent)` 派生，样式表里不写字面 hex 颜色。
- 自建顶栏（tab bar 等）用 `var(--color-titlebar)` / `var(--color-titlebar-text)`，与 pane 抬头一致。
- 组件用 SFC `scoped` 样式；构建时 `cssCodeSplit: false`，产物 `frontend.css` 由 shell 自动注入/移除。
- 不要引入全局 reset 或污染全局选择器。

## 4. 接入操作

### 4.1 开发期：standalone attach（推荐）

```bash
# 终端 1：你的后端（IDE 里跑也行）
./backend/run --kernel-ws ws://127.0.0.1:8765/ws
```

后端 hello → 注册；`push_assets` 一执行，浏览器里插件立刻出现（Dock 有入口、pane 可开）。改代码 = 重启你的进程 + 重新 push，**viewer 完全不用动**。

### 4.2 常驻：交给插件管理器

打开 Dock 的「插件管理」pane（拼图图标）→ 新建：

- **ID / 名称**：与 manifest 一致。
- **插件目录**：插件根目录（含 `backend/`）。
- **启动命令**：留空 = 默认 `backend/run`；或自定义如 `uv run python backend/main.py`（空格分隔 argv；相对路径相对插件目录；`--kernel-ws` 自动追加，别自己写）。
- **自动启动**：勾选则 viewer 启动时拉起；默认手动。

之后从面板启动/停止/重启。你应 expect 的失败语义：**连续崩溃自动重试 3 次**（指数退避），仍失败转「失败」状态不再拉起（手动启动重置计数）；进程稳定运行超过 60 秒则计数清零。日志在 `~/.local/share/viewer/logs/<id>.log`。

### 4.3 热卸载

- managed 删除条目：停进程 + 移除前端资产 → 浏览器端自动 deactivate、Dock 条目消失、已开 pane 变 placeholder。
- standalone：断开连接只注销后端（前端仍在，RPC 会 no_route）；要彻底移除请在插件管理器删除对应条目或直接调 `gateway:_:assets:remove {id}`。
- 逻辑卸载不保证内存清零（已 import 的模块字节留在页面里）——**刷新页面得到干净状态**。

## 5. 调试

| 手段 | 用途 |
|---|---|
| bus-inspector 插件（activity 图标） | 订阅 `>` 抓全部帧，按 channel/origin 过滤——看你的 hello、RPC、mailbox 是否如预期 |
| `plugins:_:list` mailbox | 你的后端是否注册、manifest 展示字段是否正确 |
| `plugins:_:assets` mailbox | 你的 bundle 是否已推送、URL/hash 是什么 |
| `supervisor:_:states` mailbox / 插件管理器 pane | managed 进程状态（启动中/运行中/已崩溃/失败）、PID、连续失败次数 |
| `~/.local/share/viewer/logs/<id>.log` | managed 插件的 stdout/stderr |
| 浏览器 console | shell 加载日志：`external plugin <id> loaded/unloaded/failed` |

## 6. 接入检查清单

- [ ] `id` 合法且全局唯一，前后端 manifest 一致
- [ ] `backend/run --kernel-ws` 可独立跑通，`plugins:_:list` 里出现
- [ ] RPC slot 已订阅且用 `Respond`/`RespondError` 回复；负载 < 1 MiB/帧
- [ ] 有 UI：vite lib 构建（external vue/pinia、cssCodeSplit:false）→ `frontend/dist/frontend.js`（+`.css`）→ hello 后 push
- [ ] 前端 `definePlugin` 的 `id` 与后端一致（shell 校验，不一致拒绝加载）
- [ ] 订阅都走 `ctx.bus`（自动清理）；deactivate 里只清非 ctx 资源（定时器等）
- [ ] SIGTERM 下进程能退出；managed 模式验证过重试/broken 语义符合预期
