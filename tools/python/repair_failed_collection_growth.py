#!/usr/bin/env python3
"""Undo the six rejected 2026-09-21 20:19–20:20 collection promotions.

The six /CharGrowth requests for collection character 920000054 (6510/1)
charged inventory and one gold payment before failing to persist a changed
character ID. This one-time repair only accepts the exact observed save and
six matching server errors, and removes the refund items those failed requests
created. Dry-run by default; applying requires stopped client/server, backs up
and verifies all nine account files, then writes only items.json/wallet.json.
"""

from __future__ import annotations

import argparse
from datetime import datetime
import json
from pathlib import Path

import save_checkpoint
from set_first_gacha_state import atomic_write_json


DEFAULT_STATE = Path(__file__).resolve().parents[2] / "data" / "state"
DEFAULT_LOG = Path(__file__).resolve().parents[2] / "logs" / "server-20260921-201837.err.log"
ERROR = "persist promoted collection character: player: collection character 920000054 not found"
IDENTITY = "char-promote:920000054:6510"
CONSUMED = {
    900000038: (11, 93, 1),
    900000041: (12, 86, 2),
    900000042: (9, 94738, 753),
    900000045: (14, 99973, 4),
    900000046: (13, 99980, 3),
}
REFUNDS = {index: (8 if index % 2 else 7, 1 if index % 2 else 3) for index in range(900000049, 900000061)}


def repair(items: dict, wallet: dict, collection: dict, log: str) -> tuple[dict, dict]:
    errors = [line for line in log.splitlines() if ERROR in line]
    if len(errors) != 6 or any("path=/CharGrowth" not in line for line in errors):
        raise ValueError(f"expected exactly six rejected promotion requests, got {len(errors)}")
    if any(value.get("version") != "2.34.13" for value in (items, wallet, collection)):
        raise ValueError("unexpected save version")
    owner = [char for char in collection.get("characters", []) if char.get("inven_index") == 920000054]
    if len(owner) != 1 or owner[0].get("id") != 6510 or owner[0].get("level") != 1:
        raise ValueError("collection character no longer matches the rejected request")
    if wallet.get("gold") != 56050 or wallet.get("spent", {}).get(IDENTITY) is not True:
        raise ValueError("wallet differs from the observed failed payment")
    inventory = {item.get("inven_index"): item for item in items.get("items", [])}
    for index, (item_id, remaining, _) in CONSUMED.items():
        item = inventory.get(index)
        if not item or item.get("id") != item_id or item.get("type") != 8 or item.get("count") != remaining:
            raise ValueError(f"item stack {index} differs from the observed failed requests")
    for index, (item_id, count) in REFUNDS.items():
        item = inventory.get(index)
        if not item or item.get("id") != item_id or item.get("type") != 8 or item.get("count") != count:
            raise ValueError(f"failed-request refund {index} was changed or used")
    for indices in items.get("grant_items", {}).values():
        if any(index in REFUNDS for index in indices):
            raise ValueError("a failed-request refund has a separate grant reference")

    corrected_items = dict(items)
    corrected_items["items"] = []
    for original in items["items"]:
        index = original["inven_index"]
        if index in REFUNDS:
            continue
        item = dict(original)
        if index in CONSUMED:
            item["count"] += 6 * CONSUMED[index][2]
        corrected_items["items"].append(item)
    corrected_wallet = dict(wallet)
    corrected_wallet["gold"] = wallet["gold"] + 10000
    corrected_wallet["spent"] = dict(wallet["spent"])
    del corrected_wallet["spent"][IDENTITY]
    return corrected_items, corrected_wallet


def run(state: Path, log_path: Path, apply: bool) -> Path | None:
    before = save_checkpoint.inspect_files(state)
    values = {}
    for name in ("items", "wallet", "collection"):
        with (state / f"{name}.json").open(encoding="utf-8") as stream:
            values[name] = json.load(stream)
    repaired_items, repaired_wallet = repair(values["items"], values["wallet"], values["collection"], log_path.read_text(encoding="utf-8"))
    print(json.dumps({
        "failed_requests": 6,
        "character": "920000054 / 6510 / level 1 (unchanged)",
        "restored_gold": 10000,
        "restored_item_counts": {str(index): 6 * value[2] for index, value in CONSUMED.items()},
        "removed_unearned_refund_indices": sorted(REFUNDS),
    }, indent=2))
    if not apply:
        return None
    running = save_checkpoint.running_processes()
    if running:
        raise RuntimeError("client/server is still running:\n" + "\n".join(running))
    backup = state / "checkpoints" / f"{datetime.now():%Y%m%d-%H%M%S}-before-failed-collection-growth-repair"
    save_checkpoint.create(state, backup, "before undoing six rejected collection CharGrowth requests")
    atomic_write_json(state / "items.json", repaired_items)
    atomic_write_json(state / "wallet.json", repaired_wallet)
    after = save_checkpoint.inspect_files(state)
    if any(before[name] != after[name] for name in save_checkpoint.STATE_FILES if name not in {"items.json", "wallet.json"}):
        raise OSError(f"unrelated account state changed; backup: {backup}")
    for name, expected in (("items.json", repaired_items), ("wallet.json", repaired_wallet)):
        with (state / name).open(encoding="utf-8") as stream:
            if json.load(stream) != expected:
                raise OSError(f"failed verification of {name}; backup: {backup}")
    save_checkpoint.verify(backup)
    return backup


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE)
    parser.add_argument("--log", type=Path, default=DEFAULT_LOG)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        backup = run(args.state.resolve(), args.log.resolve(), args.apply)
        print(f"Repaired with complete verified backup: {backup}" if backup else "Dry-run only; pass --apply after stopping client/server")
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        parser.exit(1, f"repair_failed_collection_growth: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
