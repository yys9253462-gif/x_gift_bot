#!/usr/bin/env bash
# XGift Linux installer. Run from a downloaded file, not a stdin pipe.
set -Eeuo pipefail
umask 077
REPO=yys9253462-gif/x_gift_bot
REF=
ORIGINAL_ARGS=("$@")
ROOT=/opt/xgift
DOMAIN= PORT= MODE= ACTION=install SOURCE_DIR=
YES=0 DRY=0 DEPS=1 INTERACTIVE=0 FROM_SOURCE=0
[[ ! -t 0 ]] || INTERACTIVE=1
TMP= BACKUP= CADDY_STAGE= CHANGED=0 HAD_SERVICE=0
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
  --goproxy URL          Go 模块代理；off 表示只用直连
  --jobs N               覆盖自动选择的编译并行任务数
  --build-from-source    跳过预编译下载，强制在目标机编译
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
    --domain|--port|--https|--dir|--ref|--source-dir|--goproxy|--jobs)
      (($# >= 2)) || die "$1 缺少参数"
      case "$1" in
        --domain) DOMAIN=$2;; --port) PORT=$2;; --https) MODE=$2;;
        --dir) ROOT=$2;; --ref) REF=$2;; --source-dir) SOURCE_DIR=$2;;
        --goproxy) GOPROXY_OVERRIDE=$2;; --jobs) JOBS_OVERRIDE=${2//[[:space:]]/}; [[ -n $JOBS_OVERRIDE ]] || die '--jobs 需要 1 以上的整数';;
      esac; shift 2;;
    --yes) YES=1; shift;; --dry-run) DRY=1; shift;; --no-deps) DEPS=0; shift;;
    --build-from-source) FROM_SOURCE=1; shift;;
    --upgrade|--status|--uninstall)
      [[ $ACTION == install ]] || die '操作参数不能组合'
      ACTION=${1#--}; shift;;
    --help|-h) usage; exit 0;; *) die "未知参数 $1；运行 --help 查看用法";;
  esac
