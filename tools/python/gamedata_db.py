#!/usr/bin/env python3
"""Read and inspect Brown Dust II 2.34.13 GameData without building Go.

The tool opens common-dbdata.bin as a ZIP, extracts one encrypted SQLite
member, decrypts each 4096-byte AES-CBC page independently, and exposes only
read-only SQLite operations.  It is intentionally outside the server runtime.

Examples:
  py tools/gamedata_db.py tables --db quest --match QuestTable
  py tools/gamedata_db.py schema --db quest --table QuestTable22
  py tools/gamedata_db.py row --db quest --table PackTable --id 21 --fields 45
  py tools/gamedata_db.py rewards --pack 21 --quest 38
  py tools/gamedata_db.py chain --start-pack 21
  py tools/gamedata_db.py sql --db pack21 --query "SELECT id FROM BattleDeckTable"
"""

from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import struct
import sys
import tempfile
import zipfile

try:
    from Crypto.Cipher import AES
except ImportError as exc:  # pragma: no cover - environment diagnostic
    raise SystemExit("PyCryptodome is required: py -m pip install pycryptodome") from exc


PAGE_SIZE = 4096
HEADER = b"SQLite format 3\x00"
PASSWORD_SOURCE = b"spdhdnlwmrpavmtm"
ITERATIONS = 2010
ARCHIVE_NAME = "common-dbdata.bin"
QUEST_ENTRY = "9F251C63BC72551C681EE75D328FA090D56E444B"


def derive_key() -> bytes:
    password = hashlib.sha1(PASSWORD_SOURCE).hexdigest().upper().encode("ascii")
    return hashlib.pbkdf2_hmac("sha1", password, HEADER, ITERATIONS, dklen=32)


def decrypt_pages(encrypted: bytes) -> bytes:
    if not encrypted or len(encrypted) % PAGE_SIZE:
        raise ValueError(
            f"encrypted database length {len(encrypted)} is not a non-zero "
            f"multiple of {PAGE_SIZE}"
        )
    key = derive_key()
    plain = bytearray(len(encrypted))
    for start in range(0, len(encrypted), PAGE_SIZE):
        cipher = AES.new(key, AES.MODE_CBC, iv=HEADER)
        plain[start : start + PAGE_SIZE] = cipher.decrypt(
            encrypted[start : start + PAGE_SIZE]
        )
    if not plain.startswith(HEADER):
        raise ValueError("decrypted member does not have a SQLite header")
    return bytes(plain)


def database_entry(database: str) -> str:
    if database == "quest":
        return QUEST_ENTRY
    if not database or any(char in database for char in "/\\."):
        raise ValueError(f"invalid logical database name: {database!r}")
    return hashlib.sha1(f"{database}_v1".encode()).hexdigest().upper()


def read_database(root: Path, version: str, database: str) -> bytes:
    archive = root / version / "release" / ARCHIVE_NAME
    entry_name = database_entry(database)
    with zipfile.ZipFile(archive, "r") as bundle:
        names = {name.upper(): name for name in bundle.namelist()}
        actual = names.get(entry_name.upper())
        if actual is None:
            raise FileNotFoundError(
                f"database {database!r} member {entry_name} is absent from {archive}"
            )
        return decrypt_pages(bundle.read(actual))


@contextlib.contextmanager
def open_database(args: argparse.Namespace):
    plain = read_database(Path(args.root), args.version, args.db)
    handle = tempfile.NamedTemporaryFile(prefix="bd2-gamedata-", suffix=".db", delete=False)
    path = Path(handle.name)
    try:
        handle.write(plain)
        handle.flush()
        handle.close()
        uri = path.resolve().as_uri() + "?mode=ro"
        connection = sqlite3.connect(uri, uri=True)
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA query_only=ON")
        try:
            yield connection
        finally:
            connection.close()
    finally:
        try:
            handle.close()
        except Exception:
            pass
        path.unlink(missing_ok=True)


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
    raise ValueError(f"invalid varint at offset {offset}")


def walk_wire(data: bytes):
    offset = 0
    while offset < len(data):
        tag, offset = read_varint(data, offset)
        number, wire_type = tag >> 3, tag & 7
        if number == 0:
            raise ValueError("protobuf field number zero")
        if wire_type == 0:
            value, offset = read_varint(data, offset)
        elif wire_type == 1:
            if offset + 8 > len(data):
                raise ValueError("truncated fixed64")
            value, offset = data[offset : offset + 8], offset + 8
        elif wire_type == 2:
            size, offset = read_varint(data, offset)
            if offset + size > len(data):
                raise ValueError("truncated bytes field")
            value, offset = data[offset : offset + size], offset + size
        elif wire_type == 5:
            if offset + 4 > len(data):
                raise ValueError("truncated fixed32")
            value, offset = data[offset : offset + 4], offset + 4
        else:
            raise ValueError(f"unsupported protobuf wire type {wire_type}")
        yield number, wire_type, value


