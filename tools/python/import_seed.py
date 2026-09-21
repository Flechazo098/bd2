#!/usr/bin/env python3
"""Convert decoded 2.34.13 protobuf messages into reviewed JSON seeds.

This replaces the old Go cmd/import-* programs. Network/capture decryption is
kept separate: inputs here are raw, already-decoded protobuf files. Outputs
are semantic JSON used by the server and never contain HTTP envelopes.

Examples:
  py tools/import_seed.py login LoginUser.pb --packet-code 11 --output login_user.json
  py tools/import_seed.py starter --items ItemInfo.pb --costumes CostumeInfo.pb \
      --characters CharInfo.pb --output starter_player.json
  py tools/import_seed.py mail MailInfo.pb --output mail.json
  py tools/import_seed.py readonly responses.json --output readonly.json

readonly responses.json:
  {"/SkyWayScheduleInfo":{"packet_code":162,"protobuf":"SkyWayScheduleInfo.pb"}}
"""

from __future__ import annotations

import argparse
import base64
from dataclasses import dataclass
import json
import os
from pathlib import Path
import struct


VERSION = "2.34.13"


@dataclass(frozen=True)
class Field:
    number: int
    wire_type: int
    value: int | bytes
    start: int
    end: int


def read_varint(data: bytes, offset: int) -> tuple[int, int]:
    value = 0
    shift = 0
    for index in range(offset, len(data)):
        byte = data[index]
        value |= (byte & 0x7F) << shift
        if byte < 0x80:
            return value, index + 1
        shift += 7
        if shift >= 70:
            break
    raise ValueError(f"invalid protobuf varint at offset {offset}")


def fields(data: bytes) -> list[Field]:
    result: list[Field] = []
    offset = 0
    while offset < len(data):
        start = offset
        tag, offset = read_varint(data, offset)
        number, wire_type = tag >> 3, tag & 7
        if number <= 0:
            raise ValueError("protobuf field number zero")
        if wire_type == 0:
            value, offset = read_varint(data, offset)
        elif wire_type == 1:
            if offset + 8 > len(data):
                raise ValueError("truncated fixed64")
            value, offset = data[offset : offset + 8], offset + 8
        elif wire_type == 2:
            length, offset = read_varint(data, offset)
            if offset + length > len(data):
                raise ValueError("truncated bytes field")
            value, offset = data[offset : offset + length], offset + length
        elif wire_type == 5:
            if offset + 4 > len(data):
                raise ValueError("truncated fixed32")
            value, offset = data[offset : offset + 4], offset + 4
        else:
            raise ValueError(f"unsupported protobuf wire type {wire_type}")
        result.append(Field(number, wire_type, value, start, offset))
    return result


def encode_varint(value: int) -> bytes:
    if value < 0:
        value &= (1 << 64) - 1
    result = bytearray()
    while value >= 0x80:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value)
    return bytes(result)


def encode_field(number: int, wire_type: int, payload: int | bytes) -> bytes:
    output = bytearray(encode_varint(number << 3 | wire_type))
    if wire_type == 0:
        output.extend(encode_varint(int(payload)))
    elif wire_type == 1:
        output.extend(payload)
    elif wire_type == 2:
        output.extend(encode_varint(len(payload)))
        output.extend(payload)
    elif wire_type == 5:
        output.extend(payload)
    else:
        raise ValueError(f"cannot encode wire type {wire_type}")
    return bytes(output)


def packed_varints(data: bytes) -> list[int]:
    result = []
    offset = 0
    while offset < len(data):
        value, offset = read_varint(data, offset)
        result.append(value)
    return result


def decode_varint_message(data: bytes, mapping: dict[int, str]) -> dict:
    output = {}
    for field in fields(data):
        name = mapping.get(field.number)
        if name is None:
            raise ValueError(f"unsupported field {field.number}")
        if field.wire_type != 0:
            raise ValueError(f"field {field.number} is not a varint")
        if field.value:
            output[name] = field.value
    return output


