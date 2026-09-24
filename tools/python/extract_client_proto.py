#!/usr/bin/env python3
"""Reconstruct .proto files from descriptors embedded in generated C#.

The client does not ship original .proto sources. Each *Reflection.cs embeds a
serialized FileDescriptorProto; this tool extracts those descriptors, writes a
lossless FileDescriptorSet, and renders readable .proto source files.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import shutil

from google.protobuf import descriptor_pb2

try:
    from .inherited_stage import create_inherited_stage
except ImportError:  # Direct script execution.
    from inherited_stage import create_inherited_stage


TOOL = "bd2.extract_client_proto"
MANIFEST = ".bd2-proto-extract-manifest.json"
DESCRIPTOR_SET = "client-descriptors.pb"
# Protobuf C# generator uses both string.Concat(new string[] {...}) and a
# direct literal/ordinary string concatenation depending on generator version.
REFLECTION_RE = re.compile(
    r"Convert\.FromBase64String\s*\((?P<body>.*?)\)\s*,\s*new\s+FileDescriptor",
    re.DOTALL,
)
STRING_RE = re.compile(r'"([A-Za-z0-9+/=\s]*)"')

SCALARS = {
    descriptor_pb2.FieldDescriptorProto.TYPE_DOUBLE: "double",
    descriptor_pb2.FieldDescriptorProto.TYPE_FLOAT: "float",
    descriptor_pb2.FieldDescriptorProto.TYPE_INT64: "int64",
    descriptor_pb2.FieldDescriptorProto.TYPE_UINT64: "uint64",
    descriptor_pb2.FieldDescriptorProto.TYPE_INT32: "int32",
    descriptor_pb2.FieldDescriptorProto.TYPE_FIXED64: "fixed64",
    descriptor_pb2.FieldDescriptorProto.TYPE_FIXED32: "fixed32",
    descriptor_pb2.FieldDescriptorProto.TYPE_BOOL: "bool",
    descriptor_pb2.FieldDescriptorProto.TYPE_STRING: "string",
    descriptor_pb2.FieldDescriptorProto.TYPE_GROUP: "group",
    descriptor_pb2.FieldDescriptorProto.TYPE_MESSAGE: "message",
    descriptor_pb2.FieldDescriptorProto.TYPE_BYTES: "bytes",
    descriptor_pb2.FieldDescriptorProto.TYPE_UINT32: "uint32",
    descriptor_pb2.FieldDescriptorProto.TYPE_ENUM: "enum",
    descriptor_pb2.FieldDescriptorProto.TYPE_SFIXED32: "sfixed32",
    descriptor_pb2.FieldDescriptorProto.TYPE_SFIXED64: "sfixed64",
    descriptor_pb2.FieldDescriptorProto.TYPE_SINT32: "sint32",
    descriptor_pb2.FieldDescriptorProto.TYPE_SINT64: "sint64",
}


def is_within(child: Path, parent: Path) -> bool:
    try:
        child.relative_to(parent)
        return True
    except ValueError:
        return False


def validate_paths(source: Path, output: Path) -> tuple[Path, Path]:
    source, output = source.resolve(), output.resolve()
    if not source.is_dir():
        raise ValueError(f"source is not a directory: {source}")
    if source == output or is_within(output, source) or is_within(source, output):
        raise ValueError("output must be outside, and not contain, source")
    return source, output


def is_managed(output: Path) -> bool:
    manifest = output / MANIFEST
    if not manifest.is_file():
        return False
    try:
        return json.loads(manifest.read_text(encoding="utf-8")).get("tool") == TOOL
    except (OSError, json.JSONDecodeError):
        return False


def stage_for(output: Path) -> Path:
    if output.exists() and not is_managed(output):
        raise FileExistsError(f"refusing to overwrite non-managed output directory: {output}")
    return create_inherited_stage(output)


def extract_descriptor(path: Path) -> descriptor_pb2.FileDescriptorProto | None:
    text = path.read_text(encoding="utf-8-sig")
    match = REFLECTION_RE.search(text)
    if match is None:
        return None
    encoded = "".join(piece.group(1) for piece in STRING_RE.finditer(match.group("body")))
    if not encoded:
        raise ValueError(f"reflection contains no descriptor Base64: {path}")
    descriptor = descriptor_pb2.FileDescriptorProto()
    descriptor.ParseFromString(base64.b64decode(encoded, validate=True))
    if not descriptor.name:
        raise ValueError(f"descriptor has no source name: {path}")
    return descriptor


def safe_descriptor_path(name: str) -> Path:
    pure = PurePosixPath(name.replace("\\", "/"))
    if pure.is_absolute() or not pure.parts or any(part in {"", ".", ".."} for part in pure.parts):
        raise ValueError(f"unsafe descriptor path: {name!r}")
    return Path(*pure.parts)


def quoted(value: str) -> str:
    return json.dumps(value, ensure_ascii=False)


def type_name(field: descriptor_pb2.FieldDescriptorProto) -> str:
    scalar = SCALARS.get(field.type)
    if scalar not in {"message", "enum", "group"}:
        if scalar is None:
            raise ValueError(f"unknown protobuf field type {field.type}")
        return scalar
    return field.type_name or scalar


def option_value(value) -> str:
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, str):
        return quoted(value)
    if hasattr(value, "name"):
        return value.name
    return str(value)


def rendered_options(options, allowed: set[str] | None = None) -> tuple[list[str], bool]:
    result = []
    known = options.__class__()
    for field, value in options.ListFields():
        if field.is_extension or field.name in {"uninterpreted_option", "features"}:
            continue
        if allowed is not None and field.name not in allowed:
            continue
        if field.is_repeated or field.message_type is not None:
            continue
        setattr(known, field.name, value)
        result.append(f"{field.name} = {option_value(value)}")
    return result, known.SerializeToString() != options.SerializeToString()


def inline_options(options, allowed: set[str] | None = None) -> tuple[str, bool]:
    values, incomplete = rendered_options(options, allowed)
    return (" [" + ", ".join(values) + "]" if values else ""), incomplete


def render_enum(enum, indent: str, warnings: list[str], path: str) -> list[str]:
    lines = [f"{indent}enum {enum.name} {{"]
    opts, incomplete = rendered_options(enum.options)
    for option in opts:
        lines.append(f"{indent}  option {option};")
    if incomplete:
        warnings.append(f"{path}: enum options retained only in descriptor set")
    for reserved in enum.reserved_range:
        end = reserved.end - 1
        lines.append(f"{indent}  reserved {reserved.start}{' to ' + str(end) if end != reserved.start else ''};")
    if enum.reserved_name:
        lines.append(f"{indent}  reserved " + ", ".join(quoted(v) for v in enum.reserved_name) + ";")
    for value in enum.value:
        options, missing = inline_options(value.options)
        if missing:
            warnings.append(f"{path}.{value.name}: enum value options retained only in descriptor set")
        lines.append(f"{indent}  {value.name} = {value.number}{options};")
    lines.append(f"{indent}}}")
    return lines


def map_entries(message) -> dict[str, object]:
    return {nested.name: nested for nested in message.nested_type if nested.options.map_entry}


def render_field(field, syntax: str, indent: str, maps: dict[str, object], warnings: list[str], path: str) -> str:
    target = field.type_name.rsplit(".", 1)[-1]
    if field.label == field.LABEL_REPEATED and target in maps:
        entry = maps[target]
        if len(entry.field) == 2:
            declaration = f"map<{type_name(entry.field[0])}, {type_name(entry.field[1])}>"
        else:
            declaration = f"repeated {type_name(field)}"
    else:
        label = ""
        if field.label == field.LABEL_REPEATED:
            label = "repeated "
        elif syntax != "proto3" and field.label == field.LABEL_REQUIRED:
            label = "required "
        elif syntax != "proto3" or field.proto3_optional:
            label = "optional "
        declaration = label + type_name(field)
    allowed = {"ctype", "packed", "jstype", "lazy", "deprecated", "weak", "unverified_lazy", "debug_redact", "retention"}
    values, missing = rendered_options(field.options, allowed)
    if field.default_value:
        values.insert(0, f"default = {quoted(field.default_value) if field.type in (field.TYPE_STRING, field.TYPE_BYTES) else field.default_value}")
    if field.json_name and field.json_name != field.name:
        values.append(f"json_name = {quoted(field.json_name)}")
    if missing:
        warnings.append(f"{path}: field options retained only in descriptor set")
    suffix = " [" + ", ".join(values) + "]" if values else ""
    return f"{indent}{declaration} {field.name} = {field.number}{suffix};"


def render_message(message, syntax: str, indent: str, warnings: list[str], path: str) -> list[str]:
    lines = [f"{indent}message {message.name} {{"]
    opts, incomplete = rendered_options(message.options, {"message_set_wire_format", "no_standard_descriptor_accessor", "deprecated"})
    for option in opts:
        lines.append(f"{indent}  option {option};")
    if incomplete and not message.options.map_entry:
        warnings.append(f"{path}: message options retained only in descriptor set")
    for reserved in message.reserved_range:
        end = reserved.end - 1
        lines.append(f"{indent}  reserved {reserved.start}{' to ' + str(end) if end != reserved.start else ''};")
    if message.reserved_name:
        lines.append(f"{indent}  reserved " + ", ".join(quoted(v) for v in message.reserved_name) + ";")
    for extension in message.extension_range:
        end = "max" if extension.end >= 536870912 else str(extension.end - 1)
        lines.append(f"{indent}  extensions {extension.start} to {end};")
    maps = map_entries(message)
    synthetic = {field.oneof_index for field in message.field if field.proto3_optional}
    regular_oneofs = {index for index in range(len(message.oneof_decl)) if index not in synthetic}
    for field in message.field:
        if field.HasField("oneof_index") and field.oneof_index in regular_oneofs:
            continue
        lines.append(render_field(field, syntax, indent + "  ", maps, warnings, f"{path}.{field.name}"))
    for index in sorted(regular_oneofs):
        oneof = message.oneof_decl[index]
        lines.append(f"{indent}  oneof {oneof.name} {{")
        for field in message.field:
            if field.HasField("oneof_index") and field.oneof_index == index:
                copy = descriptor_pb2.FieldDescriptorProto()
                copy.CopyFrom(field)
                copy.ClearField("oneof_index")
                copy.label = copy.LABEL_OPTIONAL
                lines.append(render_field(copy, "proto3", indent + "    ", maps, warnings, f"{path}.{field.name}"))
        lines.append(f"{indent}  }}")
    for enum in message.enum_type:
        lines.extend(render_enum(enum, indent + "  ", warnings, f"{path}.{enum.name}"))
    for nested in message.nested_type:
        if not nested.options.map_entry:
            lines.extend(render_message(nested, syntax, indent + "  ", warnings, f"{path}.{nested.name}"))
    lines.append(f"{indent}}}")
    return lines


def render_extensions(fields, syntax: str, warnings: list[str], path: str) -> list[str]:
    grouped: dict[str, list[object]] = {}
    for field in fields:
        grouped.setdefault(field.extendee, []).append(field)
    lines = []
    for extendee, entries in grouped.items():
        lines.append(f"extend {extendee} {{")
        for field in entries:
            lines.append(render_field(field, syntax, "  ", {}, warnings, f"{path}.{field.name}"))
        lines.append("}")
    return lines


def render_file(descriptor: descriptor_pb2.FileDescriptorProto) -> tuple[str, list[str]]:
    warnings: list[str] = []
    syntax = descriptor.syntax or "proto2"
    lines = [f'syntax = "{syntax}";', ""]
    if descriptor.package:
        lines += [f"package {descriptor.package};", ""]
    public = set(descriptor.public_dependency)
    weak = set(descriptor.weak_dependency)
    for index, dependency in enumerate(descriptor.dependency):
        qualifier = "public " if index in public else "weak " if index in weak else ""
        lines.append(f"import {qualifier}{quoted(dependency)};")
    if descriptor.dependency:
        lines.append("")
    options, incomplete = rendered_options(descriptor.options)
    for option in options:
        lines.append(f"option {option};")
    if incomplete:
        warnings.append(f"{descriptor.name}: file options retained only in descriptor set")
    if options:
        lines.append("")
    for enum in descriptor.enum_type:
        lines.extend(render_enum(enum, "", warnings, f"{descriptor.name}:{enum.name}"))
        lines.append("")
    for message in descriptor.message_type:
        lines.extend(render_message(message, syntax, "", warnings, f"{descriptor.name}:{message.name}"))
        lines.append("")
    lines.extend(render_extensions(descriptor.extension, syntax, warnings, descriptor.name))
    if descriptor.extension:
        lines.append("")
    for service in descriptor.service:
        lines.append(f"service {service.name} {{")
        for method in service.method:
            client = "stream " if method.client_streaming else ""
            server = "stream " if method.server_streaming else ""
            lines.append(f"  rpc {method.name} ({client}{method.input_type}) returns ({server}{method.output_type});")
        lines += ["}", ""]
    return "\n".join(lines).rstrip() + "\n", warnings


def reconstruct(source: Path, output: Path) -> dict:
    source, output = validate_paths(source, output)
    found: dict[str, tuple[descriptor_pb2.FileDescriptorProto, str]] = {}
    scan_warnings: list[str] = []
    for reflection in sorted(source.rglob("*Reflection.cs")):
        if not reflection.is_file() or "proto" not in {part.casefold() for part in reflection.parts}:
            continue
        descriptor = extract_descriptor(reflection)
        if descriptor is None:
            scan_warnings.append(f"{reflection.relative_to(source).as_posix()}: no embedded descriptor")
            continue
        relative = reflection.relative_to(source).as_posix()
        existing = found.get(descriptor.name)
        if existing is not None:
            if existing[0].SerializeToString() != descriptor.SerializeToString():
                raise ValueError(f"conflicting descriptors named {descriptor.name!r}")
            scan_warnings.append(f"{relative}: duplicate descriptor also in {existing[1]}")
            continue
        found[descriptor.name] = (descriptor, relative)
    if not found:
        raise ValueError("no embedded FileDescriptorProto values found")
    stage = stage_for(output)
    try:
        descriptor_set = descriptor_pb2.FileDescriptorSet()
        records = []
        warnings = list(scan_warnings)
        for name in sorted(found):
            descriptor, reflection = found[name]
            descriptor_set.file.add().CopyFrom(descriptor)
            relative = safe_descriptor_path(name)
            target = stage / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            rendered, file_warnings = render_file(descriptor)
            target.write_text(rendered, encoding="utf-8", newline="\n")
            warnings.extend(file_warnings)
            records.append({
                "path": relative.as_posix(),
                "package": descriptor.package,
                "syntax": descriptor.syntax or "proto2",
                "source_reflection": reflection,
                "messages": len(descriptor.message_type),
                "enums": len(descriptor.enum_type),
                "dependencies": list(descriptor.dependency),
                "descriptor_sha256": hashlib.sha256(descriptor.SerializeToString()).hexdigest(),
                "proto_sha256": hashlib.sha256(rendered.encode("utf-8")).hexdigest(),
            })
        descriptor_bytes = descriptor_set.SerializeToString()
        (stage / DESCRIPTOR_SET).write_bytes(descriptor_bytes)
        manifest = {
            "tool": TOOL,
            "source": str(source),
            "descriptor_set": DESCRIPTOR_SET,
            "descriptor_set_sha256": hashlib.sha256(descriptor_bytes).hexdigest(),
            "files": records,
            "warnings": warnings,
            "statistics": {
                "proto_files": len(records),
                "messages": sum(item["messages"] for item in records),
                "enums": sum(item["enums"] for item in records),
                "warnings": len(warnings),
            },
            "limitations": [
                "Generated source comments are unavailable in FileDescriptorProto.",
                "Options not representable by this renderer remain losslessly available in client-descriptors.pb.",
            ],
        }
        (stage / MANIFEST).write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        if output.exists():
            shutil.rmtree(output)
        stage.replace(output)
        return manifest
    except Exception:
        shutil.rmtree(stage, ignore_errors=True)
        raise


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path, help="client Assembly-CSharp directory")
    parser.add_argument("--output", required=True, type=Path, help="reconstructed Proto directory outside --source")
    return parser


def main() -> int:
    args = build_parser().parse_args()
    try:
        manifest = reconstruct(args.source, args.output)
    except (OSError, ValueError, UnicodeError) as exc:
        raise SystemExit(f"extract_client_proto: {exc}")
    stats = manifest["statistics"]
    print(f"wrote {args.output.resolve()} ({stats['proto_files']} .proto files, {stats['messages']} messages)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
