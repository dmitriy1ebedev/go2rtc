#!/usr/bin/env python3
"""Deterministic, rootless LG webOS IPK packager for go2rtc Camera PiP.

Only source-controlled app files are bundled. Never include go2rtc.yaml, key
material, camera credentials, live device settings, or historical IPKs.
"""
import argparse
import gzip
import hashlib
import io
import json
import os
import re
import tarfile
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
APP_ID = "io.go2rtc.webos.pip"


def pack_tar(entries):
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.USTAR_FORMAT) as tar:
        for name, body in entries:
            info = tarfile.TarInfo(name)
            info.uid = info.gid = 0
            info.uname = info.gname = ""
            info.mtime = int(os.environ.get("SOURCE_DATE_EPOCH", "0"))
            if body is None:
                info.type = tarfile.DIRTYPE
                info.mode = 0o755
            else:
                info.mode = 0o644
                info.size = len(body)
            tar.addfile(info, None if body is None else io.BytesIO(body))
    compressed = io.BytesIO()
    with gzip.GzipFile(filename="", mode="wb", fileobj=compressed, mtime=0) as gz:
        gz.write(raw.getvalue())
    return compressed.getvalue()


def archive(members):
    stream = io.BytesIO()
    stream.write(b"!<arch>\n")
    for name, body in members:
        # ar's 60-byte header also uses a fixed timestamp.
        h = ("%-16s%-12d%-6d%-6d%-8s%-10d"
             % (name, 0, 0, 0, "100644", len(body))).encode("ascii")
        stream.write(h + b"\x60\x0a")
        stream.write(body)
        if len(body) % 2:
            stream.write(b"\n")
    return stream.getvalue()


def build():
    meta = json.loads((ROOT / "webos/appinfo.json").read_text(encoding="utf-8"))
    if meta.get("id") != APP_ID:
        raise ValueError("wrong application id")
    version = meta["version"]
    if not isinstance(version, str) or not re.fullmatch(
            r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", version):
        raise ValueError("invalid application version")
    if any(int(n) > 999999999 for n in version.split(".")):
        raise ValueError("LG webOS version component too large")
    versions = json.loads((ROOT / "versions.json").read_text(encoding="utf-8"))
    if version != versions["webos_app"]:
        raise ValueError("webOS app version does not match versions.json")
    files = {
        "index.html": ROOT / "www/webos-pip/index.html",
        "camera.js": ROOT / "www/webos-pip/camera.js",
        "config.js": ROOT / "webos/config.js",
        "appinfo.json": ROOT / "webos/appinfo.json",
        "icon.png": ROOT / "webos/icon.png",
        "largeIcon.png": ROOT / "webos/largeIcon.png",
        "LICENSE": ROOT / "LICENSE",
        "NOTICE": ROOT / "webos/NOTICE",
    }
    content = {name: path.read_bytes() for name, path in files.items()}
    # Stamp the single verified appinfo/versions.json version into the actual
    # packaged menu. Never rely on a separately maintained UI version string.
    marker = b"{{APP_VERSION}}"
    if content["index.html"].count(marker) != 2:
        raise ValueError("both visible version labels must use the package marker")
    content["index.html"] = content["index.html"].replace(marker, version.encode("ascii"))
    prefix = "usr/palm/applications/" + APP_ID
    data = [(name, None) for name in (
        "usr", "usr/palm", "usr/palm/applications", prefix,
        "usr/palm/packages", "usr/palm/packages/" + APP_ID,
    )]
    data += [(prefix + "/" + name, content[name]) for name in sorted(content)]
    packageinfo = json.dumps({"id": APP_ID, "app": APP_ID, "version": version},
                             sort_keys=True, separators=(",", ":")).encode()
    data.append(("usr/palm/packages/" + APP_ID + "/packageinfo.json", packageinfo))
    ctrl = (
        f"Package: {APP_ID}\nVersion: {version}\nSection: misc\n"
        f"Priority: optional\nArchitecture: all\n"
        f"Installed-Size: {sum(map(len, content.values()))}\n"
        "Maintainer: go2rtc webOS PiP contributors\n"
        "Description: Standalone go2rtc camera companion for LG webOS\n"
        "webOS-Package-Format-Version: 2\nwebOS-Packager-Version: 1.0.0\n"
    ).encode()
    return archive([
        ("debian-binary", b"2.0\n"),
        ("control.tar.gz", pack_tar([("control", ctrl)])),
        ("data.tar.gz", pack_tar(data)),
    ])


def inspect(blob):
    if not blob.startswith(b"!<arch>\n"):
        raise ValueError("not an ar-based IPK")
    off = 8
    members = {}
    while off < len(blob):
        h = blob[off:off + 60]
        if len(h) != 60 or h[58:60] != b"\x60\x0a":
            raise ValueError("invalid ar header")
        name = h[:16].decode("ascii").strip().rstrip("/")
        size = int(h[48:58].decode("ascii").strip())
        off += 60
        if off + size > len(blob) or name in members:
            raise ValueError("duplicate or truncated archive member")
        members[name] = blob[off:off + size]
        off += size + (size % 2)
    if set(members) != {"debian-binary", "control.tar.gz", "data.tar.gz"}:
        raise ValueError("missing IPK sections")
    if members["debian-binary"] != b"2.0\n":
        raise ValueError("unsupported IPK format")
    content = {}
    for section in ("control.tar.gz", "data.tar.gz"):
        with tarfile.open(fileobj=io.BytesIO(members[section]), mode="r:gz") as tar:
            for item in tar:
                if (item.name.startswith("/") or ".." in item.name.split("/")
                        or item.issym() or item.islnk() or not (item.isfile() or item.isdir())
                        or item.uid or item.gid or item.mode != (0o755 if item.isdir() else 0o644)):
                    raise ValueError("unsafe tar member " + item.name)
                if item.isfile():
                    content[item.name] = tar.extractfile(item).read()
    base = "usr/palm/applications/" + APP_ID + "/"
    for filename in ("index.html", "camera.js", "config.js",
                     "appinfo.json", "icon.png", "largeIcon.png", "LICENSE", "NOTICE"):
        if base + filename not in content:
            raise ValueError("missing application file " + filename)
    meta = json.loads(content[base + "appinfo.json"])
    if meta["id"] != APP_ID:
        raise ValueError("wrong packaged app")
    expected_ui_version = ("v" + meta["version"]).encode("ascii")
    packaged_html = content[base + "index.html"]
    if b"{{APP_VERSION}}" in packaged_html or packaged_html.count(expected_ui_version) != 2:
        raise ValueError("UI version label differs from installed application")
    if "rtsp://" in content[base + "config.js"].decode("utf-8"):
        raise ValueError("credentials must not be packaged")
    # This release must not consume an LG video decoder: preserve movies/HDMI.
    if (base + "mse-player.js" in content
            or b"<video" in content[base + "index.html"]
            or b"attemptMSE" in content[base + "camera.js"]):
        raise ValueError("JPEG-only PiP must not package an MSE video player")
    return {"app_id": APP_ID, "version": meta["version"],
            "sha256": hashlib.sha256(blob).hexdigest(), "files": len(content)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()
    blob = build()
    proof = inspect(blob)
    if args.check:
        print("VALID", proof)
        return
    target = ROOT / "out/webos" / f"go2rtc-webos-pip-{proof['version']}.ipk"
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(blob)
    print("BUILT", target, len(blob), proof["sha256"])


if __name__ == "__main__":
    main()
