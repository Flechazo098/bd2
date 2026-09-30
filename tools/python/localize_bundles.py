#!/usr/bin/env python3
"""Install one complete downloaded ServerData release as the built-in local catalog.

The source directory must contain the exact ``catalog_alpha.json`` and every
bundle referenced by it. Referenced bundles are hard-linked into the game's
``StreamingAssets/aa`` directory, then a localized copy of the *same* catalog
atomically replaces ``catalog.json``. No bundle data is duplicated when source
and game are on the same filesystem.

Usage:

    python localize_bundles.py <ServerData version directory> <game aa directory>
"""

import hashlib
import io
import json
import os
import shutil
import sys
import tempfile


BS = chr(92)
REMOTE_PREFIX = "{BDNetwork.CdnInfo.Info}" + BS
LOCAL_PREFIX = "{UnityEngine.AddressableAssets.Addressables.RuntimePath}" + BS
BACKUP_NAME = "catalog.json.bak-before-local-catalog-sync"


def relative_bundle(internal_id):
    """Return the path below the version root for one remote internal ID."""
    if not internal_id.startswith(REMOTE_PREFIX):
        return None
    parts = internal_id.replace(BS, "/").split("/")
    if len(parts) < 5:
        raise ValueError("remote internal ID has no bundle path: %r" % internal_id)
    relative = "/".join(parts[4:])
    if not relative or "{" in relative or not relative.endswith(".bundle"):
        raise ValueError("remote internal ID has an invalid bundle path: %r" % internal_id)
    if relative.startswith("/") or any(part in ("", ".", "..") for part in relative.split("/")):
        raise ValueError("remote internal ID escapes the release directory: %r" % internal_id)
    return relative


def inside(root, relative):
    root = os.path.realpath(root)
    candidate = os.path.realpath(os.path.join(root, *relative.split("/")))
    if os.path.commonpath((root, candidate)) != root:
        raise ValueError("bundle path escapes its root: %s" % relative)
    return candidate


def digest(path):
    value = hashlib.sha256()
    with open(path, "rb") as stream:
        while True:
            block = stream.read(1024 * 1024)
            if not block:
                return value.digest()
            value.update(block)


def same_content(left, right):
    try:
        if os.path.samefile(left, right):
            return True
    except OSError:
        pass
    return os.path.getsize(left) == os.path.getsize(right) and digest(left) == digest(right)


def load_catalog(path):
    with io.open(path, encoding="utf-8") as stream:
        value = json.load(stream)
    internal_ids = value.get("m_InternalIds")
    if not isinstance(internal_ids, list) or not internal_ids:
        raise ValueError("catalog has no non-empty m_InternalIds array")
    if not all(isinstance(item, str) for item in internal_ids):
        raise ValueError("catalog m_InternalIds contains a non-string value")
    return value


def prepare_catalog(source_directory, aa_directory):
    source_directory = os.path.realpath(source_directory)
    aa_directory = os.path.realpath(aa_directory)
    source_catalog = os.path.join(source_directory, "catalog_alpha.json")
    target_catalog = os.path.join(aa_directory, "catalog.json")
    if not os.path.isfile(source_catalog):
        raise FileNotFoundError("source release is missing catalog_alpha.json: " + source_catalog)
    if not os.path.isdir(aa_directory):
        raise NotADirectoryError("game Addressables directory does not exist: " + aa_directory)
    if not os.path.isfile(target_catalog):
        raise FileNotFoundError("game Addressables catalog does not exist: " + target_catalog)

    catalog = load_catalog(source_catalog)
    referenced = {}
    changed = 0
    for index, internal_id in enumerate(catalog["m_InternalIds"]):
        relative = relative_bundle(internal_id)
        if relative is None:
            continue
        source = inside(source_directory, relative)
        if not os.path.isfile(source):
            raise FileNotFoundError("catalog references a missing source bundle: " + source)
        referenced[relative] = source
        catalog["m_InternalIds"][index] = LOCAL_PREFIX + relative.replace("/", BS)
        changed += 1
    if changed == 0:
        raise ValueError("source catalog contains no BDNetwork CDN bundle entries")

    linked = existing = 0
    for relative, source in sorted(referenced.items()):
        destination = inside(aa_directory, relative)
        os.makedirs(os.path.dirname(destination), exist_ok=True)
        if os.path.exists(destination):
            if not os.path.isfile(destination) or not same_content(source, destination):
                raise FileExistsError("game bundle conflicts with downloaded release: " + destination)
            existing += 1
            continue
        try:
            os.link(source, destination)
        except OSError as error:
            raise OSError(
                "could not hard-link bundle without duplicating data; keep the download and game on the same filesystem: "
                + destination
            ) from error
        linked += 1

    for internal_id in catalog["m_InternalIds"]:
        if not internal_id.startswith(LOCAL_PREFIX):
            continue
        relative = internal_id[len(LOCAL_PREFIX):].replace(BS, "/")
        if not os.path.isfile(inside(aa_directory, relative)):
            raise FileNotFoundError("localized catalog references a missing game bundle: " + relative)

    raw = json.dumps(catalog, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    backup = os.path.join(aa_directory, BACKUP_NAME)
    if not os.path.exists(backup):
        shutil.copy2(target_catalog, backup)
    elif not os.path.isfile(backup):
        raise FileExistsError("catalog backup path is not a regular file: " + backup)

    descriptor, temporary = tempfile.mkstemp(prefix=".catalog-local-", suffix=".json", dir=aa_directory)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(raw)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, target_catalog)
    except BaseException:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass
        raise
    return {
        "changed_ids": changed,
        "unique_bundles": len(referenced),
        "linked": linked,
        "existing": existing,
        "catalog_bytes": len(raw),
        "catalog_sha256": hashlib.sha256(raw).hexdigest(),
        "backup": backup,
    }


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        return 2
    try:
        result = prepare_catalog(sys.argv[1], sys.argv[2])
    except Exception as error:
        print("local catalog sync failed: %s" % error, file=sys.stderr)
        return 1
    print("localized current catalog: %d IDs, %d unique bundles" % (
        result["changed_ids"], result["unique_bundles"]))
    print("hard links: %d new, %d already exact" % (result["linked"], result["existing"]))
    print("catalog: %d bytes, sha256=%s" % (result["catalog_bytes"], result["catalog_sha256"]))
    print("backup: %s" % result["backup"])
    return 0


if __name__ == "__main__":
    sys.exit(main())
