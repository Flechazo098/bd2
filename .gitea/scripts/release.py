#!/usr/bin/env python3
"""Create a Gitea release for every CHANGELOG.md section that has none yet.

Each section becomes the tag <component>-<version> at the commit that added
the section. Existing releases are never modified.
"""
import json
import os
import re
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request

SITE = "https://bd2.flechazo.xin"
LOCALES = [("zh_cn", "简体中文"), ("zh_tw", "繁體中文"), ("en_us", "English"), ("ja_jp", "日本語"), ("ko_kr", "한국어")]
HEADING = re.compile(r"^## (server|client) (\S+) · (\d{4}-\d{2}-\d{2})")


def parse(text):
    entries, entry, block, fence = [], None, None, False
    for line in text.splitlines():
        line = line.rstrip()
        if line.lstrip().startswith(("```", "~~~")):
            fence = not fence
            continue
        if fence:
            continue
        if m := HEADING.match(line):
            entry = {"component": m[1], "version": m[2], "date": m[3], "heading": line, "meta": {}, "notes": {}}
            block = None
            entries.append(entry)
        elif entry is None:
            continue
        elif (m := re.match(r"^> ([a-z_]+): (.+)$", line)) and block is None:
            entry["meta"][m[1]] = m[2].strip()
        elif m := re.match(r"^### ([a-z]{2}_[a-z]{2}): (.+)$", line):
            block = entry["notes"][m[1]] = {"title": m[2].strip(), "items": []}
        elif block is not None and (m := re.match(r"^\s*[-*] (.+)$", line)):
            block["items"].append(m[1].strip())
        elif block is not None and line.strip() and block["items"]:
            block["items"][-1] += " " + line.strip()
    return entries


class Gitea:
    def __init__(self, api, repository, token):
        self.base = f"{api.rstrip('/')}/repos/{repository}"
        self.token = token

    def call(self, method, path, body=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request(self.base + path, data=data, method=method, headers={
            "Authorization": f"token {self.token}", "Content-Type": "application/json", "Accept": "application/json"})
        with urllib.request.urlopen(request, timeout=30) as response:
            return json.load(response)

    def tags(self):
        names, page = set(), 1
        while batch := self.call("GET", f"/releases?limit=50&page={page}"):
            names.update(r["tag_name"] for r in batch)
            page += 1
        return names


def fetch_json(url):
    try:
        with urllib.request.urlopen(url, timeout=20) as response:
            return json.load(response)
    except (OSError, ValueError):
        return None


def exists(url):
    try:
        with urllib.request.urlopen(urllib.request.Request(url, method="HEAD"), timeout=20) as response:
            return response.status == 200
    except OSError:
        return False


def file_url(relative):
    return f"{SITE}/releases/" + "/".join(urllib.parse.quote(p) for p in relative.split("/"))


def downloads(entry, manifest):
    rows = []
    version = entry["version"]
    if entry["component"] == "client":
        name = f"bd2client-{version}-windows-x64.zip"
        if exists(file_url("downloads/" + name)):
            rows.append(f"- Windows x64: [{name}]({file_url('downloads/' + name)})")
    else:
        server = (manifest or {}).get("server") or {}
        if server.get("version") == version:
            for f in server.get("files", []):
                rows.append(f"- {f['platform']}: [{f['name']}]({file_url(f['url'].removeprefix('../'))})  \n  SHA-256 `{f['sha256']}`")
        name = f"bd2server-{version}-windows-x64.zip"
        if exists(file_url("downloads/" + name)):
            rows.append(f"- windows-x64: [{name}]({file_url('downloads/' + name)})")
    return rows


def body(entry, manifest):
    meta = " · ".join([entry["date"]] + [f"{k}: {v}" for k, v in entry["meta"].items()])
    parts = [f"> {meta}", ""]
    rows = downloads(entry, manifest)
    parts += ["## Downloads", "", *(rows or [f"- {SITE}"]), ""]
    for locale, label in LOCALES:
        notes = entry["notes"].get(locale)
        if notes:
            parts += [f"## {label} · {notes['title']}", "", *[f"- {item}" for item in notes["items"]], ""]
    return "\n".join(parts).rstrip() + "\n"


def commit_of(heading):
    log = subprocess.run(["git", "log", "--format=%H", "-S", heading, "--", "CHANGELOG.md"],
                         check=True, capture_output=True, text=True).stdout.split()
    return log[-1] if log else os.environ["GITHUB_SHA"]


def main():
    gitea = Gitea(os.environ["GITEA_API"], os.environ["GITHUB_REPOSITORY"], os.environ["GITEA_TOKEN"])
    with open("CHANGELOG.md", encoding="utf-8") as stream:
        entries = parse(stream.read())
    existing = gitea.tags()
    manifest = (fetch_json(f"{SITE}/releases/stable/release.json") or {}).get("manifest")
    created = 0
    for entry in reversed(entries):
        tag = f"{entry['component']}-{entry['version']}"
        if tag in existing:
            continue
        notes = entry["notes"].get("zh_cn") or entry["notes"].get("en_us") or {"title": ""}
        release = {"tag_name": tag, "target_commitish": commit_of(entry["heading"]),
                   "name": f"{entry['component']} {entry['version']} · {notes['title']}".rstrip(" ·"),
                   "body": body(entry, manifest), "draft": False, "prerelease": False}
        try:
            gitea.call("POST", "/releases", release)
        except urllib.error.HTTPError as error:
            sys.exit(f"{tag}: HTTP {error.code} {error.read().decode(errors='replace')}")
        print(f"created {tag} at {release['target_commitish'][:12]}")
        created += 1
    print(f"{created} release(s) created, {len(entries) - created} already present")


if __name__ == "__main__":
    main()
