"""Installer CLI regression checks; no root, downloads or system changes."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("install.sh").resolve()


@unittest.skipUnless(os.name == "posix", "Run this Linux installer suite in WSL/Linux")
class InstallerCLI(unittest.TestCase):
    def run_cli(self, *args, script=SCRIPT):
        return subprocess.run(["bash", str(script), *args], stdin=subprocess.DEVNULL,
                              capture_output=True, text=True)

    def test_syntax(self):
        result = subprocess.run(["bash", "-n", str(SCRIPT)], capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_help(self):
        self.assertEqual(self.run_cli("--help").returncode, 0)

    def test_dry_run_creates_nothing(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "absent"
            # Use POSIX-compatible test paths on Windows Git Bash too.
            path = str(target).replace("\\", "/")
            if os.name == "nt":
                path = "/" + path[0].lower() + path[2:]
            result = self.run_cli("--yes", "--dry-run", "--domain", "gift.example.com",
                                  "--dir", path)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(target.exists())

    def test_invalid_arguments(self):
        cases = [("--port", "0"), ("--port", "65536"), ("--port", "abc"),
                 ("--https", "bad"), ("--dir", "/"), ("--dir", "/opt/../etc"),
                 ("--domain", "https://example.com"), ("--ref", "-bad"),
                 ("--unknown",), ("--port",), ("--status", "--upgrade")]
        for args in cases:
            with self.subTest(args=args):
                result = self.run_cli("--yes", "--dry-run", "--domain", "gift.example.com", *args)
                self.assertNotEqual(result.returncode, 0, result.stderr)

    def test_noninteractive_requires_domain(self):
        self.assertNotEqual(self.run_cli("--dry-run", "--yes").returncode, 0)

    def test_state_is_data_and_flags_override_defaults(self):
        with tempfile.TemporaryDirectory() as directory:
            copy = Path(directory) / "install.sh"
            copy.write_bytes(SCRIPT.read_bytes())
            marker = Path(directory) / "PWNED"
            (Path(directory) / "install.conf").write_text(
                f"DOMAIN=old.example.com\nPORT=8999\nMODE=external\nEVIL=$(touch {marker})\n",
                encoding="utf-8")
            result = self.run_cli("--dry-run", "--yes", "--domain", "new.example.com", script=copy)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("new.example.com", result.stderr)
            self.assertIn("8999", result.stderr)
            self.assertFalse(marker.exists())

    def test_go_version_crlf(self):
        with tempfile.TemporaryDirectory() as directory:
            mod = Path(directory) / "go.mod"
            mod.write_bytes(b"module xgift\r\n\r\ngo 1.27.1\r\n")
            result = subprocess.run(
                ["bash", "-c", "awk '$1==\"go\" {gsub(/\\r/,\"\",$2); print $2; exit}' \"$1\"", "test", str(mod)],
                capture_output=True, text=True)
            self.assertEqual(result.stdout, "1.27.1\n")
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_lf(self):
        self.assertNotIn(b"\r", SCRIPT.read_bytes())


if __name__ == "__main__":
    unittest.main()
