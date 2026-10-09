#!/usr/bin/env bash
# XGift Linux installer. Run from a downloaded file, not a stdin pipe.
set -Eeuo pipefail
umask 077
REPO=yys9253462-gif/x_gift_bot
REF=main
ROOT=/opt/xgift
DOMAIN= PORT= MODE= ACTION=install SOURCE_DIR=
YES=0 DRY=0 DEPS=1
TMP= BACKUP= CHANGED=0 HAD_SERVICE=0
SELF=$(realpath "${BASH_SOURCE[0]}")
if [[ -f "$(dirname "$SELF")/install.conf" ]]; then ROOT=$(dirname "$SELF"); fi
log() { printf '%s\n' "$*" >&2; }
die() { log "错误：$*"; exit 1; }
usage() {
  cat <<'HELP'
XGift 交互式安装器（Debian/Ubuntu + systemd，amd64/arm64）
用法：bash install.sh [选项]
  --domain HOST          站点域名（不要带 https:// 或路径）
  --port PORT            本机监听端口；默认自动选空闲端口
  --https MODE           auto 自动选择；caddy/nginx 指定宿主服务；external 外部反代
  --dir PATH             安装目录，默认 /opt/xgift
  --ref REF              仓库分支或标签，默认 main
  --source-dir PATH      从已上传源码构建（离线源码/发布前验收）
  --yes                  非交互确认；必须指定或已有域名
  --dry-run              只检查并打印计划，不写文件、不装依赖
  --no-deps              不自动安装系统依赖
  --upgrade              重新构建并升级，失败恢复旧程序和服务配置
  --status               查看服务状态，不安装依赖
  --uninstall            移除服务及本安装器的反代块，保留目录和全部数据
  --help                 显示帮助
首次安装后打开 /setup，使用安装器显示的初始化密码完成配置。
启用真实自动付款需完成支付配置并修改 site.env 的付款开关后重启。
HELP
}
while (($#)); do
  case "$1" in
    --domain|--port|--https|--dir|--ref|--source-dir)
      (($# >= 2)) || die "$1 缺少参数"
      case "$1" in
        --domain) DOMAIN=$2;; --port) PORT=$2;; --https) MODE=$2;;
        --dir) ROOT=$2;; --ref) REF=$2;; --source-dir) SOURCE_DIR=$2;;
      esac; shift 2;;
    --yes) YES=1; shift;; --dry-run) DRY=1; shift;; --no-deps) DEPS=0; shift;;
    --upgrade|--status|--uninstall)
      [[ $ACTION == install ]] || die '操作参数不能组合'
      ACTION=${1#--}; shift;;
    --help|-h) usage; exit 0;; *) die "未知参数 $1；运行 --help 查看用法";;
  esac
