# Viewer 统一全文搜索与目录元数据索引 Implementation Plan

> **状态：待用户评审，只规划，不执行。**
>
> **For Hermes：** 用户明确批准后由 Hermes 直接实现，不委派代码实现；开发过程中不重启 Viewer，不提交或推送，除非用户另行要求。

**Goal：** 为 File Manager 和 Chat 提供统一、低延迟、可持续增量更新的全文搜索，并复用文件扫描产生的持久元数据加速目录大小统计。

**Architecture：** 新增 resident core plugin `viewer.search`，统一负责查询、索引状态、文件目录与目录大小缓存。文件与 Chat 使用两个独立 Bleve 索引；文件系统仍是文件真相源，`chat.sqlite3` 仍是聊天真相源，所有索引均可删除重建。前端只提供一个 Search pane，File Manager 和 Chat 的标题栏按钮只是携带上下文过滤条件的入口。

**Tech Stack：** Go、Bleve v2、GORM + modernc SQLite、现有 WebSocket bus、Vue 3 + TypeScript、Vitest、Go tests、Python black-box smoke clients。

---

## 1. 建议用户批准的默认决策

| 决策点 | 默认方案 | 备选/影响 |
|---|---|---|
| 搜索引擎 | **Bleve 优先，先做真实数据技术验证** | 任意 substring/regex 是硬需求时，文件侧可改 Zoekt；统一 RPC/UI 不变 |
| 统一范围 | **统一 RPC、设置、状态与前端；文件/Chat 分索引** | 不把两类文档强行放进同一物理索引 |
| 文件索引根目录 | **显式配置 roots，默认不扫描整个 home** | File Manager 可将当前目录加入索引；避免首次启动无边界扫描 |
| 文件内容范围 | **第一阶段仅 UTF-8 文本、源码、Markdown、JSON/YAML 等** | PDF、Office、压缩包后置，需独立 extractor |
| 文件更新 | **fsnotify 增量 + 定时 reconciliation** | 不采用单纯定时全量重建，也不承诺 watcher 永不漏事件 |
| Chat 内容 | **messages + message_blocks；不索引重复 raw turn_events** | 以 `kind` 过滤 message/tool_call/tool_result/command/thinking 等 |
| branch 语义 | **当前 History DAG 视图中的可见性** | 不把消息写入时的 origin branch 错当成 merge 后的当前归属 |
| Chat 更新 | **SQLite transaction 内写 search outbox，Search 异步消费** | 不只依赖易丢失的 bus event |
| 目录大小 | **复用文件元数据缓存，显示 fresh/stale/rebuilding** | 全文引擎本身不负责递归 SUM(size) |
| 综合结果 | **Files/Chats 分组返回** | 不直接比较两个索引的异构 relevance score |
| 部署 | **嵌入 viewerd，无额外 daemon** | Meilisearch/Typesense/Tantivy sidecar 暂不考虑 |

若用户回复“按默认方案”，即批准以上口径；阶段 0 的基准结果仍可触发文件引擎调整。

---

## 2. 当前代码事实与约束

- `internal/plugins/fileservice/du.go:35` 已用 Go 分片遍历目录，但没有持久元数据，重复目录统计仍需重新遍历。
- `internal/plugins/fileservice/watch.go:1` 只监听已打开预览的少量文件，不能复用为整个索引根目录的递归 watcher。
- `internal/plugins/chat/store.go:76` 的 `Message` 没有简单 `branch_id`；`store.go:126` 的 branch 归属在 Turn 上，用户消息还使用 dispatch id。
- `internal/plugins/chat/view_filter.go:36` 的 `viewLineKeys()` 是当前 line 可见性的权威逻辑，包含 DAG snapshot、running turns 和 queued dispatches。
- Hindsight 是语义记忆派生层，不是完整、确定性的全文历史索引，不能替代 SQLite 搜索源。
- `docs/plugin-framework.md:607` 规定数据库访问走 GORM、禁止裸 SQL；因此默认不采用需要 `CREATE VIRTUAL TABLE` / `MATCH` 的 SQLite FTS5，除非单独批准规则例外。
- 单帧上限为 1 MiB；搜索命中、outbox、文档 hydration 都必须按条数和字节数分页。
- 设计文档只记录批准后的结论；本计划不修改 `architecture.md` 或 `docs/plugin-framework.md`。

