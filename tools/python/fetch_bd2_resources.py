#!/usr/bin/env python3
"""Download a BD2 ServerData catalog and GameData version without HTTP ranges."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
import sys
import threading
import time
import urllib.parse
import urllib.request
import zipfile
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path


SERVERDATA_HOST = "https://bd2-cdn.akamaized.net/ServerData"
GAMEDATA_HOST = "https://bd2-cdn.akamaized.net/GameData"
PLACEHOLDER = "{BDNetwork.CdnInfo.Info}"
_print_lock = threading.Lock()


def log(message: str) -> None:
    with _print_lock:
        print(message, flush=True)


def get_bytes(url: str, timeout: int = 300) -> bytes:
    # Deliberately no Range header: Akamai can return a length-correct but
    # corrupt bundle when a resource is assembled from partial responses.
    request = urllib.request.Request(url, method="GET", headers={"Accept-Encoding": "identity"})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.read()


def atomic_write(path: Path, data: bytes) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".part")
    temporary.write_bytes(data)
    os.replace(temporary, path)


def internal_paths(catalog: dict) -> list[str]:
    result: set[str] = set()
    for internal_id in catalog.get("m_InternalIds", []):
        if not isinstance(internal_id, str) or not internal_id.startswith(PLACEHOLDER):
            continue
        value = internal_id.replace("\\", "/")
        parts = value.split("/")
        # Placeholder/platform/resolution/version/<bundle path>
        if len(parts) >= 5:
            candidate = "/".join(parts[4:]).lstrip("/")
            if candidate:
                result.add(candidate)
    if not result:
        raise ValueError("catalog contains no BDNetwork.CdnInfo.Info bundle paths")
    return sorted(result)


def download_one(base: str, relative: str, destination: Path, validate_bundle: bool) -> tuple[str, int, str]:
    target = destination / relative.replace("/", os.sep)
    if target.is_file() and target.stat().st_size > 0:
        first = target.open("rb").read(7)
        if not validate_bundle or first[:7] == b"UnityFS":
            return relative, target.stat().st_size, "skip"
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_name(target.name + ".part")
    url = base.rstrip("/") + "/" + urllib.parse.quote(relative, safe="/._-=")
    last_error: Exception | None = None
    for attempt in range(1, 5):
        try:
            temporary.unlink(missing_ok=True)
            completed = subprocess.run(
                [
                    "curl", "-sS", "-L", "--fail", "--connect-timeout", "30",
                    "--max-time", "600", "--speed-limit", "1024", "--speed-time", "30",
                    "--output", str(temporary), url,
                ],
                check=False,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                text=True,
            )
            if completed.returncode != 0:
                raise RuntimeError(completed.stderr.strip() or f"curl exit {completed.returncode}")
            size = temporary.stat().st_size
            if size == 0:
                raise ValueError("empty response")
            magic = temporary.open("rb").read(16)
            if validate_bundle and magic[:7] != b"UnityFS":
                raise ValueError(f"missing UnityFS magic: {magic!r}")
            os.replace(temporary, target)
            return relative, size, "download"
        except Exception as exc:  # retry transient CDN failures
            last_error = exc
            temporary.unlink(missing_ok=True)
            if attempt < 4:
                time.sleep(attempt * 2)
    raise RuntimeError(f"{relative}: {last_error}")


def fetch_serverdata(args: argparse.Namespace, root: Path) -> dict:
    base = f"{SERVERDATA_HOST}/{args.platform}/HD/{args.bundle_version}"
    catalog_url = base + "/catalog_alpha.json"
    catalog_hash_url = base + "/catalog_alpha.hash"
    catalog_bytes = get_bytes(catalog_url)
    catalog_hash_bytes = get_bytes(catalog_hash_url)
    if not catalog_bytes.startswith(b"{"):
        raise ValueError(f"catalog is not JSON: {catalog_bytes[:16]!r}")
    catalog_hash = catalog_hash_bytes.decode("ascii").strip()
    if not re.fullmatch(r"[0-9a-fA-F]{32}", catalog_hash):
        raise ValueError(f"catalog hash is not a 32-digit hex value: {catalog_hash!r}")
    catalog = json.loads(catalog_bytes.decode("utf-8"))
    paths = internal_paths(catalog)
    destination = root / "ServerData" / args.platform / "HD" / args.bundle_version
    atomic_write(destination / "catalog_alpha.json", catalog_bytes)
    atomic_write(destination / "catalog_alpha.hash", (catalog_hash + "\n").encode("ascii"))
    log(f"ServerData {args.bundle_version}: {len(paths)} bundles -> {destination}")
    failures: list[str] = []
    done = 0
    total_bytes = 0
    if args.workers == 1:
        for path in paths:
            try:
                _, size, mode = download_one(base, path, destination, True)
                done += 1
                total_bytes += size
                if done % 25 == 0 or done == len(paths):
                    log(f"ServerData progress {done}/{len(paths)} ({total_bytes / 1024**3:.2f} GiB, {mode})")
            except Exception as exc:
                failures.append(str(exc))
                log(f"ServerData failed: {exc}")
    else:
        with ThreadPoolExecutor(max_workers=args.workers) as pool:
            futures = [pool.submit(download_one, base, path, destination, True) for path in paths]
            for future in futures:
                try:
                    _, size, mode = future.result()
                    done += 1
                    total_bytes += size
                    if done % 25 == 0 or done == len(paths):
                        log(f"ServerData progress {done}/{len(paths)} ({total_bytes / 1024**3:.2f} GiB, {mode})")
                except Exception as exc:
                    failures.append(str(exc))
    if failures:
        raise RuntimeError("ServerData failures:\n" + "\n".join(failures[:20]))
    return {
        "catalog_url": catalog_url,
        "catalog_hash_url": catalog_hash_url,
        "catalog_hash": catalog_hash.lower(),
        "bundles": len(paths),
        "bytes": total_bytes,
    }


def fetch_gamedata(args: argparse.Namespace, root: Path) -> dict:
    files = ["design.version", "release/common-dbdata.info", "release/common-dbdata.bin"]
    # The first entry is outside release; preserve the server's URL layout.
    records = []
    for relative in files:
        url = f"{GAMEDATA_HOST}/{args.game_data_version}/{relative}"
        target = root / "GameData" / args.game_data_version / relative.replace("/", os.sep)
        if target.is_file() and target.stat().st_size > 0:
            data = target.read_bytes()
            mode = "existing"
        else:
            data = get_bytes(url)
            if not data:
                raise ValueError(f"empty GameData response: {url}")
            atomic_write(target, data)
            mode = "downloaded"
        records.append({"path": relative, "bytes": len(data), "sha256": hashlib.sha256(data).hexdigest()})
        log(f"GameData {args.game_data_version}: {relative} ({len(data) / 1024**2:.1f} MiB, {mode})")
    archive = root / "GameData" / args.game_data_version / "release" / "common-dbdata.bin"
    if archive.open("rb").read(4) != b"PK\x03\x04":
        raise ValueError(f"GameData archive is missing ZIP magic: {archive}")
    with zipfile.ZipFile(archive) as package:
        damaged = package.testzip()
        if damaged is not None:
            raise ValueError(f"GameData ZIP CRC failed at {damaged!r}")
        members = len(package.infolist())
    info_path = root / "GameData" / args.game_data_version / "release" / "common-dbdata.info"
    expected_size = int(info_path.read_text(encoding="utf-8").strip())
    if archive.stat().st_size != expected_size:
        raise ValueError(f"GameData size mismatch: {archive.stat().st_size} != {expected_size}")
    log(f"GameData ZIP verified: {members} entries, {expected_size} bytes")
    return {"version": args.game_data_version, "files": records}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--bundle-version", required=True)
    parser.add_argument("--game-data-version", required=True)
    parser.add_argument("--platform", default="StandaloneWindows64")
    parser.add_argument("--output-root", type=Path, required=True)
    parser.add_argument("--workers", type=int, default=16)
    args = parser.parse_args()
    if not re.fullmatch(r"\d{14}", args.bundle_version) or not re.fullmatch(r"\d{14}", args.game_data_version):
        parser.error("versions must be 14-digit timestamps")
    root = args.output_root.resolve()
    root.mkdir(parents=True, exist_ok=True)
    result = {
        "bundle_version": args.bundle_version,
        "game_data_version": args.game_data_version,
        "game_data": fetch_gamedata(args, root),
        "server_data": fetch_serverdata(args, root),
    }
    atomic_write(root / "resource-fetch-manifest.json", (json.dumps(result, indent=2) + "\n").encode())
    log(f"resource fetch complete: {root / 'resource-fetch-manifest.json'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
