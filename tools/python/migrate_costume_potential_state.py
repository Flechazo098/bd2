#!/usr/bin/env python3
"""Add the final costume-potential ledger to the pre-release collection save.

Dry-run by default. Applying requires stopped client/server, creates and
verifies a complete nine-file checkpoint, and atomically changes only
collection.json. No runtime compatibility for the unpublished old format is
kept in the Go server.
"""

from __future__ import annotations

import argparse
from datetime import datetime
import json
from pathlib import Path

import save_checkpoint
from set_first_gacha_state import atomic_write_json


DEFAULT_STATE = Path(__file__).resolve().parents[2] / "data" / "state"


def migrate(value: dict) -> tuple[dict, bool]:
    if value.get("version") != "2.34.13" or not isinstance(value.get("costumes", []), list):
        raise ValueError("collection.json is not the expected 2.34.13 save")
    if "costume_potential" in value:
        ledger = value["costume_potential"]
        if not isinstance(ledger, dict) or any(
            not isinstance(key, str) or not isinstance(nodes, list) or
            any(not isinstance(node, int) or isinstance(node, bool) or node <= 0 for node in nodes)
            for key, nodes in ledger.items()
        ):
            raise ValueError("existing costume_potential ledger is malformed")
        return value, False
    for costume in value.get("costumes", []):
        if "potential_id" in costume or "potential_ids" in costume:
            raise ValueError("unexpected legacy potential data requires explicit review")
    result = dict(value)
    result["costume_potential"] = {}
    return result, True


def run(state: Path, apply: bool) -> Path | None:
    target = state / "collection.json"
    with target.open(encoding="utf-8") as stream:
        original = json.load(stream)
    migrated, changed = migrate(original)
    print(json.dumps({"changed": changed, "costume_count": len(original.get("costumes", [])), "active_costume_count": len(migrated.get("costume_potential", {}))}, indent=2))
    if not changed or not apply:
        return None
    running = save_checkpoint.running_processes()
    if running:
        raise RuntimeError("client/server is still running:\n" + "\n".join(running))
    before = save_checkpoint.inspect_files(state)
    backup = state / "checkpoints" / f"{datetime.now():%Y%m%d-%H%M%S}-before-costume-potential-state"
    save_checkpoint.create(state, backup, "before adding final costume potential ledger")
    atomic_write_json(target, migrated)
    with target.open(encoding="utf-8") as stream:
        written = json.load(stream)
    repeated, changed_again = migrate(written)
    after = save_checkpoint.inspect_files(state)
    if changed_again or repeated != migrated or any(before[name] != after[name] for name in save_checkpoint.STATE_FILES if name != "collection.json"):
        raise OSError(f"costume potential migration failed verification; backup: {backup}")
    save_checkpoint.verify(backup)
    return backup


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        backup = run(args.state.resolve(), args.apply)
        print(f"Migrated with complete verified backup: {backup}" if backup else "Dry-run/no change; use --apply with stopped processes when changed=true")
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        parser.exit(1, f"migrate_costume_potential_state: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