done
[[ $ROOT =~ ^/[a-zA-Z0-9_/-]+$ && $ROOT != / && $ROOT != */../* && $ROOT != */.. ]] || die '安装目录必须是无空格的绝对路径，且不能包含 ..'
[[ $REF =~ ^[a-zA-Z0-9_./-]+$ && $REF != -* ]] || die '非法仓库版本'
if [[ -n $SOURCE_DIR ]]; then
  [[ $SOURCE_DIR == /* && -f $SOURCE_DIR/go.mod && -f $SOURCE_DIR/deploy/install.sh ]] || die '源码目录必须为完整源码的绝对路径'
fi
# Saved state supplies defaults only. Never execute a state file as shell code.
if [[ -f $ROOT/install.conf ]]; then
  while IFS='=' read -r key value; do
    case "$key" in
      DOMAIN) [[ -n $DOMAIN ]] || DOMAIN=$value;;
      PORT) [[ -n $PORT ]] || PORT=$value;;
      MODE) [[ -n $MODE ]] || MODE=$value;;
    esac
  done < "$ROOT/install.conf"
fi
MODE=${MODE:-auto}
[[ $MODE == auto || $MODE == caddy || $MODE == nginx || $MODE == external ]] || die '--https 只能是 auto、caddy、nginx 或 external'
NGINX_FILE=/etc/nginx/conf.d/xgift-installer.conf
NGINX_CHANGED=0
ask() {
  local answer
  if ((YES)) || [[ ! -t 0 ]]; then printf '%s' "$2"; return; fi
  printf '%s [%s]: ' "$1" "$2" >&2
  read -r answer || die '输入中断，安装已取消'
  printf '%s' "${answer:-$2}"
}
confirm() {
  local answer
  ((YES)) && return 0
  [[ -t 0 ]] || die '当前不是交互终端；请下载脚本后运行，或显式使用 --yes --domain HOST'
  while :; do
    printf '%s [y/N]: ' "$1" >&2
    read -r answer || return 1
    case "$answer" in
      y|Y|yes|是|确定) return 0;; ''|n|N|no|否|取消) return 1;;
      *) log '请输入 y（是）或 n（否）。';;
    esac
  done
}
managed_config() {
  [[ -f /etc/caddy/Caddyfile ]] || return 0
  awk '
    $0 == "# >>> xgift installer >>>" {inside=1; next}
    $0 == "# <<< xgift installer <<<" {inside=0; next}
    !inside {print}
  ' /etc/caddy/Caddyfile
}
rollback() {
  local rc=$?
  trap - EXIT INT TERM
  if ((rc != 0 && CHANGED)); then
    log '安装失败，正在恢复本次替换的程序及服务配置；数据和密码不回滚。'
    systemctl stop xgift >/dev/null 2>&1 || true
    if [[ -d $BACKUP/bin ]]; then cp -a "$BACKUP/bin/." "$ROOT/bin/"; fi
    if [[ -f $BACKUP/unit ]]; then cp "$BACKUP/unit" /etc/systemd/system/xgift.service; else rm -f /etc/systemd/system/xgift.service; fi
    if [[ -f $BACKUP/env ]]; then cp "$BACKUP/env" "$ROOT/site.env"; fi
    if [[ -f $BACKUP/caddy ]]; then cp "$BACKUP/caddy" /etc/caddy/Caddyfile; caddy validate --config /etc/caddy/Caddyfile >/dev/null 2>&1 && systemctl reload caddy || true; fi
    if ((NGINX_CHANGED)); then
      if [[ -f $BACKUP/nginx ]]; then cp "$BACKUP/nginx" "$NGINX_FILE"; else rm -f "$NGINX_FILE"; fi
      nginx -t && systemctl reload nginx || true
    fi
    systemctl daemon-reload || true
    if ((HAD_SERVICE)); then systemctl start xgift || true; fi
    log "恢复副本：$BACKUP；首次安装可能留下依赖、目录及密码，可直接重跑。"
  fi
  [[ -z $TMP ]] || rm -rf -- "$TMP"
  exit "$rc"
}
if [[ $ACTION == status ]]; then
  [[ -r $ROOT/install.conf ]] || die "无法读取 $ROOT/install.conf；尚未安装或需要 root 权限"
  log "网址：https://$DOMAIN；监听：127.0.0.1:$PORT；目录：$ROOT"
  systemctl --no-pager status xgift
  curl -fsS --max-time 5 "http://127.0.0.1:$PORT/healthz"
  exit
fi
if [[ $ACTION == uninstall ]]; then
  if ((DRY)); then log "计划：停用 xgift、摘除自有 Caddy 标记块；保留 $ROOT 全部数据"; exit; fi
  [[ $EUID == 0 ]] || die '请以 root 运行（sudo bash install.sh --uninstall）'
  [[ -f $ROOT/install.conf ]] || die "没有找到 $ROOT/install.conf"
  confirm '确认卸载服务？全部数据与密码仍会保留' || exit 0
  TMP=$(mktemp -d); trap rollback EXIT; trap 'exit 130' INT; trap 'exit 143' TERM
  if [[ -f /etc/caddy/Caddyfile ]] && grep -qF '# >>> xgift installer >>>' /etc/caddy/Caddyfile; then
    cp /etc/caddy/Caddyfile "$TMP/caddy"
    managed_config > "$TMP/next"
    cp "$TMP/next" /etc/caddy/Caddyfile
    if ! caddy validate --config /etc/caddy/Caddyfile || ! systemctl reload caddy; then
      cp "$TMP/caddy" /etc/caddy/Caddyfile
      systemctl reload caddy || true
      die '反代移除失败，原配置已恢复，未卸载服务'
    fi
  fi
  if [[ -f $NGINX_FILE ]] && grep -qF '# xgift installer managed' "$NGINX_FILE"; then
    cp "$NGINX_FILE" "$TMP/nginx"
    rm -f "$NGINX_FILE"
    if ! nginx -t || ! systemctl reload nginx; then
      cp "$TMP/nginx" "$NGINX_FILE"
      systemctl reload nginx || true
      die 'Nginx 配置移除失败，已恢复'
    fi
    rm -f /etc/letsencrypt/renewal-hooks/deploy/xgift-nginx.sh
  fi
  systemctl disable --now xgift
  rm -f /etc/systemd/system/xgift.service
  systemctl daemon-reload
  log "已卸载服务；$ROOT 中的数据、密码和安装器均保留，重装可继续使用。"
  exit
fi
log 'XGift 一键安装：自动准备程序和服务，随后在浏览器中完成账号与支付配置。'
[[ -t 0 ]] || log '当前不是交互终端；显式参数或保存的配置将作为默认值。'
DOMAIN=$(ask '请输入站点域名（DNS 应已指向本服务器）' "$DOMAIN")
[[ ${#DOMAIN} -le 253 && $DOMAIN =~ ^([a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$ ]] || die '请输入合法域名，不要带协议、端口或路径'
if [[ -n $PORT ]]; then
  [[ $PORT =~ ^[0-9]{1,5}$ ]] || die '端口必须为数字'
  PORT=$((10#$PORT))
  ((PORT >= 1024 && PORT <= 65535)) || die '端口范围为 1024–65535'
fi
[[ $(uname -s) == Linux || $DRY == 1 ]] || die '请在目标 Linux 服务器运行，不是在 Windows 电脑运行'
if [[ -z $PORT ]]; then
  PORT=8787
  if command -v ss >/dev/null; then
    USED=$(ss -ltnH | awk '{sub(/.*:/,"",$4); print $4}')
    if command -v docker >/dev/null; then USED+=$'\n'$(docker ps --format '{{.Ports}}' 2>/dev/null | grep -oE ':[0-9]+->' | tr -cd '0-9\n' || true); fi
    while grep -qx "$PORT" <<< "$USED"; do PORT=$((PORT+1)); ((PORT <= 65535)) || die '没有空闲端口'; done
  fi
fi
log "计划：版本 $REF；目录 $ROOT；域名 $DOMAIN；监听 127.0.0.1:$PORT；HTTPS $MODE"
log '将自动安装编译依赖，构建带 with_quic,with_utls 标签的两个程序，生成首次密码并启动 systemd。'
if ((DRY)); then log 'dry-run：未写文件，未安装依赖，未操作服务。'; exit 0; fi
[[ $EUID == 0 ]] || die '请以 root 运行；已有 sudo 则用 sudo bash install.sh，否则先 su -'
[[ -d /run/systemd/system ]] || die '需要使用 systemd 的 Linux 服务器'
command -v apt-get >/dev/null || die '当前安装器支持 Debian/Ubuntu（apt-get）'
confirm '确认开始安装/升级？' || exit 0
if ((DEPS)); then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl git build-essential python3 openssl iproute2 util-linux
fi
for dep in curl git gcc python3 openssl ss flock; do command -v "$dep" >/dev/null || die "缺少 $dep；重新运行并去掉 --no-deps"; done
mkdir -p "$ROOT"
chmod 755 "$ROOT"
exec 9>"$ROOT/.install.lock"
flock -n 9 || die '另一个安装器正在运行，请等待完成'
# Before downloads or service changes, reject non-owned installations and port conflicts.
if [[ -e /etc/systemd/system/xgift.service && ! -f $ROOT/install.conf ]]; then die '发现非本安装器管理的 xgift 服务，请先迁移，未覆盖'; fi
if ss -ltnH | awk '{sub(/.*:/,"",$4); print $4}' | grep -qx "$PORT"; then
  [[ -f $ROOT/install.conf ]] && grep -qx "PORT=$PORT" "$ROOT/install.conf" && systemctl is-active --quiet xgift || die "端口 $PORT 已占用"
fi
if [[ $MODE == external && -f /etc/caddy/Caddyfile ]] && grep -qF '# >>> xgift installer >>>' /etc/caddy/Caddyfile; then
  die '已存在本安装器管理的 HTTPS 配置；请保留 --https auto，或卸载服务后切换 external（数据保留）'
fi
OWNERS=$(ss -ltnpH '( sport = :80 or sport = :443 )')
if [[ $MODE == auto ]]; then
  if [[ -z $OWNERS ]]; then MODE=caddy
  elif [[ $OWNERS == *'"nginx"'* && $OWNERS != *'"caddy"'* ]]; then MODE=nginx
  elif [[ $OWNERS == *'"caddy"'* && $OWNERS != *'"nginx"'* ]]; then MODE=caddy
  else
    log "标准端口被其他服务占用：$OWNERS"
    die '不会停止已有服务。容器反代或其他程序请使用 --https external 接入原反代；必须释放标准端口才能新建公网 HTTPS。'
  fi
fi
if [[ $MODE == caddy ]]; then
  [[ -z $OWNERS || $OWNERS == *'"caddy"'* ]] || die '标准端口不是 Caddy 管理，不能强制接管'
  if ! command -v caddy >/dev/null; then
    [[ -z $OWNERS ]] || die '标准端口已占用，不能安装第二个网页服务器'
    ((DEPS)) || die '缺少 Caddy；去掉 --no-deps 或选择 --https external'
    DEBIAN_FRONTEND=noninteractive apt-get install -y caddy
  fi
  [[ -f /etc/caddy/Caddyfile ]] || die 'Caddy 配置不存在；请用 --https external'
  if managed_config | grep -Fq "$DOMAIN"; then die "域名 $DOMAIN 已出现在现有 Caddy 配置中；未覆盖"; fi
elif [[ $MODE == nginx ]]; then
  [[ -z $OWNERS || $OWNERS == *'"nginx"'* ]] || die '标准端口不是 Nginx 管理，不能强制接管'
  command -v nginx >/dev/null || die '未发现宿主 Nginx，不能接管容器 Nginx'
  nginx -t
  [[ ! -e $NGINX_FILE ]] || grep -qF '# xgift installer managed' "$NGINX_FILE" || die '目标 Nginx 文件不是安装器创建，未覆盖'
  if nginx -T 2>&1 | grep -E '^[[:space:]]*server_name' | grep -Fq "$DOMAIN" && [[ ! -f $NGINX_FILE ]]; then die '域名已存在于 Nginx 配置，未覆盖'; fi
  if ! command -v certbot >/dev/null; then
    ((DEPS)) || die '缺少 certbot，请去掉 --no-deps'
    DEBIAN_FRONTEND=noninteractive apt-get install -y certbot
  fi
fi
TMP=$(mktemp -d)
trap rollback EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ -n $SOURCE_DIR ]]; then
  [[ -d $SOURCE_DIR && -f $SOURCE_DIR/go.mod && -f $SOURCE_DIR/deploy/install.sh ]] || die '源码目录不完整'
  mkdir "$TMP/src"
  cp -a "$SOURCE_DIR/." "$TMP/src/"
else
  git clone --depth 1 --branch "$REF" "https://github.com/$REPO.git" "$TMP/src"
fi
GO_VERSION=$(awk '$1=="go" {gsub(/\r/,"",$2); print $2; exit}' "$TMP/src/go.mod")
[[ $GO_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 'go.mod 的 Go 版本格式不支持'
case $(uname -m) in x86_64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) die '仅支持 amd64/arm64';; esac
GO_HOME="$ROOT/toolchains/go$GO_VERSION"
if [[ ! -x $GO_HOME/bin/go ]]; then
  curl -fSL --retry 2 --connect-timeout 15 'https://go.dev/dl/?mode=json&include=all' -o "$TMP/go.json"
  SHA=$(python3 -c 'import json,sys; vs=json.load(open(sys.argv[1])); fs=[f for v in vs if v["version"]==sys.argv[2] for f in v["files"] if f["filename"]==sys.argv[3]]; assert len(fs)==1,"Go archive unavailable"; print(fs[0]["sha256"])' "$TMP/go.json" "go$GO_VERSION" "go$GO_VERSION.linux-$ARCH.tar.gz")
  curl -fSL --retry 2 --connect-timeout 15 "https://go.dev/dl/go$GO_VERSION.linux-$ARCH.tar.gz" -o "$TMP/go.tar.gz"
  printf '%s  %s\n' "$SHA" "$TMP/go.tar.gz" | sha256sum -c -
  mkdir -p "$ROOT/toolchains"
  tar -xzf "$TMP/go.tar.gz" -C "$TMP"
  mv "$TMP/go" "$GO_HOME"
fi
export PATH="$GO_HOME/bin:$PATH" CGO_ENABLED=1
# Bounded compiler parallelism for small VPS machines; never change host swap automatically.
export GOMAXPROCS=1 GOMEMLIMIT=384MiB
export GOPATH="$ROOT/build-cache/gopath" GOCACHE="$ROOT/build-cache/go-build"
mkdir "$TMP/bin"
(cd "$TMP/src"; go build -p 1 -tags with_quic,with_utls -o "$TMP/bin/xgift" ./cmd/xgift; go build -p 1 -tags with_quic,with_utls -o "$TMP/bin/xgift-web" ./cmd/xgift-web)
# Stage and validate the reverse proxy before touching the running application.
if [[ $MODE == caddy ]]; then
  managed_config > "$TMP/caddy-next"
  cat >> "$TMP/caddy-next" <<CADDY
# >>> xgift installer >>>
$DOMAIN {
    encode zstd gzip
    reverse_proxy 127.0.0.1:$PORT
}
# <<< xgift installer <<<
CADDY
  caddy validate --config "$TMP/caddy-next" --adapter caddyfile
fi
BACKUP="$ROOT/backups/$(date +%Y%m%d-%H%M%S)-$$"
mkdir -p "$BACKUP"
[[ ! -d $ROOT/bin ]] || cp -a "$ROOT/bin" "$BACKUP/bin"
[[ ! -f $ROOT/site.env ]] || cp "$ROOT/site.env" "$BACKUP/env"
[[ ! -f /etc/systemd/system/xgift.service ]] || cp /etc/systemd/system/xgift.service "$BACKUP/unit"
[[ ! -f /etc/caddy/Caddyfile ]] || cp /etc/caddy/Caddyfile "$BACKUP/caddy"
[[ ! -f $NGINX_FILE ]] || cp "$NGINX_FILE" "$BACKUP/nginx"
systemctl is-active --quiet xgift && HAD_SERVICE=1 || true
CHANGED=1
systemctl stop xgift || true
id xgift >/dev/null 2>&1 || useradd --system --home-dir "$ROOT" --shell /usr/sbin/nologin xgift
install -d -m 700 -o xgift -g xgift "$ROOT/data" "$ROOT/secrets"
for name in vault-password setup-password; do
  if [[ ! -e $ROOT/secrets/$name ]]; then openssl rand -hex 32 > "$ROOT/secrets/$name"; fi
  chmod 600 "$ROOT/secrets/$name"
  chown xgift:xgift "$ROOT/secrets/$name"
done
install -d -m 755 "$ROOT/bin"
install -m 755 "$TMP/bin/xgift" "$TMP/bin/xgift-web" "$ROOT/bin/"
# Preserve optional/custom settings on upgrade; replace only installation-owned keys.
if [[ -f $ROOT/site.env ]]; then
  awk '!/^XGIFT_(ORIGIN|LISTEN|DATA_DIR|PASSWORD_FILE|ADMIN_PASSWORD_FILE|SETUP_PASSWORD_FILE)=/' "$ROOT/site.env" > "$TMP/env"
else
  printf 'XGIFT_PAYMENTS_ENABLED=false\n' > "$TMP/env"
fi
cat >> "$TMP/env" <<ENV
XGIFT_ORIGIN=https://$DOMAIN
XGIFT_LISTEN=127.0.0.1:$PORT
XGIFT_DATA_DIR=$ROOT/data
XGIFT_PASSWORD_FILE=$ROOT/secrets/vault-password
XGIFT_ADMIN_PASSWORD_FILE=$ROOT/secrets/admin-password
XGIFT_SETUP_PASSWORD_FILE=$ROOT/secrets/setup-password
ENV
install -m 600 "$TMP/env" "$ROOT/site.env"
sed -e "s|/opt/xgift|$ROOT|g" -e "s|/etc/xgift/site.env|$ROOT/site.env|" -e "s|/var/lib/xgift|$ROOT/data $ROOT/secrets|" "$TMP/src/deploy/xgift.service" > /etc/systemd/system/xgift.service
systemctl daemon-reload
systemctl enable --now xgift
healthy=0
for ((i=0;i<30;i++)); do
  if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/healthz" >/dev/null; then healthy=1; break; fi
  sleep 2
done
((healthy)) || { journalctl -u xgift -n 30 --no-pager >&2; die '服务未通过健康检查'; }
if [[ $MODE == caddy ]]; then
  cp "$TMP/caddy-next" /etc/caddy/Caddyfile
  systemctl enable --now caddy
  systemctl reload caddy
elif [[ $MODE == nginx ]]; then
  NGINX_CHANGED=1
  install -d -m 755 "$ROOT/acme/.well-known/acme-challenge"
  chmod 755 "$ROOT/acme" "$ROOT/acme/.well-known"
  cat > "$NGINX_FILE" <<NGINX
# xgift installer managed
server {
    listen 80;
    server_name $DOMAIN;
    location /.well-known/acme-challenge/ { root $ROOT/acme; }
    location / { proxy_pass http://127.0.0.1:$PORT; proxy_set_header Host \$host; }
}
NGINX
  nginx -t
  systemctl reload nginx
  certbot certonly --webroot -w "$ROOT/acme" -d "$DOMAIN" --non-interactive --agree-tos --register-unsafely-without-email --cert-name "xgift-$DOMAIN"
  cat >> "$NGINX_FILE" <<NGINX
server {
    listen 443 ssl;
    server_name $DOMAIN;
    ssl_certificate /etc/letsencrypt/live/xgift-$DOMAIN/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/xgift-$DOMAIN/privkey.pem;
    location / {
        proxy_pass http://127.0.0.1:$PORT;
        proxy_set_header Host \$host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$remote_addr;
        proxy_buffering off;
        proxy_read_timeout 120s;
    }
}
NGINX
  # Keep ACME HTTP validation available, redirect only ordinary web traffic.
  sed -i 's|location / { proxy_pass http://127.0.0.1:[0-9]*; proxy_set_header Host \$host; }|location / { return 308 https://\$host\$request_uri; }|' "$NGINX_FILE"
  nginx -t
  systemctl reload nginx
  install -d /etc/letsencrypt/renewal-hooks/deploy
  printf '#!/bin/sh\nnginx -t && systemctl reload nginx\n' > /etc/letsencrypt/renewal-hooks/deploy/xgift-nginx.sh
  chmod 755 /etc/letsencrypt/renewal-hooks/deploy/xgift-nginx.sh
  systemctl enable --now certbot.timer
fi
if [[ $MODE != external ]]; then
  tls_ok=0
  for ((i=0;i<30;i++)); do
    if curl -fsS --max-time 8 "https://$DOMAIN/healthz" >/dev/null; then tls_ok=1; break; fi
    sleep 2
  done
  ((tls_ok)) || die '公网 HTTPS 健康检查失败：检查 DNS、云安全组 80/443、AAAA 记录及 CDN 回源；已触发程序/配置回滚'
fi
printf 'DOMAIN=%s\nPORT=%s\nMODE=%s\n' "$DOMAIN" "$PORT" "$MODE" > "$ROOT/install.conf"
install -m 700 "$TMP/src/deploy/install.sh" "$ROOT/install.sh"
CHANGED=0
log "安装完成。下一步：打开 https://$DOMAIN/setup，将初始化密码填入网页向导。"
if [[ ! -s $ROOT/secrets/admin-password ]]; then
  log "初始化密码：$(< "$ROOT/secrets/setup-password")"
else log '已有管理员配置已保留，请使用原管理员账号。'; fi
if [[ $MODE == external ]]; then log "请先把 https://$DOMAIN 反代到 http://127.0.0.1:$PORT；不要公开暴露此本机端口。"; fi
log '网页向导完成后运行 systemctl restart xgift；保管库密码与 data 目录请一起备份。'
log "以后：bash $ROOT/install.sh --status | --upgrade | --uninstall"
log "旧程序备份：$BACKUP。真实付款默认关闭，配置完成后修改 $ROOT/site.env 再重启。"