def atomic_json(path: Path, value: dict, force: bool) -> None:
    path = path.resolve()
    if path.exists() and not force:
        raise FileExistsError(f"refusing to overwrite {path}; pass --force")
    path.parent.mkdir(parents=True, exist_ok=True)
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
    print(f"wrote {path}")


def import_login(args: argparse.Namespace) -> dict:
    proto = args.input.read_bytes()
    top = fields(proto)
    users = [field for field in top if field.number == 1]
    if len(users) != 1 or users[0].wire_type != 2:
        raise ValueError("LoginUser must contain exactly one UserDBInfo field")
    user = users[0].value
    stripped = b"".join(
        user[field.start : field.end]
        for field in fields(user)
        if not (field.number == 3 and field.wire_type == 2)
    )
    if not stripped:
        raise ValueError("UserDBInfo became empty after removing user_key")
    other = b"".join(
        proto[field.start : field.end] for field in top if field.number != 1
    )
    return {
        "version": VERSION,
        "packet_code": args.packet_code,
        "user_info_base64": base64.b64encode(stripped).decode("ascii"),
        "response_fields_base64": base64.b64encode(other).decode("ascii"),
    }


ITEM_FIELDS = {
    1: "inven_index", 2: "id", 3: "type", 4: "count", 5: "keep_flag",
    6: "time_value", 9: "sort_id", 10: "use_count",
}
COSTUME_FIELDS = {
    1: "inven_index", 2: "id", 3: "level", 4: "use_char", 6: "sort_id",
    8: "potential_id", 9: "design_id", 10: "burst_level", 12: "time_value",
}
CHARACTER_FIELDS = {
    1: "inven_index", 2: "id", 3: "hp", 4: "level", 5: "costume_id",
    6: "exp", 7: "use_costume", 8: "talent_level", 9: "talent_exp",
    10: "solidarity_reward", 11: "expiry_time", 13: "connect_potential_costume",
}


def repeated_messages(data: bytes, response_name: str, allow_mode: bool = False):
    messages = []
    mode = 0
    for field in fields(data):
        if field.number == 1 and field.wire_type == 2:
            messages.append(field.value)
        elif allow_mode and field.number == 2 and field.wire_type == 0:
            mode = int(field.value)
        else:
            raise ValueError(
                f"unexpected {response_name} response field {field.number}/{field.wire_type}"
            )
    return messages, mode


def import_starter(args: argparse.Namespace) -> dict:
    item_messages, _ = repeated_messages(args.items.read_bytes(), "ItemInfo")
    costume_messages, _ = repeated_messages(args.costumes.read_bytes(), "CostumeInfo")
    character_messages, mode = repeated_messages(
        args.characters.read_bytes(), "CharInfo", allow_mode=True
    )
    items = [decode_varint_message(value, ITEM_FIELDS) for value in item_messages]
    costumes = [decode_varint_message(value, COSTUME_FIELDS) for value in costume_messages]
    characters = [decode_varint_message(value, CHARACTER_FIELDS) for value in character_messages]
    for kind, entries in (("item", items), ("costume", costumes), ("character", characters)):
        for entry in entries:
            if not entry.get("id") or (kind != "item" and not entry.get("inven_index")):
                raise ValueError(f"invalid {kind}: {entry}")
            if kind == "item" and not entry.get("count"):
                raise ValueError(f"invalid item: {entry}")
    return {
        "version": VERSION,
        "items": items,
        "costumes": costumes,
        "characters": characters,
        "field_char_control_deck_type": mode,
    }