---

## 3. 目标架构

```text
SearchPane / FileManager search action / Chat search action
                            │
                     search:_:query
                            │
                    internal/plugins/search
              ┌─────────────┼──────────────┐
              │             │              │
        files.bleve    chat.bleve    search-files.sqlite3
              │             │         file metadata/index state/dir stats
              │             │
       scanner+watcher   chat search outbox adapter
              │             │
         local FS      chat:_:search:* RPC
                            │
                      chat.sqlite3 + viewLineKeys
```

### 3.1 数据落点

在 Viewer data dir 下新增：

- `search/files.bleve/`：文件名、正文与过滤字段。
- `search/chat.bleve/`：消息与 MessageBlock 文本及过滤字段。
- `search/files.sqlite3`：文件元数据、扫描代数、目录统计、文件索引游标与状态；通过 GORM 操作。
- Chat outbox 留在 `chat.sqlite3`，由 Chat 自己拥有，Search 不直接打开该数据库。

索引带 `schema_version` 和 generation。schema 不兼容时后台构建新 generation，完成后切换；旧索引在重建期间继续提供标记为 `rebuilding` 的查询服务。

### 3.2 统一 RPC

第一版只保留必要接口：

- `search:_:query`：统一结构化查询。
- `search:_:reindex`：`{source:"files"|"chat"|"all", root?}`，后台重建。
- `search:_:directory-size` / `search:_:directory-size:cancel`：目录缓存统计。
- `search:_:state` mailbox：各 source/root 的 `indexing/ready/stale/rebuilding/failed`、文档数、最后更新时间、错误与进度。
- 文件 roots、排除规则、文件大小上限等放 `plugins.search` config-store；不另造一套配置数据库。

查询请求形状：

```json
{
  "q": "目录搜索",
  "sources": ["files", "chat"],
  "file_filter": {
    "root": "/path/to/project",
    "recursive": true,
    "extensions": ["go", "md"],
    "scope": "name_and_content",
    "modified_after": 0,
    "modified_before": 0,
    "min_size": 0,
    "max_size": 0
  },
  "chat_scopes": [{"chat_id": "chat-id", "branch_ids": ["", "branch-id"]}],
  "chat_filter": {
    "role_ids": [],
    "kinds": ["message", "tool_call", "tool_result"],
    "after": 0,
    "before": 0
  },
  "limit": 50,
  "cursor": ""
}
```

约束：

- 默认 `limit=50`，硬上限 100；cursor 绑定 source、规范化查询和 index generation。
- `recursive=false` 使用精确 parent path；`recursive=true` 使用带尾部 `/` 的 descendant 前缀，避免 `/foo/bar` 错配 `/foo/barley`。
- filters 是结构化参数，不直接把用户输入拼接成 Bleve query string。
- 综合搜索分组返回；每组有独立 cursor/has_more。
- 命中只携带 id、元数据、score 和有界 snippet；完整正文按需从真相源读取。

---

## 4. 文件索引与目录大小设计

### 4.1 文件元数据

每个 regular file 至少记录：

```text
root_id, path, parent_path, relative_path, name, extension,
size, mtime_ns, content_fingerprint,
scan_generation, index_status, indexed_at, truncated, error
```

目录统计记录：

```text
path, size, files, directories, errors,
complete, fresh, scan_generation, started_at, finished_at
```

规则：

- 使用 Lstat，不跟随 directory symlink；与现有目录统计口径一致。
- `(size, mtime_ns)` 未变时跳过正文读取；需要识别内容是否真实变化时再计算 digest。
- rename/delete 清除旧 path 文档并更新目录统计；不能留下可点击但已不存在的命中。
- 文件查询 hydration 时再检查存在性和 fingerprint，过期命中触发更新而不返回为正常结果。

### 4.2 扫描与监听

1. 启动先打开旧索引，不等待全量扫描。
2. 对每个配置 root 后台 reconciliation，分批 `ReadDir`，有界 worker 和批提交。
3. fsnotify 对变更 debounce；atomic-save rename/create/delete 都进入同一失效路径。
4. watcher overflow、网络 FS 或进程停机造成的漏事件，由 reconciliation 修复。
5. 高频变化文件采用最小重索引间隔，避免日志类文件持续占满 worker。
6. 索引扫描不得通过 bus 传整个文件；File Search adapter 在服务端本地读取有界内容。

