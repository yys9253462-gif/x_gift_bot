"""Installer CLI regression checks; no root, downloads or system changes."""
import os
import shlex
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

    def run_function(self, name, body):
        import re
        match = re.search(r"^" + name + r"\(\) \{\n.*?^\}(?=\n(?:[a-z_]+\(\)|if |# |$))", SCRIPT.read_text(), re.M | re.S)
        self.assertIsNotNone(match, f"missing testable function {name}")
        return subprocess.run(["bash", "-c", "set -Eeuo pipefail\n" + match.group(0) + "\n" + body],
                              capture_output=True, text=True)

    def test_permission_denied_state_explains_sudo(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "root"
            root.mkdir(mode=0o755)
            state = root / "install.conf"
            state.write_text("DOMAIN=gift.example.com\nPORT=8999\nMODE=external\n")
            os.chmod(state, 0o000)
            try:
                result = self.run_cli("--dry-run", "--yes", "--dir", str(root).replace("\\", "/"))
            finally:
                os.chmod(state, 0o644)
            if os.geteuid() != 0:
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn("sudo", result.stderr)

    def test_root_directory_and_system_dirs_rejected(self):
        for target in ["/", "//", "///", "/bin", "/etc", "/usr", "/root", "/opt/../etc"]:
            with self.subTest(dir=target):
                result = self.run_cli("--yes", "--dry-run", "--domain", "gift.example.com", "--dir", target)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn("安装目录", result.stderr)

    def test_domain_label_length(self):
        result = self.run_cli("--yes", "--dry-run", "--domain", "a" * 64 + ".example.com")
        self.assertNotEqual(result.returncode, 0)

    def test_domain_normalized(self):
        result = self.run_cli("--yes", "--dry-run", "--domain", "Gift.EXAMPLE.Com")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("gift.example.com", result.stderr)

    def test_saved_ref_and_override(self):
        with tempfile.TemporaryDirectory() as directory:
            copy = Path(directory) / "install.sh"
            copy.write_bytes(SCRIPT.read_bytes())
            (Path(directory) / "install.conf").write_text(
                "DOMAIN=gift.example.com\nPORT=8999\nMODE=external\nREF=v1.2.3\n")
            result = self.run_cli("--yes", "--dry-run", script=copy)
            self.assertIn("v1.2.3", result.stderr)
            result = self.run_cli("--yes", "--dry-run", "--ref", "release", script=copy)
            self.assertIn("release", result.stderr)
            self.assertNotIn("v1.2.3", result.stderr)

    def test_only_expected_listener_owner(self):
        for owners, expected in [('users:(("nginx",pid=1,fd=2))', 0),
                                 ('users:(("nginx",pid=1,fd=2)) users:(("apache2",pid=2,fd=3))', 1),
                                 ('users:(("caddy",pid=1,fd=2))', 1), ('LISTEN no-process-info', 1), ('', 0)]:
            with self.subTest(owners=owners):
                result = self.run_function("only_listener_owner", f'OWNERS={owners!r}; only_listener_owner nginx')
                self.assertEqual(result.returncode, expected, result.stderr)

    def test_private_parent_rejected_without_chmod(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory) / "private"
            parent.mkdir(mode=0o700)
            result = self.run_function("check_root_parents", f'ROOT={str(parent / "xgift")!r}; die() {{ exit 1; }}; check_root_parents')
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(parent.stat().st_mode & 0o777, 0o700)
            parent.chmod(0o755)
            Path(directory).chmod(0o755)
            result = self.run_function("check_root_parents", f'ROOT={str(parent / "xgift")!r}; die() {{ exit 1; }}; check_root_parents')
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_nginx_conflict_ignores_only_owned_file(self):
        dump = ('# configuration file /etc/nginx/conf.d/xgift-installer.conf:\n'
                'server_name gift.example.com;\n'
                '# configuration file /etc/nginx/conf.d/other.conf:\n'
                'server_name Gift.Example.Com another.example.com;\n')
        result = self.run_function("nginx_domain_conflict", f'NGINX_FILE=/etc/nginx/conf.d/xgift-installer.conf; DOMAIN=gift.example.com; nginx() {{ printf "%s" {shlex.quote(dump)}; }}; nginx_domain_conflict')
        self.assertEqual(result.returncode, 0, result.stderr)
        own = dump.split('# configuration file /etc/nginx/conf.d/other.conf:')[0]
        result = self.run_function("nginx_domain_conflict", f'NGINX_FILE=/etc/nginx/conf.d/xgift-installer.conf; DOMAIN=gift.example.com; nginx() {{ printf "%s" {shlex.quote(own)}; }}; nginx_domain_conflict')
        self.assertEqual(result.returncode, 1, result.stderr)

    def test_nginx_activation_starts_before_reload(self):
        result = self.run_function("activate_nginx", 'nginx() { printf "nginx %s\\n" "$*"; }; systemctl() { printf "systemctl %s\\n" "$*"; }; activate_nginx')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ["nginx -t", "systemctl enable --now nginx", "systemctl reload nginx"])

    def test_lock_precedes_dependencies_and_uninstall_changes(self):
        source = SCRIPT.read_text()
        install = source[source.index("confirm '确认开始安装/升级？'"):]
        self.assertLess(install.index("acquire_lock"), install.index("apt-get"))
        uninstall = source[source.index("if [[ $ACTION == uninstall ]]"):source.index("# An installed entry point")]
        self.assertLess(uninstall.index("acquire_lock"), uninstall.index("cp /etc/caddy"))

    def test_external_rejects_managed_nginx(self):
        source = SCRIPT.read_text()
        check = source[source.index("if [[ $MODE == external ]] &&"):source.index("OWNERS=$(ss")]
        with tempfile.TemporaryDirectory() as directory:
            config = Path(directory) / "nginx.conf"
            config.write_text("# xgift installer managed\n")
            result = subprocess.run(["bash", "-c", f'MODE=external; NGINX_FILE={str(config)!r}; die() {{ exit 7; }}; {check}'], capture_output=True, text=True)
            self.assertEqual(result.returncode, 7, result.stderr)

    def test_resource_checks(self):
        for space, mem, expected in [(3000000, 900000, 0), (1000000, 900000, 7), (3000000, 100000, 0)]:
            with self.subTest(space=space, mem=mem):
                result = self.run_function("check_resources", f'ROOT=/opt/xgift; df() {{ printf "header\\nfs 0 0 {space} 1 /\\n"; }}; awk() {{ if [[ $* == *meminfo* ]]; then printf "{mem}\\n"; else command awk "$@"; fi; }}; die() {{ exit 7; }}; log() {{ printf "%s\\n" "$*"; }}; check_resources')
                self.assertEqual(result.returncode, expected, result.stderr)
                if mem < 786432:
                    self.assertIn("OOM", result.stdout)

    def test_download_limits_and_state_staging(self):
        source = SCRIPT.read_text()
        self.assertIn("timeout 300 git", source)
        for line in source.splitlines():
            if "curl -fSL" in line:
                self.assertIn("--max-time", line)
                self.assertIn("--retry", line)
        self.assertLess(source.index('install -m 700 "$TMP/src/deploy/install.sh" "$TMP/install.sh"'),
                        source.index('install -m 600 "$TMP/install.conf" "$ROOT/install.conf"'))
        self.assertIn('REF=%s', source)
        self.assertIn('cp "$BACKUP/$name" "$ROOT/$name"', source)

    def test_upgrade_bootstrap_downloads_requested_ref(self):
        import re
        source = SCRIPT.read_text()
        start = source.index("# An installed entry point")
        end = source.index("log 'XGift 一键安装", start)
        bootstrap = source[start:end]
        with tempfile.TemporaryDirectory() as directory:
            fake = Path(directory) / "downloaded.sh"
            fake.write_text('#!/bin/bash\nprintf "%s\\n" "boot=$XGIFT_BOOTSTRAPPED args=$*"\n')
            body = (f'ACTION=upgrade; SOURCE_DIR=; DRY=0; ROOT=/opt/xgift; REF=release; REPO=owner/repo; '
                    f'ORIGINAL_ARGS=(--upgrade --yes); die() {{ exit 7; }}; '
                    f'curl() {{ printf "URL=%s\\n" "$*"; cp {str(fake)!r} "${{@: -1}}"; }}; {bootstrap}')
            result = subprocess.run(["bash", "-c", "set -Eeuo pipefail\n" + body], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("owner/repo/release/deploy/install.sh", result.stdout)
            self.assertIn("boot=1 args=--upgrade --yes --dir /opt/xgift --ref release", result.stdout)

    def test_caddy_conflict_uses_imported_host_routes(self):
        import json
        for host, expected in [("GIFT.EXAMPLE.COM", 0), ("*.example.com", 0), ("other.example.com", 1)]:
            payload = json.dumps({"apps": {"http": {"servers": {"srv": {"routes": [{"match": [{"host": [host]}]}]}}}}})
            result = self.run_function("caddy_domain_conflict", f'DOMAIN=gift.example.com; mktemp() {{ command mktemp; }}; managed_config() {{ :; }}; caddy() {{ printf "%s" {shlex.quote(payload)}; }}; die() {{ exit 7; }}; caddy_domain_conflict')
            self.assertEqual(result.returncode, expected, result.stderr)

    def test_publish_failure_leaves_old_state(self):
        source = SCRIPT.read_text()
        stage = source[source.index('# Publish both files'):source.index('CHANGED=0\nlog "安装完成')]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "root"
            root.mkdir()
            temporary = Path(directory) / "tmp"
            temporary.mkdir()
            src = temporary / "src" / "deploy"
            src.mkdir(parents=True)
            (src / "install.sh").write_text("new installer")
            state = root / "install.conf"
            state.write_text("old state")
            body = (f'ROOT={str(root)!r}; TMP={str(temporary)!r}; DOMAIN=new.example.com; PORT=8999; MODE=external; REF=release; '
                    'install() { if [[ ${@: -1} == "$ROOT/install.sh" ]]; then return 9; fi; command install "$@"; };\n' + stage)
            result = subprocess.run(["bash", "-c", "set -Eeuo pipefail\n" + body], capture_output=True, text=True)
            self.assertEqual(result.returncode, 9, result.stderr)
            self.assertEqual(state.read_text(), "old state")

    def test_rollback_restores_installer_and_state(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "root"
            root.mkdir()
            backup = Path(directory) / "backup"
            backup.mkdir()
            for name in ["install.conf", "install.sh"]:
                (root / name).write_text("new")
                (backup / name).write_text("old " + name)
            body = (f'ROOT={str(root)!r}; BACKUP={str(backup)!r}; CHANGED=1; HAD_SERVICE=0; NGINX_CHANGED=0; TMP=; '
                    'systemctl() { :; }; log() { :; }; rm() { [[ $* == *"/etc/"* ]] || command rm "$@"; }; '
                    'set +e; false; rollback')
            result = self.run_function("rollback", body)
            self.assertEqual(result.returncode, 1, result.stderr)
            for name in ["install.conf", "install.sh"]:
                self.assertEqual((root / name).read_text(), "old " + name)

    def test_proxy_mode_switches_reject_old_managed_configs(self):
        source = SCRIPT.read_text()
        self.assertIn("已有安装器管理的 Nginx 配置", source)
        self.assertIn("已有安装器管理的 Caddy 配置", source)

    def test_caddy_stage_preserves_relative_import_base_and_cleanup(self):
        source = SCRIPT.read_text()
        self.assertIn('CADDY_STAGE=$(mktemp /etc/caddy/.xgift-stage-XXXXXX)', source)
        self.assertIn('caddy validate --config "$CADDY_STAGE"', source)
        self.assertIn('cp "$CADDY_STAGE" /etc/caddy/Caddyfile', source)
        self.assertIn('rm -f -- "$CADDY_STAGE"', source)
        self.assertNotIn('"$TMP/caddy-next"', source)

    def test_nginx_literal_suffix_comments_and_multiline(self):
        cases = [('server_name *.example.com;', 'gift.exampleXcom', 1),
                 ('server_name other.example.com; # gift.example.com', 'gift.example.com', 1),
                 ('server_name\n other.example.com\n gift.example.com;', 'gift.example.com', 0),
                 ('server_name *.example.com;', 'gift.example.com', 0),
                 ('server_name .example.com;', 'example.com', 0),
                 ('server_name ~^gift\\.example\\.com$;', 'gift.example.com', 2)]
        for config, domain, expected in cases:
            with self.subTest(config=config, domain=domain):
                dump = '# configuration file /etc/nginx/other.conf:\n' + config + '\n'
                result = self.run_function('nginx_domain_conflict', f'NGINX_FILE=/etc/nginx/owned.conf; DOMAIN={shlex.quote(domain)}; nginx() {{ printf "%s" {shlex.quote(dump)}; }}; nginx_domain_conflict')
                self.assertEqual(result.returncode, expected, result.stderr)
                if expected == 2:
                    self.assertIn('正则', result.stderr)

    def test_private_parent_dryrun_diagnostic_only(self):
        with tempfile.TemporaryDirectory() as directory:
            result = self.run_cli('--dry-run', '--yes', '--domain', 'gift.example.com', '--dir', directory + '/absent')
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn('权限', result.stderr)
            self.assertFalse((Path(directory) / 'absent').exists())
            self.assertEqual(Path(directory).stat().st_mode & 0o777, 0o700)

    def test_dns_resolution_and_ipv6_notice(self):
        source = SCRIPT.read_text()
        self.assertIn('socket.getaddrinfo', source)
        for answer, expected in [('192.0.2.1', 0), ('2001:db8::1', 0), ('', 7)]:
            result = self.run_function('check_dns', f'DOMAIN=gift.example.com; python3() {{ printf "%s\\n" {shlex.quote(answer)}; }}; log() {{ printf "%s\\n" "$*" >&2; }}; die() {{ printf "%s\\n" "$*" >&2; exit 7; }}; check_dns')
            self.assertEqual(result.returncode, expected, result.stderr)
            if ':' in answer:
                self.assertIn('IPv6', result.stderr)
        self.assertLess(source.index('check_dns\n', source.index('OWNERS=$(ss')), source.index('GO_VERSION=$(awk'))

    def test_external_blocks_and_unknown_listener_confirmation(self):
        result = self.run_function('external_instructions', 'DOMAIN=gift.example.com; PORT=8999; external_instructions')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('reverse_proxy 127.0.0.1:8999', result.stderr)
        self.assertIn('proxy_pass http://127.0.0.1:8999', result.stderr)
        for interactive, yes, expected in [(0, 0, 7), (1, 0, 0), (1, 1, 7)]:
            result = self.run_function('choose_external', f'INTERACTIVE={interactive}; YES={yes}; OWNERS=unknown; log() {{ :; }}; confirm() {{ return 0; }}; die() {{ exit 7; }}; MODE=auto; choose_external; [[ $MODE == external ]]')
            self.assertEqual(result.returncode, expected, result.stderr)

    def test_dns_python_getaddrinfo_success_and_failure(self):
        import re
        source = SCRIPT.read_text()
        match = re.search(r"addresses=\$\(python3 -c '(.*?)' \"\$DOMAIN\"\)", source, re.S)
        self.assertIsNotNone(match)
        code = match.group(1)
        for raises, expected in [(False, 0), (True, 1)]:
            wrapper = ('import socket,sys\nfrom unittest.mock import patch\nsys.argv=["dns","gift.example.com"]\n'
                       + ('with patch("socket.getaddrinfo", side_effect=socket.gaierror("missing")):\n' if raises else
                          'with patch("socket.getaddrinfo", return_value=[(socket.AF_INET6,socket.SOCK_STREAM,6,"",("2001:db8::1",443,0,0)),(socket.AF_INET,socket.SOCK_STREAM,6,"",("192.0.2.1",443))]):\n')
                       + '    exec(' + repr(code) + ')\n')
            result = subprocess.run(['python3', '-c', wrapper], capture_output=True, text=True)
            self.assertEqual(result.returncode, expected, result.stderr)
            if not raises:
                self.assertIn('2001:db8::1', result.stdout)
                self.assertIn('192.0.2.1', result.stdout)
            else:
                self.assertIn('DNS解析失败', result.stderr)
        result = self.run_function('check_dns', 'DOMAIN=gift.example.com; python3() { return 1; }; die() { exit 7; }; log() { :; }; check_dns')
        self.assertEqual(result.returncode, 7)

    def test_unknown_listener_declined_does_not_change_mode(self):
        result = self.run_function('choose_external', 'INTERACTIVE=1; YES=0; OWNERS=unknown; MODE=auto; log() { :; }; confirm() { return 1; }; die() { exit 7; }; choose_external')
        self.assertEqual(result.returncode, 7)

    def test_nginx_unreadable_dump_is_not_no_conflict(self):
        result = self.run_function('nginx_domain_conflict', 'NGINX_FILE=/etc/nginx/owned.conf; DOMAIN=gift.example.com; nginx() { return 1; }; nginx_domain_conflict')
        self.assertEqual(result.returncode, 2)

    def test_lf(self):
        self.assertNotIn(b"\r", SCRIPT.read_bytes())


if __name__ == "__main__":
    unittest.main()
