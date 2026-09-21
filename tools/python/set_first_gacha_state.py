#!/usr/bin/env python3
"""Set the explicit first-gacha completion marker in a development save.

This is a pre-release schema edit, not a compatibility migration. By default
it prints the proposed collection.json change. Applying requires stopped game
and server processes, creates a verified nine-file checkpoint, and atomically
replaces only collection.json.
"""

from __future__ import annotations

import argparse
from datetime import datetime
import json
import os
from pathlib import Path
import sys

import save_checkpoint


IDENTITY = "account:first-gacha-completed"
WORKSPACE_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_STATE = WORKSPACE_ROOT / "data" / "state"


def updated_collection(value: dict, completed: bool) -> tuple[dict, bool]:
    if value.get("version") != "2.34.13":
        raise ValueError("collection.json is not a 2.34.13 save")
    grants = value.get("grants")
    if not isinstance(grants, dict):
        raise ValueError("collection.json grants must be an object")
    result = dict(value)
    result["grants"] = dict(grants)
    before = IDENTITY in grants
    if completed:
        existing = grants.get(IDENTITY)
        if before and existing != {}:
            raise ValueError("first-gacha marker exists with unexpected payload")
        result["grants"][IDENTITY] = {}
    else:
        result["grants"].pop(IDENTITY, None)
    return result, before != completed


def atomic_write_json(path: Path, value: dict) -> None:
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("w", encoding="utf-8", newline="\n") as stream:
            json.dump(value, stream, ensure_ascii=False, separators=(",", ":"))
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def apply_state(state: Path, completed: bool, apply: bool) -> Path | None:
    collection_path = state / "collection.json"
    with collection_path.open("r", encoding="utf-8") as stream:
        current = json.load(stream)
    updated, changed = updated_collection(current, completed)
    print(json.dumps({
        "identity": IDENTITY,
        "before": IDENTITY in current["grants"],
        "after": completed,
        "changed": changed,
    }, ensure_ascii=False, indent=2))
    if not changed:
        return None
    if not apply:
        print("dry run only; stop client/server and pass --apply", file=sys.stderr)
        return None
    running = save_checkpoint.running_processes()
    if running:
        raise RuntimeError("client/server is still running:\n" + "\n".join(running))
    save_checkpoint.inspect_files(state)
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    backup = state / "checkpoints" / f"{stamp}-before-first-gacha-state"
    save_checkpoint.create(state, backup, "before explicit first-gacha state edit")
    atomic_write_json(collection_path, updated)
    with collection_path.open("r", encoding="utf-8") as stream:
        written = json.load(stream)
    if (IDENTITY in written.get("grants", {})) != completed:
        raise OSError(f"written first-gacha state failed verification; backup: {backup}")
    save_checkpoint.inspect_files(state)
    return backup


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE)
    selection = parser.add_mutually_exclusive_group(required=True)
    selection.add_argument("--completed", action="store_true")
    selection.add_argument("--not-completed", action="store_true")
    parser.add_argument("--apply", action="store_true")
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        backup = apply_state(args.state.resolve(), args.completed, args.apply)
        if backup is not None:
            print(f"updated {args.state.resolve() / 'collection.json'}; backup: {backup}")
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        parser.exit(1, f"set_first_gacha_state: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
