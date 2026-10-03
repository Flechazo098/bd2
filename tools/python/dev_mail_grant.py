#!/usr/bin/env python3
"""Local-only BD2 development mail and browser tool.

It provides development mail grants and loopback-only runtime settings without
reading or changing account state. Files are replaced atomically and consumed
by an explicitly configured local bd2server.

Example:
  python tools/python/dev_mail_grant.py serve `
    --game-data E:\\bd2\\dl\\GameData --game-data-version 20260923193640 `
    --output data\\dev\\mail-grants-spool.json

  python tools/python/dev_mail_grant.py grant `
    --output data\\dev\\currency-grants.json --identity test-grant-1 `
    --attachment 4:0:10000 --attachment 3:0:100
"""

from __future__ import annotations

import argparse
from contextlib import contextmanager
import errno
import html
import json
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import threading
import time
import uuid
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

# The grant command requires only the Python standard library. GameData's
# optional decryptor dependency is imported only by the browser's data readers.
sys.path.insert(0, str(Path(__file__).resolve().parent))


def _gamedata():
    import gamedata_db
    return gamedata_db


VERSION = "2.35.10"
MAX_INT32 = (1 << 31) - 1

# These are the local server's ItemDBInfo-backed ElementTypes.  Item names use
# distinct GameData text namespaces, so the owning namespace is part of this
# data-driven mapping instead of being guessed from numeric text IDs.
ITEM_SOURCES = (
    ("ResourceTable", 8, 4, 7, "资源", "NameTextTable"),
    ("FoodTable", 5, 7, 10, "料理", "NameTextTable"),
    ("CookingTable", 7, 3, 11, "烹饪配方", "NameTextTable"),
    ("RandomBoxTable", 9, 4, 7, "随机箱", "RandomBoxTextTable"),
    ("QuestItemTable", 13, 2, 5, "任务物品", "NameTextTable"),
    ("UseItemTable", 14, 3, 6, "使用物品", "NameTextTable"),
    ("CollectionTable", 17, 3, 7, "收藏品", "NameTextTable"),
    ("MyRoomItemTable", 27, 7, 17, "我的房间物品", "NameTextTable"),
    ("InstantUseItemTable", 29, 1, 3, "即时使用物品", "NameTextTable"),
)

# CurrencyTable.id is the EElementType. Only currencies whose durable wallet
# and mail-claim path are implemented by this server are offered. Their mail
# reward ID is zero; names still come from the current GameData rather than
# being embedded here.
MAIL_CURRENCY_TYPES = frozenset({2, 3, 4, 12, 20})

# The standalone grant CLI intentionally offers only the two audited draw
# ticket resources, rather than accepting arbitrary GameData item IDs.
MAIL_DRAW_TICKET_IDS = frozenset({1000, 1104})
MAIL_CONTENT_TICKET_ID = 450030
MAIL_ITEM_TYPES = frozenset({5, 7, 8, 9, 13, 14, 17, 27, 29})


def _varint(value: Any) -> int:
    if not isinstance(value, int) or value < 0:
        raise ValueError("expected a non-negative protobuf varint")
    return value


def fields(proto: bytes) -> dict[int, list[Any]]:
    result: dict[int, list[Any]] = {}
    for number, wire_type, value in _gamedata().walk_wire(proto):
        if wire_type != 0 and wire_type != 2:
            continue
        result.setdefault(number, []).append(value)
    return result


def first_varint(values: dict[int, list[Any]], number: int) -> int:
    entries = values.get(number, [])
    if len(entries) != 1:
        return 0
    return _varint(entries[0])


def first_text(values: dict[int, list[Any]], number: int) -> str:
    entries = values.get(number, [])
    if len(entries) != 1 or not isinstance(entries[0], bytes):
        return ""
    return entries[0].decode("utf-8")


def packed_varints(values: dict[int, list[Any]], number: int) -> list[int]:
    """Decode proto3 packed/repeated uint fields without guessing their shape."""
    result: list[int] = []
    for entry in values.get(number, []):
        if isinstance(entry, int):
            result.append(_varint(entry))
            continue
        if not isinstance(entry, bytes):
            raise ValueError(f"field {number} is not a protobuf varint")
        offset = 0
        while offset < len(entry):
            value = 0
            for shift in range(0, 70, 7):
                if offset >= len(entry):
                    raise ValueError(f"truncated packed protobuf field {number}")
                byte = entry[offset]
                offset += 1
                value |= (byte & 0x7f) << shift
                if byte < 0x80:
                    result.append(value)
                    break
            else:
                raise ValueError(f"oversized packed protobuf field {number}")
    return result


