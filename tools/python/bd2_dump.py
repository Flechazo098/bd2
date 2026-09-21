#!/usr/bin/env python3
"""bd2_dump.py — mitmproxy 抓包插件：**全抓**，能解的就解开。

## 启动

    mitmdump -s bd2_dump.py --listen-port 8080 --set stream_large_bodies=1m

`stream_large_bodies=1m` **必须加** —— 资源包单个最大 270MB，不流式的话
mitmproxy 会把它们全缓冲进内存。加了之后超过 1MB 的响应不缓冲，
`raw_content` 为 None，脚本只记大小。

让客户端走代理的三种办法：

    a) 系统代理 → 127.0.0.1:8080（最省事，但别的程序也会走）
    b) hosts 把目标域名指到本机 + 监听 443
    c) 只给游戏进程设 HTTP_PROXY 环境变量

装根证书（HTTPS 才解得开）：

    Windows:  certutil -user -addstore Root %USERPROFILE%\\.mitmproxy\\mitmproxy-ca-cert.cer
    Kali:     cp ~/.mitmproxy/mitmproxy-ca-cert.pem /usr/local/share/ca-certificates/
              update-ca-certificates

## 抓什么

**所有经过代理的流量都记**，一条不落。每条带一个 `game` 标记：

- `game=true` —— 命中 BD2 相关域名，或 UA 是 Unity/游戏的
- `game=false` —— 其它（浏览器、系统更新……）

`game=true` 的会在控制台实时打印；**全部**写进 jsonl。

## 解开什么

我们在服务端逆向时摸清的**三层编码**：

1. **外层**：JSON 信封 `{"errorType":0,"data":"<base64>","length":N,...}`
2. **AES 层**：`base64(AES-256-CBC(PKCS7(明文)))`，IV = 16 个 0 字节
3. **内层**：明文本身又是 `base64(protobuf)` —— **两层 base64**，
   少一层客户端就报「不是合法的 Base-64 字符串」

批量另有花样：`PUT /BatchRequest` 的 body 是 `base64(AES(JSON数组))`，
**只有一层 base64**（数组里每个 `requestData` 才各自 base64）。

## 密钥

- `LoginUser` / `JoinUser` 固定 `abcdefghijkrstuv024680wxyzlmnopq`
- 之后用登录响应里下发的 `user_key`（32 位十六进制），脚本会自动认出来并接管

## 输出

- 控制台：`game=true` 的实时打印
- `capture/2.34.13/<时间戳>/bd2_dump.jsonl`：全部流量，每行一条
- `capture/2.34.13/<时间戳>/bd2_dump.log`：便于人工检查的文本日志
- `capture/2.34.13/<时间戳>/bodies/`：`game=true` 的原始 body 原样存一份
  （服务端实现要按 schema 解，原字节比文本更值钱）

可用环境变量 `BD2_CAPTURE_DIR` 覆盖输出根目录。

只观察，不修改任何流量。
"""

import base64
import json
import os
import re
import time

from mitmproxy import http, ctx

# ---------------------------------------------------------------- 加密

FIX_KEY = b"abcdefghijkrstuv024680wxyzlmnopq"
ZERO_IV = b"\x00" * 16

SESS = None
TAG = time.strftime("%Y%m%d-%H%M%S")
SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
PROJECT_DIR = os.path.dirname(os.path.dirname(SCRIPT_DIR))
CAPTURE_ROOT = os.path.abspath(
    os.environ.get("BD2_CAPTURE_DIR", os.path.join(PROJECT_DIR, "data", "capture", "2.34.13"))
)
RUN_DIR = os.path.join(CAPTURE_ROOT, TAG)
LOG_PATH = os.path.join(RUN_DIR, "bd2_dump.jsonl")
TEXT_PATH = os.path.join(RUN_DIR, "bd2_dump.log")
BODY_DIR = os.path.join(RUN_DIR, "bodies")

_AES = None
_AES_IMPL = ""


def _load_aes():
    global _AES, _AES_IMPL
    if _AES is not None:
        return _AES
    try:
        from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

        def dec(key, data):
            c = Cipher(algorithms.AES(key), modes.CBC(ZERO_IV)).decryptor()
            return c.update(data) + c.finalize()

        _AES, _AES_IMPL = dec, "cryptography"
        return _AES
    except Exception:
        pass
    try:
        from Crypto.Cipher import AES as _C

        def dec(key, data):
            return _C.new(key, _C.MODE_CBC, ZERO_IV).decrypt(data)

        _AES, _AES_IMPL = dec, "pycryptodome"
        return _AES
    except Exception:
        _AES_IMPL = ""
        return None


