#!/usr/bin/env bash
# XGift Linux installer. Run from a downloaded file, not a stdin pipe.
set -Eeuo pipefail
umask 077
REPO=yys9253462-gif/x_gift_bot
REF=
ORIGINAL_ARGS=("$@")
ROOT=/opt/xgift
DOMAIN= PORT= MODE= ACTION=install SOURCE_DIR=
YES=0 DRY=0 DEPS=1 INTERACTIVE=0 FROM_SOURCE=0 REF_EXPLICIT=
[[ ! -t 0 ]] || INTERACTIVE=1
TMP= BACKUP= CADDY_STAGE= CHANGED=0 HAD_SERVICE=0
SELF=$(realpath "${BASH_SOURCE[0]}")
if [[ -f "$(dirname "$SELF")/install.conf" ]]; then ROOT=$(dirname "$SELF"); fi
log() { printf '%s\n' "$*" >&2; }
die() { log "错误：$*"; exit 1; }
# 进度显示。下载 60 MB 二进制、拉几百个 Go 模块、编译链接都可能几分钟没有
# 输出，用户会以为卡死然后 Ctrl-C——这比真的失败更伤。所以凡是能等的步骤都
# 起一个 ticker 在 stderr 打点，并写明已等待多久，让人知道程序还活着。
# 只在 stderr 是终端时打点：重定向到文件时不该被日志噪声淹没。
PROGRESS_PID=
progress_supported() { [[ -t 2 ]]; }
# progress_start <说明> [间隔秒]：起一个后台 ticker 在 stderr 打点。
progress_start() {
  local label=$1 every=${2:-15} start=0
  progress_stop
  progress_supported || return 0
  (
    while :; do
      sleep "$every"
      start=$((start + every))
      printf '  [进行中] %s（已等待 %ds）\n' "$label" "$start" >&2
    done
  ) &
  PROGRESS_PID=$!
}
# 无论成功失败都调用 progress_stop，否则 ticker 会在后续输出里继续插行。
progress_stop() {
  # 用 ${PROGRESS_PID:-} 而不是 $PROGRESS_PID：这个函数会被当清道夫调用，
  # 可能在 start 之前就触发；set -u 下裸引用会直接报错。
  [[ -n ${PROGRESS_PID:-} ]] || return 0
  kill "$PROGRESS_PID" 2>/dev/null || true
  wait "$PROGRESS_PID" 2>/dev/null || true
  PROGRESS_PID=
}
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
        --dir) ROOT=$2;; --ref) REF=$2; REF_EXPLICIT=1;; --source-dir) SOURCE_DIR=$2;;
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
# 这里先只用 validate_ref 拦住非法值；"是否要自动解析最新版本"必须等到
# latest_release_ref 定义之后再判断（shell 函数必须先定义后调用）。
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
# 本机全部公网 IP，用于判断「域名到底有没有指向这台机器」。
local_public_ips() {
  python3 - <<'PY' 2>/dev/null || true
import ipaddress, socket, subprocess
found = set()
try:
    out = subprocess.run(["ip", "-o", "addr", "show"], capture_output=True, text=True, timeout=5).stdout
except Exception:
    out = ""
for line in out.splitlines():
    parts = line.split()
    if len(parts) < 4:
        continue
    addr = parts[3].split("/")[0]
    try:
        ip = ipaddress.ip_address(addr)
    except ValueError:
        continue
    if ip.version == 4 and not (ip.is_private or ip.is_loopback or ip.is_link_local):
        found.add(str(ip))
for host in ("169.254.169.254",):
    # 云厂商元数据里的公网 IP；失败就跳过，不阻塞安装。
    pass
try:
    # 出口 IP 也算：NAT 或弹性 IP 场景下网卡上看不到公网地址。
    out = subprocess.run(
        ["curl", "-fsS", "--max-time", "6", "https://api.ipify.org"],
        capture_output=True, text=True, timeout=10).stdout.strip()
    if out:
        ipaddress.ip_address(out)
        found.add(out)
except Exception:
    pass
print("\n".join(sorted(found)))
PY
}
# 这里只做两件事：确认域名能解析，以及（尽力而为地）确认它指向本机。
# 校验失败一律给警告不拦人——多宿主、CDN、Anycast、NAT、IPv6-only 都可能让
# 「解析出的 IP 不等于本机网卡 IP」成为正常状态，硬拦会误伤真实可用环境。
# 但必须在这里说清楚：真正的 HTTPS 检查在几分钟之后，错误留到那时才暴露，
# 用户已经白等一轮。
check_dns() {
  local addresses local_ips hit=0 ip
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
  # 尽力而为的本机比对。取不到本机 IP 就不下结论，只跳过检查。
  local_ips=$(local_public_ips)
  if [[ -z $local_ips ]]; then
    log '提示：未能取得本机公网 IP，跳过「域名是否指向本机」的核对；若稍后 HTTPS 失败请优先检查 DNS。'
    return 0
  fi
  while IFS= read -r ip; do
    [[ -n $ip ]] || continue
    if grep -qxF "$ip" <<< "$local_ips"; then hit=1; break; fi
  done <<< "$addresses"
  if ((hit)); then
    log 'DNS 指向核对：解析地址与本机公网 IP 匹配。'
  else
    log '警告：域名解析出的地址与本机公网 IP 都不一致。'
    log "  解析得到：$(tr '\n' ' ' <<< "$addresses")"
    log "  本机公网：$(tr '\n' ' ' <<< "$local_ips")"
    log '  若使用 CDN、多台服务器、IPv6 或云负载均衡，这是正常的，可以继续。'
    log '  若不是，安装会在最后一步「公网 HTTPS 健康检查」失败并自动回滚——与其等几分钟，'
    log '  建议现在先停下去修 DNS，改完再重跑这条命令。'
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
# 查最新发布版本的 tag。用于两件事：
#   1) 用户没写 --ref 时，自动指向有预编译产物的版本，而不是 main（main 没有
#      Release，必然回退编译）；
#   2) 取不到产物时报错能给出版本号，而不是让用户自己猜。
# 走 GitHub API，失败就回显空字符串——绝不能因为这个查询失败就影响安装。
latest_release_ref() {
  local tag
  tag=$(curl -fsSL --max-time 12 --connect-timeout 6 \
    -H 'Accept: application/vnd.github+json' \
    "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null \
    | python3 -c 'import json,sys
try:
    print(json.load(sys.stdin).get("tag_name","") or "")
except Exception:
    print("")' 2>/dev/null) || tag=
  [[ $tag =~ ^[a-zA-Z0-9_./-]+$ && $tag != -* && $tag != *..* ]] || tag=
  printf '%s' "$tag"
}
# 用户没写 --ref、也没有已保存的 REF 时，不要让默认值落在 main 上——main 不带
# Release 产物，只会默默回退编译，让用户以为"这个项目必须编译"。这里自动指向
# 最新发布版本，让默认路径就走预编译下载。查询失败就保持 main（退回编译仍能装成）。
# 放在这里是因为 shell 函数必须先定义后调用。
if [[ -z $REF_EXPLICIT && $REF == main && -z $SOURCE_DIR && $FROM_SOURCE == 0 ]]; then
  if resolved=$(latest_release_ref) && [[ -n $resolved ]]; then
    log "未指定 --ref，已自动选用最新发布版本 $resolved（直接下载预编译产物，无需编译）。"
    REF=$resolved
  else
    log '提示：未指定 --ref，且未能查询到发布版本；默认按 main 处理，可能回退到源码编译。'
    log "      想跳过编译请显式指定：--ref <版本号>（见 https://github.com/$REPO/releases）"
  fi
fi
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
  log "正在从 GitHub Release 下载预编译程序（$REF，约 60 MB × 2）。"
  # 任一分支退出都要清掉临时目录，避免半下载的二进制留在磁盘上。
  if ! curl -fSL --retry 2 --retry-max-time 300 --connect-timeout 15 --max-time 300 \
       "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" 2>/dev/null; then
    # 这里必须把「为什么取不到」说清楚。用户看到的下一件事会是满屏 go: downloading，
    # 如果不说原因，他会以为"这个项目必须编译"——其实只是 ref 取错了。
    local code
    code=$(curl -sIL -o /dev/null -w '%{http_code}' --max-time 20 "$base/SHA256SUMS" 2>/dev/null || echo '000')
    if [[ $code == 404 ]]; then
      log "原因：$REF 不是一个带预编译产物的发布版本（HTTP 404）。"
      if [[ $REF == main ]]; then
        log '  未指定 --ref 时默认走 main 分支，而 main 上没有 Release 产物。'
      fi
      log '  可用版本见：https://github.com/'"$REPO"'/releases'
      log "  指定发布版本即可无需编译：加 --ref <版本号>（例如 --ref $(latest_release_ref 2>/dev/null || echo v0.1.0)）"
    elif [[ $code == 000 ]]; then
      log '原因：无法连接 GitHub（网络不可达或超时），因此取不到预编译产物。'
    else
      log "原因：预编译产物不可达（HTTP $code）。"
    fi
    log '现在改为在目标机从源码编译，可以装成，但会慢很多。'
    rm -rf -- "$tmp"; return 1
  fi
  for name in xgift xgift-web; do
    want=$(awk -v f="$name-linux-$arch" '$2 == f {print $1}' "$tmp/SHA256SUMS")
    if [[ ! $want =~ ^[0-9a-f]{64}$ ]]; then
      log "提示：校验清单里没有 $name-linux-$arch 的记录，将改为在目标机编译"
      rm -rf -- "$tmp"; return 1
    fi
    progress_start "下载 $name-linux-$arch（约 60 MB）" 15
    if ! curl -fSL --retry 2 --retry-max-time 600 --connect-timeout 15 --max-time 600 \
         "$base/$name-linux-$arch" -o "$tmp/$name" 2>/dev/null; then
      progress_stop
      log "提示：下载 $name-linux-$arch 失败，将改为在目标机编译"
      rm -rf -- "$tmp"; return 1
    fi
    progress_stop
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
# 只测首字节延迟是不够的：实测遇到过「延迟 0.1s 但下到一半断流」的源，
# 结果安装卡在半路，比一开始就慢更糟。所以探测两件事：
#   1) 一个小体积、真实存在的模块元数据能否**完整**下完（不只看 TTFB）；
#   2) 下完的耗时。
# 两个都通过才参与比较。都不通过就直连，并在提示里给出显式选项。
GO_PROXY_PROBES=(
  'https://proxy.golang.org|github.com/quic-go/quic-go/@v/list'
  'https://goproxy.cn|github.com/quic-go/quic-go/@v/list'
  'https://goproxy.io|github.com/quic-go/quic-go/@v/list'
)
probe_go_proxy() {
  # 输出 "<耗时秒>" 表示可用；无输出表示不可用。判据是「整体传输成功」。
  local url path body
  url=${1%%|*}; path=${1#*|}
  curl -fsS --max-time 12 --connect-timeout 6 \
    -o /dev/null -w '%{time_total}' "$url/$path" 2>/dev/null || return 1
}
setup_go_proxy() {
  if [[ -n ${GOPROXY_OVERRIDE:-} ]]; then
    # off 明确要求直连：这里必须 unset。只"不设置"是不够的——环境或已保存
    # 的 GOPROXY 仍然生效，用户要求直连却被悄悄走了代理。
    if [[ $GOPROXY_OVERRIDE == off ]]; then
      unset GOPROXY
      log 'Go 模块代理：按 --goproxy off 直连（不使用任何模块代理）。'
    else
      export GOPROXY="$GOPROXY_OVERRIDE"
      log "Go 模块代理：$GOPROXY（由 --goproxy 指定）"
    fi
    return 0
  fi
  # 用户已设置则保留原值。用 if 而非 `[[ ]] || return`：后者在 set -e 下
  # 条件为假时会让整个函数以非零状态退出，安装随之中断。
  if [[ -n ${GOPROXY:-} ]]; then
    log "Go 模块代理：沿用环境中的 GOPROXY=$GOPROXY"
    return 0
  fi
  log '正在探测 Go 模块代理（比较完整下载小文件的实际耗时，而不只是延迟）。'
  local entry probe fastest= best= failed=()
  for entry in "${GO_PROXY_PROBES[@]}"; do
    if probe=$(probe_go_proxy "$entry"); then
      log "  ${entry%%|*} 可用，耗时 ${probe}s"
      if [[ -z $fastest ]] || awk "BEGIN{exit !($probe < $fastest)}"; then
        fastest=$probe; best=${entry%%|*}
      fi
    else
      log "  ${entry%%|*} 不可用（连接失败或传输未完成）"
      failed+=("${entry%%|*}")
    fi
  done
  if [[ -z $best ]]; then
    export GOPROXY='direct'
    log '提示：所有 Go 模块代理都探测失败，已改为直连。'
    log '      若编译或下载在直连下变慢/失败，可用 --goproxy https://goproxy.cn 指定。'
  else
    # 首选探测通过的，其余按顺序兜底。Go 自身在某个代理返回错误时会
    # 自动尝试列表里的下一个，所以把失败过的留在末尾没有坏处。
    export GOPROXY="$best,direct"
    log "Go 模块代理：$best → 直连（实测完整下载 ${fastest}s）"
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
#
# 自举的目的**就是拿到修好的安装器**，所以它必须去找最新的，绝不能锚在用户当前
# 用的版本上。踩过的坑：曾经优先用 install.conf 里记的 REF（"上次装成功的版本"），
# 结果用户停在 v0.1.0、而 v0.1.0 恰好冻结着带 bug 的安装器，于是每次升级都把
# 旧安装器拉回来原地复现同一个失败——自举完全失去意义。
#
# 拉取顺序：main（修复总是先合进 main）→ 最新发布 tag → 用户当前 ref → 本地文件。
# 只要能取到一份语法正确、且认识当前路径约定的安装器就重跑，否则用自己继续。
installer_looks_current() {
  local file=$1
  bash -n "$file" 2>/dev/null || return 1
  grep -q 'TMP_ASSETS' "$file"
}
if [[ $ACTION == upgrade && -z $SOURCE_DIR && $DRY == 0 && ${XGIFT_BOOTSTRAPPED:-0} != 1 ]]; then
  command -v curl >/dev/null || die '升级需要 curl'
  UPDATE_TMP=$(mktemp -d)
  trap 'rm -rf -- "$UPDATE_TMP"' EXIT
  UPDATE_LATEST=$(latest_release_ref)
  for candidate in main "$UPDATE_LATEST" "$REF"; do
    [[ -n $candidate ]] || continue
    if curl -fSL --retry 2 --retry-max-time 180 --connect-timeout 15 --max-time 60 \
         "https://raw.githubusercontent.com/$REPO/$candidate/deploy/install.sh" -o "$UPDATE_TMP/install.sh" 2>/dev/null \
       && installer_looks_current "$UPDATE_TMP/install.sh"; then
      log "升级前已取得安装器（$candidate）。"
      XGIFT_BOOTSTRAPPED=1 bash "$UPDATE_TMP/install.sh" "${ORIGINAL_ARGS[@]}" --dir "$ROOT" --ref "$REF"
      exit
    fi
  done
  log '提示：未能取得新安装器，改用当前文件继续升级。'
  XGIFT_BOOTSTRAPPED=1 bash "$SELF" "${ORIGINAL_ARGS[@]}" --dir "$ROOT"
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
log "将优先进口已发布的预编译程序（$REF）；仅在取不到时才回退到本机编译。随后生成首次密码并启动 systemd。"
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
#
# 无论走哪条路径，部署资产（systemd 单元、安装器自身）都必须先落地到
# $TMP/assets：预编译路径不会克隆源码，若沿用 $TMP/src/deploy 会直接读不到文件。
TMP_ASSETS="$TMP/assets"
mkdir -p "$TMP_ASSETS"
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
    log "正在获取源码（$REPO @ $REF）。"
    for attempt in 1 2 3; do
      progress_start "克隆源码（第 $attempt/3 次尝试）" 20
      if timeout 300 git -c http.lowSpeedLimit=1024 -c http.lowSpeedTime=30 clone --depth 1 --branch "$REF" "https://github.com/$REPO.git" "$TMP/src"; then
        progress_stop; break
      fi
      progress_stop
      rm -rf -- "$TMP/src"
      ((attempt < 3)) || die '源码下载失败（3 次，单次限时 300 秒）；检查 GitHub 连通性或使用 --source-dir'
      log "第 $attempt 次源码下载失败，稍后重试。"
    done
  fi
  GO_VERSION=$(awk '$1=="go" {gsub(/\r/,"",$2); print $2; exit}' "$TMP/src/go.mod")
  [[ $GO_VERSION =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 'go.mod 的 Go 版本格式不支持'
  case $(uname -m) in x86_64) ARCH=amd64;; aarch64|arm64) ARCH=arm64;; *) die '仅支持 amd64/arm64';; esac
  GO_HOME="$ROOT/toolchains/go$GO_VERSION"
  if [[ ! -x $GO_HOME/bin/go ]]; then
    log "正在下载 Go 工具链 go$GO_VERSION（约 70 MB）。"
    progress_start "下载 Go 工具链 go$GO_VERSION" 15
    curl -fSL --retry 2 --retry-max-time 900 --connect-timeout 15 --max-time 300 'https://go.dev/dl/?mode=json&include=all' -o "$TMP/go.json"
    SHA=$(python3 -c 'import json,sys; vs=json.load(open(sys.argv[1])); fs=[f for v in vs if v["version"]==sys.argv[2] for f in v["files"] if f["filename"]==sys.argv[3]]; assert len(fs)==1,"Go archive unavailable"; print(fs[0]["sha256"])' "$TMP/go.json" "go$GO_VERSION" "go$GO_VERSION.linux-$ARCH.tar.gz")
    curl -fSL --retry 2 --retry-max-time 900 --connect-timeout 15 --max-time 300 "https://go.dev/dl/go$GO_VERSION.linux-$ARCH.tar.gz" -o "$TMP/go.tar.gz"
    progress_stop
    printf '%s  %s\n' "$SHA" "$TMP/go.tar.gz" | sha256sum -c -
    mkdir -p "$ROOT/toolchains"
    tar -xzf "$TMP/go.tar.gz" -C "$TMP"
    mv "$TMP/go" "$GO_HOME"
    log "Go 工具链已就位：$GO_HOME"
  fi
  export PATH="$GO_HOME/bin:$PATH" CGO_ENABLED=1
  # Parallelism and module proxy are chosen for the machine, not fixed at the
  # values a 1 GiB VPS needs. Never change host swap automatically.
  plan_build_parallelism
  setup_go_proxy
  export GOPATH="$ROOT/build-cache/gopath" GOCACHE="$ROOT/build-cache/go-build"
  mkdir "$TMP/bin"
  if [[ -n $SOURCE_DIR ]]; then
    log '开始编译（本地源码，无需拉取依赖时可更快）。'
  else
    log '开始编译。首次安装要下载 sing-box 依赖树（几百个模块），随后编译链接；'
    log '全程几分钟到十几分钟，且可能长时间只有零星输出——这是正常的，不是卡死。'
  fi
  START_BUILD=$(date +%s)
  progress_start '编译中（下载依赖 + 编译链接）' 20
  # 分两步编译：先 xgift CLI 再 Web 服务。用户看到的 go downloading 输出是第
  # 一步在拉依赖，第二次编译会复用构建缓存，明显快得多。
  (cd "$TMP/src"; go build -p "$BUILD_JOBS" -tags with_quic,with_utls -o "$TMP/bin/xgift" ./cmd/xgift)
  log "已编译 xgift，用时 $(( $(date +%s) - START_BUILD )) 秒；继续编译 Web 服务。"
  (cd "$TMP/src"; go build -p "$BUILD_JOBS" -tags with_quic,with_utls -o "$TMP/bin/xgift-web" ./cmd/xgift-web)
  progress_stop
  log "编译完成，总用时 $(( $(date +%s) - START_BUILD )) 秒"
fi
# 部署资产落地。编译路径从 $TMP/src/deploy 取，预编译路径没有源码目录，
# 改为下载与编译器同一个 tag 的 deploy/*（此时 $REF 已在 download_prebuilt
# 里校验过对应的 Release 资产）。取不到就是硬错误：systemd 单元和安装器
# 自身都必须来自与二进制同一版本，不能拿运行中的旧副本凑。
stage_assets() {
  local base src_dir
  if ((PREBUILT == 0)); then
    src_dir="$TMP/src/deploy"
  else
    base="https://github.com/$REPO/raw/$REF/deploy"
    src_dir="$TMP_ASSETS/fetched"
    mkdir -p "$src_dir"
    local asset
    for asset in xgift.service install.sh; do
      curl -fSL --retry 3 --retry-max-time 300 --connect-timeout 15 --max-time 120 \
        --retry-delay 2 --retry-all-errors "$base/$asset" -o "$src_dir/$asset" 2>/dev/null \
        || return 1
    done
    # 旧发布版本里冻结的 install.sh 可能是修复前的版本（例如仍从 $TMP/src/deploy
    # 读单元文件）。把它装成 $ROOT/install.sh 会让用户下次升级又踩同一个坑。
    # 这里做一次自检：下载到的安装器必须能通过语法检查，且必须认识 $TMP_ASSETS
    # 这套路径约定；不合格就退回正在运行的自己。systemd 单元没有这个问题，
    # 它只是路径替换的模板。
    if ! bash -n "$src_dir/install.sh" 2>/dev/null \
       || ! grep -q 'TMP_ASSETS' "$src_dir/install.sh"; then
      log "提示：$REF 里的安装器是旧版本，改用当前运行的安装器写入 $ROOT（避免下次升级重复踩坑）。"
      cp "$SELF" "$src_dir/install.sh"
    fi
  fi
  [[ -f $src_dir/xgift.service && -f $src_dir/install.sh ]] || return 1
  cp "$src_dir/xgift.service" "$src_dir/install.sh" "$TMP_ASSETS/"
  return 0
}
if ((DRY == 0)); then
  stage_assets || die "无法取得部署资产（xgift.service / install.sh，来自 $REF）；请改用 --build-from-source 或 --source-dir"
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
# 首次安装时服务还不存在，systemctl stop 会打印一条红色 "Unit ... not loaded."
# 让用户以为装挂了。这不是错误：只要不是「正在运行却没停下来」就继续。
if [[ -f /etc/systemd/system/xgift.service ]] || [[ $HAD_SERVICE == 1 ]]; then
  systemctl stop xgift || true
else
  log '（首次安装，无需停止已有服务。）'
fi
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
sed -e "s|/opt/xgift|$ROOT|g" -e "s|/etc/xgift/site.env|$ROOT/site.env|" -e "s|/var/lib/xgift|$ROOT/data $ROOT/secrets|" "$TMP_ASSETS/xgift.service" > /etc/systemd/system/xgift.service
systemctl daemon-reload
systemctl enable --now xgift
log "服务已启动，正在等待本机健康检查（127.0.0.1:$PORT/healthz）。"
healthy=0
# -s -S 让失败时也不要吐 "Failed to connect ... Couldn't connect to server"。
# 刚 enable --now 的瞬间进程还没绑上端口，第一次探测必然失败，那是正常的；
# 但这条红色 curl 报错会被当成"装挂了"，所以整体静音，只在最终失败时才解释。
for ((i=0;i<30;i++)); do
  if curl -fsS --max-time 2 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then healthy=1; break; fi
  sleep 2
done
((healthy)) || { journalctl -u xgift -n 30 --no-pager >&2; die '服务未通过健康检查'; }
log '本机健康检查通过。'
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
  log "正在等待公网 HTTPS 生效：https://$DOMAIN/healthz（签发证书 + 反代就绪通常要 10–30 秒）。"
  tls_ok=0
  # 同样静音中间失败：这一步本来就要重试几十秒，把每次失败都打出来只会淹没
  # 关键信息。真正的诊断在下面统一输出一次。
  for ((i=0;i<30;i++)); do
    if curl -fsS --max-time 8 "https://$DOMAIN/healthz" >/dev/null 2>&1; then tls_ok=1; break; fi
    sleep 2
  done
  if ((tls_ok)); then
    log '公网 HTTPS 健康检查通过。'
  else
    # 把最后一条 curl 的真实错误打出来。只说"检查 DNS"没有用——用户看到的
    # 是"DNS 我明明指对了"，真正的原因可能是证书签发失败、安全组没开、
    # AAAA 记录指向别处，或者 CDN 回源打不通。
    tls_err=$(curl -sS -o /dev/null --max-time 8 "https://$DOMAIN/healthz" 2>&1 || true)
    log '公网 HTTPS 健康检查失败，诊断如下：'
    log "  curl 报错：${tls_err:-（无输出）}"
    log "  域名解析：$(python3 -c 'import socket,sys;print(",".join(sorted({i[4][0] for i in socket.getaddrinfo(sys.argv[1],443,type=socket.SOCK_STREAM)})))' "$DOMAIN" 2>/dev/null || echo '解析失败')"
    log "  本机公网：$(local_public_ips | tr '\n' ' ')"
    [[ $MODE == caddy ]] && log '  Caddy 状态：' && systemctl is-active caddy 2>&1 | sed 's/^/    /'
    [[ $MODE == nginx ]] && log '  Nginx 状态：' && systemctl is-active nginx 2>&1 | sed 's/^/    /'
    log '  常见原因：DNS 未生效或指向别的机器、云安全组未放行 80/443、'
    log '            存在指向错误地址的 AAAA(IPv6) 记录、CDN 回源配置不对。'
    die '公网 HTTPS 健康检查失败；已触发程序/配置回滚'
  fi
fi
# Publish both files only after staging succeeds; rollback restores both on failure.
install -m 700 "$TMP_ASSETS/install.sh" "$TMP/install.sh"
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
