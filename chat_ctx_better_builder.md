# Chat Context Bootstrap 改进设计（chat_ctx_better_builder）

状态：设计阶段（文档先行，逐步实现，逐步修改本文档）
创建：2026-08-06
相关代码：`backend/app/agent_history.py`、`backend/app/super_workspace_runtime.py`、`scripts/preview_chat_memory_bootstrap.py`（preview 脚本）、`tmp_out.md`（首次 preview 输出样例）

---

## 1. 问题与目标

### 现状

Viewer 的 Super Workspace 在创建新 Session（手动 New Session、闪电并行、context recycle）时，用一段固定 5K tokens 的 history bootstrap：

- 从 SQLite 倒序取最多 200 条可见消息（用户 query + 各 Role 最终回复），按粗略 token 数塞到上限；
- 除时间顺序外，没有任何相关性筛选、状态提炼或指代消解；
- 本质上是"最近若干条消息的截断"，不是"这个 Chat 目前在做什么"。

### 目标

新 Session 应继承**整个 Chat 的协作状态**，而不是某个 Role 最近 5K tokens 的聊天片段：

- 跨 Role、跨 Provider、跨 Session：新 Session 要知道整个 Chat 里聊了什么，包括其他 agent、其他 provider 产生的消息和用户输入；
- 信息密度高：同样的 ~5K 预算装更多有效信息；
- 支持更激进的 session reset：有了可靠 bootstrap，可以在 40–60% context 时就轮换 Session，降低单 Session 成本、减少旧上下文噪声。

---

## 2. 现有基础（已验证）

- **Hindsight 0.8.3 已接入**：每个 Chat 一个独立 memory bank，天然覆盖所有 Role 和 Provider。
- **retain 已在做**：每条可见消息（用户输入 + 各 Role 最终回复）异步 retain 到 Chat 级 bank，带 provider、role、时间、message id 元数据。不增加发送延迟。
- **SQLite 是 source of truth**：完整可见时间线、raw provider events（`super_workspace_provider_raw_events`）都已独立归档，memory 只是可重建的派生索引。
- **Hindsight 提供 `recall` 和 `reflect`**：reflect 支持 JSON Schema，可产出结构化 briefing。
- **preview 脚本已存在**：`scripts/preview_chat_memory_bootstrap.py`，可对任意 message 生成 bootstrap 预览（见第 4 节实验结果）。

---

## 3. 总体设计：混合 bootstrap

不在"自己压缩"和"Hindsight memory"之间二选一，而是分层混合：**Hindsight 负责语义检索与组织，SQLite 继续作为事实来源**。

新 Session 注入的上下文由四层组成：

```text
Cached whole-chat briefing   ← 后台预计算（LLM），不在派送关键路径上
+ Query-specific selected facts  ← recall 候选经 briefing 层筛选后的少量事实
+ Recent exact SQLite tail   ← 最近 1–2K tokens 原文，永远兜底
+ Current query
```

### 分层职责

1. **Briefing（重 LLM，后台缓存）**
   固定结构：Goals / Decisions / Current state / Constraints / Relevant earlier facts / Open work。每隔 N 条消息或 Chat 空闲时后台刷新，新 Session 创建时直接读缓存。保存引用的 memory/message id 方便审查。

2. **Recall（候选证据，不直接注入）**
   用当前 query 检索 Chat bank，限制 5–8 条。召回结果**不直接塞进 Agent context**，只作为 briefing 生成/筛选的候选，避免无关 memory 污染 Session。

3. **Retrieval query 构建（默认不用 LLM）**
   启发式拼装：当前 query + 上一条用户消息 + 上一条 Agent 回复开头（+ cached briefing 摘要）。只有召回质量不够时才引入小模型改写（可选项）。

4. **Recent exact tail（SQLite 兜底）**
   最近 1–2K tokens 原文。即使 retain 未完成、recall 失败、memory 未索引，新 Session 仍能准确理解最近在讨论什么。Hindsight 不可用时整体降级到现有的 5K recent-history 逻辑。

### 关键边界