def import_mail_entry(data: bytes) -> dict:
    output = {}
    scalar = {1: "mail_id", 2: "mail_type", 3: "template_id", 7: "expires_at", 13: "sent_at"}
    text = {4: "sender", 5: "title", 6: "body"}
    packed = {8: "reward_types", 9: "reward_ids", 10: "reward_counts"}
    for field in fields(data):
        if field.number in scalar and field.wire_type == 0:
            if field.value:
                output[scalar[field.number]] = field.value
        elif field.number in text and field.wire_type == 2:
            value = field.value.decode("utf-8")
            if value:
                output[text[field.number]] = value
        elif field.number in packed and field.wire_type == 2:
            values = packed_varints(field.value)
            if values:
                output[packed[field.number]] = values
        else:
            raise ValueError(f"unsupported MailDBInfo field {field.number}/{field.wire_type}")
    reward_lengths = [len(output.get(name, [])) for name in packed.values()]
    if len(set(reward_lengths)) != 1:
        raise ValueError(f"mail reward arrays differ: {output}")
    if not output.get("mail_id") or not output.get("expires_at") or not output.get("sent_at"):
        raise ValueError(f"mail identity/time is incomplete: {output}")
    return output


def import_mail(args: argparse.Namespace) -> dict:
    mails = []
    mail_count = max_mail_id = 0
    for field in fields(args.input.read_bytes()):
        if field.number == 1 and field.wire_type == 2:
            mails.append(import_mail_entry(field.value))
        elif field.number == 2 and field.wire_type == 0:
            mail_count = int(field.value)
        elif field.number == 3 and field.wire_type == 0:
            max_mail_id = int(field.value)
        else:
            raise ValueError(f"unsupported MailInfo field {field.number}/{field.wire_type}")
    if mail_count != len(mails) + 1:
        raise ValueError("mail_count must include the server sentinel")
    return {"version": VERSION, "mails": mails, "mail_count": mail_count, "max_mail_id": max_mail_id}


def readonly_fields(data: bytes) -> list[dict]:
    output = []
    for field in fields(data):
        item = {"number": field.number, "type": field.wire_type}
        if field.wire_type == 0:
            if field.value:
                item["varint"] = field.value
        elif field.wire_type == 1:
            value = struct.unpack("<Q", field.value)[0]
            if value:
                item["fixed64_le"] = value
        elif field.wire_type == 5:
            value = struct.unpack("<I", field.value)[0]
            if value:
                item["fixed32_le"] = value
        else:
            if field.value:
                try:
                    nested = readonly_fields(field.value)
                    item["fields"] = nested
                except ValueError:
                    item["bytes"] = list(field.value)
        output.append(item)
    return output


def import_readonly(args: argparse.Namespace) -> dict:
    manifest_path = args.input.resolve()
    with manifest_path.open("r", encoding="utf-8") as stream:
        manifest = json.load(stream)
    responses = {}
    for path, description in manifest.items():
        if not path.startswith("/") or int(description["packet_code"]) < 0:
            raise ValueError(f"invalid readonly response {path!r}")
        proto_path = (manifest_path.parent / description["protobuf"]).resolve()
        responses[path] = {
            "packet_code": int(description["packet_code"]),
            "fields": readonly_fields(proto_path.read_bytes()),
        }
    if not responses:
        raise ValueError("readonly manifest is empty")
    return {"version": VERSION, "responses": responses}


def output_options(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--force", action="store_true")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    command = sub.add_parser("login")
    command.add_argument("input", type=Path)
    command.add_argument("--packet-code", required=True, type=int)
    output_options(command)
    command.set_defaults(importer=import_login)

    command = sub.add_parser("starter")
    command.add_argument("--items", required=True, type=Path)
    command.add_argument("--costumes", required=True, type=Path)
    command.add_argument("--characters", required=True, type=Path)
    output_options(command)
    command.set_defaults(importer=import_starter)

    command = sub.add_parser("mail")
    command.add_argument("input", type=Path)
    output_options(command)
    command.set_defaults(importer=import_mail)

    command = sub.add_parser("readonly")
    command.add_argument("input", type=Path, help="JSON mapping path to packet_code/protobuf")
    output_options(command)
    command.set_defaults(importer=import_readonly)
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        value = args.importer(args)
        atomic_json(args.output, value, args.force)
    except (OSError, ValueError, KeyError, json.JSONDecodeError) as exc:
        parser.exit(1, f"import_seed: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