def open_readonly_database(root: Path, version: str) -> tuple[sqlite3.Connection, Path]:
    """Open the current common database in a private read-only SQLite file."""
    plain = _gamedata().read_database(root, version, "quest")
    handle = tempfile.NamedTemporaryFile(prefix="bd2-dev-mail-", suffix=".db", delete=False)
    path = Path(handle.name)
    try:
        handle.write(plain)
        handle.flush()
    finally:
        handle.close()
    try:
        connection = sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True)
        connection.execute("PRAGMA query_only=ON")
        return connection, path
    except Exception:
        path.unlink(missing_ok=True)
        raise


def _usable_display_name(value: str) -> bool:
    return bool(value) and not value.startswith("<未找到本地化文本 #") and value not in {"（不使用）", "(不使用)"}


def _localized_names(connection: sqlite3.Connection, table: str) -> dict[int, str]:
    """Read the common localized-text protobuf shape from its owning table."""
    result: dict[int, str] = {}
    for row_id, proto in connection.execute(f'SELECT id, ProtoBuf FROM "{table}"'):
        decoded = fields(proto)
        text_id = first_varint(decoded, 2) or int(row_id)
        name = first_text(decoded, 4) or first_text(decoded, 5) or first_text(decoded, 3)
        if name:
            result[text_id] = name
    return result


def _static_items(connection: sqlite3.Connection) -> list[dict[str, Any]]:
    """Load ItemDBInfo tables through their declared text namespaces."""
    text_tables = {source[5] for source in ITEM_SOURCES}
    localized_names = {
        table: _localized_names(connection, table)
        for table in text_tables
    }
    items: list[dict[str, Any]] = []
    for table, element_type, id_field, name_field, category, text_table in ITEM_SOURCES:
        text_names = localized_names[text_table]
        for row_id, proto in connection.execute(f'SELECT id, ProtoBuf FROM "{table}" ORDER BY id'):
            decoded = fields(proto)
            item_id = first_varint(decoded, id_field) or int(row_id)
            name_text_id = first_varint(decoded, name_field)
            items.append({
                "id": item_id,
                "element_type": element_type,
                "name": text_names.get(name_text_id, f"<未找到本地化文本 #{name_text_id}>"),
                "category": category,
                "source_table": table,
                "name_text_id": name_text_id,
                "resource_type": first_varint(decoded, 13) if table == "ResourceTable" else None,
            })
    return items


def _mail_currencies(connection: sqlite3.Connection) -> list[dict[str, Any]]:
    """Load server-supported account currencies from CurrencyTable."""
    names = _localized_names(connection, "NameTextTable")
    result: list[dict[str, Any]] = []
    for row_id, proto in connection.execute('SELECT id, ProtoBuf FROM "CurrencyTable" ORDER BY id'):
        decoded = fields(proto)
        element_type = first_varint(decoded, 3) or int(row_id)
        if element_type not in MAIL_CURRENCY_TYPES:
            continue
        name_text_id = first_varint(decoded, 5)
        result.append({
            "id": 0,
            "element_type": element_type,
            "name": names.get(name_text_id, f"<未找到本地化文本 #{name_text_id}>"),
            "category": "货币（直接入账）",
            "source_table": "CurrencyTable",
            "name_text_id": name_text_id,
            "resource_type": None,
            "details": "邮件领取后直接叠加到账户余额，不生成背包物品或随机箱",
        })
    return result


def load_inventory_limits(root: Path, version: str) -> dict[str, dict[str, int]]:
    connection, temporary = open_readonly_database(root, version)
    try:
        row = connection.execute('SELECT ProtoBuf FROM "GameDefaultTable" WHERE id=0').fetchone()
        if row is None:
            raise ValueError("GameDefaultTable[0] 不存在")
        decoded = fields(row[0])
        result = {
            "baseline": {"items": first_varint(decoded, 34), "equipment": first_varint(decoded, 30)},
            "enabled_limits": {"items": first_varint(decoded, 80), "equipment": first_varint(decoded, 73)},
        }
        if any(value <= 0 for group in result.values() for value in group.values()):
            raise ValueError("GameData 背包容量配置无效")
        if result["baseline"]["items"] > result["enabled_limits"]["items"] or result["baseline"]["equipment"] > result["enabled_limits"]["equipment"]:
            raise ValueError("GameData 背包默认容量超过最大值")
        return result
    finally:
        connection.close()
        temporary.unlink(missing_ok=True)


def _safe_direct_mail_item(item: dict[str, Any]) -> bool:
    # RandomBox requires a second protocol and its entered count is not the
    # final reward count, so this direct-mail form never exposes type 9.
    if item["element_type"] == 9:
        return False
    # ResourceTable Type=2 rows are field-object presentation sentinels, not
    # inventory materials. 90045/90046 even point at costume IDs for their
    # field popup and crash ItemInfoPopupUI when presented as normal resources.
    if item.get("source_table") == "ResourceTable" and item.get("resource_type") == 2:
        return False
    return _usable_display_name(item["name"])


