# next-go — Viewer 微内核栈的 Go 主线

设计决策见 `docs/plugin-framework.md` §17（v0.20）。核心集（内核 + core plugins + 前端 embed）交付为单一静态二进制；外挂插件独立进程连总线，语言无关。

## 布局

```
cmd/viewerd/        单二进制入口（kernel + 核心插件集 + embed 前端）
cmd/viewer-kernel/  独立 kernel 入口（SDK 测试与外挂调试）
internal/protocol/  帧类型、channel 匹配、hello 校验（纯函数，无 IO）
internal/broker/    publish 路由 + mailbox(retained) + 连接注册表
internal/kernel/    WS 端点 + 连接生命周期
internal/busclient/ Go 插件总线 SDK（RPC、订阅、重连）
internal/pluginapi/ 进程内插件契约、编译期 registry 与生命周期装配
internal/plugins/   核心插件集（supervisor/terminal/gateway/...）
web/                go:embed 前端 dist 的挂载点
```

## 单二进制运行

`viewerd` 启动内核后，把 inspector、config-store、instance-store、
file-service、terminal、supervisor 和 gateway 全部装配到同一进程。每个插件仍用
Go bus SDK 经 loopback WebSocket 连接内核，不存在第二种进程内 transport；外挂插件
继续直连内核 `/ws`。

```bash
go build -o /tmp/viewerd ./cmd/viewerd
/tmp/viewerd \
  --host 127.0.0.1 --port 18730 \
  --kernel-host 127.0.0.1 --kernel-port 8765 \
  --data-dir /tmp/viewer-data
```

- 浏览器与 SDK：`ws://127.0.0.1:18730/ws`（gateway），HTTP 静态资源同端口。
- 外挂插件：`ws://127.0.0.1:8765/ws`（kernel）。内核默认且应保持 loopback；
  `--kernel-host` 只有显式指定时才会改为其他地址。
- gateway 可用 `--host` 对外暴露；这不会改变内核绑定地址。
- `--data-dir` 默认是 `$XDG_DATA_HOME/viewer`，未设置 XDG 时为
  `~/.local/share/viewer`；`config.json`、`instance.json`、外挂 registry 和日志均在其中。
- 默认静态资源是从 `next/frontend` 构建并内嵌到二进制的 UI。开发时用
  `--static ../next/frontend/dist` 覆盖 embed。

SIGINT/SIGTERM 会让内核先向所有连接广播 4009，再逆序关闭插件和 PTY，整个关闭流程
受 10 秒 deadline 约束。插件顶层、订阅 handler 与 SDK callback 都有带 plugin id 和
stack 的 panic 防护。

## 迁移顺序与验收

内核 → terminal → gateway(+embed) → supervisor / config-store /
instance-store / file-service / inspector → 前端适配 → chat。

每一步的验收标准：现有测试套件（`next/tests/` pytest + `next/ts-sdk/`
vitest）直接对新栈通过——测试即规格。Python 栈（`next/`）为协议参考实现。
`next/ts-sdk` vitest 直接运行 Go kernel binary，可用 `VIEWER_KERNEL_BIN` 覆盖路径。

## 构建

```bash
export PATH="$HOME/.local/go/bin:$PATH"
go build ./... && go vet ./... && go test ./...
```

发布构建先构建 `next/frontend`、同步到 embed 目录，再产出单二进制（默认
`next-go/dist/viewerd`，可把输出路径作为第一个参数）：

```bash
./web/build-release.sh
```

单二进制黑盒验收：

```bash
go build -o /tmp/viewerd ./cmd/viewerd
../next/.venv/bin/python scripts/smoke_single_binary.py --viewerd-bin /tmp/viewerd
```

Go SDK 的双内核集成冒烟：

```bash
./scripts/smoke_sdk.sh
```

构建 C0 supervisor 与 bus-inspector 后，可用 Python SDK 做黑盒冒烟（默认监听端口 29371/29372）：

```bash
go build -o /tmp/viewerd ./cmd/viewerd
go build -o /tmp/viewer-supervisor ./cmd/viewer-supervisor
go build -o /tmp/viewer-inspector ./cmd/viewer-inspector
../next/.venv/bin/python scripts/smoke_supervisor.py
../next/.venv/bin/python scripts/smoke_inspector.py
```

双向 RPC 示例见 `examples/pingpong/`。