def strip_pkcs7(b: bytes) -> bytes:
    if not b:
        return b
    n = b[-1]
    if n == 0 or n > 16 or n > len(b):
        return b
    if b[-n:] != bytes([n]) * n:
        return b
    return b[:-n]


def aes_unwrap(s: str, key: bytes):
    """base64 -> AES -> 去 PKCS7。解不开返回 None。"""
    dec = _load_aes()
    if dec is None or not s:
        return None
    try:
        raw = base64.b64decode(s, validate=False)
    except Exception:
        return None
    if not raw or len(raw) % 16:
        return None
    try:
        return strip_pkcs7(dec(key, raw))
    except Exception:
        return None


# ---------------------------------------------------------------- protobuf 转储

PRINTABLE = re.compile(rb"^[\x20-\x7e\n\r\t]*$")


def pb_dump(b: bytes, indent: int = 0, depth: int = 0, limit: int = 60):
    """没有 schema 也能看：按 varint 拆字段号 + 值，嵌套递归一层层往下。"""
    out = []
    i, n = 0, len(b)
    pad = "  " * indent
    while i < n and depth < 6 and len(out) < limit:
        tag = shift = 0
        while i < n:
            c = b[i]
            i += 1
            tag |= (c & 0x7F) << shift
            shift += 7
            if not c & 0x80:
                break
        else:
            break
        fn, wt = tag >> 3, tag & 7
        if fn == 0:
            break
        if wt == 0:
            v = shift = 0
            while i < n:
                c = b[i]
                i += 1
                v |= (c & 0x7F) << shift
                shift += 7
                if not c & 0x80:
                    break
            out.append("%s#%d varint %d" % (pad, fn, v))
        elif wt == 2:
            ln = shift = 0
            while i < n:
                c = b[i]
                i += 1
                ln |= (c & 0x7F) << shift
                shift += 7
                if not c & 0x80:
                    break
            if i + ln > n:
                break
            chunk = b[i : i + ln]
            i += ln
            if chunk and PRINTABLE.match(chunk):
                try:
                    out.append("%s#%d str  %r" % (pad, fn, chunk.decode("utf-8")))
                except Exception:
                    out.append("%s#%d bytes(%d)" % (pad, fn, ln))
            else:
                sub = pb_dump(chunk, indent + 1, depth + 1, limit - len(out))
                if sub:
                    out.append("%s#%d 嵌套 {" % (pad, fn))
                    out.extend(sub)
                    out.append("%s}" % pad)
                else:
                    out.append("%s#%d bytes(%d)" % (pad, fn, ln))
        elif wt == 5:
            i += 4
            out.append("%s#%d fixed32" % (pad, fn))
        elif wt == 1:
            i += 8
            out.append("%s#%d fixed64" % (pad, fn))
        else:
            break
    if len(out) >= limit:
        out.append("%s…(截断)" % pad)
    return out


# ---------------------------------------------------------------- 输出

_fh = None
_tf = None
_body_seq = [0]


def _ensure_fh():
    global _fh, _tf
    os.makedirs(RUN_DIR, exist_ok=True)
    if _fh is None:
        _fh = open(LOG_PATH, "a", encoding="utf-8")
    if _tf is None:
        _tf = open(TEXT_PATH, "a", encoding="utf-8")
    return _fh


def render_text(rec: dict) -> str:
    """把一条记录渲染成人能读的多行文本。proto 字段缩进展开，不塞成一行。"""
    L = []
    ts = rec.get("ts", "")
    ev = rec.get("ev", "?")

    if ev == "session":
        return "\n[%s] ★ 会话密钥 user_key=%s\n" % (ts, rec.get("user_key"))
    if ev == "set_cookie":
        return "[%s]   Set-Cookie: %s\n" % (ts, rec.get("value"))

    flag = "" if rec.get("game") else "  (非游戏)"
    if ev == "req":
        L.append("\n%s[%s] >>> %s %s%s" % ("", ts, rec.get("method"), rec.get("path"), flag))
        if rec.get("shape"):
            L.append("      形状=%s  raw=%dB" % (rec["shape"], rec.get("len", 0)))
        if rec.get("cookie"):
            L.append("      cookie=%s" % rec["cookie"])
        if rec.get("raw_file"):
            L.append("      原始=%s" % rec["raw_file"])
        if rec.get("batch_count"):
            L.append("      批量 %d 条：" % rec["batch_count"])
            for e in rec.get("batch", []):
                L.append("        · %-32s req=%dB" % (e.get("path"), e.get("req_len", 0)))
                for line in e.get("pb", []):
                    L.append("            " + line)
        for line in rec.get("pb", []):
            L.append("      " + line)
    else:
        L.append("\n[%s] <<< %s  HTTP %s%s" % (ts, rec.get("path"), rec.get("status"), flag))
        if rec.get("errorType") is not None:
            L.append("      errorType=%s length=%s %s" % (
                rec.get("errorType"), rec.get("length"),
                ("msg=" + rec["errorMessage"]) if rec.get("errorMessage") else ""))
        if rec.get("raw_file"):
            L.append("      原始=%s" % rec["raw_file"])
        for line in rec.get("pb", []):
            L.append("      " + line)
    return "\n".join(L) + "\n"


