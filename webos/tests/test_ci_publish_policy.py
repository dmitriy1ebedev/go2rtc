"""GitVerse must carry only the independently installable webOS client."""
import fnmatch
import json
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
REQUIRED = {
    ".mcpshka/publish.json", "LICENSE", "versions.json",
    "webos/NOTICE", "webos/appinfo.json", "webos/build_ipk.py",
    "webos/config.js", "webos/icon.png", "webos/largeIcon.png",
    "webos/tests/test_client.js", "webos/tests/test_ipk.py",
    "webos/tests/test_ci_publish_policy.py", "www/webos-pip/camera.js",
    "www/webos-pip/index.html",
}
DROPPED = (
    "main.go", "go.mod", "go.sum", "internal/webospip/ptz.go",
    "internal/api/api.go", "pkg/onvif/client.go", "docs/webos/ISSUES.md",
    "docs/server/ROADMAP.md", "README.md", ".github/workflows/test.yml",
    ".mcpshka/profile.json", "go2rtc.yaml", "webos/devices.json",
    "webos/known_hosts", "webos/install.py", "scripts/test_webos.py",
    "www/webos-pip/mse-player.js", "webos/camera.local.js",
    "camera.pem", "secret.env", "backup.sqlite", "out/webos/latest.ipk",
)


def matching(pattern, filename):
    if pattern.endswith("/"):
        return filename.startswith(pattern)
    if "/" not in pattern:
        return fnmatch.fnmatchcase(filename.rsplit("/", 1)[-1], pattern)
    return fnmatch.fnmatchcase(filename, pattern)


def included(policy, filename):
    if filename.startswith(".gitverse/"):
        return True
    if any(matching(p, filename) for p in policy["keep"]):
        return True
    return not any(matching(p, filename) for p in policy["exclude"])


class GitVerseAppOnlyTests(unittest.TestCase):
    def setUp(self):
        self.policy = json.loads((ROOT / ".mcpshka/publish.json").read_text())

    def test_exact_allowlist(self):
        self.assertEqual(self.policy["mode"], "code_only")
        self.assertEqual(self.policy["exclude"], ["*"])
        self.assertEqual(set(self.policy["keep"]), REQUIRED)
        for name in REQUIRED:
            with self.subTest(name=name):
                self.assertTrue((ROOT / name).is_file())
                self.assertTrue(included(self.policy, name))

    def test_no_server_or_private_material(self):
        for name in DROPPED:
            with self.subTest(name=name):
                self.assertFalse(included(self.policy, name))

    def test_no_go_build_in_gitverse(self):
        ci = (ROOT / ".gitverse/workflows/ci.yml").read_text()
        self.assertIn("webos/build_ipk.py", ci)
        self.assertIn("actions/upload-artifact@v4", ci)
        self.assertIn("webos/tests/test_client.js", ci)
        self.assertIn("timeout-minutes: 20", ci)
        for forbidden in ("go test", "go vet", "go build", "continue-on-error"):
            self.assertNotIn(forbidden, ci)


if __name__ == "__main__":
    unittest.main()