- Hindsight **不是**唯一事实库；SQLite 才是 source of truth。换模型、Hindsight 数据损坏、recall 效果变化都不丢历史。
- 同步 reflect 实测 ~75 秒 / 8.5K tokens，**绝不能放在创建 Session 的关键路径上**，必须后台预计算。
- 普通 Session 延续不做 bootstrap（底层 Session 自己有上下文）；仅 New Session / 闪电 / recycle 触发。

---

## 4. 首次 preview 实验发现（2026-08-06，tmp_out.md）

用真实 Chat 数据跑了一次完整 preview，结果：

- 最终 context 约 3,294 tokens（粗略计），recall 25 条，reflect 消耗 8,554 tokens，总耗时 ~75 秒。
- 结构化 briefing 正确理解了"这个方案"这类指代，提取出了目标、决定、当前状态、未完成事项和时间线 —— **briefing 的质量明显高于 raw recall**。
- 暴露的问题：
  1. **裸 query recall 无效**："按照这个方案"本身没有语义，直接检索找回 14 条无关旧记忆，reflect 只返回 "I don't have information"。必须先附带最近上下文做指代消解再检索。
  2. **Hindsight recall query 有 500 tokens 硬限制**：不能把完整 recent tail 当检索 query，只能取末尾窗口；正式实现需要单独的 retrieval query builder。
  3. **Raw recall 噪声大**：25 条里有不少旧的无关内容，不适合直接注入；必须经过 briefing 层筛选。
  4. **同步 reflect 太慢**：~75 秒，确认必须后台缓存。

---

## 5. LLM 需求结论

无论哪条路，链路上都需要一个 LLM，差别只在**用在哪里、用多重、能不能缓存**：

- **Briefing 生成**：LLM 绕不掉（要从散乱跨 Role 记忆里提炼结构化状态）。用法 = 后台异步预计算 + 缓存。
- **Recall query 生成**：默认启发式拼装，零成本零延迟；小模型改写是可选项。
- Hindsight 自带 LLM 配置（reflect 用的就是它），理论上不需要再单独维护一个模型 —— 除非想用便宜/本地模型压 briefing 成本，这正是第 6 节第一步要验证的。

---

## 6. 分阶段实施计划

### Phase 0：本地模型可行性实验（当前优先）

目标：找一个 OK 的本地模型，验证它能否胜任 briefing/summary 和 Hindsight reflect，降低成本和外部依赖。

1. 选定本地推理方式（本机 GPU/CPU 情况待定；候选 llama.cpp / vLLM / Ollama，OpenAI 兼容接口）。
2. 候选模型：待实验（Qwen 系列中小尺寸是合理起点；以 briefing 质量为准，不以跑分为准）。
3. 评测方法：用 `scripts/preview_chat_memory_bootstrap.py` 对同一条真实 message（如 `940c5409945d4a11a40190e9106043f9`）分别用云端模型和本地模型生成 briefing，人工对比 tmp_out 质量：
   - 指代消解是否正确（"这个方案"是否被正确理解）；
   - Goals/Decisions/Current state/Open work 是否齐全且不编造；
   - 是否过滤掉无关 recall 噪声；
   - 耗时与 token 成本。
4. 同时验证本地模型能否作为 Hindsight 自身的 LLM（reflect/retain 提取质量）。
5. 产出结论：本地模型是否可用、用于哪一层（briefing only / 全部 / 不可用则回退云端）。

### Phase 1：Cached briefing 后台刷新

- Chat 级 briefing 缓存表/字段（内容 + 来源 message/memory id + 生成时间 + 使用的模型）。
- 触发策略：每 N 条可见消息 / Chat 空闲 / New Session 前若缓存过旧则先刷新（或先用旧缓存 + 后台刷新）。
- 降级：Hindsight 或 LLM 不可用时回退现有 5K recent-history。
- 设置项：总预算、recent-tail 预算、刷新间隔、超时、fallback 开关。

### Phase 2：Query-aware recall + briefing 合成

