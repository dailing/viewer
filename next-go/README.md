# next-go — Viewer 微内核栈的 Go 主线

设计决策见 `docs/plugin-framework.md` §17（v0.20）。核心集（内核 + core plugins + 前端 embed）交付为单一静态二进制；外挂插件独立进程连总线，语言无关。

## 布局

```
cmd/viewerd/        单二进制入口（kernel + 核心插件集 + embed 前端）
internal/protocol/  帧类型、channel 匹配、hello 校验（纯函数，无 IO）
internal/broker/    publish 路由 + mailbox(retained) + 连接注册表
internal/kernel/    WS 端点 + 连接生命周期 + autostart
internal/pluginapi/ 进程内插件契约（interface + 编译期 registry）
internal/plugins/   核心插件集（supervisor/terminal/gateway/...）
web/                go:embed 前端 dist 的挂载点
```

## 迁移顺序与验收

内核 → terminal → gateway(+embed) → supervisor / config-store /
instance-store / file-service / inspector → 前端适配 → chat。

每一步的验收标准：现有测试套件（`next/tests/` pytest + `next/ts-sdk/`
vitest）直接对新栈通过——测试即规格。Python 栈（`next/`）为协议参考实现。

## 构建

```bash
export PATH="$HOME/.local/go/bin:$PATH"
go build ./... && go vet ./... && go test ./...
```
