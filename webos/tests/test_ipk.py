"""Reproducibility and structural integrity of the self-contained webOS IPK."""
import importlib.util
import json
import os
import re
import subprocess
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("go2rtc_webos_build", ROOT / "webos/build_ipk.py")
pkg = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(pkg)


class BuildTests(unittest.TestCase):
    def test_deterministic_archive(self):
        first = pkg.build()
        with mock.patch.dict(os.environ, {"SOURCE_DATE_EPOCH": "1234567890"}):
            second = pkg.build()
        # SOURCE_DATE_EPOCH is deliberately variable: identical input means identical output.
        with mock.patch.dict(os.environ, {"SOURCE_DATE_EPOCH": "0"}):
            third = pkg.build()
        self.assertEqual(first, third)
        self.assertNotEqual(first, second)
        self.assertEqual(pkg.inspect(first)["app_id"], "io.go2rtc.webos.pip")

    def test_present_and_valid_version(self):
        blob = pkg.build()
        version = json.loads((ROOT / "webos/appinfo.json").read_text())["version"]
        self.assertEqual(pkg.inspect(blob)["version"], version)
        self.assertNotIn(b"<video", (ROOT / "www/webos-pip/index.html").read_bytes())
        self.assertNotIn(b"attemptMSE", (ROOT / "www/webos-pip/camera.js").read_bytes())
        self.assertNotIn(b'mse-player.js":', (ROOT / "webos/build_ipk.py").read_bytes())
        self.assertEqual(pkg.inspect(blob)["files"], 10)

    def test_fail_closed_on_tamper(self):
        for invalid in (b"not an ipk", b"!<arch>\n", pkg.build()[:100]):
            with self.assertRaises((ValueError, KeyError, OSError)):
                pkg.inspect(invalid)

    def test_no_host_or_camera_or_secret_in_package(self):
        blob = pkg.build()
        self.assertNotIn(b"192.168.0.", blob)
        self.assertNotIn(b"tapo_c230", blob)
        self.assertNotIn(b"rtsp://", blob)
        cfg = (ROOT / "webos/config.js").read_text()
        self.assertNotIn("http://192.", cfg)
        self.assertNotIn("camera_stream:", cfg)

    def test_structure_contains_notice(self):
        self.assertIn(b"Alexey Khit", (ROOT / "webos/NOTICE").read_bytes())
        proof = pkg.inspect(pkg.build())
        self.assertGreaterEqual(proof["files"], 10)


if __name__ == "__main__":
    unittest.main()
