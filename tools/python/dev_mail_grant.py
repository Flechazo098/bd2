#!/usr/bin/env python3
"""Local-only browser tool for adding ItemDBInfo-compatible GameData items to mail.

This program deliberately is not part of bd2server.  It reads the selected
2.34.13 GameData archive and writes a complete replacement mail seed using an
atomic rename.  Start bd2server once with --mail-seed pointing at the same
--output; subsequent grants are hot-loaded by the normal /MailInfo request.
It never reads or changes data/state.

Example:
  python tools/python/dev_mail_grant.py serve `
    --game-data E:\\bd2\\dl\\GameData --game-data-version 20260910162539 `
    --mail-seed go\\seed\\v2_34_13\\mail.json --output data\\dev\\mail-grants.json
"""

from __future__ import annotations

import argparse
import html
import json
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import threading
import time
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

# gamedata_db is the repository's reviewed, read-only GameData decryptor.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from gamedata_db import read_database, walk_wire  # noqa: E402


VERSION = "2.34.13"
MAX_INT32 = (1 << 31) - 1

# These are the local server's ItemDBInfo-backed ElementTypes.  The mapping is
# checked against 2.34.13 DataManager.GetItemInfo; characters, equipment,
# costumes, and trophies use separate RewardDBInfoBundle fields and are not
# falsely offered by this tool.
ITEM_SOURCES = (
    ("ResourceTable", 8, 4, 7, "资源"),
    ("FoodTable", 5, 7, 10, "料理"),
    ("CookingTable", 7, 3, 11, "烹饪配方"),
    ("RandomBoxTable", 9, 4, 7, "随机箱"),
    ("QuestItemTable", 13, 2, 5, "任务物品"),
    ("UseItemTable", 14, 3, 6, "使用物品"),
    ("CollectionTable", 17, 3, 7, "收藏品"),
    ("MyRoomItemTable", 27, 7, 17, "我的房间物品"),
    ("InstantUseItemTable", 29, 1, 3, "即时使用物品"),
)


def _varint(value: Any) -> int:
    if not isinstance(value, int) or value < 0:
        raise ValueError("expected a non-negative protobuf varint")
    return value


def fields(proto: bytes) -> dict[int, list[Any]]:
    result: dict[int, list[Any]] = {}
    for number, wire_type, value in walk_wire(proto):
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
    plain = read_database(root, version, "quest")
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
    source_boxes: dict[tuple[int, int], list[tuple[int, int]]] = {}
    for box_id, (reward_type, reward_id, reward_count) in fixed_boxes.items():
        target_key = (reward_type, reward_id)
        target = by_key.get(target_key)
        box = by_key.get((9, box_id))
        if target is None or box is None:
            continue
        source_boxes.setdefault(target_key, []).append((box_id, reward_count))
        aliases = target_aliases.setdefault(target_key, {})
        for alias, frequency in product_aliases.get(box_id, {}).items():
            if _usable_display_name(alias):
                aliases[alias] = aliases.get(alias, 0) + frequency

    for target_key, aliases in target_aliases.items():
        target = by_key[target_key]
        canonical = max(aliases, key=lambda value: (aliases[value], -len(value), value)) if aliases else ""
        if not _usable_display_name(target["name"]):
            if canonical:
                target["name"] = canonical
        # Keep only the dominant authoritative product label. Minority product
        # names can describe expiry/conversion products (for example a ticket
        # which converts to 女神之泪) and must not pollute direct-item search.
        target["aliases"] = [canonical] if canonical and canonical != target["name"] else []
        boxes = source_boxes[target_key]
        preview = "、".join(str(box_id) for box_id, _ in boxes[:4])
        if len(boxes) > 4:
            preview += f" 等 {len(boxes)} 个"
        details = ["开发邮件直接发放此物品（无需开箱）"]
        if target["aliases"]:
            details.append("商品名：" + canonical)
        details.append("固定箱映射：" + preview)
        target["details"] = "；".join(details)

    return [item for item in items if _safe_direct_mail_item(item)]


def load_items(root: Path, version: str) -> list[dict[str, Any]]:
    """Return every safe ItemDBInfo-backed static item, with Chinese names."""
    connection, temporary = open_readonly_database(root, version)
    try:
        names: dict[int, str] = {}
        for row_id, proto in connection.execute("SELECT id, ProtoBuf FROM LocalTextTable"):
            decoded = fields(proto)
            # LocalTextTable: id=2, text_cn=4, text_en=5 (client descriptor).
            text_id = first_varint(decoded, 2) or int(row_id)
            name = first_text(decoded, 4) or first_text(decoded, 5) or first_text(decoded, 3)
            if name:
                names[text_id] = name

        items: list[dict[str, Any]] = []
        for table, element_type, id_field, name_field, category in ITEM_SOURCES:
            for row_id, proto in connection.execute(f"SELECT id, ProtoBuf FROM {table} ORDER BY id"):
                decoded = fields(proto)
                item_id = first_varint(decoded, id_field) or int(row_id)
                name_text_id = first_varint(decoded, name_field)
                items.append({
                    "id": item_id,
                    "element_type": element_type,
                    "name": names.get(name_text_id, f"<未找到本地化文本 #{name_text_id}>"),
                    "category": category,
                    "source_table": table,
                    "name_text_id": name_text_id,
                    "resource_type": first_varint(decoded, 13) if table == "ResourceTable" else None,
                })
        # Currency is not an ItemDBInfo row. MailOpen and RewardDBInfoBundle
        # represent it as type=Gold(4), id=0 and the entered count, which the
        # wallet atomically adds to the existing balance.
        items.append({
            "id": 0,
            "element_type": 4,
            "name": "金币",
            "category": "货币（直接入账）",
            "source_table": "Currency",
            "name_text_id": 0,
            "resource_type": None,
            "details": "邮件领取后直接叠加到金币余额，不生成背包物品或随机箱",
        })
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
            valid_id = reward_id == 0 if reward_type in {3, 4} else reward_id != 0
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
            product_name = names.get(product_text_id, "")
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


