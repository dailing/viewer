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