第一阶段文本规则：

- 检测 UTF-8 和明显二进制；二进制只索引文件名与元数据。
- 默认单文件正文上限 2 MiB，超限标记 `truncated`，不伪称全文完整。
- 可配置排除 `.git`、`node_modules`、构建产物及隐藏路径。
- roots 默认空；File Manager 提供“索引此目录”动作，不默认遍历整个 home。

### 4.3 目录大小迁移

- 新 Search 文件目录在扫描时同时维护文件元数据和目录汇总。
- `FileBrowser.vue` 从旧 `file:_:du` 迁移到 `search:_:directory-size`。
- fresh complete 缓存直接返回；stale/missing 返回当前值并后台重建；UI 明示状态。
- 迁移验收后删除 `internal/plugins/fileservice/du.go` 及旧 RPC/progress，避免永久保留两套目录遍历机制。
- `file:_:list/read/watch/upload` 等基本文件能力不动。

---

## 5. Chat 索引设计

### 5.1 Outbox 与文档读取

新增 GORM `SearchOutbox`：

```text
seq, operation, entity_kind, entity_id, occurred_at
```

要求：

- message/block 的 insert/update/delete 与 outbox 写在同一 SQLite transaction。
- 覆盖 queued message edit/cancel、普通消息持久化、流式 block 更新、删除和错误块。
- Search 保存已确认 seq；重启后继续消费，失败可重试。
- changes RPC 只返回小型操作记录；文档 RPC 按 ids 和总字节预算返回有界文本。
- 全量 rebuild 直接通过 Chat 提供的分页读取接口完成，不读取 Hindsight。

建议 Chat 内部接口：

- `chat:_:search:changes {after_seq,limit}`。
- `chat:_:search:documents {kind,ids}`，按响应字节预算分批。
- `chat:_:search:rebuild {kind,cursor,limit}`。
- `chat:_:search:hydrate {refs}`，返回有界显示元数据。
- `chat:_:search:visible {chat_id,branch_ids,refs}`，复用当前 view membership 过滤候选。

这些是 Chat 拥有的派生索引接口，不允许 Search 直接导入 Chat 私有 store 或打开其 DB。

### 5.2 branch 过滤

第一版采用正确性优先的候选过滤：

1. Chat Bleve 索引按 `chat_id + q + role/kind/time` 返回候选页。
2. 有 branch 条件时，将候选 refs 交给 Chat；Chat 用 `viewLineKeys()` 判断当前可见性。
3. 搜索 cursor 同时保存 Bleve 候选位置，持续取候选直到填满可见结果页或耗尽。
4. merge/archive 不需要重写全文文档，因为最终过滤总是读取当前 DAG。
5. 对候选过滤做基准；只有实测成为瓶颈时，才设计可随 turn/merge 更新的 materialized membership，不在第一版复制 DAG 状态。

测试必须覆盖：mainline、命名 branch、多级 fork、merge 后目标线、archive 后不可见线、running turn、queued dispatch、共享用户消息。

---

## 6. 前端设计

新增内置 `search` plugin 和 singleton Search pane：

- 标签：`全部 / 文件 / Chat`。
- 文件过滤：root、recursive、extensions、name/content/both、mtime、size。
- Chat 过滤：chat、branch、role、kind、time。
- 显示每组结果、snippet、source 图标、索引状态和独立加载更多。
- 文件结果打开 File Viewer；Chat 结果打开对应 chat view/branch 并定位消息。
- File Manager 标题栏搜索按钮预填当前 dir；另有“索引此目录”。
- Chat 标题栏搜索按钮预填当前 chat + 当前 branch。
- 关闭 Search pane 不影响后台索引；状态来自 mailbox，而非组件本地轮询。

第一版不做全局快捷键、跨索引统一 relevance 排序、语义搜索或向量索引。

---

## 7. 分阶段实施计划

### 阶段 0：搜索引擎技术验证与决策闸门

**Files：**

- Create `internal/plugins/search/engine.go`
- Create `internal/plugins/search/engine_test.go`
- Modify `go.mod`, `go.sum`