def load_seed(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise ValueError(f"无法读取邮件种子 {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise ValueError(f"邮件种子不是 JSON: {exc}") from exc
    if value.get("version") != VERSION or not isinstance(value.get("mails"), list):
        raise ValueError(f"邮件种子必须是 version={VERSION} 且含 mails 数组")
    ids: set[int] = set()
    for entry in value["mails"]:
        mail_id = entry.get("mail_id")
        if not isinstance(mail_id, int) or mail_id <= 0 or mail_id in ids:
            raise ValueError("邮件种子含零、非整数或重复的 mail_id")
        ids.add(mail_id)
    return value


def normalise_seed(seed: dict[str, Any]) -> dict[str, Any]:
    """Make the server sentinel fields agree with the complete mail list."""
    result = dict(seed)
    result["version"] = VERSION
    result["mails"] = list(seed["mails"])
    result["mail_count"] = len(result["mails"]) + 1
    result["max_mail_id"] = max((entry["mail_id"] for entry in result["mails"]), default=0)
    return result


def atomic_json(path: Path, value: dict[str, Any]) -> None:
    path = path.resolve()
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


class MailGrantStore:
    def __init__(self, source: Path, output: Path, items: list[dict[str, Any]], expires_days: int):
        self.source = source.resolve()
        self.output = output.resolve()
        self.items = items
        self.item_keys = {(item["element_type"], item["id"]) for item in items}
        self.expires_days = expires_days
        self.lock = threading.Lock()
        self.seed = normalise_seed(load_seed(self.output if self.output.exists() else self.source))
        # Write the complete baseline immediately. The game server can
        # therefore begin watching --output before the first browser grant.
        if not self.output.exists():
            atomic_json(self.output, self.seed)

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

        current_ids = {entry["mail_id"] for entry in self.seed["mails"]}
        mail_id = max(current_ids, default=13_000_000_000) + 1
        # MailDBInfo's InvenIndex is int64 in the 2.34.13 client descriptor.
        if mail_id > (1 << 63) - 1:
            raise ValueError("没有可用的正 int64 邮件 ID")
        now = int(time.time() * 1000)
        expires = now + self.expires_days * 24 * 60 * 60 * 1000
        entry = {
            "mail_id": mail_id,
            "mail_type": 2,
            "title": title,
            "body": body,
            "expires_at": expires,
            "reward_types": [element_type],
            "reward_ids": [item_id],
            "reward_counts": [count],
            "sent_at": now,
        }
        next_seed = normalise_seed({**self.seed, "mails": [*self.seed["mails"], entry]})
        atomic_json(self.output, next_seed)
        self.seed = next_seed
        return {"mail": entry, "output": str(self.output), "restart_required": False}


PAGE = """<!doctype html><meta charset=utf-8><title>BD2 开发邮件发放</title>
<style>body{font:14px system-ui;max-width:1060px;margin:2rem auto;padding:0 1rem}input,textarea,button{font:inherit;padding:.4rem}input{width:100%}table{border-collapse:collapse;width:100%;margin:0}th,td{border:1px solid #ccc;padding:.4rem;text-align:left}tr:hover{background:#f5f5f5}#status{white-space:pre-wrap;margin:1rem 0}.small{color:#555}.pick{white-space:nowrap}#item-picker{margin:1rem 0;border:1px solid #ccc;border-radius:.35rem;padding:.55rem}#item-picker summary{cursor:pointer;font-weight:600}#item-picker[open] summary{margin-bottom:.75rem}.item-list{max-height:min(40vh,28rem);overflow:auto;border:1px solid #ccc;margin-top:1rem}.item-list thead th{position:sticky;top:0;background:#fff}.item-list table{min-width:760px}</style>
<h1>BD2 开发邮件发放</h1><p class=small>只列出可由当前邮件链路直接领取的安全物品和货币。固定内容随机箱已映射成真实内容物；其他随机箱与“遗失物品”等内部哨兵不会显示。金币直接叠加到钱包。提交会原子写入临时邮件种子；重新打开或刷新游戏邮箱即可热载，无需重启服务端。</p>
<details id=item-picker><summary>选择开发测试物品 <span id=count class=small></span></summary><label>搜索（ID、名称、类别、固定箱映射）<input id=q></label><div class=item-list><table><thead><tr><th>ID</th><th>类型</th><th>名称</th><th>类别/内容</th><th></th></tr></thead><tbody id=items></tbody></table></div></details>
<h2>发放一个附件</h2><form id=form><label>物品 ID<input id=item_id required readonly></label><input id=element_type required readonly type=hidden><label>数量（1–2147483647）<input id=quantity type=number min=1 max=2147483647 value=1 required></label><label>邮件标题<input id=title value="开发测试物品" required maxlength=500></label><label>正文<textarea id=body maxlength=5000>由本地开发邮件工具发放。</textarea></label><p><button>写入临时邮件种子</button></p></form><pre id=status></pre>
<script>let all=[];const $=id=>document.getElementById(id);function render(){let q=$('q').value.toLowerCase();let matches=all.filter(x=>(x.id+' '+x.element_type+' '+x.name+' '+x.category+' '+(x.details||'')).toLowerCase().includes(q));let rows=matches.slice(0,500);$('count').textContent=`（匹配 ${matches.length} / ${all.length} 项；显示前 ${rows.length} 项）`; $('items').innerHTML=rows.map(x=>`<tr><td>${x.id}</td><td>${x.element_type}</td><td>${esc(x.name)}</td><td>${esc(x.category+(x.details?'：'+x.details:''))}</td><td class=pick><button onclick="pick(${x.element_type},${x.id})">选择</button></td></tr>`).join('')}function esc(s){let d=document.createElement('div');d.textContent=s;return d.innerHTML}function pick(t,id){$('item_id').value=id;$('element_type').value=t;$('item-picker').open=false;$('quantity').focus();$('form').scrollIntoView({block:'nearest',behavior:'smooth'})}$('q').oninput=render;$('form').onsubmit=async e=>{e.preventDefault();let r=await fetch('/api/grants',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({item_id:+$('item_id').value,element_type:+$('element_type').value,count:+$('quantity').value,title:$('title').value,body:$('body').value})});let x=await r.json();$('status').textContent=r.ok?`已写入邮件 #${x.mail.mail_id}。\n重新打开或刷新游戏邮箱即可看到并领取；服务端无需重启。\n货币会直接叠加，固定箱映射会直接发放内容物。\n热载文件：${x.output}`:x.error};fetch('/api/items').then(r=>r.json()).then(x=>{all=x.items;render()});</script>"""


class Handler(BaseHTTPRequestHandler):
    store: MailGrantStore

    def do_GET(self) -> None:  # noqa: N802
        if self.path == "/":
            self.reply(HTTPStatus.OK, "text/html; charset=utf-8", PAGE.encode())
        elif self.path == "/api/items":
            self.reply_json(HTTPStatus.OK, {"items": self.store.items})
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

    def reply(self, status: HTTPStatus, content_type: str, body: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def reply_json(self, status: HTTPStatus, value: dict[str, Any]) -> None:
        self.reply(status, "application/json; charset=utf-8", json.dumps(value, ensure_ascii=False).encode())

    def log_message(self, format: str, *args: object) -> None:
        print("dev-mail:", format % args)


def serve(args: argparse.Namespace) -> int:
    if args.expires_days < 1 or args.expires_days > 3650:
        raise ValueError("--expires-days 必须是 1 到 3650")
    items = load_items(args.game_data, args.game_data_version)
    store = MailGrantStore(args.mail_seed, args.output, items, args.expires_days)
    Handler.store = store
    server = ThreadingHTTPServer((args.listen_host, args.listen_port), Handler)
    print(f"已读取 {len(items)} 个可由 ItemDBInfo 领取的 GameData 物品。")
    print(f"浏览器打开：http://{args.listen_host}:{args.listen_port}/")
    print(f"临时邮件种子：{store.output}")
    print("此服务不修改 data/state；bd2server 指向该 seed 后，每次 /MailInfo 自动热载。")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\n开发邮件工具已停止。")
    finally:
        server.server_close()
    return 0


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)
    command = commands.add_parser("serve", help="start the loopback browser UI")
    command.add_argument("--game-data", type=Path, required=True, help="GameData root")
    command.add_argument("--game-data-version", required=True, help="validated GameData version")
    command.add_argument("--mail-seed", type=Path, required=True, help="base mail seed; read only")
    command.add_argument("--output", type=Path, required=True, help="generated development mail seed")
    command.add_argument("--listen-host", default="127.0.0.1", help="loopback host (default: 127.0.0.1)")
    command.add_argument("--listen-port", default=8765, type=int, help="loopback port (default: 8765)")
    command.add_argument("--expires-days", default=365, type=int, help="development mail validity (default: 365)")
    command.set_defaults(run=serve)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        if args.listen_host not in {"127.0.0.1", "localhost", "::1"}:
            raise ValueError("开发邮件服务只允许监听本机回环地址")
        return args.run(args)
    except (OSError, ValueError, sqlite3.Error) as exc:
        print(f"dev_mail_grant: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
