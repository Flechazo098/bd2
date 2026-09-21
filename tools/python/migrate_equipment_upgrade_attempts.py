#!/usr/bin/env python3
"""Add the final per-instance equipment upgrade-attempt counter.

Dry-run by default. Applying requires stopped client/server, creates a verified
nine-file checkpoint, and atomically changes only equipment.json. Existing
equipment has never been successfully upgraded, so every instance starts at 0.
"""

from __future__ import annotations

import argparse
from datetime import datetime
import json
from pathlib import Path

import save_checkpoint
from set_first_gacha_state import atomic_write_json

DEFAULT_STATE = Path(__file__).resolve().parents[2] / "data" / "state"

def migrate(value: dict) -> tuple[dict, list[int]]:
    if value.get("version") != "2.34.13" or not isinstance(value.get("equipment"), list):
        raise ValueError("equipment.json is not the expected 2.34.13 save")
    result = dict(value)
    result["equipment"] = []
    changed = []
    for original in value["equipment"]:
        if not isinstance(original, dict) or not isinstance(original.get("inven_index"), int):
            raise ValueError("invalid equipment instance")
        item = dict(original)
        if "upgrade_attempts" not in item:
            item["upgrade_attempts"] = 0
            changed.append(item["inven_index"])
        elif not isinstance(item["upgrade_attempts"], int) or isinstance(item["upgrade_attempts"], bool) or item["upgrade_attempts"] < 0:
            raise ValueError(f"equipment {item['inven_index']} has invalid upgrade_attempts")
        result["equipment"].append(item)
    return result, changed

def run(state: Path, apply: bool) -> Path | None:
    target = state / "equipment.json"
    with target.open(encoding="utf-8") as stream:
        original = json.load(stream)
    migrated, changed = migrate(original)
    print(json.dumps({"equipment_count": len(migrated["equipment"]), "changed_count": len(changed), "changed_indices": changed}, indent=2))
    if not changed or not apply:
        return None
    running = save_checkpoint.running_processes()
    if running:
        raise RuntimeError("client/server is still running:\n" + "\n".join(running))
    before = save_checkpoint.inspect_files(state)
    backup = state / "checkpoints" / f"{datetime.now():%Y%m%d-%H%M%S}-before-equipment-upgrade-attempts"
    save_checkpoint.create(state, backup, "before adding equipment upgrade attempt counters")
    atomic_write_json(target, migrated)
    with target.open(encoding="utf-8") as stream:
        written = json.load(stream)
    repeated, remaining = migrate(written)
    after = save_checkpoint.inspect_files(state)
    if remaining or repeated != migrated or any(before[name] != after[name] for name in save_checkpoint.STATE_FILES if name != "equipment.json"):
        raise OSError(f"equipment upgrade migration failed verification; backup: {backup}")
    save_checkpoint.verify(backup)
    return backup

def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        backup = run(args.state.resolve(), args.apply)
        print(f"Migrated with complete verified backup: {backup}" if backup else "Dry-run/no change; use --apply with stopped processes when changed_count>0")
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        parser.exit(1, f"migrate_equipment_upgrade_attempts: {exc}\n")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
