#!/usr/bin/env python3
"""Narrowly roll the local account back to the pack21 quest-38 boundary.

This is a one-off development/recovery tool, not server code. It understands
both legacy progress saves and the v2 ``pack:quest`` schema. By default it only
prints the planned changes; pass --apply after stopping the client and server.
Before applying it copies all nine account files to a timestamped checkpoint
and verifies every SHA-256 hash.
"""

from __future__ import annotations

import argparse
import copy
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys


STATE_FILES = (
    "characters.json",
    "collection.json",
    "deck.json",
    "equipment.json",
    "items.json",
    "mail.json",
    "missions.json",
    "progress.json",
    "wallet.json",
)
QUEST_ITEMS = {
    900000033: (122, 19, 1),
    900000034: (11, 19, 1),
    900000035: (501, 27, 6),
    900000036: (101, 27, 3),
}
ITEM_GRANT = "pack21:quest38:items"
WALLET_GRANT = "pack21:quest38"
WORKSPACE_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_STATE = WORKSPACE_ROOT / "data" / "state"


def load_json(path: Path):
    with path.open("r", encoding="utf-8") as stream:
        return json.load(stream)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest().upper()


def quest_key(progress: dict, pack_id: int, quest_id: int) -> str:
    version = int(progress.get("version", 0))
    return f"{pack_id}:{quest_id}" if version >= 2 else str(quest_id)


def quest_cleared(progress: dict, pack_id: int, quest_id: int) -> bool:
    value = progress.get("cleared_quests", {}).get(quest_key(progress, pack_id, quest_id))
    if int(progress.get("version", 0)) >= 2:
        return value is True
    return value == pack_id


def running_game_processes() -> list[str]:
    if os.name != "nt":
        return []
    result = subprocess.run(
        ["tasklist", "/FO", "CSV", "/NH"],
        check=False,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )
    names = []
    for line in result.stdout.splitlines():
        lowered = line.lower()
        if "browndust2" in lowered or "brown dust ii" in lowered or "bd2server.exe" in lowered:
            names.append(line)
    return names


def build_changes(state: Path, reference: Path) -> dict[str, dict]:
    progress = load_json(state / "progress.json")
    prior_progress = load_json(reference / "progress.json")
    if not quest_cleared(progress, 21, 38):
        raise ValueError("current save has not cleared pack21 quest38")
    if not quest_cleared(prior_progress, 21, 37) or quest_cleared(prior_progress, 21, 38):
        raise ValueError("reference is not at the quest37-complete/quest38-pending boundary")

    progress = copy.deepcopy(progress)
    progress.get("cleared_quests", {}).pop(quest_key(progress, 21, 38), None)
    progress.get("quests", {}).pop(quest_key(progress, 21, 38), None)
    progress["position"] = copy.deepcopy(prior_progress["position"])

    items = load_json(state / "items.json")
    indices = items.get("grant_items", {}).get(ITEM_GRANT)
    if (
        items.get("granted", {}).get(ITEM_GRANT) is not True
        or not isinstance(indices, list)
        or set(map(int, indices)) != set(QUEST_ITEMS)
    ):
        raise ValueError("quest38 item grant does not match the expected four instances")
    remaining = []
    removed: set[int] = set()
    for item in items.get("items", []):
        index = int(item.get("inven_index", 0))
        if index not in QUEST_ITEMS:
            remaining.append(item)
            continue
        expected = QUEST_ITEMS[index]
        actual = (int(item.get("id", 0)), int(item.get("type", 0)), int(item.get("count", 0)))
        if actual != expected:
            raise ValueError(f"unexpected quest38 reward item {item!r}; expected {expected}")
        removed.add(index)
    if removed != set(QUEST_ITEMS):
        raise ValueError(f"quest38 reward instances are incomplete: found {sorted(removed)}")
    items = copy.deepcopy(items)
    items["items"] = remaining
    items["granted"].pop(ITEM_GRANT, None)
    items["grant_items"].pop(ITEM_GRANT, None)
    items["next_index"] = max(
        (int(item.get("inven_index", 0)) + 1 for item in remaining), default=1
    )

    wallet = load_json(state / "wallet.json")
    if wallet.get("granted", {}).get(WALLET_GRANT) is not True or int(wallet.get("gold", 0)) < 1500:
        raise ValueError("quest38 wallet grant is absent or gold is below 1500")
    wallet = copy.deepcopy(wallet)
    wallet["gold"] = int(wallet["gold"]) - 1500
    wallet["granted"].pop(WALLET_GRANT, None)

    deck = load_json(state / "deck.json")
    prior_deck = load_json(reference / "deck.json")
    if "field_char_control_deck_type" not in prior_deck:
        raise ValueError("reference deck has no field_char_control_deck_type")
    deck = copy.deepcopy(deck)
    deck["field_char_control_deck_type"] = prior_deck["field_char_control_deck_type"]

    return {
        "progress.json": progress,
        "items.json": items,
        "wallet.json": wallet,
        "deck.json": deck,
    }