def packed_varints(data: bytes) -> list[int] | None:
    result: list[int] = []
    offset = 0
    try:
        while offset < len(data):
            value, offset = read_varint(data, offset)
            result.append(value)
    except ValueError:
        return None
    return result


def wire_fields(data: bytes, selected: set[int] | None = None) -> dict[int, list[dict]]:
    result: dict[int, list[dict]] = {}
    for number, wire_type, value in walk_wire(data):
        if selected is not None and number not in selected:
            continue
        rendered: dict[str, object] = {"wire_type": wire_type}
        if wire_type == 0:
            rendered["varint"] = value
        elif wire_type == 1:
            rendered["hex"] = value.hex()
            rendered["fixed64"] = int.from_bytes(value, "little")
            rendered["double"] = struct.unpack("<d", value)[0]
        elif wire_type == 5:
            rendered["hex"] = value.hex()
            rendered["fixed32"] = int.from_bytes(value, "little")
            rendered["float"] = struct.unpack("<f", value)[0]
        else:
            rendered["length"] = len(value)
            rendered["hex"] = value.hex()
            packed = packed_varints(value)
            if packed is not None:
                rendered["packed_varints"] = packed
            try:
                text = value.decode("utf-8")
                if text.isprintable():
                    rendered["utf8"] = text
            except UnicodeDecodeError:
                pass
        result.setdefault(number, []).append(rendered)
    return result


def parse_field_selection(text: str | None) -> set[int] | None:
    if not text or text.lower() == "all":
        return None
    result: set[int] = set()
    for part in text.split(","):
        part = part.strip()
        if "-" in part:
            left, right = part.split("-", 1)
            result.update(range(int(left), int(right) + 1))
        else:
            result.add(int(part))
    if not result or min(result) <= 0:
        raise ValueError("protobuf fields must be positive integers")
    return result


def json_value(value):
    if isinstance(value, bytes):
        return {"length": len(value), "hex": value.hex()}
    return value


def print_rows(cursor: sqlite3.Cursor) -> None:
    rows = cursor.fetchall()
    output = [
        {key: json_value(row[key]) for key in row.keys()}
        for row in rows
    ]
    print(json.dumps(output, ensure_ascii=False, indent=2))


def cmd_tables(args: argparse.Namespace) -> None:
    with open_database(args) as db:
        pattern = f"%{args.match}%" if args.match else "%"
        rows = db.execute(
            "SELECT name, sql FROM sqlite_master "
            "WHERE type='table' AND name LIKE ? ORDER BY name",
            (pattern,),
        )
        print_rows(rows)


def quote_identifier(identifier: str) -> str:
    if not identifier or "\x00" in identifier:
        raise ValueError("invalid SQLite identifier")
    return '"' + identifier.replace('"', '""') + '"'


def cmd_schema(args: argparse.Namespace) -> None:
    with open_database(args) as db:
        table = quote_identifier(args.table)
        print_rows(db.execute(f"PRAGMA table_info({table})"))


def parse_parameter(value: str):
    try:
        return json.loads(value)
    except json.JSONDecodeError:
        return value


def cmd_sql(args: argparse.Namespace) -> None:
    first = args.query.lstrip().split(None, 1)[0].upper() if args.query.strip() else ""
    if first not in {"SELECT", "PRAGMA", "WITH", "EXPLAIN"}:
        raise ValueError("only read-only SELECT/PRAGMA/WITH/EXPLAIN queries are allowed")
    with open_database(args) as db:
        print_rows(db.execute(args.query, tuple(map(parse_parameter, args.param))))


def cmd_row(args: argparse.Namespace) -> None:
    fields = parse_field_selection(args.fields)
    with open_database(args) as db:
        table = quote_identifier(args.table)
        column = quote_identifier(args.id_column)
        rows = db.execute(
            f"SELECT * FROM {table} WHERE {column}=? ORDER BY rowid", (args.id,)
        ).fetchall()
        output = []
        for row in rows:
            rendered = {key: json_value(row[key]) for key in row.keys() if key != args.proto_column}
            proto = row[args.proto_column]
            if not isinstance(proto, bytes):
                raise ValueError(f"{args.table}.{args.proto_column} is not a blob")
            rendered[args.proto_column] = wire_fields(proto, fields)
            output.append(rendered)
        print(json.dumps(output, ensure_ascii=False, indent=2))


def scalar_packed(fields: dict[int, list[dict]], number: int) -> list[int]:
    values: list[int] = []
    for occurrence in fields.get(number, []):
        if "varint" in occurrence:
            values.append(int(occurrence["varint"]))
        else:
            values.extend(int(v) for v in occurrence.get("packed_varints", []))
    return values


