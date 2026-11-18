# Viewer Browser

`viewer.browser` 是独立外部 Viewer 插件。它在服务器后台运行持久 Chrome，把真实网页以低延迟 H.264/WebRTC 视频显示在 Viewer pane 中，并向 Agent 提供结构化 DOM 工具。没有浏览器扩展、配对流程或第二套后端。

## 目录

- `backend/`：Go 后端；Chrome/Xvfb 生命周期、CDP、页面注册表、lease、FFmpeg 采集编码、Viewer RPC 和 loopback MCP。
- `frontend/`：Browser Dock 与视频 pane；Dock 列出所有已打开 Page（包括当前没有 watcher 的 Page），点击条目可重新打开 pane，“＋”新建并打开 Page；地址栏、人工输入和四组独立 viewport/视频参数。
- `plugin.json`：插件 manifest。

## 页面与视频

- 每个 Page 是同一持久 Chrome profile 下的普通 Chrome window；window 内支持多个 tabs。不同 Page 共享服务器端 Cookie 和登录 session，但可分别导航、调整 viewport 和编码。
- 后端生成 `pg_` + 128-bit 随机 ID。Agent 只能使用用户明确提供的 `page_id`；没有页面枚举、`current page` 或 `bind` 工具。
- `browser_require` / `browser_release` 使用随机 `lease_id`。同页第二个 require 返回 `page_busy`，不会等待、抢占或改选其他页面。新建 Agent lease 时只读当前 window 各 tab 的可见状态，以人工选中的 visible tab 建立控制基线；不会调用 `bringToFront` 或切回旧 tab，续用同一 lease 也不重置该基线。
- 人工输入与 Agent lease 互斥。人工 takeover 撤销 Agent lease，旧 Agent 的下一次调用得到 `invalid_lease`。
- 关闭 Viewer pane 只停止观看并释放该客户端的人工控制，不关闭 Chrome Page、不中止 Agent、不释放 Agent lease。最后一名 watcher 离开后停止 FFmpeg。
- Page ID、URL 和 viewport/video 设置持久化到 `browser-pages.json`；后端重启后重开同一 Page ID，旧 lease 全部失效。
- Chrome 使用 Xvfb 中的 headed 普通窗口，不为每个 tab 新开 Viewer Page。FFmpeg 采集当前可见 tab 的 X11 raw window pixels，经 libx264 编码为 Annex-B H.264 access units；不使用 JPEG、CDP screencast 或 base64 图片帧。Agent 遇到 popup 或看不见的后台 tab 时不会盲点，而是得到 `page_visible_required`。
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

- 页面与 lease：`browser_status`、`browser_require` / `browser_release`。
- 观察：`browser_snapshot`、`browser_evaluate`、`browser_screenshot`、`browser_wait`。
- 导航：`browser_navigate`、`browser_go_back` / `browser_go_forward`、`browser_reload`、`browser_scroll`。
- 操作：`browser_click` / `browser_double_click` / `browser_right_click`、`browser_hover`、`browser_fill` / `browser_type`、`browser_select`、`browser_send_keys`、`browser_find_text`、`browser_dropdown_options`。
- 文件：`browser_upload`、`browser_downloads`。
- 当前 Page 内的 tabs：`browser_list_tabs`、`browser_new_tab`、`browser_switch_tab`、`browser_close_tab`。

`browser_snapshot` 聚合主文档、open shadow roots 以及同源/跨源 frames。`elements[]` 使用 Page 内全局临时元素 ID，并携带 `frame_id`；`frames[]` 返回各 frame 的 ID、parent 和 URL，`text`、`max_chars`、`max_elements` 预算是整个聚合快照共享的。元素动作通过执行上下文绑定到原 frame，子 frame 坐标换算到所属 window；frame 导航后旧引用返回 `stale_frame`，不会退化到主文档或同 ID 的另一个元素。`browser_evaluate` 可用 `frame_id` 指定 frame，省略时只操作主文档。

快照元素引用只在原 `snapshot_id`、tab 和 frame 上下文中有效；导航、DOM 重新定位或 tab 操作后应重新 snapshot。密码字段拒绝自动填写，导航只允许 HTTP(S)，点击与填写不会替代视频人工观察。后台或 popup 遮挡导致无法证明目标可见时，普通动作快速失败并给出 `page_visible_required`，不会盲点坐标。

`browser_list_tabs` 只列当前 Page 的 window，不是跨 Page 枚举。人工切换 active tab 后，旧 Agent 得到 `active_tab_changed`；未经用户确认不能自动切回。用户确认恢复后的新 Turn 必须重新 require；新 lease 采用人工留下的 visible tab，随后重新 snapshot，无需也不应再调用 `browser_switch_tab`。`browser_upload` 只接受显式输入，多个文件需要声明 `files`；`browser_downloads` 列出该 Page 的 managed 下载，完成后可通过 `read_file` 或文件插件查看。下载不是由 MCP 猜测全局 Chrome 下载目录。

`browser_status` 在 JS dialog、CAPTCHA、OAuth 或意外 popup 需要人工时返回 `human_required={kind,detail}`。普通动作在此期间立即失败；dialog 打开/关闭通过当前 window 的 CDP 事件循环跟踪，不存在自动 accept/dismiss 工具。

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

## 人工接管与恢复

登录、密码、2FA、CAPTCHA、OAuth/授权、支付确认和不明确的权限/dialog 由用户在真实 Page 中完成。Agent 不使用 JS、截图、键鼠、Cookie/storage 或原始 CDP 绕过这一边界，也不在聊天或 Decision 中索取凭据。

需要人工时，Agent 停止操作并释放 lease，结束当前 Turn 但保持 Task 和 Page 打开；回复中给出 `page_id`、当前进度和具体步骤，然后使用普通 Decision。例如：

```text
Page pg_... 已打开登录页，请你在该 Page 完成登录；我不会读取或填写密码。
<viewer-decision>
version: 1
kind: choice
question: 登录需要人工完成，之后继续原 Page
option: 我已完成登录，重新接管并继续
option: 无法登录，换其他方案
option: 停止浏览器任务
display: buttons
tone: neutral
</viewer-decision>
```

普通 Decision 回复启动新 Turn。Agent 必须重新 `browser_require` 并取得新 snapshot；旧 lease、snapshot、元素、frame 和 tab 引用都不能复用。`invalid_lease`、`page_busy`、`active_tab_changed`、`human_required` 都表示停止并询问，不能盲目重试、抢占或自动切回用户刚切走的 tab。Chat/Decision 不需要 Browser 专用字段或 action。

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

# 真实 Chrome：tabs、shadow DOM、同源/跨源 frames、上传下载、人工 dialog
VIEWER_BROWSER_INTEGRATION=1 go test ./plugins/browser/backend -count=1

# 真实 Pion WebRTC H.264/RTP 数据面
go test ./internal/plugins/gateway -run TestMediaHubH264WebRTCEndToEnd

# 前端
cd plugins/browser/frontend && npm run build

# 对正在运行的 managed 插件执行真实 Chrome + FFmpeg smoke
.venv/bin/python scripts/smoke_browser_plugin.py \
  --gateway ws://127.0.0.1:18100/ws \
  --mcp http://127.0.0.1:18732/mcp
```

当前剩余非目标：上传目录选择或任意路径写入、浏览器全局下载目录扫描、剪贴板、完整触摸手势/IME、自动处理 JS dialog/CAPTCHA/OAuth/支付、跨 Page tab 操作、页面枚举或 current-page 自动绑定。登录和授权继续复用 Viewer 的人工控制与普通 Decision，不增加 Browser 专用 Chat/Decision 字段或 action。
