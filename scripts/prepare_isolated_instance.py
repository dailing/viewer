#!/usr/bin/env python3
"""Clone live Viewer configuration/data into an isolated test instance."""

from __future__ import annotations

import argparse
import shutil
import sqlite3
from pathlib import Path


PROJECT_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_SOURCE = Path.home() / ".view"
DEFAULT_TARGET = PROJECT_ROOT / ".test-instance"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-config-dir", type=Path, default=DEFAULT_SOURCE)
    parser.add_argument("--source-data-dir", type=Path, default=DEFAULT_SOURCE)
    parser.add_argument("--target-config-dir", type=Path, default=DEFAULT_TARGET / "config")
    parser.add_argument("--target-data-dir", type=Path, default=DEFAULT_TARGET / "data")
    parser.add_argument(
        "--replace",
        action="store_true",
        help="Replace an existing isolated config/database backup.",
    )
    return parser.parse_args()


def copy_config(source_dir: Path, target_dir: Path, replace: bool) -> None:
    source = source_dir.expanduser().resolve() / "config.json"
    target = target_dir.expanduser().resolve() / "config.json"
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists() and not replace:
        print(f"Keeping existing isolated config: {target}")
        return
    if source.exists():
        shutil.copy2(source, target)
        print(f"Copied config: {source} -> {target}")
    else:
        print(f"Source config does not exist; isolated instance will use defaults: {source}")


def copy_database(source_dir: Path, target_dir: Path, replace: bool) -> None:
    source = source_dir.expanduser().resolve() / "agent-history.sqlite3"
    target = target_dir.expanduser().resolve() / "agent-history.sqlite3"
    target.parent.mkdir(parents=True, exist_ok=True)
    if target.exists() and not replace:
        print(f"Keeping existing isolated database: {target}")
        return
    if not source.exists():
        print(f"Source database does not exist; isolated instance will create one: {source}")
        return
    with sqlite3.connect(source) as source_db, sqlite3.connect(target) as target_db:
        source_db.backup(target_db)
    print(f"Backed up database: {source} -> {target}")


def main() -> None:
    args = parse_args()
    copy_config(args.source_config_dir, args.target_config_dir, args.replace)
    copy_database(args.source_data_dir, args.target_data_dir, args.replace)


if __name__ == "__main__":
    main()