def map_fixed_boxes_to_direct_items(
    items: list[dict[str, Any]],
    fixed_boxes: dict[int, tuple[int, int, int]],
    product_aliases: dict[int, dict[str, int]],
) -> list[dict[str, Any]]:
    """Hide deterministic boxes and expose their contained ItemDBInfo directly.

    The quantity entered in the development form is the final material count;
    the source box multiplier is deliberately informational and is not applied.
    """
    by_key = {(item["element_type"], item["id"]): item for item in items}
    target_aliases: dict[tuple[int, int], dict[str, int]] = {}
    box_aliases: dict[tuple[int, int], dict[str, int]] = {}
    source_boxes: dict[tuple[int, int], list[tuple[int, int]]] = {}
    for box_id, (reward_type, reward_id, reward_count) in fixed_boxes.items():
        target_key = (reward_type, reward_id)
        target = by_key.get(target_key)
        box = by_key.get((9, box_id))
        if target is None or box is None:
            continue
        source_boxes.setdefault(target_key, []).append((box_id, reward_count))
        if _usable_display_name(box["name"]):
            aliases = box_aliases.setdefault(target_key, {})
            aliases[box["name"]] = aliases.get(box["name"], 0) + 1
        aliases = target_aliases.setdefault(target_key, {})
        for alias, frequency in product_aliases.get(box_id, {}).items():
            if _usable_display_name(alias):
                aliases[alias] = aliases.get(alias, 0) + frequency

    for target_key, boxes in source_boxes.items():
        target = by_key[target_key]
        original_name = target["name"]
        aliases = target_aliases.get(target_key, {})
        canonical = max(aliases, key=lambda value: (aliases[value], -len(value), value)) if aliases else ""
        box_names = box_aliases.get(target_key, {})
        # When every named deterministic wrapper agrees on one material name,
        # that name is the most specific GameData label for an otherwise valid
        # generic resource row. Keep the generic table name searchable as an
        # alias. If the resource itself has no usable name, retain the product
        # fallback below because a lone wrapper can still have a generic label.
        box_name = next(iter(box_names)) if len(box_names) == 1 else ""
        if _usable_display_name(original_name) and box_name and box_name != original_name:
            target["name"] = box_name
        elif not _usable_display_name(original_name):
            if canonical:
                target["name"] = canonical
        # Deterministic RandomBox names come from RandomBoxTextTable and describe
        # the actual contained material.  Product labels remain a secondary
        # fallback because some products describe expiry/conversion behavior.
        target["aliases"] = []
        for value in (original_name, box_name, canonical):
            if _usable_display_name(value) and value != target["name"] and value not in target["aliases"]:
                target["aliases"].append(value)
        preview = "、".join(str(box_id) for box_id, _ in boxes[:4])
        if len(boxes) > 4:
            preview += f" 等 {len(boxes)} 个"
        details = ["开发邮件直接发放此物品（无需开箱）"]
        if original_name != target["name"] and _usable_display_name(original_name):
            details.append("原始资源名：" + original_name)
        if box_name and box_name != target["name"]:
            details.append("确定性箱名称：" + box_name)
        if target["aliases"]:
            if canonical and canonical != target["name"]:
                details.append("商品名：" + canonical)
        details.append("固定箱映射：" + preview)
        target["details"] = "；".join(details)

    return [item for item in items if _safe_direct_mail_item(item)]


