#!/usr/bin/env python3
"""Create a searchable, read-only deobfuscated mirror of C# client source.

The original source tree is never modified.  Mapping files use ``left⇨right``;
``#ReverseOrder`` reverses mappings on subsequent lines.  Output is a mirror
with a JSON manifest describing every effective replacement and limitation.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import shutil
import tempfile
from typing import Iterable
import unicodedata


TOOL = "bd2.deobfuscate_client_source"
MANIFEST = ".bd2-deobfuscate-manifest.json"
ARROW = "⇨"
CSHARP_KEYWORDS = {
    "abstract", "as", "base", "bool", "break", "byte", "case", "catch", "char",
    "checked", "class", "const", "continue", "decimal", "default", "delegate", "do",
    "double", "else", "enum", "event", "explicit", "extern", "false", "finally",
    "fixed", "float", "for", "foreach", "goto", "if", "implicit", "in", "int",
    "interface", "internal", "is", "lock", "long", "namespace", "new", "null",
    "object", "operator", "out", "override", "params", "private", "protected", "public",
    "readonly", "ref", "return", "sbyte", "sealed", "short", "sizeof", "stackalloc",
    "static", "string", "struct", "switch", "this", "throw", "true", "try", "typeof",
    "uint", "ulong", "unchecked", "unsafe", "ushort", "using", "virtual", "void",
    "volatile", "while",
}


def is_within(child: Path, parent: Path) -> bool:
    try:
        child.relative_to(parent)
        return True
    except ValueError:
        return False


def require_distinct_trees(source: Path, output: Path) -> tuple[Path, Path]:
    source, output = source.resolve(), output.resolve()
    if not source.is_dir():
        raise ValueError(f"source is not a directory: {source}")
    if source == output or is_within(output, source) or is_within(source, output):
        raise ValueError("output must be outside, and not contain, source")
    return source, output


def managed_output(output: Path) -> bool:
    manifest = output / MANIFEST
    if not manifest.is_file():
        return False
    try:
        return json.loads(manifest.read_text(encoding="utf-8")).get("tool") == TOOL
    except (OSError, json.JSONDecodeError):
        return False


def prepare_stage(output: Path) -> Path:
    if output.exists() and not managed_output(output):
        raise FileExistsError(
            f"refusing to overwrite non-managed output directory: {output}"
        )
    output.parent.mkdir(parents=True, exist_ok=True)
    return Path(tempfile.mkdtemp(prefix=f".{output.name}.", dir=output.parent))


def publish_stage(stage: Path, output: Path) -> None:
    if output.exists():
        # The caller already established that this is a directory created by us.
        shutil.rmtree(output)
    stage.replace(output)


def identifier_from_meaning(value: str) -> str:
    """Turn a map value into one legal, readable C# identifier.

    Translation databases sometimes store a qualified path such as
    ``Net.Player/UserInfo``.  An identifier replacement must be a single token,
    so the useful terminal component is selected before normalising it.
    """
    parts = [part for part in re.split(r"(?:\.|::|/|\\)+", value.strip()) if part]
    raw = parts[-1] if parts else value.strip()
    result = []
    for index, char in enumerate(raw):
        if (char == "_" or char.isascii() and char.isalpha() or
                index > 0 and char.isascii() and char.isdigit()):
            result.append(char)
        elif char.isascii() and char.isdigit() and index == 0:
            result.extend(("_", char))
        else:
            result.append("_")
    name = "".join(result).strip("_") or "unnamed"
    if name[0].isdigit():
        name = "_" + name
    if name in CSHARP_KEYWORDS:
        name = "_" + name
    return name


def is_identifier_start(char: str) -> bool:
    return char == "_" or unicodedata.category(char) in {
        "Lu", "Ll", "Lt", "Lm", "Lo", "Nl",
    }


def is_identifier_continue(char: str) -> bool:
    return is_identifier_start(char) or unicodedata.category(char) in {
        "Mn", "Mc", "Nd", "Pc", "Cf",
    }


def parse_mapping(path: Path) -> tuple[list[dict], list[str]]:
    """Read the mapping while retaining malformed/conflicting entries as notes."""
    entries: list[dict] = []
    warnings: list[str] = []
    for line_number, raw_line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):
        line = raw_line.strip()
        if not line or line.startswith("//"):
            continue
        if line.casefold() == "#reverseorder":
            # Official files carry this as format metadata, but their actual
            # rows are still visibly obfuscated-name ⇨ readable-name.
            continue
        if ARROW not in line:
            if not line.startswith("#"):
                warnings.append(f"line {line_number}: ignored (no {ARROW!r})")
            continue
        left, right = (part.strip() for part in line.split(ARROW, 1))
        source, meaning = left, right
        if not source or not meaning:
            warnings.append(f"line {line_number}: ignored (empty mapping side)")
            continue
        if not source or not is_identifier_start(source[0]) or not all(
                is_identifier_continue(char) for char in source[1:]):
            warnings.append(f"line {line_number}: ignored (non-identifier source {source!r})")
            continue
        entries.append({
            "line": line_number,
            "source": source,
            "meaning": meaning,
        })
    return entries, warnings


def build_replacements(entries: Iterable[dict]) -> tuple[dict[str, str], list[dict], list[str]]:
    replacements: dict[str, str] = {}
    report: list[dict] = []
    warnings: list[str] = []
    used: set[str] = set()
    grouped: dict[str, list[dict]] = {}
    for entry in entries:
        grouped.setdefault(entry["source"], []).append(entry)
    for source, candidates in grouped.items():
        meanings = {entry["meaning"] for entry in candidates}
        if len(meanings) != 1:
            warnings.append(
                f"source {source!r}: ignored ambiguous scoped mappings "
                f"({len(meanings)} meanings)"
            )
            continue
        entry = candidates[0]
        base = identifier_from_meaning(entry["meaning"])
        replacement = base
        collision = False
        if replacement in used:
            collision = True
            suffix = re.sub(r"[^A-Za-z0-9_]", "_", source)
            if not suffix.strip("_"):
                suffix = "_".join(f"u{ord(char):04X}" for char in source)
            replacement = f"{base}__from_{suffix}"
            number = 2
            while replacement in used:
                replacement = f"{base}__from_{suffix}_{number}"
                number += 1
        used.add(replacement)
        replacements[source] = replacement
        report.append({
            **entry,
            "replacement": replacement,
            "sanitized": replacement != entry["meaning"],
            "name_collision": collision,
        })
    return replacements, report, warnings


def _consume_quoted(text: str, start: int, quote: str) -> int:
    """Return the index after a C# string/character/raw-string literal."""
    quotes = 0
    while start + quotes < len(text) and text[start + quotes] == quote:
        quotes += 1
    if quote == '"' and quotes >= 3:
        end_marker = quote * quotes
        end = text.find(end_marker, start + quotes)
        return len(text) if end < 0 else end + quotes
    index = start + 1
    verbatim = start > 0 and text[start - 1] == "@"
    while index < len(text):
        if verbatim and quote == '"' and text.startswith('""', index):
            index += 2
        elif text[index] == quote:
            return index + 1
        elif not verbatim and text[index] == "\\":
            index += 2
        else:
            index += 1
    return len(text)