def save_body(kind: str, path: str, raw: bytes) -> str:
    """原字节存一份 —— 后面要按 schema 解，字符比文本值钱。"""
    if not raw:
        return ""
    os.makedirs(BODY_DIR, exist_ok=True)
    _body_seq[0] += 1
    safe = re.sub(r"[^A-Za-z0-9_.-]", "_", path.strip("/"))[:60] or "root"
    fn = os.path.join(BODY_DIR, "%05d_%s_%s.bin" % (_body_seq[0], kind, safe))
    with open(fn, "wb") as f:
        f.write(raw)
    return os.path.relpath(fn, RUN_DIR)


def header_pairs(headers) -> list[list[str]]:
    """保留重复头部（尤其 Set-Cookie），便于后续按真实响应回放。"""
    try:
        return [[str(k), str(v)] for k, v in headers.items(multi=True)]
    except TypeError:
        return [[str(k), str(v)] for k, v in headers.items()]


def emit(rec: dict, console: bool):
    rec["ts"] = time.strftime("%H:%M:%S")
    _ensure_fh()

    # JSONL：给脚本用的，字段完整
    _fh.write(json.dumps(rec, ensure_ascii=False) + "\n")
    _fh.flush()

    # 文本日志：给人看的，proto 逐字段展开
    _tf.write(render_text(rec))
    _tf.flush()

    if console:
        # 控制台只打一行摘要，细节都在文本日志里 —— 否则 proto dump 会把屏幕刷爆
        if rec.get("ev") == "req":
            head = ">>> %s %s  [%s]" % (rec.get("method"), rec.get("path"), rec.get("shape", ""))
            if rec.get("batch_count"):
                head += " 批量%d条" % rec["batch_count"]
        elif rec.get("ev") == "resp":
            head = "<<< %s  HTTP %s  errorType=%s" % (
                rec.get("path"), rec.get("status"), rec.get("errorType"))
        else:
            head = json.dumps(rec, ensure_ascii=False)
        ctx.log.info(head)


# ---------------------------------------------------------------- 会话

USER_KEY = [None]


def sniff_user_key(plain: bytes):
    if USER_KEY[0] or not plain:
        return
    m = re.search(rb"[0-9a-f]{32}", plain)
    if m:
        USER_KEY[0] = m.group(0).decode()
        emit({"ev": "session", "user_key": USER_KEY[0],
              "note": "从登录响应认出 user_key，后续包用它解密"}, True)


def cur_key() -> bytes:
    return USER_KEY[0].encode() if USER_KEY[0] else FIX_KEY


# ---------------------------------------------------------------- 判定

GAME_HOSTS = (
    "bd2.pmang.cloud",
    "akamaized.net",
    "neonapi.com",
    "neowizplay.com",
    "pmangplus",
    "neon-file",
    "browndust",
    "gamfs",
)

# 资源包域名：只记元信息，不存 body
RESOURCE_HOSTS = ("dl.bd2.pmang.cloud", "cdn.bd2.pmang.cloud", "bd2-cdn.akamaized.net")


def is_game(host: str, ua: str) -> bool:
    h, u = host.lower(), (ua or "").lower()
    if any(g in h for g in GAME_HOSTS):
        return True
    return "unity" in u


def is_resource(host: str) -> bool:
    return any(r in host.lower() for r in RESOURCE_HOSTS)


# ---------------------------------------------------------------- 解码

BATCH_KEYS = ("path", "requestData")


def looks_like_batch(b: bytes) -> bool:
    try:
        arr = json.loads(b)
    except Exception:
        return False
    return (
        isinstance(arr, list)
        and bool(arr)
        and isinstance(arr[0], dict)
        and all(k in arr[0] for k in BATCH_KEYS)
    )