def load_items(root: Path, version: str) -> list[dict[str, Any]]:
    """Return every safe ItemDBInfo-backed static item, with Chinese names."""
    connection, temporary = open_readonly_database(root, version)
    try:
        items = _static_items(connection)
        # CashProductTable.ProductLocalTextId is intentionally a LocalTextTable
        # reference, unlike the ItemNameTextId fields handled above.
        local_names = _localized_names(connection, "LocalTextTable")
        # Supported account currencies are discovered from CurrencyTable. The
        # mail protocol identifies them by ElementType with reward ID zero.
        items.extend(_mail_currencies(connection))
        if not items:
            raise ValueError("可领取的 ItemDBInfo 静态表为空")

        # A RandomBox is itself a legitimate ItemDBInfo attachment, but its
        # visible name is often a generic box name while players search for a
        # guaranteed material inside it (for example 女神之泪).  Follow only
        # one-entry RewardGroupTable definitions: that is a real, deterministic
        # GameData relationship, not an invented unpack result.  Cash-product
        # display names are aliases as well, so names such as 光明圣石 which are
        # used by a product but not by the ResourceTable row remain searchable.
        by_key = {(item["element_type"], item["id"]): item for item in items}
        groups: dict[int, tuple[int, int, int] | None] = {}
        for group_id, proto in connection.execute("SELECT id, ProtoBuf FROM RewardGroupTable"):
            decoded = fields(proto)
            reward_ids = packed_varints(decoded, 5)
            reward_types = packed_varints(decoded, 6)
            reward_counts = packed_varints(decoded, 4)
            if len(reward_ids) != 1 or len(reward_types) != 1 or len(reward_counts) != 1:
                groups[int(group_id)] = None
                continue
            reward_id, reward_type, reward_count = reward_ids[0], reward_types[0], reward_counts[0]
            valid_id = reward_id == 0 if reward_type in MAIL_CURRENCY_TYPES else reward_id != 0
            groups[int(group_id)] = (reward_type, reward_id, reward_count) if reward_type and valid_id and reward_count else None

        fixed_boxes: dict[int, tuple[int, int, int]] = {}
        for box_id, proto in connection.execute("SELECT id, ProtoBuf FROM RandomBoxTable"):
            decoded = fields(proto)
            reward_group_id = first_varint(decoded, 9)
            reward = groups.get(reward_group_id)
            if reward is None:
                continue
            reward_type, reward_id, reward_count = reward
            contained = by_key.get((reward_type, reward_id))
            if contained is None:
                continue
            fixed_boxes[int(box_id)] = (reward_type, reward_id, reward_count)

        product_aliases: dict[int, dict[str, int]] = {}
        for (proto,) in connection.execute("SELECT ProtoBuf FROM CashProductTable"):
            decoded = fields(proto)
            box_id = first_varint(decoded, 14)
            product_text_id = first_varint(decoded, 11)
            product_name = local_names.get(product_text_id, "")
            if box_id and product_name and box_id in fixed_boxes:
                aliases = product_aliases.setdefault(box_id, {})
                aliases[product_name] = aliases.get(product_name, 0) + 1

        # A deterministic type-9 wrapper is unsuitable for this developer
        # mailbox: the user wants the material count they entered, immediately
        # usable after MailOpen. Replace such choices with their authoritative
        # contained ItemDBInfo rather than requiring /UseRandomBox afterwards.
        return map_fixed_boxes_to_direct_items(items, fixed_boxes, product_aliases)
    finally:
        connection.close()
        temporary.unlink(missing_ok=True)