- Retrieval query builder（启发式拼装，500 token 限制内）。
- Recall 候选（5–8 条）+ recent tail + cached briefing → briefing 合成/筛选 → 最终注入格式。
- 在 driver run 里记录本次 bootstrap 来源、耗时、结构化结果，便于观察效果。

### Phase 3：更激进的 session reset

- 有了可靠 bootstrap 后，在 40–60% context 时轮换 Session。
- 观察成本与回答质量变化，调阈值。

---

## 7. 待决策 / 开放问题

- 本地推理运行时选型（llama.cpp vs vLLM vs Ollama）与具体模型尺寸，取决于本机硬件实测。
- Briefing 刷新触发策略的具体参数（N 条消息 / 空闲多久）。
- Recall 是否需要小模型改写 query（先看启发式效果）。
- 是否将 briefing 也用于 Role 常驻 system prompt 之外的其他场景（如 Chat 摘要视图）。
- 闪电 Session 的 bootstrap 是否与 New Session 完全一致（当前假设一致）。

---

## 8. 变更日志

- 2026-08-06：初版。记录混合 bootstrap 设计、首次 preview 实验发现（tmp_out.md）、LLM 需求结论、Phase 0–3 计划。当前优先做 Phase 0 本地模型实验。
- 2026-08-06：Phase 0 第一批实测（`summary_test_inputs/` 14 条跨 Chat 真实 response，`scripts/summarize_test.py`，输出 `sumerytest/<model>/`）：
  - `qwen3-14b`（vLLM AWQ，3090，no-think 模板）：14/14 成功，单条 2.9–7.1s，completion 203–342 tokens，结构完整。当前质量/速度基线。
  - `gemma4:26b`（Ollama Q4_K_M，CPU，brew 版 ollama 0.32.5 无 CUDA 后端）：14/14 成功但单条 115–676s（比 GPU qwen 慢约 90 倍）；thinking 模型，completion 1199–2048 tokens 大部分是不可见 reasoning。
  - 坑 1：thinking 模型必须 `max_tokens ≥ 2048`，否则 reasoning 吃光额度、content 为空（1024 时 14 条全空）。
  - 坑 2：Ollama 默认 ctx 4096，需 `OLLAMA_CONTEXT_LENGTH` 调大；brew 版 ollama 只带 CPU ggml 后端，要用 GPU 必须装官方 ollama 包（cuda_v12/v13 runner）。
  - 坑 3：`CUDA_VISIBLE_DEVICES=1` 数字索引在 ollama 0.32.6 上映射错了 GPU（把模型放到了 2080Ti 并触发 CPU 混合 offload，0.39 tok/s），必须用 GPU UUID 形式。
  - 坑 4：Ollama OpenAI 兼容端点不支持关 thinking（`chat_template_kwargs`/`think` 均被忽略，0.32.6）；必须用原生 `/api/chat` + `think:false`（脚本已加 `--ollama-native`）。
  - 坑 5：提取实验输入时注意 schema 陷阱——`message:query` 行的用户文本在 `query` 列，`text` 列为空；读 `text` 会得到"无关联 query 记录"。提取器：`scripts/extract_summary_test_inputs.py`。
  - 输入修正（补上 query）后 gemma 已重跑（14/14，5.0–10.5s）：任务归因明显改善（如"比赛列表页面""提升并发量和解码速度"均来自 query 原文）；抽检验证 gemma 的细节依然忠实（text_009 行排列顺序正确而 qwen 顺序有误、text_014 保留关键脚本名 `print_structure.py` 而 qwen 丢失）。修正版 qwen 对比未跑（vLLM 已停，与 Ollama 无法同卡共存）。
  - 待用户人工对比两模型摘要质量（`sumerytest/qwen3-14b/` vs `sumerytest/gemma4:26b/`）。
  - 模型对比评估（14 条样本）：**忠实度两者均无数字幻觉**（摘要中出现的数字 100% 能在原文找到，qwen 0 / gemma 0 个无源数字）；数字召回率 qwen 0.44 vs gemma 0.47；速度 qwen 平均 ~4.8s vs gemma ~6.8s；gemma 输出约长 17%，更倾向引用具体数字/阈值（如 text_012 全部 benchmark 数字精确引用，qwen 只写"明显快很多"）；qwen 在 text_005 出现一次无依据的约束推断（"避免提及基金等敏感词"，原文无此要求）——此类"发明用户约束"对 briefing 场景毒性最大。结论：**gemma4:26b（think:false）质量略优且更忠实，qwen3-14b 更快更省显存（AWQ ~9GB vs Q4_K_M ~18GB）**；briefing 是后台任务、速度不敏感，推荐 gemma4:26b，若显存紧张则 qwen3-14b 也可用。
  - `gemma4:26b`（官方 ollama 0.32.6，3090 全量 offload，32K ctx，**think:false**）：14/14 完整，单条 3.5–8.6s，completion 240–380 tokens——与 qwen3-14b（2.9–7.1s）基本同速；开 thinking 时单条 10.8–37.1s、completion 1048–1934（约 3/4 是不可见 reasoning），对摘要质量无明显提升，结论：摘要任务关 thinking。
