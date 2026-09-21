#!/usr/bin/env python3
"""Repair pre-release equipment saves with missing three-slot rank arrays.

The 2.34.13 client indexes EquipBaseInfo.Rank[0..2] even before enhancement.
Dry-run by default. Applying requires stopped client/server processes, verifies
and backs up all nine state files, and atomically updates equipment.json only.
"""

from __future__ import annotations

import argparse
from datetime import datetime
import json
from pathlib import Path

import save_checkpoint
from set_first_gacha_state import atomic_write_json


DEFAULT_STATE = Path(__file__).resolve().parents[2] / "data" / "state"


def repair(value: dict) -> tuple[dict, list[int]]:
    if value.get("version") != "2.34.13":
        raise ValueError("equipment.json must be a 2.34.13 save")
    entries = value.get("equipment")
    if not isinstance(entries, list):
        raise ValueError("equipment must be an array")
    result = dict(value)
    result["equipment"] = []
    updated = []
    for item in entries:
        if not isinstance(item, dict) or not isinstance(item.get("inven_index"), int):
            raise ValueError("invalid equipment instance")
        copy = dict(item)
        if "rank" not in copy:
            copy["rank"] = [0, 0, 0]
            updated.append(copy["inven_index"])
        elif not isinstance(copy["rank"], list) or len(copy["rank"]) != 3 or any(
            not isinstance(rank, int) or isinstance(rank, bool) or rank < 0 or rank > 4
            for rank in copy["rank"]
        ):
            raise ValueError(f"equipment {copy['inven_index']} has an invalid rank array")
        result["equipment"].append(copy)
    return result, updated


def apply_state(state: Path, apply: bool) -> Path | None:
    target = state / "equipment.json"
    with target.open("r", encoding="utf-8") as stream:
        original = json.load(stream)
    repaired, updated = repair(original)
    print(json.dumps({"equipment_count": len(repaired["equipment"]), "missing_rank_count": len(updated), "updated_indices": updated}, indent=2))
    if not updated or not apply:
        return None
    running = save_checkpoint.running_processes()
    if running:
        raise RuntimeError("client/server is still running:\n" + "\n".join(running))
    before = save_checkpoint.inspect_files(state)
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    backup = state / "checkpoints" / f"{stamp}-before-equipment-ranks"
    save_checkpoint.create(state, backup, "before three-slot equipment rank repair")
    atomic_write_json(target, repaired)
    with target.open("r", encoding="utf-8") as stream:
        written = json.load(stream)
    _, remaining = repair(written)
    after = save_checkpoint.inspect_files(state)
    if remaining or any(before[name] != after[name] for name in save_checkpoint.STATE_FILES if name != "equipment.json"):
        raise OSError(f"equipment repair failed verification; backup: {backup}")
    return backup


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        backup = apply_state(args.state.resolve(), args.apply)
        if backup:
            print(f"updated equipment.json; verified nine-file backup: {backup}")
        elif not args.apply:
            print("dry run only; pass --apply after stopping game and server")
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        parser.exit(1, f"repair_equipment_ranks: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
