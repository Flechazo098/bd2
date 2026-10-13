#!/usr/bin/env python3
"""Prepare the pinned BD2 playtest using only Python 3's standard library."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shutil
import stat
import struct
import subprocess
import sys
import threading
import time
import urllib.parse
import urllib.request
import zipfile
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path, PurePosixPath

GAME_VERSION = "2.36.7"
GAME_DATA_VERSION = "20261008155037"
BUNDLE_VERSION = "20261007175947"
SERVER_ORIGIN = "https://bd2-test.45.76.195.129.sslip.io"
BUILD_ORIGINS = ("https://cdn.dev.bd2.pmang.cloud", "https://dl.dev.bd2.pmang.cloud")
RESOURCE_ORIGINS = ("https://dl.bd2.pmang.cloud", "https://cdn.bd2.pmang.cloud", "https://bd2-cdn.akamaized.net")
GAME_PATH = "/Build/Windows/20261002/StandaloneWindows64.2.36.7.zip"
GAME_SHA256 = "3ca28b7c94d65b2cb4e33784d843902758415a3a83870c5f6198f5cd90e3bff3"
GAME_BYTES = 413552979
DATA_SHA256 = "44cc20963abf800f4b3af449ff901534a1b99089571a820eb7bbb841fd0f0cd1"
DATA_BYTES = 191754787
CATALOG_SHA256 = "c9ed216c66903bfa1e556f38cc02e2923a48164e8469767d7987f7989d86d385"
CATALOG_BYTES = 67338179
CATALOG_HASH = "0f90a94601be05737f86a4ec7a1c889f"
SERVER_PATH = f"/ServerData/StandaloneWindows64/HD/{BUNDLE_VERSION}"
DATA_PATH = f"/GameData/{GAME_DATA_VERSION}"
GAME_REQUIRED = ("BrownDust II.exe", "UnityPlayer.dll", "BrownDust II_Data/globalgamemanagers", "BrownDust II_Data/Managed/Assembly-CSharp.dll")
_print_lock = threading.Lock()


def log(message: str) -> None:
    with _print_lock:
        print(message, flush=True)


def require_game_stopped(game: Path) -> None:
    if os.name != "nt":
        return
    import ctypes
    from ctypes import wintypes

    class ProcessEntry(ctypes.Structure):
        _fields_ = [
            ("dwSize", wintypes.DWORD), ("cntUsage", wintypes.DWORD),
            ("th32ProcessID", wintypes.DWORD), ("th32DefaultHeapID", ctypes.c_size_t),
            ("th32ModuleID", wintypes.DWORD), ("cntThreads", wintypes.DWORD),
            ("th32ParentProcessID", wintypes.DWORD), ("pcPriClassBase", wintypes.LONG),
            ("dwFlags", wintypes.DWORD), ("szExeFile", wintypes.WCHAR * 260),
        ]

    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel.CreateToolhelp32Snapshot.argtypes = (wintypes.DWORD, wintypes.DWORD)
    kernel.CreateToolhelp32Snapshot.restype = wintypes.HANDLE
    for name in ("Process32FirstW", "Process32NextW"):
        function = getattr(kernel, name)
        function.argtypes = (wintypes.HANDLE, ctypes.POINTER(ProcessEntry))
        function.restype = wintypes.BOOL
    kernel.OpenProcess.argtypes = (wintypes.DWORD, wintypes.BOOL, wintypes.DWORD)
    kernel.OpenProcess.restype = wintypes.HANDLE
    kernel.QueryFullProcessImageNameW.argtypes = (wintypes.HANDLE, wintypes.DWORD, wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD))
    kernel.QueryFullProcessImageNameW.restype = wintypes.BOOL
    kernel.CloseHandle.argtypes = (wintypes.HANDLE,)
    kernel.CloseHandle.restype = wintypes.BOOL
    snapshot = kernel.CreateToolhelp32Snapshot(0x00000002, 0)
    if snapshot == ctypes.c_void_p(-1).value:
        raise ctypes.WinError(ctypes.get_last_error())
    expected = os.path.normcase(str((game / "BrownDust II.exe").resolve()))
    entry = ProcessEntry()
    entry.dwSize = ctypes.sizeof(entry)
    try:
        found = kernel.Process32FirstW(snapshot, ctypes.byref(entry))
        while found:
            if entry.szExeFile.casefold() == "browndust ii.exe":
                process = kernel.OpenProcess(0x1000, False, entry.th32ProcessID)
                if not process:
                    error = ctypes.get_last_error()
                    if error != 87:  # A process may exit after the snapshot.
                        raise RuntimeError("无法确认正在运行的 BrownDust II 游戏目录，请关闭游戏后重新运行。")
                else:
                    try:
                        image = ctypes.create_unicode_buffer(32768)
                        length = wintypes.DWORD(len(image))
                        if not kernel.QueryFullProcessImageNameW(process, 0, image, ctypes.byref(length)):
                            raise RuntimeError("无法读取正在运行的 BrownDust II 游戏目录，请关闭游戏后重新运行。")
                        if os.path.normcase(str(Path(image.value).resolve())) == expected:
                            raise RuntimeError("这份游戏正在运行，请先退出游戏，再重新运行下载脚本。")
                    finally:
                        kernel.CloseHandle(process)
            found = kernel.Process32NextW(snapshot, ctypes.byref(entry))
        if ctypes.get_last_error() != 18:
            raise ctypes.WinError(ctypes.get_last_error())
    finally:
        kernel.CloseHandle(snapshot)


def atomic_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".part")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    os.replace(temporary, path)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def opener():
    # The measured line and actual transfers both connect directly to the CDN.
    return urllib.request.build_opener(urllib.request.ProxyHandler({}))


def measure_origin(origin: str, relative: str) -> tuple[str, float, str]:
    started = time.monotonic()
    received = 0
    error = ""
    request = urllib.request.Request(origin + relative, headers={"Range": "bytes=0-4194303", "Accept-Encoding": "identity"})
    try:
        with opener().open(request, timeout=8) as response:
            if response.status not in (200, 206):
                raise ValueError(f"HTTP {response.status}")
            while received < 4 * 1024**2 and time.monotonic() - started < 18:
                chunk = response.read1(min(64 * 1024, 4 * 1024**2 - received))
                if not chunk:
                    break
                received += len(chunk)
    except Exception as exc:
        error = str(exc)
    elapsed = max(time.monotonic() - started, 0.001)
    return origin, received / elapsed if received >= 256 * 1024 else 0, error


def fastest_origins(origins: tuple[str, ...], relative: str, label: str) -> list[str]:
    log(f"正在直连测速：{label}（每条线路最多 4 MiB）")
    with ThreadPoolExecutor(max_workers=len(origins)) as pool:
        results = list(pool.map(lambda origin: measure_origin(origin, relative), origins))
    for origin, speed, error in results:
        log(f"  {origin}: {speed / 1024**2:.2f} MiB/s" if speed else f"  {origin}: 不可用 {error}")
    ranked = [origin for origin, speed, _ in sorted(results, key=lambda result: result[1], reverse=True) if speed]
    if not ranked:
        raise RuntimeError(f"{label} 的官方线路均无法下载，请检查网络后重新运行。")
    log(f"{label} 选择：{ranked[0]}")
    return ranked


def unity_bundle_size(path: Path) -> int:
    with path.open("rb") as source:
        header = source.read(512)
    if not header.startswith(b"UnityFS\0"):
        return -1
    offset = 12
    try:
        for _ in range(2):
            offset = header.index(b"\0", offset) + 1
        return struct.unpack_from(">Q", header, offset)[0]
    except (ValueError, struct.error):
        return -1


@dataclass(frozen=True)
class Download:
    remote: str
    target: Path
    digest: str = ""
    size: int = 0
    bundle: bool = False


class Downloader:
    def __init__(self, root: Path):
        self.root = root
        self.checkpoint = root / "downloads" / "verified-downloads.json"
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.records = {}
        if self.checkpoint.is_file():
            saved = json.loads(self.checkpoint.read_text(encoding="utf-8-sig"))
            if isinstance(saved, dict) and saved.get("schema_version") == 1:
                self.records = saved.get("files", {})

    def key(self, path: Path) -> str:
        return path.relative_to(self.root).as_posix()

    def verified(self, item: Download) -> bool:
        if not item.target.is_file():
            return False
        size = item.target.stat().st_size
        if not size or (item.size and size != item.size) or (item.bundle and unity_bundle_size(item.target) != size):
            return False
        record = self.records.get(self.key(item.target), {})
        expected = item.digest or (record.get("sha256") if record.get("bytes") == size else "")
        return bool(expected) and sha256(item.target) == expected

    def receive(self, item: Download, origins: list[str]) -> str:
        if self.stop.is_set():
            return "cancelled"
        if self.verified(item):
            return "verified"
        item.target.parent.mkdir(parents=True, exist_ok=True)
        temporary = item.target.with_name(item.target.name + ".part")
        last_error = None
        for attempt in range(max(4, len(origins) * 2)):
            if self.stop.is_set():
                return "cancelled"
            origin = origins[attempt % len(origins)]
            try:
                # Full GET only. Partial CDN bundle responses can have corrupt contents.
                request = urllib.request.Request(origin + item.remote, headers={"Accept-Encoding": "identity"})
                digest = hashlib.sha256()
                size = 0
                started = time.monotonic()
                with opener().open(request, timeout=30) as response, temporary.open("wb") as destination:
                    if response.status != 200:
                        raise ValueError(f"完整下载需要 HTTP 200，收到 {response.status}")
                    length = response.headers.get("Content-Length")
                    while True:
                        if self.stop.is_set():
                            raise InterruptedError("下载已取消")
                        if time.monotonic() - started > 1800:
                            raise TimeoutError("单个文件下载超过 30 分钟")
                        chunk = response.read1(256 * 1024)
                        if not chunk:
                            break
                        destination.write(chunk)
                        digest.update(chunk)
                        size += len(chunk)
                actual_hash = digest.hexdigest()
                if not size or (length and size != int(length)) or (item.size and size != item.size):
                    raise ValueError("下载文件大小不完整")
                if item.bundle and unity_bundle_size(temporary) != size:
                    raise ValueError("UnityFS 资源包长度校验失败")
                if item.digest and item.digest != actual_hash:
                    raise ValueError("SHA-256 校验失败")
                os.replace(temporary, item.target)
                with self.lock:
                    self.records[self.key(item.target)] = {"bytes": size, "sha256": actual_hash}
                    atomic_json(self.checkpoint, {"schema_version": 1, "files": self.records})
                return "downloaded"
            except Exception as exc:
                last_error = exc
                temporary.unlink(missing_ok=True)
                if self.stop.is_set():
                    return "cancelled"
                log(f"重试 {item.target.name}：{exc}")
        raise RuntimeError(f"下载失败：{item.remote}\n{last_error}")

    def receive_all(self, files: list[Download], origins: list[str], workers: int) -> None:
        log(f"检查并下载 {len(files)} 个文件；已完成的文件校验通过后复用。")
        pool = ThreadPoolExecutor(max_workers=workers)
        futures = [pool.submit(self.receive, item, origins) for item in files]
        completed = 0
        try:
            for future in as_completed(futures):
                future.result()
                completed += 1
                if completed % 25 == 0 or completed == len(files):
                    log(f"完成 {completed}/{len(files)}")
        except BaseException:
            self.stop.set()
            for future in futures:
                future.cancel()
            raise
        finally:
            pool.shutdown(wait=True, cancel_futures=True)


def safe_target(root: Path, relative: str) -> Path:
    parts = relative.replace("\\", "/").split("/")
    if any(part in ("", ".", "..") or ":" in part or part.rstrip(" .") != part for part in parts):
        raise ValueError(f"不安全的归档或目录路径：{relative}")
    target = root.joinpath(*parts)
    if not target.resolve().is_relative_to(root.resolve()):
        raise ValueError(f"目录越界：{relative}")
    return target


def archive_entries(archive: zipfile.ZipFile, root: Path, prefix: str = ""):
    result = []
    for entry in archive.infolist():
        name = entry.filename.replace("\\", "/")
        if not name.startswith(prefix):
            continue
        relative = name[len(prefix):].rstrip("/")
        if not relative:
            continue
        mode = entry.external_attr >> 16
        if stat.S_ISLNK(mode):
            raise ValueError(f"ZIP 不允许符号链接：{name}")
        result.append((entry, safe_target(root, relative)))
    return result


def extract_entries(archive: zipfile.ZipFile, entries) -> None:
    for entry, target in entries:
        if entry.is_dir():
            target.mkdir(parents=True, exist_ok=True)
            continue
        target.parent.mkdir(parents=True, exist_ok=True)
        temporary = target.with_name(target.name + ".bd2-extract.part")
        try:
            with archive.open(entry) as source, temporary.open("wb") as destination:
                shutil.copyfileobj(source, destination, 1024 * 1024)
            os.replace(temporary, target)
        finally:
            temporary.unlink(missing_ok=True)


def extract_game(path: Path, game: Path) -> None:
    require_game_stopped(game)
    marker = game / ".bd2-official-game-version"
    with zipfile.ZipFile(path) as archive:
        executables = [entry for entry in archive.infolist() if PurePosixPath(entry.filename.replace("\\", "/")).name == "BrownDust II.exe"]
        if len(executables) != 1:
            raise ValueError("官方 ZIP 必须仅包含一个 BrownDust II.exe。")
        prefix = executables[0].filename.replace("\\", "/")[:-len("BrownDust II.exe")]
        entries = archive_entries(archive, game, prefix)
        entry_names = {target.relative_to(game).as_posix() for entry, target in entries if not entry.is_dir()}
        if not set(GAME_REQUIRED).issubset(entry_names):
            raise ValueError("官方 ZIP 缺少必要游戏文件。")
        if marker.is_file() and marker.read_text(encoding="utf-8").strip() == GAME_SHA256 and all(target.is_dir() if entry.is_dir() else target.is_file() and target.stat().st_size == entry.file_size for entry, target in entries):
            log("官方游戏已解压，复用现有安装。")
            return
        log("解压官方游戏目录……")
        extract_entries(archive, entries)
    marker.write_text(GAME_SHA256 + "\n", encoding="utf-8")


def install_bepinex(package: Path, game: Path) -> None:
    require_game_stopped(game)
    with zipfile.ZipFile(package / "bepinex.zip") as archive:
        entries = archive_entries(archive, game)
        names = {target.relative_to(game).as_posix() for entry, target in entries if not entry.is_dir()}
        if not {"winhttp.dll", "doorstop_config.ini", "BepInEx/core/BepInEx.dll"}.issubset(names):
            raise ValueError("随包 bepinex.zip 缺少必要文件。")
        core = game / "BepInEx" / "core"
        if core.exists():
            if core.is_symlink() or not core.resolve().is_relative_to(game.resolve()):
                raise ValueError("BepInEx/core 目录不安全。")
            shutil.rmtree(core)
        extract_entries(archive, entries)


def bundle_paths(catalog: dict) -> list[str]:
    paths = set()
    for value in catalog.get("m_InternalIds", []):
        if not isinstance(value, str):
            continue
        parts = value.replace("\\", "/").split("/")
        if parts[0] != "{BDNetwork.CdnInfo.Info}":
            continue
        if len(parts) < 5 or parts[1] != "StandaloneWindows64" or parts[2] not in ("HD", "{BDNetwork.CdnInfo.Resolution}") or parts[3] not in (BUNDLE_VERSION, "{BDNetwork.CdnInfo.Version}"):
            raise ValueError(f"资源目录版本不匹配：{value}")
        relative = "/".join(parts[4:])
        safe_target(Path.cwd(), relative)
        if not relative.endswith(".bundle"):
            raise ValueError(f"非资源包路径：{relative}")
        paths.add(relative)
    if not paths:
        raise ValueError("官方目录中没有资源包路径。")
    return sorted(paths)


def prepare(package: Path, game: Path, resources: Path) -> None:
    require_game_stopped(game)
    log("同步客户端插件、连接设置和 Client Studio 游戏目录……")
    command = [str(package / "bd2client.exe"), "prepare", "--game-dir", str(game), "--server-origin", SERVER_ORIGIN, "--resource-dir", str(resources)]
    result = subprocess.run(command, cwd=package, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding="utf-8", errors="replace")
    if result.stdout.strip():
        log(result.stdout.strip())
    if result.stderr.strip():
        log(result.stderr.strip())
    if result.returncode:
        raise RuntimeError(f"Client Studio 准备失败（退出码 {result.returncode}）。上方为完整错误信息。")


def main() -> int:
    parser = argparse.ArgumentParser(description="下载 BD2 2.36.7 官方游戏和完整资源，然后准备 Client Studio。")
    parser.add_argument("--package-dir", type=Path, default=Path(__file__).resolve().parent)
    parser.add_argument("--workers", type=int, default=8, help="资源下载并发数（1–16，默认 8）")
    parser.add_argument("--probe-only", action="store_true", help="仅测量官方线路，不下载或修改安装")
    args = parser.parse_args()
    if not 1 <= args.workers <= 16:
        parser.error("--workers 必须为 1–16")
    package = args.package_dir.resolve()
    if not args.probe_only:
        for name in ("bd2client.exe", "bepinex.zip", "versions.json"):
            if not (package / name).is_file():
                raise FileNotFoundError(f"请将脚本放在完整试玩包中：缺少 {name}")
        versions = json.loads((package / "versions.json").read_text(encoding="utf-8-sig"))
        if (versions.get("game_version"), versions.get("game_data_version"), versions.get("bundle_version")) != (GAME_VERSION, GAME_DATA_VERSION, BUNDLE_VERSION):
            raise ValueError("试玩包版本与下载脚本不一致。")
        require_game_stopped(package / "game")
    build_origins = fastest_origins(BUILD_ORIGINS, GAME_PATH, "游戏 ZIP")
    data_origins = fastest_origins(RESOURCE_ORIGINS, DATA_PATH + "/release/common-dbdata.bin", "GameData")
    bundle_origins = fastest_origins(RESOURCE_ORIGINS, SERVER_PATH + "/common-bundlecommon_assets_all.bundle", "ServerData")
    if args.probe_only:
        return 0
    downloader = Downloader(package)
    game = package / "game"
    resources = package / "resources"
    archive = package / "downloads" / f"BrownDust-II-{GAME_VERSION}.zip"
    downloader.receive_all([Download(GAME_PATH, archive, GAME_SHA256, GAME_BYTES)], build_origins, 1)
    extract_game(archive, game)
    server_dir = resources / SERVER_PATH.lstrip("/")
    catalog_file = server_dir / "catalog_alpha.json"
    catalog_hash_file = server_dir / "catalog_alpha.hash"
    downloader.receive_all([Download(SERVER_PATH + "/catalog_alpha.json", catalog_file, CATALOG_SHA256, CATALOG_BYTES), Download(SERVER_PATH + "/catalog_alpha.hash", catalog_hash_file)], bundle_origins, 2)
    if catalog_hash_file.read_text(encoding="ascii").strip() != CATALOG_HASH:
        raise ValueError("官方资源目录 hash 与当前版本不一致。")
    paths = bundle_paths(json.loads(catalog_file.read_text(encoding="utf-8-sig")))
    data_dir = resources / DATA_PATH.lstrip("/")
    downloader.receive_all([Download(DATA_PATH + "/design.version", data_dir / "design.version"), Download(DATA_PATH + "/release/common-dbdata.info", data_dir / "release/common-dbdata.info"), Download(DATA_PATH + "/release/common-dbdata.bin", data_dir / "release/common-dbdata.bin", DATA_SHA256, DATA_BYTES)], data_origins, 3)
    if str(json.loads((data_dir / "design.version").read_text(encoding="utf-8-sig")).get("version")) != GAME_DATA_VERSION:
        raise ValueError("GameData 版本不一致。")
    if int((data_dir / "release/common-dbdata.info").read_text(encoding="utf-8-sig").strip()) != DATA_BYTES:
        raise ValueError("GameData 元数据大小不一致。")
    downloader.receive_all([Download(SERVER_PATH + "/" + urllib.parse.quote(relative, safe="/._-="), safe_target(server_dir, relative), bundle=True) for relative in paths], bundle_origins, args.workers)
    install_bepinex(package, game)
    prepare(package, game, resources)
    log("准备完成。双击 bd2client.exe 打开 Client Studio，在工作台启动游戏。")
    return 0


if __name__ == "__main__":
    try:
        code = main()
    except KeyboardInterrupt:
        log("已停止；完成的文件已保存，下次运行会校验并继续。")
        code = 130
    except Exception as exc:
        log(f"失败：{exc}\n已完成的下载保留，重新运行即可继续。")
        code = 1
    if sys.stdin.isatty() and len(sys.argv) == 1:
        try:
            input("按 Enter 关闭窗口……")
        except (EOFError, KeyboardInterrupt):
            pass
    raise SystemExit(code)