- 2026-08-06：**LLM provider 链已落地**（`backend/app/llm_client.py`）。原 dispatcher 的单一 `active_dispatch_profile_id` 改为有序 provider 链：按顺序调用，出错冻结 1 小时（`llm_provider_freeze_seconds`，状态持久化在 `~/.view/llm-provider-health.json`），自动落到下一个，语义与 agent target 的 health/cooldown 一致。Settings → Super Workspace 里可排序/启停/增删，预设含 Ollama Gemma（默认链首位，127.0.0.1:11434 `gemma4:26b`）、Local vLLM、DeepSeek；freeze 状态在 UI 可见可手动清除（`GET/POST /api/config/llm-provider-states[/clear]`）。dispatcher 已切到 `chat_completion()`；**后续 briefing/summary 直接复用 `llm_client.chat_completion()` 即可**，不需要再单独维护模型配置。已实测：真实调用命中 ollama-gemma（2.5s 返回合法 JSON）；人为冻结 ollama 后自动跳过（提示 frozen + 剩余秒数）→ local-vllm 连接拒绝被真实冻结 → 落到 deepseek 成功返回；清除 freeze 生效。
- 2026-08-07：**vLLM 已从本机部署与 provider 链中移除**（用户决定不再使用）。systemd 用户服务 `vllm-qwen3.service` 已 disable 并删除，`/mnt/oldroot/home/d/vllm/`（含 19G AWQ 模型，共 35G）已删除；链默认与 live config 只剩 `ollama-gemma` → `deepseek`；代码默认、Settings 预设、`VIEWER_VLLM_API_KEY`、`scripts/summarize_test.py` 默认 base-url 同步清理。Ollama 改由 systemd 用户服务 `ollama.service`（官方 /usr/local/bin/ollama 0.32.6，linger 开机自启，3090 UUID 锁定，ctx 32K）托管。本文前面 Phase 0 的 qwen3-14b/vLLM 记录保留为历史实验档案。
- 2026-08-07：**Hindsight 服务端 LLM 切换到本地 gemma4:26b**。此前配置指向已删除的 vLLM（`127.0.0.1:8010/v1` qwen3-14b），retain 抽取实际上一直处于失败状态。`~/Sync/hindsight/hindsight.env`（systemd `hindsight-embed.service` 的 EnvironmentFile）与 `~/.hindsight/profiles/sync-memory.env` 均改为 `openai` provider + `http://127.0.0.1:11434/v1` + `gemma4:26b`，并设 `HINDSIGHT_API_LLM_REASONING_EFFORT=low`（不支持则被忽略）。

## Phase 1 实现记录（2026-08-07）

**已落地**：turn 级摘要 + 上下文桥接。