def replace_csharp_identifiers(text: str, replacements: dict[str, str]) -> tuple[str, int]:
    """Replace code identifiers only; comments and literal payloads stay exact."""
    output: list[str] = []
    index = changed = 0
    length = len(text)
    while index < length:
        if text.startswith("//", index):
            end = text.find("\n", index)
            end = length if end < 0 else end
            output.append(text[index:end])
            index = end
        elif text.startswith("/*", index):
            end = text.find("*/", index + 2)
            end = length if end < 0 else end + 2
            output.append(text[index:end])
            index = end
        elif text[index] in "\"'":
            end = _consume_quoted(text, index, text[index])
            output.append(text[index:end])
            index = end
        elif is_identifier_start(text[index]):
            end = index + 1
            while end < length and is_identifier_continue(text[end]):
                end += 1
            token = text[index:end]
            replacement = replacements.get(token, token)
            output.append(replacement)
            changed += replacement != token
            index = end
        else:
            output.append(text[index])
            index += 1
    return "".join(output), changed


def read_csharp(path: Path) -> tuple[str, str] | None:
    data = path.read_bytes()
    if data.startswith(b"\xff\xfe"):
        return data[2:].decode("utf-16-le"), "utf-16-le"
    if data.startswith(b"\xfe\xff"):
        return data[2:].decode("utf-16-be"), "utf-16-be"
    try:
        return data.decode("utf-8-sig"), "utf-8-sig" if data.startswith(b"\xef\xbb\xbf") else "utf-8"
    except UnicodeDecodeError:
        return None