**Steps：**

1. 写 Bleve schema/query 的最小测试：中文、英文、标识符、精确短语、prefix、bool filter、numeric/date range、highlight。
2. 建立可重复 benchmark corpus：中英混合 Chat、Go/TS/Markdown、路径和大小过滤。
3. 测索引大小、构建吞吐、增量提交延迟和 warm query p50/p95。
4. 验证 CJK 单字/二字词、`ReadDir`、`foo_bar`、路径边界的实际语义。
5. 若任意 substring/regex 是硬需求，做同数据 Zoekt 对照，不引入第二个正式 backend，只做决策测试。

**Gate：** 默认继续 Bleve；若文件检索语义不满足，批准“Zoekt files + Bleve chat”，统一 RPC/UI 不变。

### 阶段 1：Search 核心骨架与持久状态

**Files：**

- Create `internal/plugins/search/{plugin.go,config.go,types.go,store.go,query.go}`
- Create 对应 `*_test.go`
- Modify `internal/pluginapi/registry.go`

**Steps：**

1. 定义 manifest、RPC、config、状态 mailbox 和生命周期。
2. 用 GORM 建文件元数据/状态 schema 与 migration tests。
3. 实现 query 参数规范化、limit/cursor 校验和错误契约。
4. 注册到 resident core set，确保 Close 停止 workers、watcher 和 index handles。

### 阶段 2：文件目录、正文索引与目录大小

**Files：**

- Create `internal/plugins/search/{file_catalog.go,file_scan.go,file_watch.go,file_index.go,text.go,directory_size.go}`
- Create 对应 `*_test.go`
- Modify `frontend/src/plugins/file-manager/FileBrowser.vue`
- Modify `frontend/src/plugins/file-manager/FileManagerPane.vue`
- Modify `internal/plugins/fileservice/{fileservice.go,du.go,du_test.go}`（最终移除旧 du 实现）
- Update `architecture.md`, `docs/plugin-framework.md`（仅在该阶段口径验收后）

**Steps：**

1. TDD 覆盖分片扫描、symlink、大小/mtime 跳过、二进制和文本上限。
2. 实现持久 file catalog 与 Bleve batch index。
3. 实现 fsnotify 失效、delete/rename 和 reconciliation。
4. 实现 name/content、direct/recursive、extension、mtime/size 查询。
5. 实现 fresh/stale 目录统计和取消语义。
6. File Manager 接入新目录统计与“索引此目录”。
7. 新旧实现对同一目录树结果对账后删除旧 du 路径。

### 阶段 3：Chat outbox、索引与 branch 过滤

**Files：**

- Create `internal/plugins/search/{chat_index.go,chat_source.go}`
- Create 对应 `*_test.go`
- Create `internal/plugins/chat/search_rpc.go`
- Create `internal/plugins/chat/search_rpc_test.go`
- Modify `internal/plugins/chat/store.go`
- Modify 所有 message/block mutation 所在文件，尤其 `runtime.go` 与 queued edit/cancel 路径
- Modify `internal/plugins/chat/view_filter.go`（只抽取复用，不改变可见性语义）

**Steps：**

1. 先写 transaction/outbox 原子性、重启续传和 rebuild 测试。
2. 将每种 message/block mutation 接入同一 outbox helper，审计遗漏路径。
3. 实现有界 changes/documents/rebuild RPC。
4. 实现 Chat Bleve 文档、删除、更新与游标。
5. 实现候选页 + 当前 branch visibility 过滤和稳定分页。
6. 做 merge/archive/queued/running 的端到端回归。

### 阶段 4：统一 Search pane 与上下文入口

**Files：**

- Create `frontend/src/plugins/search/{index.ts,SearchPane.vue,search.ts,types.ts}`
- Create `frontend/src/plugins/search/*.test.ts`
- Modify `frontend/src/plugins/file-manager/FileManagerPane.vue`
- Modify `frontend/src/plugins/chat/ChatPane.vue`
- Modify `frontend/src/plugins/chat/viewInstances.ts`（仅在结果跳转需要 branch 参数时）

**Steps：**

1. 写 query 构造、cursor、状态与结果归并测试。
2. 实现 Search pane 和分组结果。
3. 接文件/Chat 上下文按钮与结果跳转。
4. 验证 pane 关闭重开不丢索引状态，多实例入口条件互不串扰。

