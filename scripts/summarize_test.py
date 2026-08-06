#!/usr/bin/env python3
"""Summarize viewer test responses with an OpenAI-compatible (vLLM) endpoint.

Reads input mds from an input dir (default: summary_test_inputs/),
sends each to the model with a structured-summary prompt, and writes
results to <output_root>/<model_name>/text_xxx.md for side-by-side
model comparison.

Usage:
    python scripts/summarize_test.py --model qwen3-14b
    python scripts/summarize_test.py --model qwen3-14b --base-url http://127.0.0.1:8010/v1
    python scripts/summarize_test.py --model some-other-model --concurrency 4
"""

from __future__ import annotations

import argparse
import datetime
import json
import sys
import time
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent

SYSTEM_PROMPT = """你是一个精确的对话摘要器。给你一段「用户请求 + Agent 回复」，你要产出一份给后续新会话使用的结构化中文摘要。

要求：
- 严格忠实于原文，不得编造原文没有的信息；原文没说的就写"未提及"。
- 保留关键的具体事实：文件路径、命令、数字、状态、决定、未解决的问题。
- 删除客套话、重复解释和过程性描述。
- 总长度控制在 200-400 字。

输出固定结构（不要额外小节）：

**任务**: 用户这次要求做什么（一句话）
**结果**: Agent 实际完成了什么 / 给出什么结论（1-3 句）
**关键细节**: 路径、命令、数字、决定等要点列表（3-6 条，每条一行，"- "开头）
**状态**: 已完成 / 进行中 / 受阻，以及任何未解决或待用户确认的事项（一句话）"""


def call_model(base_url: str, model: str, content: str, timeout: float) -> dict:
    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": SYSTEM_PROMPT},
            {"role": "user", "content": content},
        ],
        "temperature": 0.2,
        "max_tokens": 1024,
    }
    req = urllib.request.Request(
        f"{base_url.rstrip('/')}/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    started = time.monotonic()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        body = json.loads(resp.read().decode("utf-8"))
    return {"body": body, "latency_s": round(time.monotonic() - started, 2)}


def summarize_one(path: Path, base_url: str, model: str, timeout: float) -> dict:
    result = call_model(base_url, model, path.read_text(encoding="utf-8"), timeout)
    body = result["body"]
    choice = body["choices"][0]
    usage = body.get("usage") or {}
    return {
        "input": path.name,
        "summary": choice["message"]["content"].strip(),
        "latency_s": result["latency_s"],
        "prompt_tokens": usage.get("prompt_tokens"),
        "completion_tokens": usage.get("completion_tokens"),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", required=True, help="served model name")
    parser.add_argument("--base-url", default="http://127.0.0.1:8010/v1")
    parser.add_argument("--input-dir", default=str(REPO_ROOT / "summary_test_inputs"))
    parser.add_argument("--output-root", default=str(REPO_ROOT / "sumerytest"))
    parser.add_argument("--concurrency", type=int, default=4)
    parser.add_argument("--timeout", type=float, default=300.0)
    args = parser.parse_args()

    input_dir = Path(args.input_dir)
    inputs = sorted(input_dir.glob("text_*.md"))
    if not inputs:
        print(f"no text_*.md found in {input_dir}", file=sys.stderr)
        return 1

    out_dir = Path(args.output_root) / args.model
    out_dir.mkdir(parents=True, exist_ok=True)

    print(f"model={args.model} base_url={args.base_url} inputs={len(inputs)} -> {out_dir}")
    results: list[dict] = []
    errors: list[str] = []
    with ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        futures = {
            pool.submit(summarize_one, p, args.base_url, args.model, args.timeout): p
            for p in inputs
        }
        for fut in as_completed(futures):
            p = futures[fut]
            try:
                r = fut.result()
            except Exception as exc:  # noqa: BLE001 - report and continue
                errors.append(f"{p.name}: {exc}")
                print(f"FAIL {p.name}: {exc}", file=sys.stderr)
                continue
            results.append(r)
            out = (
                f"---\nmodel: {args.model}\ninput: {r['input']}\n"
                f"generated_at: {datetime.datetime.now().isoformat(timespec='seconds')}\n"
                f"latency_s: {r['latency_s']}\n"
                f"prompt_tokens: {r['prompt_tokens']}\n"
                f"completion_tokens: {r['completion_tokens']}\n"
                f"---\n\n{r['summary']}\n"
            )
            (out_dir / p.name).write_text(out, encoding="utf-8")
            print(f"OK   {r['input']} latency={r['latency_s']}s "
                  f"completion_tokens={r['completion_tokens']}")

    total = sum(r["latency_s"] for r in results)
    print(f"\ndone: {len(results)} ok, {len(errors)} failed, "
          f"sum_latency={total:.1f}s -> {out_dir}")
    return 0 if not errors else 2


if __name__ == "__main__":
    raise SystemExit(main())
