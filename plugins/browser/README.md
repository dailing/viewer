# Viewer Browser

`viewer.browser` 是独立外部 Viewer 插件。它在服务器后台运行持久 Chrome，把真实网页以低延迟 H.264/WebRTC 视频显示在 Viewer pane 中，并向 Agent 提供结构化 DOM 工具。没有浏览器扩展、配对流程或第二套后端。

## 目录

- `backend/`：Go 后端；Chrome/Xvfb 生命周期、CDP、页面注册表、lease、FFmpeg 采集编码、Viewer RPC 和 loopback MCP。
- `frontend/`：Browser Dock 与视频 pane；Dock 只列出当前有 watcher 的已打开 Page，“＋”新建并打开 Page；地址栏、人工输入和四组独立 viewport/视频参数。
- `plugin.json`：插件 manifest。

## 页面与视频

- 每个 Page 是同一持久 Chrome profile 下的独立 app window；不同 Page 共享服务器端 Cookie 和登录 session，但可分别导航、调整 viewport 和编码。
- 后端生成 `pg_` + 128-bit 随机 ID。Agent 只能使用用户明确提供的 `page_id`；没有页面枚举、`current page` 或 `bind` 工具。
- `browser_require` / `browser_release` 使用随机 `lease_id`。同页第二个 require 返回 `page_busy`，不会等待、抢占或改选其他页面。
- 人工输入与 Agent lease 互斥。人工 takeover 撤销 Agent lease，旧 Agent 的下一次调用得到 `invalid_lease`。
- 关闭 Viewer pane 只停止观看并释放该客户端的人工控制，不关闭 Chrome Page、不中止 Agent、不释放 Agent lease。最后一名 watcher 离开后停止 FFmpeg。
- Page ID、URL 和 viewport/video 设置持久化到 `browser-pages.json`；后端重启后重开同一 Page ID，旧 lease 全部失效。
- Chrome 使用 Xvfb 中的 headed app window。FFmpeg 直接采集 X11 raw window pixels，经 libx264 编码为 Annex-B H.264 access units；不使用 JPEG、CDP screencast 或 base64 图片帧。
- 插件向 Gateway loopback `/api/media/publish` 发布视频。Viewer 前端通过同源 `/api/media/ticket` 和 `/api/media/webrtc` 建立 WebRTC；不会连接插件 MCP 或 Chrome 调试端口。
- 视频不经过 Viewer Bus mailbox；页面管理、watch 和人工输入仍走 Bus RPC。

## 独立参数

```text
viewport_css_width  = pane_width  / page_zoom
viewport_css_height = pane_height / page_zoom
video_width         = even(viewport_css_width  × render_scale)
video_height        = even(viewport_css_height × render_scale)
```

- **页面缩放（page_zoom）**：改变后台 CSS viewport 和网页布局。
- **render_scale**：只改变编码像素分辨率，不改变 CSS viewport 或输入坐标。
- **FPS**：只改变采集/编码帧率，范围 1–60，默认 15。
- **bitrate**：只改变 H.264 码率，范围 100–50000 Kbps，默认 4000。

一个 Page 可有多名 watcher，但只有一个 viewport owner 和一个人工输入控制者。非 owner 的 pane 缩放不会改变后台窗口。

## Agent MCP

MCP 默认启用并监听 `http://127.0.0.1:18732/mcp`，仅绑定 loopback。Viewer「插件管理 → Browser → 插件设置」可启停 MCP 或修改端口；配置持久化在 `plugins.viewer.browser.mcp`，listener 热切换，不重启 Browser、Chrome 或视频流。工具为：

- `browser_status`
- `browser_require` / `browser_release`
- `browser_snapshot`
- `browser_click` / `browser_fill`
- `browser_navigate` / `browser_wait` / `browser_scroll`

快照返回正文和临时元素 ID；点击、填写要求同一 `snapshot_id`。密码字段拒绝自动填写，导航只允许 HTTP(S)。Agent 数据面是结构化 DOM，不需要从视频猜坐标。

Hermes 配置：

```yaml
mcp_servers:
  viewer_browser:
    url: "http://127.0.0.1:18732/mcp"
    timeout: 45
    connect_timeout: 10
    sampling:
      enabled: false
```

工具注册为 `mcp_viewer_browser_browser_*`。已有 Agent 进程不会热加载新增 MCP 配置，需重读配置后使用。

## 运行依赖与构建

需要完整 Chrome/Chromium、Xvfb、X11 工具，以及带 `x11grab` 和 `libx264` 的 FFmpeg。Chrome 路径可由 `--chrome-path` 指定；未指定时自动探测。

```bash
cd plugins/browser/frontend
npm ci
npm run build

cd ../../..
go test ./plugins/browser/backend
go build -o /tmp/viewer-browser ./plugins/browser/backend
```

`backend/run` 每次启动都会重新构建后端。监督模式在 Viewer「插件管理」中注册 `viewer.browser`，启动命令留空；也可 standalone：

```bash
plugins/browser/backend/run --kernel-ws ws://127.0.0.1:8765/ws
```

默认状态位于 Viewer 数据目录的 `browser-pages.json` 和 `browser-profile/`。默认 MCP 健康检查为 `http://127.0.0.1:18732/health`；改端口后以插件设置中显示的运行地址为准。

## 验证

```bash
# 页面、lease、参数独立性和恢复语义
go test ./plugins/browser/backend

# 真实 Pion WebRTC H.264/RTP 数据面
go test ./internal/plugins/gateway -run TestMediaHubH264WebRTCEndToEnd

# 前端
cd plugins/browser/frontend && npm run build

# 对正在运行的 managed 插件执行真实 Chrome + FFmpeg smoke
.venv/bin/python scripts/smoke_browser_plugin.py \
  --gateway ws://127.0.0.1:18100/ws \
  --mcp http://127.0.0.1:18732/mcp
```

当前版本尚未实现 Managed 下载与文件插件联动、跨 iframe 元素定位、文件上传、剪贴板、完整触摸手势/IME、JS dialog 和权限弹窗。