### 阶段 5：黑盒、性能验收与文档定稿

**Files：**

- Create `scripts/smoke_search.py`
- Modify `scripts/smoke_all.sh`
- Update `architecture.md`
- Update `docs/plugin-framework.md`，一次性记录已验收的版本结论

**Steps：**

1. 黑盒覆盖文件扫描、修改、rename/delete、Chat 新消息、编辑、删除和 rebuild。
2. 跑 watcher 漏事件模拟、进程重启后续传、索引损坏/版本升级重建。
3. 在真实数据采样上测查询 p95、索引延迟、磁盘占用与目录大小命中率。
4. 更新项目地图和框架文档；不记录未实现的 PDF/Office/Zoekt 为既成功能。

---

## 8. 验证与验收

### 标准检查

```bash
gofmt -l .
go build ./...
go test ./...
go test -race ./internal/plugins/search ./internal/plugins/chat
```

```bash
cd frontend
npx vue-tsc --noEmit
npm test
npm run build
```

```bash
bash scripts/smoke_all.sh
```

不启动 frontend dev server，不重启 Viewer；最终由用户手动测试运行中的服务。

### 功能验收

- 文件名、正文、中文、英文、精确短语均返回正确命中与 snippet。
- 当前目录与递归子目录过滤无路径边界误判；扩展名/时间/大小组合正确。
- 文件 modify、atomic save、rename、delete 后索引最终一致；重启不漏变更。
- 大目录扫描有界，不产生超大 bus frame；查询始终分页。
- fresh 目录大小不再重复完整遍历；stale 状态可见。
- Chat 可按 chat、当前 branch、role、kind、时间过滤。
- queued/running/merge/archive 的 branch 搜索结果与 Chat 时间线可见性一致。
- 删除索引目录后可从文件系统和 `chat.sqlite3` 完整重建。
- 关闭任一 frontend pane 不影响索引 worker 和其他实例。

### 性能验收

先用真实数据确定基线，再固定回归阈值。建议目标而非预先承诺：

- 常用 warm query p95 `<100 ms`。
- 普通文件/Chat 变更在常规负载下数秒内可搜索。
- 搜索响应始终有界；索引扫描不会阻塞 Chat dispatch 或文件列表 RPC。
- 索引磁盘占用、重建吞吐和内存峰值有记录，超出预期时回到引擎决策闸门。

---

## 9. 风险与权衡

- **中文与代码 tokenization：** Bleve CJK bigram 不等同中文分词，也不自动等于任意代码 substring；阶段 0 是硬性闸门。
- **索引新鲜度：** fsnotify 可能 overflow，必须保留 reconciliation 和 stale 状态。
- **目录统计一致性：** 缓存加速来自元数据持久化；变化中的目录只能给带状态的结果，不能伪称实时精确。
- **Chat branch 复杂度：** 静态 `branch_id` 无法表达 merge 后的当前视图；第一版候选过滤可能多取文档，但优先保证不漏、不串线。
- **写放大：** messages、blocks、outbox、Bleve 都增加写入；需批提交，且索引失败不能阻塞 Chat 真相源写入。
- **索引体积：** 不存 raw turn events，不默认索引二进制，不在 bus 上传完整文件；正文上限和排除规则必须可观察。
- **双引擎风险：** 只有 Bleve 实测不满足文件搜索时才引入 Zoekt；不得第一版同时维护两套文件索引。
- **范围膨胀：** 向量/语义搜索、PDF/Office、跨机器索引和独立搜索服务均不在本计划内。

---

## 10. 实施前最终确认项

用户可以直接批准默认方案，或只修改以下项目：

1. 文件搜索是否必须支持任意 substring/regex？默认不把它作为第一阶段硬需求。
2. 是否接受 roots 显式配置、默认不扫描整个 home？默认接受。
3. 是否接受第一阶段只索引文本类文件？默认接受。
4. Chat 是否默认包含所有可见 MessageBlock，并通过 kind 排除？默认包含。
5. 是否接受旧 `file:_:du` 在新目录统计验收后删除？默认接受，避免双机制。

批准后再进入阶段 0；本计划本身不授权产品代码改动。