- **新表 `super_workspace_turn_summaries`**（`agent_history.py`）：turn_id 主键，存摘要文本、role/provider/model 溯源、源消息数/字符数、延迟、状态。
- **新模块 `backend/app/turn_summary.py`**：
  - `build_turn_transcript()`：取该 turn 的 query + assistant 消息（全文）+ tool 事件（`function_call`/`tool_call`/`custom_tool_call`/`tool_result`/`patch_apply_end`，**每条截断到 `turn_summary_tool_char_budget`（默认 1500 字符）**）+ 关联 file changes（diff 同样截断）；reasoning/plan/session_update 跳过。
  - `generate_turn_summary()`：turn 完成时后台触发（`_dispatch_task` → `_schedule_turn_summary`，`asyncio.to_thread` 不阻塞事件循环），走 LLM provider 链生成四节中文摘要（任务/关键动作与改动/结果/未决事项），无 assistant 内容的 turn 跳过。
  - `build_new_session_context()`：新 session 的 bootstrap = **turn 摘要区 + Hindsight recall 区** + 原有可见历史尾部（`chat_history_prompt` 组合）。
  - `build_role_switch_bridge()`：**复用 session 且角色切换**时（chat 在该 role 上次活动后有其他 role 的可见活动），在 routed prompt 前塞 "While you were away" 桥接段：未见 turn 的摘要（排除自己产生的 turn——复用 session 本来就知道）+ recall 区。`_dispatch_candidate` 仅在 session 确定复用时调用。
- **Hindsight recall 接入**（`super_workspace_memory.recall_chat_memories()`）：budget=mid、超时 10s、失败返回空不阻塞 dispatch；recall query 用当前 query + 最近尾部拼接（预览脚本模式）。
- **配置**（`SuperWorkspaceConfig`）：`turn_summary_enabled`、`turn_summary_tool_char_budget`(1500)、`turn_summary_timeout_seconds`(180)、`context_bridge_enabled`、`context_bridge_summary_char_budget`(8000，2026-08-08 由 `context_bridge_max_summaries`(6) 条数制改为字符预算制)、`context_bridge_hindsight_enabled`、`context_bridge_hindsight_max_tokens`(800)。
- **验证**：临时脚本 ad-hoc 19/19（真实 gemma 生成 2 条摘要、截断/时序/桥接过滤/空 turn 跳过/新 session 组合、对真实 chat bank recall 返回 9 条）+ pytest 89 passed + compileall OK。生效需重启 Viewer 后端。
- **2026-08-07 上线 + prompt 日志**：已 `POST /api/admin/restart` 生效；`generate_turn_summary()` 现在把完整 prompt（`[system]`/`[user]` 含整段 transcript）和生成结果（`[summary]`）写入 worker 日志（`~/.view/logs/super-workspace-worker.log`，grep `Turn summary prompt` / `Turn summary stored`）。真实 turn 实测（vLLM 移除 turn，28 块/10193 字符）：gemma4:26b 19.3s 返回四节摘要并成功入库。
- **2026-08-08 两段合并为摘要层（预算制）**：原"摘要区 + 可见消息原文大尾巴"重复覆盖同一段近期历史，合并为三层结构——① turn 摘要区（`build_turn_summaries_section` 改为字符预算制，`context_bridge_summary_char_budget` 默认 8000，新→旧贪心装满，最新一条永远保留、超预算则截断；实测摘要均值 ~820 字符/轮，8000 预算 ≈ 10 轮）；② 未摘要缺口区（新增 `build_unsummarized_tail_section` + store `latest_turn_summary_time`/`visible_chat_history_context(after_time)`：摘要异步生成有 ~19s 滞后且可能失败，缺口窗口内用原文兜底，预算 `chat_history_bootstrap_tokens` 默认降至 1500；chat 无任何摘要时退化为原来的纯原文尾巴）；③ Hindsight recall 区（不动）。摘要生成 prompt 加软上限"≤800 字符/条"保证预算利用率。`build_role_switch_bridge` 同样获得缺口兜底。验证：ad-hoc 19/19（临时 sqlite 走真实代码路径，注意 store 查询 user_id 被 `normalize_user_id` 固定为 "dailing"）+ pytest 89 passed + npm build + compileall OK。生效需重启后端。
- **后续观察点**：摘要质量（gemma 幻觉底线）；recall 注入噪声（skill 警告 raw recall 可能带噪声，必要时降级为关闭 recall 只留摘要）；Hindsight 切 gemma 后 retain 抽取质量需抽查。