def checkpoint_all(state: Path, target: Path) -> None:
    if target.exists():
        raise FileExistsError(f"checkpoint already exists: {target}")
    target.mkdir(parents=True)
    try:
        for name in STATE_FILES:
            source = state / name
            if not source.is_file():
                raise FileNotFoundError(f"missing state file: {source}")
            shutil.copy2(source, target / name)
        mismatches = [
            name for name in STATE_FILES if sha256(state / name) != sha256(target / name)
        ]
        if mismatches:
            raise OSError(f"checkpoint SHA-256 mismatch: {mismatches}")
    except Exception:
        shutil.rmtree(target, ignore_errors=True)
        raise


def atomic_write_json(path: Path, value: dict) -> None:
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("w", encoding="utf-8", newline="\n") as stream:
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def describe(changes: dict[str, dict]) -> dict:
    progress = changes["progress.json"]
    wallet = changes["wallet.json"]
    items = changes["items.json"]
    deck = changes["deck.json"]
    return {
        "quest37_cleared": quest_cleared(progress, 21, 37),
        "quest38_cleared": quest_cleared(progress, 21, 38),
        "position": progress.get("position"),
        "gold": wallet.get("gold"),
        "next_item_index": items.get("next_index"),
        "field_char_control_deck_type": deck.get("field_char_control_deck_type"),
        "files_to_modify": sorted(changes),
    }


def verify_written_state(state: Path) -> dict:
    progress = load_json(state / "progress.json")
    items = load_json(state / "items.json")
    wallet = load_json(state / "wallet.json")
    deck = load_json(state / "deck.json")
    if not quest_cleared(progress, 21, 37) or quest_cleared(progress, 21, 38):
        raise ValueError("written progress is not at the quest38 boundary")
    if ITEM_GRANT in items.get("granted", {}) or ITEM_GRANT in items.get("grant_items", {}):
        raise ValueError("written inventory still contains the quest38 grant")
    if any(int(item.get("inven_index", 0)) in QUEST_ITEMS for item in items.get("items", [])):
        raise ValueError("written inventory still contains a quest38 reward instance")
    if WALLET_GRANT in wallet.get("granted", {}):
        raise ValueError("written wallet still contains the quest38 grant")
    return describe(
        {
            "progress.json": progress,
            "items.json": items,
            "wallet.json": wallet,
            "deck.json": deck,
        }
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("state", nargs="?", default=DEFAULT_STATE, type=Path)
    parser.add_argument(
        "reference",
        nargs="?",
        default=DEFAULT_STATE / "checkpoints" / "20260920-1801-before-regular-gacha",
        type=Path,
    )
    parser.add_argument("--apply", action="store_true", help="perform the rollback")
    parser.add_argument(
        "--backup",
        type=Path,
        help="checkpoint destination; defaults below state/checkpoints",
    )
    args = parser.parse_args()
    state = args.state.resolve()
    reference = args.reference.resolve()
    try:
        changes = build_changes(state, reference)
        print(json.dumps(describe(changes), ensure_ascii=False, indent=2))
        if not args.apply:
            print("dry run only; stop client/server and pass --apply to write", file=sys.stderr)
            return 0
        running = running_game_processes()
        if running:
            raise RuntimeError("client/server is still running:\n" + "\n".join(running))
        backup = args.backup
        if backup is None:
            stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
            backup = state / "checkpoints" / f"{stamp}-before-quest38-rollback"
        backup = backup.resolve()
        checkpoint_all(state, backup)
        for name, value in changes.items():
            atomic_write_json(state / name, value)
        verified = verify_written_state(state)
        print(f"rollback applied; verified checkpoint: {backup}")
        print(json.dumps(verified, ensure_ascii=False, indent=2))
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        parser.exit(1, f"rollback_quest38: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