done
[[ $ROOT == /* && $ROOT != *$'\n'* && $ROOT != *' '* ]] || die '安装目录必须是无空格的绝对路径'
[[ $ROOT != *'..'* ]] || die '安装目录不能包含 ..'
# Reject aliases of "/" such as "//" and "///" before anything is written:
# they resolve to the filesystem root and would chmod it and drop files in /bin.
ROOT_CANON=$(realpath -m -- "$ROOT")
[[ $ROOT_CANON != / ]] || die '安装目录不能是根目录及其别名（/、//），请使用 /opt/xgift'
for blocked in /bin /sbin /lib /lib32 /lib64 /libx32 /usr /etc /boot /dev /proc /sys /run /root /home /mnt /media /srv /var; do
  [[ $ROOT_CANON != "$blocked" && $ROOT_CANON != "$blocked"/* ]] || die "安装目录不能位于系统路径（$blocked）下，请改用 /opt/xgift；当前解析为 $ROOT_CANON"
done
ROOT=$ROOT_CANON
validate_ref() {
  [[ $REF =~ ^[a-zA-Z0-9_./-]+$ && $REF != -* && $REF != *..* ]] || die '非法仓库版本'
}
if [[ -n $SOURCE_DIR ]]; then
  [[ $SOURCE_DIR == /* && -f $SOURCE_DIR/go.mod && -f $SOURCE_DIR/deploy/install.sh ]] || die '源码目录必须为完整源码的绝对路径'
fi
# Saved state supplies defaults only. Never execute a state file as shell code.
if [[ -e $ROOT/install.conf && ! -r $ROOT/install.conf ]]; then
  die "无法读取 $ROOT/install.conf（权限不足）；请改用 sudo bash 运行本安装器"
fi
if [[ -f $ROOT/install.conf ]]; then
  while IFS='=' read -r key value; do
    case "$key" in
      DOMAIN) [[ -n $DOMAIN ]] || DOMAIN=$value;;
      PORT) [[ -n $PORT ]] || PORT=$value;;
      MODE) [[ -n $MODE ]] || MODE=$value;;
      REF) [[ -n $REF ]] || REF=$value;;
    esac
  done < "$ROOT/install.conf"
fi
REF=${REF:-main}
validate_ref
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
only_listener_owner() {
  local line names name
  [[ -n $OWNERS ]] || return 0
  while IFS= read -r line; do
    [[ -n $line ]] || continue
    names=$(grep -oE '"[^" ]+"' <<< "$line" || true)
    [[ -n $names ]] || return 1
    while IFS= read -r name; do [[ $name == \"$1\" ]] || return 1; done <<< "$names"
  done <<< "$OWNERS"
}
check_root_parents() {
  local parent mode
  parent=$(dirname "$(realpath -m "$ROOT")")
  while [[ $parent != / ]]; do
    if [[ -e $parent ]]; then
      [[ -d $parent ]] || die "安装目录父路径不是目录：$parent"
      mode=$(stat -c '%a' "$parent")
      (( (8#$mode & 1) != 0 )) || die "父目录 $parent 不允许服务用户遍历；请改用 /opt/xgift，不会修改您的父目录权限"
    fi
    parent=$(dirname "$parent")
  done
}
caddy_domain_conflict() {
  local adapted check_file
  # Keep the temporary config beside Caddyfile so relative imports resolve correctly.
  check_file=$(mktemp /etc/caddy/.xgift-check-XXXXXX)
  managed_config > "$check_file"
  if ! adapted=$(caddy adapt --config "$check_file" --adapter caddyfile); then
    rm -f "$check_file"
    die '现有 Caddy 配置解析失败，未覆盖'
  fi
  rm -f "$check_file"
  python3 -c 'import json,sys,fnmatch
host=sys.argv[1]
def conflict(node):
    if isinstance(node,dict):
        if any(fnmatch.fnmatchcase(host, h.lower()) for h in node.get("host",[]) if isinstance(h,str)): return True
        return any(conflict(v) for v in node.values())
    if isinstance(node,list): return any(conflict(v) for v in node)
    return False
sys.exit(0 if conflict(json.load(sys.stdin)) else 1)' "$DOMAIN" <<< "$adapted"
}
nginx_domain_conflict() {
  local dump
  dump=$(nginx -T 2>&1) || { printf '%s\n' "$dump" >&2; return 2; }
  awk -v own="$NGINX_FILE" -v domain="$DOMAIN" '
    function suffix(host, tail) {return length(host)>length(tail) && substr(host,length(host)-length(tail)+1)==tail}
    /^# configuration file / {file=$0; sub(/^# configuration file /,"",file); sub(/:$/,"",file); active=0; next}
    file != own {
      line=$0; sub(/#.*/,"",line)
      gsub(/[;{}]/," & ",line); n=split(line,words,/[[:space:]]+/)
      for (i=1;i<=n;i++) {
        name=tolower(words[i])
        if (name=="server_name") {active=1; continue}
        if (name==";" || name=="{" || name=="}") {active=0; continue}
        if (!active || name=="") continue
        if (substr(name,1,1)=="~") {uncertain=1; continue}
        if (name==domain || (substr(name,1,2)=="*." && suffix(domain,substr(name,2))) ||
            (substr(name,1,1)=="." && (domain==substr(name,2) || suffix(domain,name))) ||
            (substr(name,length(name)-1)==".*" && substr(domain,1,length(name)-1)==substr(name,1,length(name)-1))) found=1
      }
    }
    END {
      if (found) exit 0
      if (uncertain) {print "发现正则 server_name，无法自动判定域名冲突；请人工核对后使用 --https external 接入原反代。" > "/dev/stderr"; exit 2}
      exit 1
    }
  ' <<< "$dump"
}
check_dns() {
  local addresses
  addresses=$(python3 -c 'import socket,sys
try:
    addresses=sorted({item[4][0] for item in socket.getaddrinfo(sys.argv[1],443,type=socket.SOCK_STREAM)})
    if not addresses: raise OSError("no addresses")
    print("\n".join(addresses))
except OSError as error:
    print("DNS解析失败："+str(error),file=sys.stderr); sys.exit(1)' "$DOMAIN") || die "域名 $DOMAIN 无法解析；请检查 DNS 后重试，尚未开始源码下载与构建"
  [[ -n $addresses ]] || die "域名 $DOMAIN 没有解析地址，尚未开始构建"
  log "DNS 解析结果：$addresses"
  if [[ $addresses == *:* ]]; then
    log '检测到 AAAA/IPv6：请确认上述 IPv6 对应本机，且本机 IPv6 入站 80/443 与反代监听可达；错误 AAAA 会导致证书或 HTTPS 失败。不会自动修改 DNS、防火墙或 CDN。'
  fi
}
choose_external() {
  log "标准端口被其他服务占用：$OWNERS"
  log '不会强停未知或非 HTTP 服务。若已有 HTTP(S) 反代，请选 external 接入；若为非 HTTP 服务，请改用具有独立公网 80/443 的宿主/入口，external 本身不能将未知协议变成 HTTPS。'
  if ((INTERACTIVE && !YES)); then
    confirm '确认改为 external，并由您配置已有 HTTP(S) 入口？' || die '未同意切换 external，未接管已有监听者'
    MODE=external
  else
    die '未自动切换模式；已有 HTTP(S) 入口请显式指定 --https external，否则准备独立公网 HTTPS 入口后重试'
  fi
}
external_instructions() {
  cat >&2 <<PROXY
external 接入：以下二选一，合并到已有宿主反代配置；不要同时启用两套，不会自动修改配置。
Caddy（宿主）：
$DOMAIN {
    reverse_proxy 127.0.0.1:$PORT
}
Nginx（合并到已有且已配置证书的 HTTPS server 内）：
location / {
    proxy_pass http://127.0.0.1:$PORT;
    proxy_set_header Host \$host;
    proxy_set_header X-Forwarded-Proto \$scheme;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$remote_addr;
    proxy_buffering off;
    proxy_read_timeout 120s;
}
上游只监听本机。容器中的 127.0.0.1 不是宿主：请使用已有宿主网络/可达宿主地址，先确认连通，不要直接公开程序端口。
非 HTTP 监听者不能使用以上 HTTP 反代块；请准备独立公网 HTTPS 入口。配置完成后验证 https://$DOMAIN/healthz。
PROXY
}
acquire_lock() {
  command -v flock >/dev/null || die '缺少 flock；请安装 util-linux 后重试'
  # Shared lock lives outside ROOT, so no chmod or package-manager changes precede it.
  exec 9>/run/lock/xgift-installer.lock
  flock -n 9 || die '另一个安装器正在运行，请等待完成'
}
activate_nginx() {
  nginx -t
  systemctl enable --now nginx
  systemctl reload nginx
}
check_resources() {
  local available memory
  available=$(df -Pk "${TMPDIR:-/tmp}" "$ROOT" | awk 'NR>1 {if(min==0 || $4<min) min=$4} END {print min}')
  ((available >= 2097152)) || die '编译至少需要 2 GiB 空闲磁盘（临时目录与安装目录），请清理磁盘后重试'
  memory=$(awk '/MemAvailable:|SwapFree:/ {sum+=$2} END {print sum}' /proc/meminfo)
  if ((memory < 786432)); then log '提示：可用内存与 swap 合计不足 768 MiB，编译可能被 OOM 杀死；请增加内存或 swap 后重试。'; fi
}
# 编译并行度按可用内存与 CPU 核数决定。sing-box 带 QUIC/uTLS 标签，依赖树
# 预编译二进制：从 GitHub Release 下载并在落盘前强制校验 SHA256。
# 校验不是可选项——安装器随后会把这些二进制装成 root 服务，一个被替换的
# 产物等于把整台机器交出去。校验不过就当作下载失败，交给调用方回退编译。
# 返回 0 表示两个二进制都已就位且校验通过。
download_prebuilt() {
  local arch base tmp sums want got name
  case $(uname -m) in
    x86_64) arch=amd64;;
    aarch64|arm64) arch=arm64;;
    *) log "提示：架构 $(uname -m) 没有预编译产物，将改为在目标机编译"; return 1;;
  esac
  base="https://github.com/$REPO/releases/download/$REF"
  tmp=$(mktemp -d)
  # 任一分支退出都要清掉临时目录，避免半下载的二进制留在磁盘上。
  if ! curl -fSL --retry 2 --retry-max-time 300 --connect-timeout 15 --max-time 300 \
       "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" 2>/dev/null; then
    log "提示：$REF 没有发布预编译产物（或校验清单不可达），将改为在目标机编译"
    rm -rf -- "$tmp"; return 1
  fi
  for name in xgift xgift-web; do
    want=$(awk -v f="$name-linux-$arch" '$2 == f {print $1}' "$tmp/SHA256SUMS")
    if [[ ! $want =~ ^[0-9a-f]{64}$ ]]; then
      log "提示：校验清单里没有 $name-linux-$arch 的记录，将改为在目标机编译"
      rm -rf -- "$tmp"; return 1
    fi
    if ! curl -fSL --retry 2 --retry-max-time 600 --connect-timeout 15 --max-time 600 \
         "$base/$name-linux-$arch" -o "$tmp/$name" 2>/dev/null; then
      log "提示：下载 $name-linux-$arch 失败，将改为在目标机编译"
      rm -rf -- "$tmp"; return 1
    fi
    got=$(sha256sum "$tmp/$name" | awk '{print $1}')
    if [[ $got != "$want" ]]; then
      # 明确区分「下载坏了」和「被篡改」：后者要立刻停下让人看见。
      log "错误：$name-linux-$arch 的 SHA256 不匹配"
      log "  期望 $want"
      log "  实际 $got"
      log '产物可能已损坏或被替换，已放弃安装预编译版本，不写入任何文件。'
      rm -rf -- "$tmp"; return 1
    fi
    log "校验通过：$name-linux-$arch"
  done
  mkdir -p "$TMP/bin"
  mv "$tmp/xgift" "$tmp/xgift-web" "$TMP/bin/"
  chmod 700 "$TMP/bin/xgift" "$TMP/bin/xgift-web"
  rm -rf -- "$tmp"
  return 0
}
# 很大，单核编译在正常配置的机器上要慢一个数量级；而在 1 GiB 无 swap 的小机
# 上放开并行又会被 OOM 杀死。所以两边都要看，而不是写死。
#   1.5 GiB 及以上：放开到 CPU 核数（上限 8），这是普通 VPS 和家用机的常态。
#   1 GiB 上下：折中为 2。
#   768 MiB 及以下：保持单编译任务 + GOMAXPROCS=1。
plan_build_parallelism() {
  local memory cpus jobs
  memory=$(awk '/MemTotal:/ {print $2; exit}' /proc/meminfo)
  cpus=$(nproc 2>/dev/null || echo 1)
  ((cpus >= 1)) || cpus=1
  if [[ -n ${JOBS_OVERRIDE:-} ]]; then
    [[ $JOBS_OVERRIDE =~ ^[0-9]+$ ]] && ((JOBS_OVERRIDE >= 1)) || die "--jobs 需要 1 以上的整数"
    jobs=$JOBS_OVERRIDE
    log "编译并行度：${jobs} 任务（由 --jobs 指定；可用内存 ${memory} kB，过大可能被 OOM 杀死）"
  elif ((memory >= 1572864)); then jobs=$cpus
  elif ((memory >= 1048576)); then jobs=2
  else jobs=1
  fi
  ((jobs > 8)) && jobs=8
  ((jobs < 1)) && jobs=1
  if ((jobs == 1)); then
    BUILD_JOBS=1
    export GOMAXPROCS=1 GOMEMLIMIT=384MiB
    log "编译并行度：单任务（可用内存与 swap 合计 ${memory} kB，机器较小；如需更快可加 swap 后重跑）"
  else
    BUILD_JOBS=$jobs
    # GOMEMLIMIT 只设上限而非配额，留给链接器和其余进程余量。
    export GOMAXPROCS=$jobs GOMEMLIMIT=$((memory * 3 / 4))
    log "编译并行度：${jobs} 任务（${cpus} 核，可用内存 ${memory} kB）"
  fi
}
# Go 模块默认走 proxy.golang.org。实测同一台美国机器上它只要 0.08s，而
# goproxy.cn 要 1.56s——把国内镜像设成无条件默认会让境外机器反而更慢。
# 但国内线路访问 proxy.golang.org 会退化到逐个模块串行等待，sing-box 依赖树
# 有几百个模块（含 QUIC、Caddy server、chromium 内核），拖到几十分钟很常见。
# 所以先各测一次连通性，再选快的那条；两个都通但都不快就退回默认链。
setup_go_proxy() {
  if [[ -n ${GOPROXY_OVERRIDE:-} ]]; then
    if [[ $GOPROXY_OVERRIDE != off ]]; then export GOPROXY="$GOPROXY_OVERRIDE"; fi
    return 0
  fi
  # 用户已设置则保留原值。用 if 而非 `[[ ]] || return`：后者在 set -e 下
  # 条件为假时会让整个函数以非零状态退出，安装随之中断。
  if [[ -n ${GOPROXY:-} ]]; then return 0; fi
  local probe url fastest= best=
  for url in 'https://proxy.golang.org' 'https://goproxy.cn'; do
    probe=$(curl -fsS --max-time 8 -o /dev/null -w '%{time_total}' \
      "$url/github.com/quic-go/quic-go/@v/list" 2>/dev/null || echo '')
    if [[ -n $probe ]] && { [[ -z $fastest ]] || awk "BEGIN{exit !($probe < $fastest)}"; }; then
      fastest=$probe; best=$url
    fi
  done
  if [[ -z $best ]]; then
    export GOPROXY='direct'
    log '提示：两个 Go 模块代理都探测失败，已改为直连；如下载变慢可加 --goproxy https://goproxy.cn'
  elif awk "BEGIN{exit !($fastest > 0.8)}"; then
    export GOPROXY='https://goproxy.cn,https://goproxy.io,direct'
    log "Go 模块代理：goproxy.cn → goproxy.io → 直连（默认线路实测 ${fastest}s，偏慢）"
  else
    export GOPROXY="$best,direct"
    log "Go 模块代理：${best}（实测 ${fastest}s）"
  fi
}
rollback() {
  local rc=$?
  trap - EXIT INT TERM
  if ((rc != 0 && CHANGED)); then
    log '安装失败，正在恢复本次替换的程序及服务配置；数据和密码不回滚。'
    systemctl stop xgift >/dev/null 2>&1 || true
    if [[ -d $BACKUP/bin ]]; then cp -a "$BACKUP/bin/." "$ROOT/bin/"; else rm -f "$ROOT/bin/xgift" "$ROOT/bin/xgift-web"; fi
    for name in install.conf install.sh; do
      if [[ -f $BACKUP/$name ]]; then cp "$BACKUP/$name" "$ROOT/$name"; else rm -f "$ROOT/$name"; fi
    done
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
  [[ -z ${CADDY_STAGE:-} ]] || rm -f -- "$CADDY_STAGE"
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
  acquire_lock
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
# An installed entry point must pick up installer fixes before upgrading the application.
if [[ $ACTION == upgrade && -z $SOURCE_DIR && $DRY == 0 && ${XGIFT_BOOTSTRAPPED:-0} != 1 ]]; then
  command -v curl >/dev/null || die '升级需要 curl'
  UPDATE_TMP=$(mktemp -d)
  trap 'rm -rf -- "$UPDATE_TMP"' EXIT
  curl -fSL --retry 2 --retry-max-time 180 --connect-timeout 15 --max-time 60 \
    "https://raw.githubusercontent.com/$REPO/$REF/deploy/install.sh" -o "$UPDATE_TMP/install.sh"
  bash -n "$UPDATE_TMP/install.sh" || die '下载的安装器未通过语法检查'
  XGIFT_BOOTSTRAPPED=1 bash "$UPDATE_TMP/install.sh" "${ORIGINAL_ARGS[@]}" --dir "$ROOT" --ref "$REF"
  exit
fi
log 'XGift 一键安装：自动准备程序和服务，随后在浏览器中完成账号与支付配置。'
[[ -t 0 ]] || log '当前不是交互终端；显式参数或保存的配置将作为默认值。'
DOMAIN=$(ask '请输入站点域名（DNS 应已指向本服务器）' "$DOMAIN")
DOMAIN=${DOMAIN,,}
[[ ${#DOMAIN} -le 253 && $DOMAIN =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$ ]] || die '请输入合法域名，不要带协议、端口或路径'
IFS=. read -ra labels <<< "$DOMAIN"
for label in "${labels[@]}"; do ((${#label} <= 63)) || die '域名每段不能超过 63 个字符'; done
if ((DRY)); then
  log 'dry-run 权限诊断：跳过安装目录父目录权限阻断；实际安装仍要求服务用户能够遍历全部父目录，不会自动修改父目录权限。'
else
  check_root_parents
fi
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
acquire_lock
mkdir -p "$ROOT"
check_resources
if ((DEPS)); then
  apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 update
  DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=2 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 install -y ca-certificates curl git build-essential python3 openssl iproute2 util-linux
fi
for dep in curl git gcc python3 openssl ss flock; do command -v "$dep" >/dev/null || die "缺少 $dep；重新运行并去掉 --no-deps"; done
mkdir -p "$ROOT"
chmod 755 "$ROOT"
# Before downloads or service changes, reject non-owned installations and port conflicts.
if [[ -e /etc/systemd/system/xgift.service && ! -f $ROOT/install.conf ]]; then die '发现非本安装器管理的 xgift 服务，请先迁移，未覆盖'; fi
if ss -ltnH | awk '{sub(/.*:/,"",$4); print $4}' | grep -qx "$PORT"; then
  [[ -f $ROOT/install.conf ]] && grep -qx "PORT=$PORT" "$ROOT/install.conf" && systemctl is-active --quiet xgift || die "端口 $PORT 已占用"
fi
if [[ $MODE == external ]] && { { [[ -f /etc/caddy/Caddyfile ]] && grep -qF '# >>> xgift installer >>>' /etc/caddy/Caddyfile; } || { [[ -f $NGINX_FILE ]] && grep -qF '# xgift installer managed' "$NGINX_FILE"; }; }; then
  die '已存在本安装器管理的 HTTPS 配置；请保留 --https auto，或卸载服务后切换 external（数据保留）'
fi
OWNERS=$(ss -ltnpH '( sport = :80 or sport = :443 )')
if [[ $MODE == auto ]]; then
  if [[ -z $OWNERS ]]; then MODE=caddy
  elif only_listener_owner nginx; then MODE=nginx
  elif only_listener_owner caddy; then MODE=caddy
  else
    choose_external
  fi
fi
if [[ $MODE == external ]]; then
  # A consented auto -> external choice must obey the same old-config checks.
  if { [[ -f /etc/caddy/Caddyfile ]] && grep -qF '# >>> xgift installer >>>' /etc/caddy/Caddyfile; } || { [[ -f $NGINX_FILE ]] && grep -qF '# xgift installer managed' "$NGINX_FILE"; }; then
    die '已有安装器管理的 HTTPS 配置；请先卸载服务后切换 external（数据保留）'
  fi
  external_instructions
else
  check_dns
fi
if [[ $MODE == caddy ]]; then
  [[ ! -f $NGINX_FILE ]] || ! grep -qF '# xgift installer managed' "$NGINX_FILE" || die '已有安装器管理的 Nginx 配置；请先卸载服务后切换（数据保留）'
  only_listener_owner caddy || die '标准端口不是 Caddy 管理，不能强制接管'
  if ! command -v caddy >/dev/null; then
    [[ -z $OWNERS ]] || die '标准端口已占用，不能安装第二个网页服务器'
    ((DEPS)) || die '缺少 Caddy；去掉 --no-deps 或选择 --https external'
    DEBIAN_FRONTEND=noninteractive apt-get install -y caddy
  fi
  [[ -f /etc/caddy/Caddyfile ]] || die 'Caddy 配置不存在；请用 --https external'
  if caddy_domain_conflict; then die "域名 $DOMAIN 已出现在现有 Caddy 配置中；未覆盖"; fi
elif [[ $MODE == nginx ]]; then
  [[ ! -f /etc/caddy/Caddyfile ]] || ! grep -qF '# >>> xgift installer >>>' /etc/caddy/Caddyfile || die '已有安装器管理的 Caddy 配置；请先卸载服务后切换（数据保留）'
  only_listener_owner nginx || die '标准端口不是 Nginx 管理，不能强制接管'
  command -v nginx >/dev/null || die '未发现宿主 Nginx，不能接管容器 Nginx'
  nginx -t
  [[ ! -e $NGINX_FILE ]] || grep -qF '# xgift installer managed' "$NGINX_FILE" || die '目标 Nginx 文件不是安装器创建，未覆盖'
  if nginx_domain_conflict; then
    die '域名已存在于其他 Nginx 配置，未覆盖'
  else
    conflict_status=$?
    ((conflict_status == 1)) || die '无法可靠判定 Nginx 域名冲突；未覆盖，请人工核对配置或使用 --https external'
  fi
  if ! command -v certbot >/dev/null; then
    ((DEPS)) || die '缺少 certbot，请去掉 --no-deps'
    DEBIAN_FRONTEND=noninteractive apt-get install -y certbot
  fi
fi
TMP=$(mktemp -d)
trap rollback EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
# 优先用 Release 里的预编译二进制：用户不必装 Go 工具链、拉 1.4 GB 依赖树、
# 再花几分钟编译。下载或校验失败都自动回退到在目标机编译，不会因为发布侧
# 的问题让用户装不上。--source-dir 与 --build-from-source 直接走编译。
PREBUILT=0
if ((DRY)); then
  log 'dry-run：跳过预编译下载与编译。'
elif ((FROM_SOURCE)); then
  log '按 --build-from-source 在目标机编译。'
elif [[ -n $SOURCE_DIR ]]; then
  log '按 --source-dir 使用本机源码编译。'
elif download_prebuilt; then
  PREBUILT=1
  log "已取得校验通过的预编译程序（$REF）；跳过工具链下载与编译。"
else
  log '改为在目标机编译。'
fi
if ((PREBUILT == 0 && DRY == 0)); then
  if [[ -n $SOURCE_DIR ]]; then
    [[ -d $SOURCE_DIR && -f $SOURCE_DIR/go.mod && -f $SOURCE_DIR/deploy/install.sh ]] || die '源码目录不完整'
    mkdir "$TMP/src"
    cp -a "$SOURCE_DIR/." "$TMP/src/"
  else
    for attempt in 1 2 3; do
      if timeout 300 git -c http.lowSpeedLimit=1024 -c http.lowSpeedTime=30 clone --depth 1 --branch "$REF" "https://github.com/$REPO.git" "$TMP/src"; then break; fi
      rm -rf -- "$TMP/src"
      ((attempt < 3)) || die '源码下载失败（3 次，单次限时 300 秒）；检查 GitHub 连通性或使用 --source-dir'
    done
  fi
  GO_VERSION=$(awk '$1=="go" {gsub(/\r/,"",$2); print $2; exit}' "$TMP/src/go.mod")
  [[ $GO_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 'go.mod 的 Go 版本格式不支持'
  case $(uname -m) in x86_64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) die '仅支持 amd64/arm64';; esac
  GO_HOME="$ROOT/toolchains/go$GO_VERSION"
  if [[ ! -x $GO_HOME/bin/go ]]; then
    curl -fSL --retry 2 --retry-max-time 900 --connect-timeout 15 --max-time 300 'https://go.dev/dl/?mode=json&include=all' -o "$TMP/go.json"
    SHA=$(python3 -c 'import json,sys; vs=json.load(open(sys.argv[1])); fs=[f for v in vs if v["version"]==sys.argv[2] for f in v["files"] if f["filename"]==sys.argv[3]]; assert len(fs)==1,"Go archive unavailable"; print(fs[0]["sha256"])' "$TMP/go.json" "go$GO_VERSION" "go$GO_VERSION.linux-$ARCH.tar.gz")
    curl -fSL --retry 2 --retry-max-time 900 --connect-timeout 15 --max-time 300 "https://go.dev/dl/go$GO_VERSION.linux-$ARCH.tar.gz" -o "$TMP/go.tar.gz"
    printf '%s  %s\n' "$SHA" "$TMP/go.tar.gz" | sha256sum -c -
    mkdir -p "$ROOT/toolchains"
    tar -xzf "$TMP/go.tar.gz" -C "$TMP"
    mv "$TMP/go" "$GO_HOME"
  fi
  export PATH="$GO_HOME/bin:$PATH" CGO_ENABLED=1
  # Parallelism and module proxy are chosen for the machine, not fixed at the
  # values a 1 GiB VPS needs. Never change host swap automatically.
  plan_build_parallelism
  setup_go_proxy
  export GOPATH="$ROOT/build-cache/gopath" GOCACHE="$ROOT/build-cache/go-build"
  mkdir "$TMP/bin"
  log "开始编译（首次安装需拉取 sing-box 依赖树，耗时取决于网络；进度见下方 go downloading 输出）"
  START_BUILD=$(date +%s)
  (cd "$TMP/src"; go build -p "$BUILD_JOBS" -tags with_quic,with_utls -o "$TMP/bin/xgift" ./cmd/xgift; go build -p "$BUILD_JOBS" -tags with_quic,with_utls -o "$TMP/bin/xgift-web" ./cmd/xgift-web)
  log "编译完成，用时 $(( $(date +%s) - START_BUILD )) 秒"
fi
# Stage and validate the reverse proxy before touching the running application.
if [[ $MODE == caddy ]]; then
  CADDY_STAGE=$(mktemp /etc/caddy/.xgift-stage-XXXXXX)
  managed_config > "$CADDY_STAGE"
  cat >> "$CADDY_STAGE" <<CADDY
# >>> xgift installer >>>
$DOMAIN {
    encode zstd gzip
    reverse_proxy 127.0.0.1:$PORT
}
# <<< xgift installer <<<
CADDY
  caddy validate --config "$CADDY_STAGE" --adapter caddyfile
fi
BACKUP="$ROOT/backups/$(date +%Y%m%d-%H%M%S)-$$"
mkdir -p "$BACKUP"
[[ ! -d $ROOT/bin ]] || cp -a "$ROOT/bin" "$BACKUP/bin"
[[ ! -f $ROOT/site.env ]] || cp "$ROOT/site.env" "$BACKUP/env"
for name in install.conf install.sh; do [[ ! -f $ROOT/$name ]] || cp "$ROOT/$name" "$BACKUP/$name"; done
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
  cp "$CADDY_STAGE" /etc/caddy/Caddyfile
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
  activate_nginx
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
# Publish both files only after staging succeeds; rollback restores both on failure.
install -m 700 "$TMP/src/deploy/install.sh" "$TMP/install.sh"
printf 'DOMAIN=%s\nPORT=%s\nMODE=%s\nREF=%s\n' "$DOMAIN" "$PORT" "$MODE" "$REF" > "$TMP/install.conf"
install -m 700 "$TMP/install.sh" "$ROOT/install.sh"
install -m 600 "$TMP/install.conf" "$ROOT/install.conf"
CHANGED=0
log "安装完成。下一步：打开 https://$DOMAIN/setup，将初始化密码填入网页向导。"
if [[ ! -s $ROOT/secrets/admin-password ]]; then
  log "初始化密码：$(< "$ROOT/secrets/setup-password")"
else log '已有管理员配置已保留，请使用原管理员账号。'; fi
if [[ $MODE == external ]]; then external_instructions; fi
log '网页向导完成后运行 systemctl restart xgift；保管库密码与 data 目录请一起备份。'
log "以后：bash $ROOT/install.sh --status | --upgrade | --uninstall"
log "旧程序备份：$BACKUP。真实付款默认关闭，配置完成后修改 $ROOT/site.env 再重启。"
