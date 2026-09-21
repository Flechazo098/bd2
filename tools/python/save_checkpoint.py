#!/usr/bin/env python3
"""Create, verify, and restore complete local-account checkpoints.

Unlike the old tutorial-specific Go command this tool does not synthesize game
state. It copies the nine authoritative JSON files, records SHA-256 hashes, and
requires --apply plus stopped client/server processes before restore.
"""

from __future__ import annotations

import argparse
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
MANIFEST = "checkpoint.json"
WORKSPACE_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_STATE = WORKSPACE_ROOT / "data" / "state"


def digest(path: Path) -> str:
    result = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            result.update(block)
    return result.hexdigest().upper()


def inspect_files(directory: Path) -> dict[str, dict[str, object]]:
    result = {}
    for name in STATE_FILES:
        path = directory / name
        if not path.is_file():
            raise FileNotFoundError(f"missing account state file: {path}")
        with path.open("r", encoding="utf-8") as stream:
            json.load(stream)
        result[name] = {"size": path.stat().st_size, "sha256": digest(path)}
    return result


def running_processes() -> list[str]:
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
    return [
        line
        for line in result.stdout.splitlines()
        if "browndust2" in line.lower() or "bd2server.exe" in line.lower()
    ]


def write_manifest(target: Path, files: dict[str, dict[str, object]], label: str) -> None:
    value = {
        "format": 1,
        "created_at": datetime.now().astimezone().isoformat(),
        "label": label,
        "files": files,
    }
    path = target / MANIFEST
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


def create(state: Path, target: Path, label: str) -> dict[str, dict[str, object]]:
    source_info = inspect_files(state)
    if target.exists():
        raise FileExistsError(f"checkpoint already exists: {target}")
    target.mkdir(parents=True)
    try:
        for name in STATE_FILES:
            shutil.copy2(state / name, target / name)
        copied_info = inspect_files(target)
        if copied_info != source_info:
            raise OSError("checkpoint differs from source after SHA-256 verification")
        write_manifest(target, copied_info, label)
        return copied_info
    except Exception:
        shutil.rmtree(target, ignore_errors=True)
        raise


def verify(checkpoint: Path) -> dict[str, dict[str, object]]:
    actual = inspect_files(checkpoint)
    manifest_path = checkpoint / MANIFEST
    if manifest_path.is_file():
        with manifest_path.open("r", encoding="utf-8") as stream:
            manifest = json.load(stream)
        if manifest.get("format") != 1 or manifest.get("files") != actual:
            raise ValueError("checkpoint manifest does not match its files")
    return actual


def atomic_copy(source: Path, target: Path) -> None:
    temporary = target.with_name(f".{target.name}.{os.getpid()}.tmp")
    try:
        with source.open("rb") as reader, temporary.open("wb") as writer:
            shutil.copyfileobj(reader, writer, length=1024 * 1024)
            writer.flush()
            os.fsync(writer.fileno())
        os.replace(temporary, target)
    finally:
        temporary.unlink(missing_ok=True)


def restore(state: Path, checkpoint: Path, apply: bool) -> Path | None:
    checkpoint_info = verify(checkpoint)
    print(json.dumps(checkpoint_info, ensure_ascii=False, indent=2))
    if not apply:
        print("dry run only; stop client/server and pass --apply to restore", file=sys.stderr)
        return None
    running = running_processes()
    if running:
        raise RuntimeError("client/server is still running:\n" + "\n".join(running))
    stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
    backup = state / "checkpoints" / f"{stamp}-before-restore"
    create(state, backup, "automatic backup before restore")
    for name in STATE_FILES:
        atomic_copy(checkpoint / name, state / name)
    if inspect_files(state) != checkpoint_info:
        raise OSError(f"restore verification failed; previous state is at {backup}")
    return backup


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    command = sub.add_parser("create")
    command.add_argument("--state", type=Path, default=DEFAULT_STATE)
    command.add_argument("--label", default="manual")
    command.add_argument("--output", type=Path)

    command = sub.add_parser("verify")
    command.add_argument("checkpoint", type=Path)

    command = sub.add_parser("restore")
    command.add_argument("checkpoint", type=Path)
    command.add_argument("--state", type=Path, default=DEFAULT_STATE)
    command.add_argument("--apply", action="store_true")
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        if args.command == "create":
            state = args.state.resolve()
            output = args.output
            if output is None:
                stamp = datetime.now().strftime("%Y%m%d-%H%M%S")
                safe_label = "-".join(args.label.strip().split()) or "manual"
                output = state / "checkpoints" / f"{stamp}-{safe_label}"
            output = output.resolve()
            info = create(state, output, args.label)
            print(f"created {output}")
            print(json.dumps(info, ensure_ascii=False, indent=2))
        elif args.command == "verify":
            print(json.dumps(verify(args.checkpoint.resolve()), ensure_ascii=False, indent=2))
        else:
            backup = restore(args.state.resolve(), args.checkpoint.resolve(), args.apply)
            if backup is not None:
                print(f"restored {args.checkpoint.resolve()}; previous state: {backup}")
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        parser.exit(1, f"save_checkpoint: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
