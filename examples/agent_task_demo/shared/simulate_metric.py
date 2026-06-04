#!/usr/bin/env python3
"""Tiny deterministic metric simulator for Agent Task DAG testing."""

from __future__ import annotations

import argparse
import json
import math
import time
from pathlib import Path


def metric_for(mode: str, seed: int) -> dict:
    if mode == "baseline":
        score = 0.70 + 0.03 * math.sin(seed)
        return {"mode": mode, "seed": seed, "score": round(score, 4), "status": "ok"}
    if mode == "variant":
        raise RuntimeError(
            "variant mode is intentionally unsupported in shared code. "
            "Executor should request manager review instead of editing shared code."
        )
    raise ValueError(f"Unknown mode: {mode}")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", default="baseline")
    parser.add_argument("--seed", type=int, default=1)
    parser.add_argument("--out", required=True)
    parser.add_argument("--sleep", type=float, default=1.0)
    args = parser.parse_args()

    time.sleep(max(0.0, args.sleep))
    payload = metric_for(args.mode, args.seed)
    output = Path(args.out)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(payload, sort_keys=True))


if __name__ == "__main__":
    main()