def decode_request(body: bytes):
    if not body:
        return "空", b"", None
    if looks_like_batch(body):
        try:
            return "批量(明文JSON)", body, json.loads(body)
        except Exception:
            return "批量(明文JSON)", body, None

    txt = body.decode("ascii", "ignore").strip()
    plain = aes_unwrap(txt, cur_key())
    if plain is not None:
        if looks_like_batch(plain):
            try:
                return "批量(加密JSON)", plain, json.loads(plain)
            except Exception:
                return "批量(加密JSON)", plain, None
        try:
            return "加密包", base64.b64decode(plain, validate=False), None
        except Exception:
            return "加密包(内层非base64)", plain, None

    try:
        return "明文包", base64.b64decode(body, validate=False), None
    except Exception:
        return "未识别", body, None


def decode_response(body: bytes):
    try:
        env = json.loads(body)
    except Exception:
        return None, None
    if not isinstance(env, dict) or "errorType" not in env:
        return None, None
    data = env.get("data") or ""
    if not data:
        return env, b""
    plain = aes_unwrap(data, cur_key())
    if plain is not None:
        try:
            return env, base64.b64decode(plain, validate=False)
        except Exception:
            return env, plain
    try:
        return env, base64.b64decode(data, validate=False)
    except Exception:
        return env, b""


# ---------------------------------------------------------------- 钩子


def running():
    _load_aes()
    _ensure_fh()
    ctx.log.info("━" * 60)
    ctx.log.info("bd2_dump 已加载   AES=%s" % (_AES_IMPL or "不可用(只dump不解)"))
    ctx.log.info("文本日志(看这个): %s" % TEXT_PATH)
    ctx.log.info("JSONL(喂脚本):    %s" % LOG_PATH)
    ctx.log.info("原始 body:        %s/" % BODY_DIR)
    ctx.log.info("━" * 60)


def request(flow: http.HTTPFlow):
    host = flow.request.pretty_host
    ua = flow.request.headers.get("user-agent", "")
    game = is_game(host, ua)
    resource = is_resource(host)

    if resource:
        emit({"ev": "req", "flow_id": flow.id,
              "game": True, "resource": True,
              "method": flow.request.method, "host": host,
              "path": flow.request.path, "url": flow.request.pretty_url,
              "headers": header_pairs(flow.request.headers),
              "len": len(flow.request.raw_content or b"")}, False)
        return

    body = flow.request.raw_content or b""
    rec = {"ev": "req", "flow_id": flow.id,
           "game": game, "method": flow.request.method,
           "host": host, "path": flow.request.path,
           "url": flow.request.pretty_url,
           "headers": header_pairs(flow.request.headers), "len": len(body)}
    if not game:
        emit(rec, False)
        return

    rec["ua"] = ua[:80]
    ck = flow.request.headers.get("cookie")
    if ck:
        rec["cookie"] = ck

    shape, plain, batch = decode_request(body)
    rec["shape"] = shape
    rec["raw_file"] = save_body("req", flow.request.path, body)

    if batch:
        rec["batch_count"] = len(batch)
        rec["batch"] = [
            {"path": e.get("path"),
             "req_len": len(base64.b64decode(e.get("requestData") or "") or b""),
             "pb": pb_dump(base64.b64decode(e.get("requestData") or "") or b"")}
            for e in batch[:100]
        ]
    elif plain:
        rec["pb"] = pb_dump(plain)

    emit(rec, True)


def response(flow: http.HTTPFlow):
    host = flow.request.pretty_host
    resource = is_resource(host)
    body = flow.response.raw_content or b""

    if resource:
        emit({"ev": "resp", "flow_id": flow.id,
              "game": True, "resource": True,
              "method": flow.request.method, "host": host,
              "path": flow.request.path,
              "url": flow.request.pretty_url,
              "status": flow.response.status_code,
              "headers": header_pairs(flow.response.headers),
              "len": len(body),
              "streamed": flow.response.raw_content is None}, False)
        return

    ua = flow.request.headers.get("user-agent", "")
    game = is_game(host, ua)
    rec = {"ev": "resp", "flow_id": flow.id,
           "game": game, "method": flow.request.method,
           "host": host, "path": flow.request.path,
           "url": flow.request.pretty_url,
           "status": flow.response.status_code,
           "headers": header_pairs(flow.response.headers), "len": len(body)}
    if not game:
        emit(rec, False)
        return

    sc = flow.response.headers.get("set-cookie")
    if sc:
        emit({"ev": "set_cookie", "value": sc}, True)

    env, plain = decode_response(body)
    if env is not None:
        rec["errorType"] = env.get("errorType")
        rec["length"] = env.get("length")
        if env.get("errorMessage"):
            rec["errorMessage"] = env["errorMessage"]
    rec["raw_file"] = save_body("resp", flow.request.path, body)
    if plain:
        if re.search(r"LoginUser|JoinUser", flow.request.path):
            sniff_user_key(plain)
        rec["pb"] = pb_dump(plain)

    emit(rec, True)