def atomic_json(path: Path, value: dict[str, Any]) -> None:
    path = path.resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, name = tempfile.mkstemp(prefix=f".{path.name}.", suffix=".tmp", dir=path.parent)
    temporary = Path(name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as stream:
            json.dump(value, stream, ensure_ascii=False, indent=2)
            stream.write("\n")
            stream.flush()
            os.fsync(stream.fileno())
        for attempt in range(20):
            try:
                os.replace(temporary, path)
                break
            except PermissionError:
                # Windows virus scanners and indexers can briefly open the
                # destination without delete sharing. The grant-file lock
                # already serializes writers, so retry only this transient OS
                # condition and never fall back to a non-atomic overwrite.
                if os.name != "nt" or attempt == 19:
                    raise
                time.sleep(0.025 * (attempt + 1))
    finally:
        temporary.unlink(missing_ok=True)


@contextmanager
def grant_file_lock(path: Path):
    """Serialize each complete read/append/replace across CLI processes.

    Keep the sidecar lock file: deleting it would allow another process to lock
    a different file while a waiting process still owns the original inode.
    """
    path = path.resolve()
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.with_name(path.name + ".lock").open("a+b") as stream:
        if os.name == "nt":
            import msvcrt
            stream.seek(0, os.SEEK_END)
            if stream.tell() == 0:
                stream.write(b"\0")
                stream.flush()
            while True:
                stream.seek(0)
                try:
                    msvcrt.locking(stream.fileno(), msvcrt.LK_NBLCK, 1)
                    break
                except OSError as exc:
                    if exc.errno not in {errno.EACCES, errno.EAGAIN, errno.EDEADLK}:
                        raise
                    time.sleep(0.05)
            try:
                yield
            finally:
                stream.seek(0)
                msvcrt.locking(stream.fileno(), msvcrt.LK_UNLCK, 1)
        else:
            import fcntl
            fcntl.flock(stream.fileno(), fcntl.LOCK_EX)
            try:
                yield
            finally:
                fcntl.flock(stream.fileno(), fcntl.LOCK_UN)


def _validate_reward(value: Any) -> dict[str, int]:
    if not isinstance(value, dict) or set(value) != {"type", "id", "count"}:
        raise ValueError("附件必须只包含 type、id、count")
    if type(value["type"]) is not int or type(value["id"]) is not int:
        raise ValueError("附件 type 和 id 必须是整数")
    if value["type"] in MAIL_CURRENCY_TYPES:
        if value["id"] != 0:
            raise ValueError("货币附件 id 必须为 0")
    elif value["type"] == 19:
        if value["id"] != MAIL_CONTENT_TICKET_ID or type(value["count"]) is not int or value["count"] != 1:
            raise ValueError("内容券附件只允许满月甄选券 type19、id450030、count1")
    elif value["type"] not in MAIL_ITEM_TYPES or value["id"] <= 0:
        raise ValueError("物品附件必须使用已支持的 ItemDBInfo 类型和正数 id")
    if type(value["count"]) is not int or not 1 <= value["count"] <= MAX_INT32:
        raise ValueError(f"附件 count 必须是 1 到 {MAX_INT32} 的整数")
    return dict(value)


def attachment(value: str) -> dict[str, int]:
    try:
        parts = value.split(":")
        if len(parts) != 3:
            raise ValueError("附件格式必须是 TYPE:ID:COUNT")
        reward = _validate_reward(dict(zip(("type", "id", "count"), map(int, parts))))
        if reward["type"] not in MAIL_CURRENCY_TYPES and not (
            reward["type"] == 8 and reward["id"] in MAIL_DRAW_TICKET_IDS
        ) and not (reward["type"] == 19 and reward["id"] == MAIL_CONTENT_TICKET_ID):
            raise ValueError("命令行附件只开放已审计的货币、抽抽乐券和满月甄选券")
        return reward
    except ValueError as exc:
        raise argparse.ArgumentTypeError(str(exc)) from exc


def _validate_grant(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict) or set(value) != {"identity", "title", "body", "sent_at", "rewards"}:
        raise ValueError("发放记录必须只包含 identity、title、body、sent_at、rewards")
    identity, title, body = value["identity"], value["title"], value["body"]
    if not isinstance(identity, str) or not identity.strip() or len(identity) > 500:
        raise ValueError("identity 不能为空且不超过 500 字符")
    if not isinstance(title, str) or not title.strip() or len(title) > 500:
        raise ValueError("标题不能为空且不超过 500 字符")
    if not isinstance(body, str) or not body.strip() or len(body) > 5000:
        raise ValueError("正文不能为空且不超过 5000 字符")
    if type(value["sent_at"]) is not int or not 1 <= value["sent_at"] <= (1 << 63) - 1:
        raise ValueError("sent_at 必须是正 int64 毫秒时间戳")
    if not isinstance(value["rewards"], list) or not value["rewards"]:
        raise ValueError("至少需要一个附件")
    return {**value, "rewards": [_validate_reward(reward) for reward in value["rewards"]]}


def load_grants(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {"version": 1, "grants": []}
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError(f"无法读取邮件发放文件 {path}: {exc}") from exc
    if not isinstance(value, dict) or set(value) != {"version", "grants"} or type(value["version"]) is not int or value["version"] != 1 or not isinstance(value["grants"], list):
        raise ValueError("邮件发放文件必须只包含 version=1 和 grants 数组")
    grants = [_validate_grant(entry) for entry in value["grants"]]
    identities = [entry["identity"] for entry in grants]
    if len(set(identities)) != len(identities):
        raise ValueError("邮件发放文件包含重复 identity")
    return {"version": 1, "grants": grants}


def grant(args: argparse.Namespace) -> int:
    entry = _validate_grant({
        "identity": args.identity.strip() if args.identity is not None else str(uuid.uuid4()),
        "title": args.title.strip(),
        "body": args.body.strip(),
        "sent_at": time.time_ns() // 1_000_000,
        "rewards": args.attachment,
    })
    output = args.output.resolve()
    with grant_file_lock(output):
        value = load_grants(output)
        existing = next((item for item in value["grants"] if item["identity"] == entry["identity"]), None)
        if existing is not None:
            if any(existing[key] != entry[key] for key in ("title", "body", "rewards")):
                raise ValueError(f"identity {entry['identity']!r} 已存在且内容不同")
            entry, created = existing, False
        else:
            value["grants"].append(entry)
            atomic_json(output, value)
            created = True
    print(json.dumps({"grant": entry, "output": str(output), "created": created}, ensure_ascii=False))
    return 0


class MailGrantStore:
    def __init__(self, output: Path, items: list[dict[str, Any]]):
        self.output = output.resolve()
        self.items = items
        self.item_keys = {(item["element_type"], item["id"]) for item in items}
        self.lock = threading.Lock()
        if not self.output.exists():
            atomic_json(self.output, {"version": 1, "grants": []})

    def grant(self, payload: Any) -> dict[str, Any]:
        with self.lock:
            return self._grant_locked(payload)

    def _grant_locked(self, payload: Any) -> dict[str, Any]:
        if not isinstance(payload, dict):
            raise ValueError("请求必须是 JSON 对象")
        item_id = payload.get("item_id")
        element_type = payload.get("element_type")
        count = payload.get("count")
        if not isinstance(item_id, int) or not isinstance(element_type, int) or (element_type, item_id) not in self.item_keys:
            raise ValueError("element_type 与 item_id 必须是当前直接邮件列表中的安全组合")
        if not isinstance(count, int) or not 1 <= count <= MAX_INT32:
            raise ValueError(f"数量必须是 1 到 {MAX_INT32}")
        title = payload.get("title", "开发测试物品")
        body = payload.get("body", "由本地开发邮件工具发放。")
        if not isinstance(title, str) or not isinstance(body, str):
            raise ValueError("标题和正文必须是字符串")
        title, body = title.strip(), body.strip()
        if not title or len(title) > 500 or len(body) > 5000:
            raise ValueError("标题不能为空且不超过 500 字符；正文不超过 5000 字符")

        now = int(time.time() * 1000)
        entry = _validate_grant({
            "identity": str(uuid.uuid4()),
            "title": title,
            "body": body,
            "sent_at": now,
            "rewards": [{"type": element_type, "id": item_id, "count": count}],
        })
        with grant_file_lock(self.output):
            value = load_grants(self.output)
            value["grants"].append(entry)
            atomic_json(self.output, value)
        return {"grant": entry, "output": str(self.output), "restart_required": False}


class DevelopmentSettingsStore:
    def __init__(self, path: Path, limits: dict[str, dict[str, int]]):
        self.path = path.resolve()
        self.limits = limits
        self.lock = threading.Lock()
        if self.path.exists():
            self.settings = self._load()
        else:
            self.settings = {"version": 1, "inventory": {"unlimited": False}}
            atomic_json(self.path, self.settings)

    @staticmethod
    def _validate(value: Any) -> dict[str, Any]:
        if not isinstance(value, dict) or set(value) != {"version", "inventory"} or value.get("version") != 1:
            raise ValueError("开发工具配置必须是 version=1 的严格对象")
        inventory = value.get("inventory")
        if not isinstance(inventory, dict) or set(inventory) != {"unlimited"} or type(inventory.get("unlimited")) is not bool:
            raise ValueError("inventory.unlimited 必须是布尔值且不能包含额外字段")
        return {"version": 1, "inventory": {"unlimited": inventory["unlimited"]}}

    def _load(self) -> dict[str, Any]:
        try:
            return self._validate(json.loads(self.path.read_text(encoding="utf-8")))
        except OSError as exc:
            raise ValueError(f"无法读取开发工具配置 {self.path}: {exc}") from exc
        except json.JSONDecodeError as exc:
            raise ValueError(f"开发工具配置不是 JSON: {exc}") from exc

    def snapshot(self) -> dict[str, Any]:
        with self.lock:
            return self._snapshot_locked()

    def _snapshot_locked(self) -> dict[str, Any]:
        return {
            "inventory": {
                "unlimited": self.settings["inventory"]["unlimited"],
                **self.limits,
                "effective_after": "next_login",
            },
            "output": str(self.path),
        }

    def set_inventory(self, payload: Any) -> dict[str, Any]:
        if not isinstance(payload, dict) or set(payload) != {"unlimited"} or type(payload.get("unlimited")) is not bool:
            raise ValueError("请求必须只包含布尔字段 unlimited")
        with self.lock:
            next_settings = {"version": 1, "inventory": {"unlimited": payload["unlimited"]}}
            atomic_json(self.path, next_settings)
            self.settings = next_settings
            return self._snapshot_locked()


PAGE = """<!doctype html><meta charset=utf-8><title>BD2 开发工具</title>
<style>body{font:14px system-ui;max-width:1060px;margin:2rem auto;padding:0 1rem}input,textarea,button{font:inherit;padding:.4rem}input{width:100%}input[type=checkbox]{width:auto;transform:scale(1.2);margin-right:.5rem}section{border-top:1px solid #ddd;margin-top:2rem;padding-top:1rem}table{border-collapse:collapse;width:100%;margin:0}th,td{border:1px solid #ccc;padding:.4rem;text-align:left}tr:hover{background:#f5f5f5}#status,#inventory-status{white-space:pre-wrap;margin:1rem 0}.small{color:#555}.pick{white-space:nowrap}#item-picker{margin:1rem 0;border:1px solid #ccc;border-radius:.35rem;padding:.55rem}#item-picker summary{cursor:pointer;font-weight:600}#item-picker[open] summary{margin-bottom:.75rem}.item-list{max-height:min(40vh,28rem);overflow:auto;border:1px solid #ccc;margin-top:1rem}.item-list thead th{position:sticky;top:0;background:#fff}.item-list table{min-width:760px}</style>
<h1>BD2 开发工具</h1><section><h2>开发邮件发放</h2><p class=small>只列出可由当前邮件链路直接领取的安全物品和货币。固定内容随机箱已映射成真实内容物；其他随机箱与“遗失物品”等内部哨兵不会显示。货币直接叠加到钱包。提交会原子追加到动态邮件队列，由服务端唯一分配邮件 ID；重新打开或刷新游戏邮箱即可热载，无需重启服务端。</p>
<details id=item-picker><summary>选择开发测试物品 <span id=count class=small></span></summary><label>搜索（ID、名称、类别、固定箱映射）<input id=q></label><div class=item-list><table><thead><tr><th>ID</th><th>类型</th><th>名称</th><th>类别/内容</th><th></th></tr></thead><tbody id=items></tbody></table></div></details>
<h3>发放一个附件</h3><form id=form><label>物品 ID<input id=item_id required readonly></label><input id=element_type required readonly type=hidden><label>数量（1–2147483647）<input id=quantity type=number min=1 max=2147483647 value=1 required></label><label>邮件标题<input id=title value="开发测试物品" required maxlength=500></label><label>正文<textarea id=body maxlength=5000>由本地开发工具发放。</textarea></label><p><button id=grant-submit>加入动态邮件队列</button></p></form><pre id=status></pre></section>
<section><h2>背包容量</h2><label><input id=unlimited-inventory type=checkbox>无限背包容量</label><p id=inventory-limits class=small></p><p class=small>使用当前客户端 GameData 的安全上限，不写入账号存档。切换后无需重启服务端，但必须重新登录客户端才会生效。</p><pre id=inventory-status></pre></section>
<script>let all=[],submitting=false;const $=id=>document.getElementById(id);function render(){let q=$('q').value.toLowerCase();let matches=all.filter(x=>(x.id+' '+x.element_type+' '+x.name+' '+x.category+' '+(x.aliases||[]).join(' ')+' '+(x.details||'')).toLowerCase().includes(q));let rows=matches.slice(0,500);$('count').textContent=`（匹配 ${matches.length} / ${all.length} 项；显示前 ${rows.length} 项）`; $('items').innerHTML=rows.map(x=>`<tr><td>${x.id}</td><td>${x.element_type}</td><td>${esc(x.name)}</td><td>${esc(x.category+(x.details?'：'+x.details:''))}</td><td class=pick><button onclick="pick(${x.element_type},${x.id})">选择</button></td></tr>`).join('')}function esc(s){let d=document.createElement('div');d.textContent=s;return d.innerHTML}function pick(t,id){$('item_id').value=id;$('element_type').value=t;$('item-picker').open=false;$('quantity').focus();$('form').scrollIntoView({block:'nearest',behavior:'smooth'})}function showSettings(x){$('unlimited-inventory').checked=x.inventory.unlimited;$('inventory-limits').textContent=`关闭时：道具 ${x.inventory.baseline.items}、装备 ${x.inventory.baseline.equipment}；开启时：道具 ${x.inventory.enabled_limits.items}、装备 ${x.inventory.enabled_limits.equipment}`}$('q').oninput=render;$('form').onsubmit=async e=>{e.preventDefault();if(submitting)return;submitting=true;$('grant-submit').disabled=true;let r=await fetch('/api/grants',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({item_id:+$('item_id').value,element_type:+$('element_type').value,count:+$('quantity').value,title:$('title').value,body:$('body').value})});let x=await r.json();$('status').textContent=r.ok?`已加入动态邮件队列，标识 ${x.grant.identity}。\n重新打开或刷新游戏邮箱即可看到并领取；服务端无需重启。\n货币会直接叠加，固定箱映射会直接发放内容物。\n队列文件：${x.output}`:x.error;submitting=false;$('grant-submit').disabled=false};$('unlimited-inventory').onchange=async e=>{let box=e.target,old=!box.checked;box.disabled=true;let r=await fetch('/api/settings/inventory',{method:'PUT',headers:{'content-type':'application/json'},body:JSON.stringify({unlimited:box.checked})});let x=await r.json();box.disabled=false;if(r.ok){showSettings(x);$('inventory-status').textContent='设置已保存。无需重启服务端；请重新登录客户端后生效。'}else{box.checked=old;$('inventory-status').textContent=x.error}};Promise.all([fetch('/api/items').then(r=>r.json()),fetch('/api/settings').then(r=>r.json())]).then(([x,s])=>{all=x.items;render();showSettings(s)});</script>"""


class Handler(BaseHTTPRequestHandler):
    store: MailGrantStore
    settings: DevelopmentSettingsStore

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/":
            self.reply(HTTPStatus.OK, "text/html; charset=utf-8", PAGE.encode())
        elif self.path == "/api/items":
            self.reply_json(HTTPStatus.OK, {"items": self.store.items})
        elif self.path == "/api/settings":
            self.reply_json(HTTPStatus.OK, self.settings.snapshot())
        else:
            self.reply_json(HTTPStatus.NOT_FOUND, {"error": "not found"})

    def do_POST(self) -> None:  # noqa: N802
        if self.path != "/api/grants":
            self.reply_json(HTTPStatus.NOT_FOUND, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > 32_768:
                raise ValueError("请求体大小无效")
            result = self.store.grant(json.loads(self.rfile.read(length)))
            self.reply_json(HTTPStatus.CREATED, result)
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            self.reply_json(HTTPStatus.BAD_REQUEST, {"error": str(exc)})

    def do_PUT(self) -> None:  # noqa: N802
        if self.path != "/api/settings/inventory":
            self.reply_json(HTTPStatus.NOT_FOUND, {"error": "not found"})
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length <= 0 or length > 1024:
                raise ValueError("请求体大小无效")
            result = self.settings.set_inventory(json.loads(self.rfile.read(length)))
            self.reply_json(HTTPStatus.OK, result)
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            self.reply_json(HTTPStatus.BAD_REQUEST, {"error": str(exc)})

    def reply(self, status: HTTPStatus, content_type: str, body: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        self.end_headers()
        self.wfile.write(body)

    def reply_json(self, status: HTTPStatus, value: dict[str, Any]) -> None:
        self.reply(status, "application/json; charset=utf-8", json.dumps(value, ensure_ascii=False).encode())

    def log_message(self, format: str, *args: object) -> None:
        print("dev-tools:", format % args)


def serve(args: argparse.Namespace) -> int:
    items = load_items(args.game_data, args.game_data_version)
    store = MailGrantStore(args.output, items)
    settings = DevelopmentSettingsStore(args.settings_output, load_inventory_limits(args.game_data, args.game_data_version))
    Handler.store = store
    Handler.settings = settings
    server = ThreadingHTTPServer((args.listen_host, args.listen_port), Handler)
    print(f"已读取 {len(items)} 个可由 ItemDBInfo 领取的 GameData 物品。")
    print(f"浏览器打开：http://{args.listen_host}:{args.listen_port}/")
    print(f"动态邮件队列：{store.output}")
    print(f"开发工具配置：{settings.path}")
    print("此服务不修改 data/state；bd2server 每次 /MailInfo 导入队列并唯一分配邮件 ID。")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nBD2 开发工具已停止。")
    finally:
        server.server_close()
    return 0


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)
    command = commands.add_parser("serve", help="start the loopback browser UI")
    command.add_argument("--game-data", type=Path, required=True, help="GameData root")
    command.add_argument("--game-data-version", required=True, help="validated GameData version")
    command.add_argument("--output", type=Path, required=True, help="version=1 dynamic mail grant spool")
    command.add_argument("--settings-output", type=Path, default=Path("data/dev/dev-tools.json"), help="development settings JSON")
    command.add_argument("--listen-host", default="127.0.0.1", help="loopback host (default: 127.0.0.1)")
    command.add_argument("--listen-port", default=8765, type=int, help="loopback port (default: 8765)")
    command.set_defaults(run=serve)
    command = commands.add_parser("grant", help="append one durable currency or draw ticket mail grant (standard library only)")
    command.add_argument("--output", type=Path, required=True, help="version=1 development mail grants JSON")
    command.add_argument("--attachment", type=attachment, action="append", required=True, metavar="TYPE:ID:COUNT", help="supported currency or draw ticket reward; repeat to include multiple attachments in one mail")
    command.add_argument("--identity", help="stable idempotency identity (default: a new UUID)")
    command.add_argument("--title", default="开发测试物品", help="mail title (maximum 500 characters)")
    command.add_argument("--body", default="由本地开发邮件工具发放。", help="mail body (maximum 5000 characters)")
    command.set_defaults(run=grant)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        if args.command == "serve" and args.listen_host not in {"127.0.0.1", "localhost", "::1"}:
            raise ValueError("开发工具只允许监听本机回环地址")
        return args.run(args)
    except (OSError, ValueError, sqlite3.Error) as exc:
        print(f"dev_tools: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
