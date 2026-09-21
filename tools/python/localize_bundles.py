#!/usr/bin/env python3
"""把远程 bundle 变成本地直读：硬链接进 StreamingAssets/aa + 改写 catalog。

## 为什么要用 {RuntimePath} 而不是绝对路径

试过把 catalog 里的条目改成 `E:/bd2/dl/.../xxx.bundle` 这种绝对路径，结果带子目录的
bundle 报：

    RemoteProviderException : Invalid path in AssetBundleProvider

原因是 Addressables 的 `AssetBundleResource.GetLoadInfo()`：

    if (!(location?.Data is AssetBundleRequestOptions)) { loadType = None; }   // ← 落到这
    else if (ShouldPathUseWebRequest(path))      loadType = Web;
    else if (UseUnityWebRequestForLocalBundles)  loadType = Web;
    else                                         loadType = Local;

    ...
    default:  // LoadType.None
        Complete(null, false, new RemoteProviderException(
            $"Invalid path in AssetBundleProvider: '{path}'."));

绝对路径这种 location 上挂不到 `AssetBundleRequestOptions`，于是 LoadType.None。

**客户端自带那 10 个本地 bundle 用的是 `{RuntimePath}\\<包名>`** ——
那是 Addressables 原生的本地形态，一定挂得上 options。照抄它。

## 硬链接

`RuntimePath` 解析成 `StreamingAssets/aa`，所以 bundle 得摆进去。用 NTFS 硬链接
**零额外空间**（同一份数据两个路径）。包名里带子目录的（798 个）要建同样的子目录。

## 用法

    python localize_bundles.py <bundle 所在目录> <aa 目录>
"""
import hashlib
import io
import json
import os
import re
import shutil
import sys

BS = chr(92)
LOCAL_PREFIX = "{UnityEngine.AddressableAssets.Addressables.RuntimePath}" + BS


def strip_prefix(s):
    """`{BDNetwork.CdnInfo.Info}\\平台\\{分辨率}\\{版本}\\<包名>` -> `<包名>`"""
    parts = s.replace(BS, "/").split("/")
    return "/".join(parts[4:])


def main():
    if len(sys.argv) != 3:
        print(__doc__)
        return 1
    src, aa = sys.argv[1], sys.argv[2]
    cat = os.path.join(aa, "catalog.json")
    if not os.path.isfile(cat):
        print("找不到 %s" % cat)
        return 1
    if not os.path.isdir(src):
        print("找不到 bundle 目录 %s" % src)
        return 1

    # ---- 1. 备份 ----
    bak = cat + ".bak-before-localize"
    if not os.path.exists(bak):
        shutil.copy2(cat, bak)
        print("备份 -> %s" % os.path.basename(bak))
    else:
        print("备份已存在，跳过")

    # ---- 2. 硬链接 ----
    linked = skipped = copied = 0
    for dp, _, fn in os.walk(src):
        for f in fn:
            if not f.endswith(".bundle"):
                continue
            s = os.path.join(dp, f)
            d = os.path.join(aa, os.path.relpath(s, src))
            os.makedirs(os.path.dirname(d), exist_ok=True)
            if os.path.exists(d):
                skipped += 1
                continue
            try:
                os.link(s, d)
                linked += 1
            except OSError:
                shutil.copy2(s, d)
                copied += 1
    print("硬链接 新建%d 已存在%d 退回复制%d" % (linked, skipped, copied))

    # ---- 3. 改写 internal id ----
    o = json.load(io.open(cat, encoding="utf-8"))
    ids = o["m_InternalIds"]
    changed = 0
    for i, s in enumerate(ids):
        if not s.startswith("{BDNetwork.CdnInfo.Info}"):
            continue
        name = strip_prefix(s)
        if not name or "{" in name:
            print("解析异常，中止：%r -> %r" % (s, name))
            return 1
        ids[i] = LOCAL_PREFIX + name
        changed += 1
    print("改写 %d 条" % changed)

    # 长度不变，下标引用依然有效
    raw = json.dumps(o, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    io.open(cat, "wb").write(raw)
    print("写回 %s（%d 字节）" % (os.path.basename(cat), len(raw)))
    print("  md5 = %s" % hashlib.md5(raw).hexdigest())
    return 0


if __name__ == "__main__":
    sys.exit(main())