def write_csharp(path: Path, text: str, encoding: str) -> None:
    if encoding == "utf-16-le":
        path.write_bytes(b"\xff\xfe" + text.encode(encoding))
    elif encoding == "utf-16-be":
        path.write_bytes(b"\xfe\xff" + text.encode(encoding))
    elif encoding == "utf-8-sig":
        path.write_bytes(text.encode(encoding))
    else:
        path.write_text(text, encoding="utf-8", newline="")


def destination_for(relative: Path, replacements: dict[str, str], occupied: set[Path]) -> Path:
    if relative.suffix.casefold() != ".cs":
        return relative
    stem = replacements.get(relative.stem, relative.stem)
    candidate = relative.with_name(stem + relative.suffix)
    if candidate not in occupied:
        return candidate
    number = 2
    while True:
        candidate = relative.with_name(f"{stem}__file_{number}{relative.suffix}")
        if candidate not in occupied:
            return candidate
        number += 1


def deobfuscate(source: Path, mapping: Path, output: Path) -> dict:
    source, output = require_distinct_trees(source, output)
    mapping = mapping.resolve()
    if not mapping.is_file():
        raise ValueError(f"mapping is not a file: {mapping}")
    entries, warnings = parse_mapping(mapping)
    replacements, mapping_report, replacement_warnings = build_replacements(entries)
    warnings.extend(replacement_warnings)
    stage = prepare_stage(output)
    files: list[dict] = []
    occupied: set[Path] = set()
    try:
        for input_path in sorted(path for path in source.rglob("*") if path.is_file()):
            relative = input_path.relative_to(source)
            destination = destination_for(relative, replacements, occupied)
            occupied.add(destination)
            destination_path = stage / destination
            destination_path.parent.mkdir(parents=True, exist_ok=True)
            record = {"source": relative.as_posix(), "output": destination.as_posix()}
            if input_path.suffix.casefold() == ".cs":
                decoded = read_csharp(input_path)
                if decoded is None:
                    shutil.copy2(input_path, destination_path)
                    record.update({"action": "copied", "reason": "unsupported text encoding"})
                    warnings.append(f"{relative}: copied without replacement (unsupported encoding)")
                else:
                    text, encoding = decoded
                    translated, changed = replace_csharp_identifiers(text, replacements)
                    write_csharp(destination_path, translated, encoding)
                    record.update({"action": "translated", "identifier_replacements": changed})
            else:
                shutil.copy2(input_path, destination_path)
                record["action"] = "copied"
            files.append(record)
        manifest = {
            "tool": TOOL,
            "source": str(source),
            "mapping": str(mapping),
            "mapping_entries": mapping_report,
            "files": files,
            "warnings": warnings,
            "statistics": {
                "mapping_entries": len(mapping_report),
                "files": len(files),
                "csharp_files": sum(item["source"].casefold().endswith(".cs") for item in files),
                "identifier_replacements": sum(item.get("identifier_replacements", 0) for item in files),
            },
            "limitations": [
                "Only C# identifier tokens are changed; comments and literal contents are preserved.",
                "Qualified mapping values become their terminal identifier component.",
                "This is a searchable mirror, not a promise that the transformed source compiles.",
            ],
        }
        (stage / MANIFEST).write_text(
            json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
        )
        publish_stage(stage, output)
        return manifest
    except Exception:
        shutil.rmtree(stage, ignore_errors=True)
        raise


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path, help="client Assembly-CSharp directory")
    parser.add_argument("--mapping", required=True, type=Path, help="ObfuscationTranslation file")
    parser.add_argument("--output", required=True, type=Path, help="mirror directory outside --source")
    return parser


def main() -> int:
    args = build_parser().parse_args()
    try:
        manifest = deobfuscate(args.source, args.mapping, args.output)
    except (OSError, ValueError, UnicodeError) as exc:
        raise SystemExit(f"deobfuscate_client_source: {exc}")
    stats = manifest["statistics"]
    print(f"wrote {args.output.resolve()} ({stats['files']} files, {stats['identifier_replacements']} replacements)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