def cmd_rewards(args: argparse.Namespace) -> None:
    table_name = f"QuestTable{args.pack}"
    with open_database(args) as db:
        rows = db.execute(
            f"SELECT id, ProtoBuf FROM {quote_identifier(table_name)} "
            "WHERE (? IS NULL OR id=?) ORDER BY id",
            (args.quest, args.quest),
        ).fetchall()
        output = []
        for row in rows:
            fields = wire_fields(row["ProtoBuf"])
            slots = []
            for slot in range(5):
                slots.append(
                    {
                        "slot": slot,
                        "types": scalar_packed(fields, 56 + slot),
                        "ids": scalar_packed(fields, 51 + slot),
                        "counts": scalar_packed(fields, 46 + slot),
                        "display_types": scalar_packed(fields, 24 + slot),
                        "display_ids": scalar_packed(fields, 19 + slot),
                        "display_counts": scalar_packed(fields, 14 + slot),
                    }
                )
            output.append({"pack": args.pack, "quest": row["id"], "rewards": slots})
        print(json.dumps(output, ensure_ascii=False, indent=2))


def cmd_chain(args: argparse.Namespace) -> None:
    with open_database(args) as db:
        current = args.start_pack
        visited: set[int] = set()
        output = []
        while current:
            if current in visited:
                raise ValueError(f"cyclic PackTable.NextPackId at {current}")
            visited.add(current)
            row = db.execute("SELECT ProtoBuf FROM PackTable WHERE id=?", (current,)).fetchone()
            if row is None:
                raise ValueError(f"PackTable {current} does not exist")
            fields = wire_fields(row["ProtoBuf"])
            next_values = scalar_packed(fields, 45)
            next_pack = next_values[0] if next_values else 0
            table = f"QuestTable{current}"
            count, minimum, maximum = db.execute(
                f"SELECT COUNT(*), MIN(id), MAX(id) FROM {quote_identifier(table)}"
            ).fetchone()
            output.append(
                {
                    "pack": current,
                    "next_pack": next_pack,
                    "quest_count": count,
                    "min_quest": minimum,
                    "max_quest": maximum,
                }
            )
            current = next_pack
            if len(visited) > args.limit:
                raise ValueError(f"pack chain exceeds safety limit {args.limit}")
        print(json.dumps(output, ensure_ascii=False, indent=2))


def cmd_extract(args: argparse.Namespace) -> None:
    target = Path(args.output).resolve()
    if target.exists() and not args.force:
        raise FileExistsError(f"refusing to overwrite {target}; pass --force")
    plain = read_database(Path(args.root), args.version, args.db)
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_name(f".{target.name}.{os.getpid()}.tmp")
    try:
        temporary.write_bytes(plain)
        os.replace(temporary, target)
    finally:
        temporary.unlink(missing_ok=True)
    print(f"wrote {target} ({len(plain)} bytes)")


def add_database_options(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--root", required=True, help="GameData root")
    parser.add_argument("--version", required=True, help="GameData version")
    parser.add_argument(
        "--db",
        default="quest",
        help="quest for the shared DB, or a logical DB name such as pack21",
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    command = sub.add_parser("tables", help="list tables and schemas")
    add_database_options(command)
    command.add_argument("--match", help="substring filter")
    command.set_defaults(handler=cmd_tables)

    command = sub.add_parser("schema", help="show PRAGMA table_info")
    add_database_options(command)
    command.add_argument("--table", required=True)
    command.set_defaults(handler=cmd_schema)

    command = sub.add_parser("sql", help="execute one read-only query")
    add_database_options(command)
    command.add_argument("--query", required=True)
    command.add_argument("--param", action="append", default=[], help="JSON or string parameter")
    command.set_defaults(handler=cmd_sql)

    command = sub.add_parser("row", help="show a row and decode its ProtoBuf blob")
    add_database_options(command)
    command.add_argument("--table", required=True)
    command.add_argument("--id", required=True, type=int)
    command.add_argument("--id-column", default="id")
    command.add_argument("--proto-column", default="ProtoBuf")
    command.add_argument("--fields", default="all", help="all, 1,3,5-9")
    command.set_defaults(handler=cmd_row)

    command = sub.add_parser("rewards", help="decode real QuestTable reward slots")
    add_database_options(command)
    command.set_defaults(db="quest")
    command.add_argument("--pack", required=True, type=int)
    command.add_argument("--quest", type=int, help="omit to list every quest")
    command.set_defaults(handler=cmd_rewards)

    command = sub.add_parser("chain", help="follow PackTable.NextPackId")
    add_database_options(command)
    command.set_defaults(db="quest")
    command.add_argument("--start-pack", required=True, type=int)
    command.add_argument("--limit", default=64, type=int)
    command.set_defaults(handler=cmd_chain)

    command = sub.add_parser("extract", help="write a decrypted SQLite file")
    add_database_options(command)
    command.add_argument("--output", required=True)
    command.add_argument("--force", action="store_true")
    command.set_defaults(handler=cmd_extract)
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    try:
        args.handler(args)
    except (OSError, ValueError, sqlite3.Error, zipfile.BadZipFile) as exc:
        parser.exit(1, f"gamedata_db: {exc}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
