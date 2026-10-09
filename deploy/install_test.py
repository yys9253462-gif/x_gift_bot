"""Installer CLI regression checks; no root, downloads or system changes."""
import os
import re
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
        return subprocess.run(["bash", "-c", "set -Eeuo pipefail\n" + self.extract_function(name) + "\n" + body],
                              capture_output=True, text=True)

    def extract_function(self, name):
        """从 install.sh 里取出一个顶层函数定义。

        按缩进判定结束：顶层 ``}`` 之后的第一行若不以空白或 ``}`` 开头，函数即
        结束。这种写法会在函数体含 here-doc 时出错——here-doc 正文可能顶格出现
        ``}``，被误判为函数结尾。所以先扫出函数体内的 here-doc 区间，落在区间
        内的行不参与结束判定。
        """
        import re
        source = SCRIPT.read_text()
        start = source.index(f"{name}() {{")
        lines = source[start:].splitlines()
        heredocs = []  # 待匹配的 here-doc 结束标记
        for index, line in enumerate(lines):
            if index and not heredocs and line.startswith("}") and line != "}":
                continue
            if index and not heredocs and line == "}":
                return "\n".join(lines[: index + 1]) + "\n"
            # <<、<<-、<<'EOF'、<<"EOF" 形式；只跟踪标记本身，正文原样跳过。
            for match in re.finditer(r"<<-?\s*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\1", line):
                heredocs.append(match.group(2))
            if heredocs and line.strip() == heredocs[0]:
                heredocs.pop(0)
        raise AssertionError(f"missing testable function {name}")

    def test_long_waits_report_progress(self):
        """下载/编译期间必须有进度输出。

        用户反复以为"卡住了"然后中断安装，比安装真的失败更伤。凡是能等几十秒
        以上的步骤都要起 ticker，并写明已等待多久。
        """
        source = SCRIPT.read_text()
        for helper in ("progress_start()", "progress_stop()"):
            self.assertIn(helper, source)
        # 只认顶层函数定义，避免把注释里的名字当实现。
        self.assertIn("\nprogress_start() {", source)
        self.assertIn("\nprogress_stop() {", source)

        # 这些是实际会长时间无输出的点，每处都必须配对 start/stop。
        for label, scope in [("预编译下载", ("download_prebuilt() {", "\n}\n", "progress_start")),
                             ("编译", ("plan_build_parallelism", "log \"编译完成", "progress_start")),
                             ("源码克隆", ("elif download_prebuilt; then", "GO_VERSION=$(awk", "progress_start"))]:
            begin = source.index(scope[0])
            end = source.index(scope[1], begin)
            self.assertIn("progress_start", source[begin:end], f"{label} 缺少进度输出")
        # 每个 start 都要有 stop 兜底，否则 ticker 会在后续输出里继续插行。
        # stop 可以多于 start（例如"成功路径 stop 一次、失败路径再 stop 一次"
        # 是幂等的），但绝不能少于 start——少一个就会留下野 ticker。
        def call_sites(name):
            pattern = re.compile(r"^[ \t]*" + name + r"[ \t]+[^-]|^[ \t]*" + name + r"[ \t]*$", re.M)
            return [m.group(0) for m in pattern.finditer(source) if "() {" not in m.group(0)]
        starts, stops = call_sites("progress_start"), call_sites("progress_stop")
        self.assertGreaterEqual(len(stops), len(starts),
                                f"stop 少于 start，会留下野 ticker：start={len(starts)} stop={len(stops)}")
        self.assertGreaterEqual(len(starts), 4, "长等待步骤的进度打点太少")

        # ticker 只在 stderr 是终端时启用；重定向到日志文件时不该被噪声淹没。
        definition = self.extract_function('progress_start') + "\n" + self.extract_function('progress_stop')
        body = ("progress_supported() { return 0; }; "
                "progress_start '测试中' 1; sleep 2; progress_stop; echo stopped")
        result = subprocess.run(["bash", "-c", "set -Eeuo pipefail\n" + definition + "\n" + body],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("测试中", result.stderr)
        self.assertIn("已等待", result.stderr)
        self.assertIn("stopped", result.stdout)

        # 非终端（重定向）时不打点。
        quiet = subprocess.run(
            ["bash", "-c", "set -Eeuo pipefail\n" + definition + "\n"
             "progress_supported() { return 1; }; progress_start 'quiet' 1; sleep 1; progress_stop; echo done"],
            capture_output=True, text=True)
        self.assertEqual(quiet.returncode, 0, quiet.stderr)
        self.assertNotIn("quiet", quiet.stderr)

    def test_shared_progress_ticker_is_replaced(self):
        """连开两次 progress_start 不能留下两个 ticker。"""
        body = ("progress_supported() { return 0; }; "
                "progress_start 'first' 1; progress_start 'second' 1; sleep 2; "
                "[[ -n $PROGRESS_PID ]] || { echo 'PID 丢失'; exit 9; }; "
                "progress_stop; [[ -z $PROGRESS_PID ]] || { echo '未清空'; exit 8; }; echo ok")
        definition = self.extract_function('progress_start') + "\n" + self.extract_function('progress_stop')
        result = subprocess.run(["bash", "-c", "set -Eeuo pipefail\n" + definition + "\n" + body],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("ok", result.stdout)
        self.assertIn("second", result.stderr)
        self.assertNotIn("first", result.stderr)

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
        self.assertLess(source.index('install -m 700 "$TMP_ASSETS/install.sh" "$TMP/install.sh"'),
                        source.index('install -m 600 "$TMP/install.conf" "$ROOT/install.conf"'))
        self.assertIn('REF=%s', source)
        self.assertIn('cp "$BACKUP/$name" "$ROOT/$name"', source)

    def test_upgrade_bootstrap_prefers_newest_installer(self):
        """自举必须优先找"修好的"安装器，不能锚在用户当前版本上。

        踩过的坑：曾经优先用 install.conf 里记的 REF（"上次装成功的版本"），
        结果用户停在 v0.1.0、而 v0.1.0 恰好冻结着带 bug 的安装器，于是每次
        升级都把旧安装器拉回来，原地复现同一个失败——自举完全失去意义。

        这里只做结构断言，不去复刻整套下载逻辑：行为验证已经由真机验收覆盖
        （v0.1.0 → v0.2.0 的实际升级），在测试里重复实现一遍 curl 桩只会
        把实现细节抄进测试，反而更脆。
        """
        source = SCRIPT.read_text()
        start = source.index("# An installed entry point")
        end = source.index("log 'XGift 一键安装", start)
        bootstrap = source[start:end]

        # 必须有"安装器是否合格"的判据，且判据要认识当前路径约定。
        self.assertIn("installer_looks_current() {", bootstrap)
        self.assertIn("grep -q 'TMP_ASSETS'", bootstrap)

        # 候选顺序：main（修复先合进 main）必须排在最新发布 tag 之前，
        # 而且都不能是 install.conf 里的旧 REF。
        candidates = re.search(r"for candidate in ([^;]+);", bootstrap)
        self.assertIsNotNone(candidates)
        order = candidates.group(1)
        self.assertIn("main", order)
        self.assertIn("UPDATE_LATEST", order)
        self.assertLess(order.index("main"), order.index("UPDATE_LATEST"))
        # 不能把 install.conf 读出来的旧 REF 放在前面。
        self.assertNotIn("UPDATE_REF", bootstrap)

        # 必须查最新发布 tag，并把结果纳入候选。
        self.assertIn("UPDATE_LATEST=$(latest_release_ref)", bootstrap)

        # 每个候选都必须过一次合格性检查；不合格的不能拿去重跑。
        self.assertIn('&& installer_looks_current "$UPDATE_TMP/install.sh"', bootstrap)

        # 全都不合格时退回当前文件，而不是用带 bug 的旧版本。
        self.assertIn('bash "$SELF"', bootstrap)

    def test_stage_assets_replaces_stale_installer(self):
        """从 tag 拿到的安装器若是旧版本，不能写进 $ROOT 让用户下次踩坑。"""
        source = SCRIPT.read_text()
        stage = source[source.index("stage_assets() {"):source.index("if ((DRY == 0)); then\n  stage_assets")]
        self.assertIn("bash -n", stage, "必须做语法检查")
        self.assertIn("grep -q 'TMP_ASSETS'", stage, "必须验证路径约定")
        self.assertIn('cp "$SELF"', stage, "旧版本应替换为当前运行的安装器")

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
            src = temporary / "assets"
            src.mkdir(parents=True)
            (src / "install.sh").write_text("new installer")
            state = root / "install.conf"
            state.write_text("old state")
            body = (f'ROOT={str(root)!r}; TMP={str(temporary)!r}; TMP_ASSETS={str(src)!r}; DOMAIN=new.example.com; PORT=8999; MODE=external; REF=release; '
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
        # 取不到本机公网 IP 时必须跳过比对而不是拦人——多宿主/CDN/NAT 环境下
        # 「解析 IP != 本机网卡 IP」是正常状态，硬拦会误伤可用环境。
        no_ip = ('DOMAIN=gift.example.com; python3() { printf "%s\\n" 192.0.2.1; }; '
                 'local_public_ips() { return 0; }; log() { printf "%s\\n" "$*" >&2; }; '
                 'die() { printf "%s\\n" "$*" >&2; exit 7; }; check_dns')
        result = self.run_function('check_dns', no_ip)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('跳过', result.stderr)
        for answer, expected in [('192.0.2.1', 0), ('2001:db8::1', 0), ('', 7)]:
            probe = (f'DOMAIN=gift.example.com; python3() {{ printf "%s\\n" {shlex.quote(answer)}; }}; '
                     'local_public_ips() { printf "%s\\n" 192.0.2.1; }; '
                     'log() { printf "%s\\n" "$*" >&2; }; '
                     'die() { printf "%s\\n" "$*" >&2; exit 7; }; check_dns')
            result = self.run_function('check_dns', probe)
            self.assertEqual(result.returncode, expected, result.stderr)
            if ':' in answer:
                self.assertIn('IPv6', result.stderr)
        # 解析地址与本机不符时只警告，不能中断安装。
        mismatch = ('DOMAIN=gift.example.com; python3() { printf "%s\\n" 203.0.113.9; }; '
                    'local_public_ips() { printf "%s\\n" 198.51.100.7; }; '
                    'log() { printf "%s\\n" "$*" >&2; }; '
                    'die() { printf "%s\\n" "$*" >&2; exit 7; }; check_dns')
        result = self.run_function('check_dns', mismatch)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('警告', result.stderr)
        self.assertIn('203.0.113.9', result.stderr)
        self.assertIn('198.51.100.7', result.stderr)
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

    def test_build_parallelism_scales_with_machine(self):
        """按机器规格选并行度：小机保持单任务，大机不再被硬编码成单核。"""
        source = SCRIPT.read_text()
        # 旧写法在执行路径上把 1 GiB 测试机的限制写死，所有机器都按单核编译。
        # 小机分支里仍应保留 GOMAXPROCS=1，所以只断言执行路径不再硬编码。
        main_path = source[source.index("export PATH="):source.index("# Stage and validate the reverse proxy")]
        self.assertNotIn("GOMAXPROCS=1", main_path)
        self.assertNotIn("GOMEMLIMIT=384MiB", main_path)
        self.assertNotIn("go build -p 1 -tags", source)
        self.assertIn('go build -p "$BUILD_JOBS" -tags', main_path)
        self.assertIn("MemTotal", source)
        self.assertIn("nproc", source)

        def parallel_jobs(memtotal_kb, cpus):
            # awk is shadowed so the decision reads a fixture instead of the real
            # /proc/meminfo; the installer itself must not gain a test hook.
            body = (f'awk() {{ echo {memtotal_kb}; }}; nproc() {{ echo {cpus}; }}; '
                    'BUILD_JOBS=; die() { exit 7; }; log() { :; }; '
                    'plan_build_parallelism; echo "jobs=$BUILD_JOBS memlimit=$GOMEMLIMIT procs=$GOMAXPROCS"')
            return self.run_function('plan_build_parallelism', body)

        # 1.5 GiB 以上放开到核数：3.8 GB / 3 核的机器不应再按单核编译。
        big = parallel_jobs(3900000, 3)
        self.assertEqual(big.returncode, 0, big.stderr)
        self.assertIn("jobs=3", big.stdout)
        self.assertIn("procs=3", big.stdout)
        # 1 GiB 上下折中为 2。
        mid = parallel_jobs(1048576, 8)
        self.assertEqual(mid.returncode, 0, mid.stderr)
        self.assertIn("jobs=2", mid.stdout)
        # 小机仍然单任务，不能因为放开并行把 945 MB 测试机 OOM 掉。
        small = parallel_jobs(945000, 2)
        self.assertEqual(small.returncode, 0, small.stderr)
        self.assertIn("jobs=1", small.stdout)
        self.assertIn("memlimit=384MiB", small.stdout)
        # 核数上限 8，避免在 64 核机器上把内存打爆。
        huge = parallel_jobs(64000000, 64)
        self.assertIn("jobs=8", huge.stdout)

    def test_jobs_override_is_validated(self):
        def run(override):
            return self.run_function(
                'plan_build_parallelism',
                'nproc() { echo 3; }; BUILD_JOBS=; die() { exit 7; }; log() { :; }; '
                f'JOBS_OVERRIDE={override!r}; plan_build_parallelism; echo "jobs=$BUILD_JOBS"')

        for bad in ["0", "-1", "abc"]:
            self.assertEqual(run(bad).returncode, 7, f"--jobs {bad!r} should be rejected")
        good = run("6")
        self.assertEqual(good.returncode, 0, good.stderr)
        self.assertIn("jobs=6", good.stdout)
        # 空值在参数解析阶段就必须报错：--jobs "" 被静默忽略等于用户以为
        # 设了并行度，实际仍按自动值编译。
        result = self.run_cli("--dry-run", "--yes", "--dir", "/opt/xgift-jobs-test", "--jobs", "")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("--jobs", result.stderr)

    def test_goproxy_probes_before_choosing(self):
        """不能无条件用国内镜像：同一台美国机器上 goproxy.cn 比默认慢 19 倍。

        探测必须验证「完整下载成功」，而不只是量首字节延迟——实测遇到过
        延迟 0.1s 但下到一半断流的源，那种情况下安装会卡在半路。
        """
        source = SCRIPT.read_text()
        self.assertIn("goproxy.cn", source)
        self.assertIn("proxy.golang.org", source)
        self.assertIn("time_total", source)
        # 探测要限时，否则一个吊死的代理会让安装停在这里。
        self.assertIn("--connect-timeout", source)

        def run(env, override=None, default_time=None, cn_time=None):
            # curl is shadowed so each candidate can be given a synthetic latency.
            # 探测函数 probe_go_proxy 是独立顶层函数，不在 setup_go_proxy 的
            # 抽取范围内，所以这里直接 shadow 它：成功时回显耗时，失败时非零。
            stub = "probe_go_proxy() { case \"$1\" in "
            for needle, value in [("proxy.golang.org", default_time), ("goproxy.cn", cn_time)]:
                if value is not None:
                    stub += f'*{needle}*) echo {value}; return 0;; '
            stub += "esac; return 1; }; "
            body = (f"GOPROXY={env}; log() {{ :; }}; GOPROXY_PROBES_STUB=1; "
                    + (f"GOPROXY_OVERRIDE={override!r}; " if override is not None else "GOPROXY_OVERRIDE=; ")
                    + stub
                    + "GO_PROXY_PROBES=('https://proxy.golang.org|x' 'https://goproxy.cn|x'); "
                    + "setup_go_proxy; echo \"proxy=${GOPROXY:-<unset>}\"")
            return self.run_function('setup_go_proxy', body)

        # 默认线路快：保持默认，不要被国内镜像拖慢。
        fast = run("''", default_time="0.08", cn_time="1.56")
        self.assertEqual(fast.returncode, 0, fast.stderr)
        self.assertIn("proxy=https://proxy.golang.org,direct", fast.stdout)

        # 默认线路慢（国内常见）：切到 goproxy.cn，不走单点。
        slow_default = run("''", default_time="9.0", cn_time="0.46")
        self.assertEqual(slow_default.returncode, 0, slow_default.stderr)
        self.assertIn("proxy=https://goproxy.cn,direct", slow_default.stdout)

        # 两个都慢也仍然选实测更快的那条，同时保留 direct 兜底。
        all_slow = run("''", default_time="9.0", cn_time="4.0")
        self.assertIn("proxy=https://goproxy.cn,direct", all_slow.stdout)

        # 只有国内镜像可达：也要能切过去。
        cn_only = run("''", cn_time="0.46")
        self.assertIn("proxy=https://goproxy.cn,direct", cn_only.stdout)

        # 探测未完成（curl 失败）的源不能因为"延迟低"被选中。
        # 这里 default 与 cn 都探测失败，应退回直连而不是硬编码某个代理。
        neither = run("''")
        self.assertEqual(neither.returncode, 0, neither.stderr)
        self.assertIn("proxy=direct", neither.stdout)

        # --goproxy off 必须禁用代理走直连；显式 --goproxy URL 必须原样生效。
        off = run("'https://ignored.example'", override="off", default_time="0.1")
        # --goproxy off 必须真正关闭代理：环境里的 GOPROXY 也不能漏网。
        # 旧实现只"不设置"，继承来的值照样生效，用户要求直连却被走了代理。
        off = run("https://ignored.example", override="off", default_time="0.1")
        self.assertEqual(off.returncode, 0, off.stderr)
        self.assertNotIn("ignored.example", off.stdout)

        # 显式 --goproxy URL 原样生效，且不触发探测。
        explicit = run("''", override="https://goproxy.cn", default_time="9.0")
        self.assertEqual(explicit.returncode, 0, explicit.stderr)
        self.assertIn("proxy=https://goproxy.cn", explicit.stdout)

        # 用户已设置 GOPROXY 时不得覆盖。
        keep = run("https://internal.example/proxy", default_time="9.0", cn_time="0.46")
        self.assertIn("proxy=https://internal.example/proxy", keep.stdout)

    def test_build_flags_are_documented_and_forwarded(self):
        source = SCRIPT.read_text()
        self.assertIn("--goproxy", source)
        self.assertIn("--jobs", source)
        # 升级重入必须带上原参数，否则 --jobs/--goproxy 会在 bootstrap 时丢失。
        self.assertIn('"${ORIGINAL_ARGS[@]}" --dir "$ROOT" --ref "$REF"', source)

    def test_prebuilt_download_verifies_checksum(self):
        """预编译产物必须校验 SHA256；不匹配要拒绝且不留下任何二进制。"""
        import hashlib
        source = SCRIPT.read_text()
        self.assertIn("download_prebuilt", source)
        self.assertIn("SHA256SUMS", source)
        self.assertIn("sha256sum", source)

        good = b"#!/bin/sh\necho xgift\n"
        digest = hashlib.sha256(good).hexdigest()
        sums = (f"{digest}  xgift-linux-amd64\n"
                f"{digest}  xgift-web-linux-amd64\n")

        def run(sums_body, payload, uname_out="x86_64", curl_status=0):
            # curl is shadowed: the checksum manifest and both binaries come from
            # fixtures, so the verification path is exercised without network.
            # log() must reach stderr: the tamper case is only observable there.
            with tempfile.TemporaryDirectory() as directory:
                d = Path(directory)
                (d / "sums").write_text(sums_body)
                (d / "payload").write_bytes(payload)
                body = f'''
uname() {{ echo {uname_out}; }}
TMP={str(d)!r}/tmp
mkdir -p "$TMP"
log() {{ printf "%s\\n" "$*" >&2; }}
curl() {{
  local u=${{@: -1}}
  case "$u" in
    */SHA256SUMS) cp {str(d / "sums")!r} "$u" 2>/dev/null || return 1;;
    *) cp {str(d / "payload")!r} "$u" 2>/dev/null || return 1;;
  esac
  return {curl_status}
}}
REPO=owner/repo; REF=v1.0.0
if download_prebuilt; then
  echo "result=ok"
else
  echo "result=fallback"
fi
# 未通过校验的二进制绝不能留在 TMP 里被后续流程用到。
left=$(find "$TMP" -type f 2>/dev/null | wc -l)
echo "files=$left"
'''
                # Must go through run_function: it injects the real
                # download_prebuilt so the verification path is the shipped one.
                return self.run_function("download_prebuilt", body)

        # 校验通过：两个二进制就位。
        ok = run(sums, good)
        self.assertEqual(ok.returncode, 0, ok.stderr)
        self.assertIn("result=ok", ok.stdout)
        self.assertIn("files=2", ok.stdout)

        # 校验不匹配：必须回退，且不能留下任何未验证的二进制。
        bad = run(sums, b"#!/bin/sh\necho tampered\n")
        self.assertEqual(bad.returncode, 0, bad.stderr)
        self.assertIn("result=fallback", bad.stdout)
        self.assertIn("不匹配", bad.stderr)
        self.assertIn("files=0", bad.stdout)

        # 清单里没有本架构的记录：回退，不猜文件名。
        missing = run(f"{digest}  xgift-linux-arm64\n", good)
        self.assertIn("result=fallback", missing.stdout)

        # 清单本身不可达：回退，并且必须说明原因。
        # 用户看到的下一个动作是满屏 go: downloading，不给原因他会以为
        # "这个项目必须编译"——其实多数情况只是 ref 取错了。
        unreachable = self.run_function(
            "download_prebuilt",
            'uname() { echo x86_64; }\n'
            'log() { printf "%s\\n" "$*" >&2; }\n'
            'curl() { return 22; }\n'
            'REPO=owner/repo; REF=v1.0.0\n'
            'TMP=$(mktemp -d)\n'
            'if download_prebuilt; then echo "result=ok"; else echo "result=fallback"; fi\n')
        self.assertIn("result=fallback", unreachable.stdout)
        self.assertIn("原因", unreachable.stderr)
        self.assertIn("编译", unreachable.stderr)

        # REF=main 一定要点明"main 上没有 Release 产物"，这是最常见的误用。
        main_ref = self.run_function(
            "download_prebuilt",
            'uname() { echo x86_64; }\n'
            'log() { printf "%s\\n" "$*" >&2; }\n'
            'curl() { local a; for a in "$@"; do case "$a" in *%{http_code}*) echo 404; return 0;; esac; done; return 22; }\n'
            'REPO=owner/repo; REF=main\n'
            'TMP=$(mktemp -d)\n'
            'download_prebuilt || true\n')
        self.assertIn("main", main_ref.stderr)
        self.assertIn("--ref", main_ref.stderr)
        self.assertIn("404", main_ref.stderr)

        # 不支持的架构：回退，不尝试下载。
        other = run(sums, good, uname_out="riscv64")
        self.assertIn("result=fallback", other.stdout)

    def test_installer_prefers_prebuilt_then_falls_back(self):
        """主流程顺序：先试预编译，失败才走编译；显式参数直接编译。"""
        source = SCRIPT.read_text()
        prebuilt_at = source.index("if download_prebuilt; then")
        compile_at = source.index("START_BUILD=$(date +%s)")
        self.assertLess(prebuilt_at, compile_at, "预编译必须先于编译尝试")
        # 三条直接编译的旁路都要在。
        self.assertIn("--build-from-source", source)
        self.assertIn("((FROM_SOURCE))", source)
        self.assertIn("elif [[ -n $SOURCE_DIR ]]", source)
        # 编译前必须已经确定 BUILD_JOBS 与 GOPROXY，不能因跳过而留下未定义变量。
        self.assertIn("plan_build_parallelism", source)
        self.assertIn("setup_go_proxy", source)

    def test_lf(self):
        self.assertNotIn(b"\r", SCRIPT.read_bytes())

    def test_deploy_assets_do_not_depend_on_source_checkout(self):
        """预编译路径没有源码目录，部署资产不能改成从 $TMP/src 读。

        回归用例：曾经 xgift.service 与 install.sh 都从 $TMP/src/deploy 取，
        而预编译路径从不创建 $TMP/src —— 结果是二进制下载校验全过、却在装
        systemd 单元时 sed 报「No such file or directory」并触发整轮回滚。
        """
        source = SCRIPT.read_text()
        # 两处消费点必须走 $TMP_ASSETS，不能是 $TMP/src/deploy。
        self.assertIn('"$TMP_ASSETS/xgift.service"', source)
        self.assertIn('"$TMP_ASSETS/install.sh"', source)
        self.assertNotIn('"$TMP/src/deploy/xgift.service"', source)
        self.assertNotIn('"$TMP/src/deploy/install.sh"', source)
        # $TMP/src 只能出现在编译分支内部（在 stage_assets 定义之前）。
        stage_at = source.index("stage_assets() {")
        for line in source[:stage_at].splitlines():
            if "TMP/src" in line and not line.lstrip().startswith("#"):
                self.assertIn("PREBUILT", source[:stage_at],
                              f"$TMP/src 在编译分支外被使用: {line.strip()}")
                break

    def test_stage_assets_handles_both_paths(self):
        """stage_assets 必须为预编译路径单独取 deploy/*，且缺失时报硬错误。"""
        source = SCRIPT.read_text()
        block = source[source.index("stage_assets() {"):source.index("stage_assets() {") + 1300]
        self.assertIn("raw/$REF/deploy", block, "预编译路径必须从同 tag 取 deploy/*")
        self.assertIn("for asset in xgift.service install.sh", block)
        self.assertIn("--retry", block, "取部署资产必须带重试")
        # 取不到必须 die，不能静默沿用旧单元。
        self.assertIn("stage_assets || die", source)

    def test_release_workflow_shape(self):
        """发布工作流必须仍然产出安装器期望的资产名，并按架构原生构建。"""
        path = SCRIPT.parent.parent / ".github" / "workflows" / "release.yml"
        self.assertTrue(path.exists(), "缺少 release.yml，预编译路径会永远回退")
        text = path.read_text()
        # 安装器按这些名字拼 URL，改名就会下载失败。
        for asset in ("xgift-linux-", "xgift-web-linux-", "SHA256SUMS"):
            self.assertIn(asset, text, f"工作流不再产出 {asset}")
        # CGO 依赖使得纯 Go 交叉编译不可行，必须两个原生 runner。
        self.assertIn("ubuntu-24.04-arm", text)
        self.assertIn("ubuntu-24.04", text)
        # 静态链接自检：判据必须是 ldd 的退出码，不能是输出文本匹配。
        self.assertIn('if ldd "$f" >/dev/null 2>&1; then', text,
                      "静态链接自检必须用 ldd 退出码判定")
        # tag 触发。
        self.assertIn("tags:", text)
        # YAML 缩进一旦被破坏就整体不工作，这里做一次解析兜底。
        try:
            import yaml
        except ImportError:
            self.skipTest("PyYAML 不可用")
        parsed = yaml.safe_load(text)
        build = parsed["jobs"]["build"]
        self.assertEqual(parsed["permissions"]["contents"], "write")
        self.assertEqual(len(build["strategy"]["matrix"]["include"]), 2)
        self.assertIn("publish", parsed["jobs"])


if __name__ == "__main__":
    unittest.main()
