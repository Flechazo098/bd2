#!/usr/bin/env python3
"""Remove internal ResourceTable sentinels accidentally granted by dev mail.

Dry-run by default. Applying requires stopped client/server, creates a verified
nine-file checkpoint, and atomically changes only items.json. Historical mail
grant ledgers remain so the invalid attachment cannot be replayed.
"""

from __future__ import annotations

import argparse
from datetime import datetime
import json
from pathlib import Path

import save_checkpoint
from set_first_gacha_state import atomic_write_json


DEFAULT_STATE = Path(__file__).resolve().parents[2] / "data" / "state"
INVALID_RESOURCE_IDS = {90045, 90046}


def repair(value: dict) -> tuple[dict, list[int]]:
    if value.get("version") != "2.34.13" or not isinstance(value.get("items"), list):
        raise ValueError("items.json is not the expected 2.34.13 save")
    grant_items = value.get("grant_items")
    granted = value.get("granted")
    if not isinstance(grant_items, dict) or not isinstance(granted, dict):
        raise ValueError("items.json grant ledgers are missing")
    bad_indices: list[int] = []
    for item in value["items"]:
        if not isinstance(item, dict):
            raise ValueError("invalid item instance")
        if item.get("type") == 8 and item.get("id") in INVALID_RESOURCE_IDS:
            index = item.get("inven_index")
            if not isinstance(index, int) or index <= 0:
                raise ValueError("invalid internal-resource inventory index")
            owners = [
                identity for identity, indices in grant_items.items()
                if isinstance(indices, list) and index in indices
            ]
            if len(owners) != 1 or not owners[0].startswith("mail:") or not granted.get(owners[0]):
                raise ValueError(f"internal resource {index} is not an acknowledged mail grant")
            bad_indices.append(index)
    result = dict(value)
    result["items"] = [item for item in value["items"] if item.get("inven_index") not in set(bad_indices)]
    return result, bad_indices


def run(state: Path, apply: bool) -> Path | None:
    target = state / "items.json"
    with target.open(encoding="utf-8") as stream:
        original = json.load(stream)
    repaired, removed = repair(original)
    print(json.dumps({"removed_count": len(removed), "removed_indices": removed}, indent=2))
    if not removed or not apply:
        return None
    running = save_checkpoint.running_processes()
    if running:
        raise RuntimeError("client/server is still running:\n" + "\n".join(running))
    before = save_checkpoint.inspect_files(state)
    backup = state / "checkpoints" / f"{datetime.now():%Y%m%d-%H%M%S}-before-invalid-dev-mail-resource-repair"
    save_checkpoint.create(state, backup, "before removing invalid developer-mail resource sentinels")
    atomic_write_json(target, repaired)
    with target.open(encoding="utf-8") as stream:
        written = json.load(stream)
    repeated, remaining = repair(written)
    after = save_checkpoint.inspect_files(state)
    if remaining or repeated != repaired or any(
        before[name] != after[name] for name in save_checkpoint.STATE_FILES if name != "items.json"
    ):
        raise OSError(f"invalid developer-mail resource repair failed verification; backup: {backup}")
    save_checkpoint.verify(backup)
    return backup


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state", type=Path, default=DEFAULT_STATE)
    parser.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        backup = run(args.state.resolve(), args.apply)
        print(f"Repaired with complete verified backup: {backup}" if backup else "Dry-run/no change; use --apply after stopping the client/server")
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        parser.exit(1, f"repair_invalid_dev_mail_resources: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
