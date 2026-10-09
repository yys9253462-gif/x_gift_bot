# XGift

X（Twitter）Premium 礼品兑换平台。你生成兑换码发给用户，用户在网页上输入兑换码和自己的 X 用户名，系统自动完成 Premium 赠送的下单与付款。

## 这个项目做了什么

在兑换码站点的核心流程之上，重点补了三块**运维友好性**：

**一、业务配置在浏览器里完成**

服务器部署、环境开关和备份仍需终端；初始化与日常业务调整不依赖 CLI 业务向导：

- **首次初始化**（`/setup`）：验证部署时生成的初始化口令，只设置并确认管理员密码（32–256 UTF-8 字节，不允许首尾空白、换行或空字符）。不收集 X 凭据、支付卡、出站节点、商品价格。**保存后需重启服务；重启后初始化页面关闭**。
- **后台设置面板**（`/admin`）：用户名固定为 `admin`，使用自己设置的密码登录；X 登录凭据、支付卡、商品与价格、付款出站和查询出口分别配置。域名、监听地址和自动付款开关由部署环境设置。
- **粘贴整串 Cookie 自动拆分**：换 X 账号时不用逐个字段抄 `auth_token` 和 `ct0`，直接粘贴浏览器请求头原文、扩展导出的 JSON 数组、或含无关项的整段文本，会自动识别填入。

**二、付款出站可在后台管理**

X 按**出口所在国**报价，所以出口配在哪直接决定成本。这块做成可视化编辑：

- 支持 HTTP、SOCKS、Shadowsocks、VMess、VLESS、Trojan、AnyTLS、Hysteria、Hysteria2、TUIC 共 10 种协议，表单填参数即可
- 复杂配置（带完整 transport 块的 VLESS 等）走**原始 JSON 入口**
- 保存时逐节点校验：tag 必须唯一、必须有 server 与 server_port、不接受 detour，最多 128 个节点
- 改动**下一次付款即生效，不需要重启**；已在途的订单仍走原出口，不受影响
- **区域询价与创建订单强制走同一出口** —— 两者分离会导致报价与实际不符，低价区下单必然失败

**三、抗上游失效**

X 会不定期更换 GraphQL 的查询标识（`queryId`），届时所有请求开始失败。上游版本需要改代码重新编译；现在做成在线热更新：

- `ops show / set / reset / probe` 四个命令，**改完下一笔订单即生效，不用重新构建**
- `ops probe` 用一个不存在的用户名探测：X 正常执行查询会回「用户不存在」而非「操作不存在」，以此区分**标识失效**和**账号问题**，避免网络故障时误改配置
- 界面上把「X 拒绝赠送资格」和「标识可能失效」分开提示

## 界面

管理后台按用途分成三组导航，所有需要填写的地方各占一项：

| 分组 | 页面 | 用途 |
|---|---|---|
| 日常 | 兑换码 | 生成、批次管理、列表与筛选 |
| 日常 | 查询 | 按客户或按兑换码直接查订单 |
| 配置 | X 登录凭据 | `auth_token` / `ct0`，支持粘贴自动拆分 |
| 配置 | 支付卡 | 卡池增删、解封 |
| 配置 | 商品与价格 | 套餐时长、金额、商户与商品标识 |
| 配置 | 付款出站 | 决定 X 报价区域的出口节点 |
| 配置 | 查询出口 | 账号资格查询的出口 |
| 运维 | 运维 | 付款节点状态、手动补单、统计概览 |

**界面细节**：统一的面板组件（语义化 `section` + 标题层级）、超宽屏自适应居中、深色模式对比度符合 WCAG AA、窄屏侧栏自动收起。

## 本地预览（不写任何真实配置）

只需 Node.js 22+：

```sh
npm ci
npm run build
npm run preview
```

打开 http://127.0.0.1:4173 是兑换页，http://127.0.0.1:4173/admin 是管理后台。所有数据都是内存示例，不连接真实服务。在兑换页输入 `XG-` 加 48 个字母 `A` 可以演示完整成功流程。

---

## 部署

### 一条命令交互式安装（推荐）

在**目标服务器**运行（Debian/Ubuntu、systemd、amd64/arm64），先把域名 DNS 指向该服务器：

```sh
curl -fSL -H 'Accept: application/vnd.github.raw+json' https://api.github.com/repos/yys9253462-gif/x_gift_bot/contents/deploy/install.sh?ref=main -o /tmp/xgift-install.sh && bash /tmp/xgift-install.sh
```

请以 root 登录执行；非 root 用户将最后的 `bash` 改成 `sudo bash`。下载失败时不会继续运行，也不会让下载管道占用交互输入。

> **不用加 `--ref`，安装器会自动选最新发布版本。** 未指定 `--ref` 时，安装器先查询 GitHub 上的最新 Release，用它的版本号直接下载对应架构的预编译程序，通常几十秒装完，**不需要在目标机上编译**。只有在预编译产物确实取不到时（无 Release、无对应架构产物、校验失败、网络不通）才回退源码编译；`--ref <版本号>` 则用于固定使用某个历史版本，详见 [发布版本](https://github.com/yys9253462-gif/x_gift_bot/releases)。

安装器询问域名和确认后，**优先下载官方 GitHub Release 里对应架构的预编译程序**（通常几十秒即可装完，无需在目标机编译）；下载后先核对 `SHA256SUMS` 中的 SHA256，校验不通过或没有匹配架构的产物时，会自动回退到源码编译，不会留下任何未经验证的文件。回退路径会自动安装编译依赖与匹配 `go.mod` 的 Go（官方 SHA256 校验，不替换系统 Go）、构建两个程序。两条路径都会配置 systemd 并生成保管库密码和初始化口令，默认自动选空闲本机端口（从 8787 起）。回退到源码编译时首次构建可能耗时较长（取决于网络与机器）；编译并行度与 Go 内存软限制不等于总内存上限，仍可能遇到 OOM、下载或编译失败。

| 80/443 场景 | `--https auto` 行为 | 边界 |
|---|---|---|
| 两端口空闲 | 安装/使用宿主 Caddy，追加站点并校验、重载 | 需标准 `/etc/caddy/Caddyfile`、DNS 正确且 80/443 可达 |
| 宿主 Caddy | 复用 Caddy，自有标记块更新，保留其他站点 | 同域名已在非托管配置出现则停止；自定义配置路径未自动适配 |
| 宿主 Nginx | 写独立 `/etc/nginx/conf.d/xgift-installer.conf`；Certbot webroot 签发，启用续期 timer 和重载 hook | 需宿主 nginx 命令、有效配置且实际加载 conf.d；不覆盖非托管文件或现有同名站点 |
| 容器反代、混合监听或其他服务 | 停止并提示，不停止原服务 | 交互模式可选择改用 `--https external`（明确同意后）；非交互需显式指定。未自动修改容器或其他反代 |

Caddy/Nginx 路径必须通过本机服务与公网 HTTPS 健康检查才报告完成；`external` 只验证本机服务，不签发证书、不验证外部 HTTPS。安装器在编译前先检查域名解析，解析失败会直接终止；出现 IPv6 记录时会提示确认本机 IPv6 与入站连通性，但不会修改 DNS、防火墙或 CDN。域名冲突、错误 AAAA、云安全组、防火墙、CDN 回源、非标准宿主配置和证书签发限制都可能需要人工处理，**不保证首次安装必定成功**。非 HTTP 服务占用同一入口的 443 时，不能靠新增 HTTP 站点直接共享；需迁移端口、另一个入口或外部反代。

自动 HTTPS 完成（external 则先接好 HTTPS）后，打开 `https://gift.example.com/setup`，填写安装器显示的初始化口令，并设置、确认管理员密码（32–256 UTF-8 字节，不允许首尾空白、换行或空字符）。保存后点击网页重启，或运行 `systemctl restart xgift`；然后使用 `admin` 和自己设置的密码进入 `/admin` 配置业务。自动付款默认关闭；配置齐全后编辑 `/opt/xgift/site.env`，将 `XGIFT_PAYMENTS_ENABLED=false` 改为 `true` 并重启。部署、初始化、健康检查和重启本身不会发起付款。

启用付款后若卡池或出站尚未配置，服务仍会正常启动并开放后台，只是充值入口保持关闭，后台侧栏显示「充值未开放：重启后生效」；补齐支付卡与节点后重启一次即开放。加密数据损坏仍会直接让服务启动失败，避免带着坏配置运行。

```sh
# 非交互安装 / 仅预览计划
bash /tmp/xgift-install.sh --yes --domain gift.example.com
bash /tmp/xgift-install.sh --dry-run --yes --domain gift.example.com
# 已有 HTTPS 反代
bash /tmp/xgift-install.sh --domain gift.example.com --https external
# 安装后的状态 / 升级 / 卸载
bash /opt/xgift/install.sh --status
bash /opt/xgift/install.sh --upgrade
bash /opt/xgift/install.sh --uninstall
```

默认安装到 `/opt/xgift`，支持 `--dir`、`--port`、`--ref`、`--no-deps`、`--jobs`、`--goproxy`、`--build-from-source`；发布前或离线源码验收可使用 `--source-dir /绝对路径/源码`。

程序来源按以下顺序决定：不指定 `--ref` 时先查询最新 Release 并采用其版本号（查不到则按 `main` 处理）；随后尝试下载该版本对应的 Release 预编译产物（`xgift-linux-amd64` / `xgift-web-linux-amd64` 及 arm64 版本），经 `SHA256SUMS` 校验后直接使用；下载不到、校验失败或当前架构无产物时回退源码编译。失败时会把原因说清楚（例如 `HTTP 404` 表示该 ref 不是带预编译产物的发布版本、`HTTP 000` 表示连不上 GitHub）。`--build-from-source` 可跳过下载强制在目标机编译，`--source-dir` 使用本机源码。想固定使用某个已发布版本，用 `--ref <版本号>`。

回退编译时，编译并行度按机器内存与核数自动决定（1.5 GiB 以上放开到核数，1 GiB 上下为 2，768 MiB 以下保持单任务；可用 `--jobs N` 覆盖），Go 模块代理先实测连通性再选（默认线路偏慢时自动切到 `goproxy.cn`，可用 `--goproxy URL` 指定、`--goproxy off` 强制直连），编译前后会打印所用并行度、代理和耗时；完整参数运行 `--help`。重跑保留数据、密码与付款开关。升级有本机健康门禁，失败恢复旧程序及服务配置；不回滚数据库。卸载保留全部数据与密码，不删除系统依赖。备份时必须同时保留 `data/` 和 `secrets/`；历史程序备份位于 `backups/`，由管理员按需要清理。

安装器还会提前检查并明确提示：磁盘与可用内存、域名解析结果（含 IPv6 提醒）、80/443 实际占用者、宿主反代配置是否已包含该域名、安装目录父路径是否允许服务用户遍历。发现问题时会在下载和编译之前终止，不会留下改到一半的系统状态。

> **升级到新版安装器**：早期版本的安装器不会自动换成本文档描述的新版逻辑。升级时安装器会优先从 `main` 拉取最新安装器再执行（拉不到才用当前文件），所以直接重新下载脚本并执行升级即可：
>
> ```sh
> curl -fSL -H 'Accept: application/vnd.github.raw+json' 'https://api.github.com/repos/yys9253462-gif/x_gift_bot/contents/deploy/install.sh?ref=main' -o /tmp/xgift-install.sh && sudo bash /tmp/xgift-install.sh --upgrade
> ```

安装器 CLI 回归：`python3 deploy/install_test.py`。完整安装、HTTPS 签发和卸载应在独立 Linux 测试机验收。

以下为手动部署流程。

### 手动部署：构建前准备

下面命令在目标 Linux 服务器的源码目录执行，使用发行版包管理器准备 Git、C 编译器、OpenSSL、Go（版本符合 `go.mod`，当前为 1.27.1）和 systemd。前端重建才需要 Node.js 22+。域名 DNS 与 80/443 入口需提前准备；**业务材料不阻塞管理员初始化**，在后台配置时再准备：

1. **X 登录 Cookie**（`auth_token` 和 `ct0`）：在浏览器登录 x.com 后，按 F12 打开开发者工具 → Application（应用）→ Cookies → `https://x.com`，复制这两项的值。这是系统以你的 X 账号身份发起赠送的凭据。
2. **用于付款的银行卡（一张或多张）**：卡号、有效期、CVC，以及发卡行登记的持卡人姓名、账单邮箱和账单国家（两位代码，如 `BD`）。多张卡会在服务端加密保存并随机轮换。请只填真实信息。
3. **代理（可选）**：服务器能直接访问 x.com 就选「直连」；否则准备一个代理节点。支持 sing-box 的任意 outbound 类型，也可以直接粘贴完整 sing-box 配置。
4. **Stripe 公钥**：X 结账页面使用的 `pk_live_` 开头公钥。

**关于网络路径**（这块容易配错，单独说明）：

| 用途 | 使用哪个出口 | 为什么 |
|---|---|---|
| X 账号与资格检查 | 后台「查询出口」配置（可直连） | 与区域定价通道分开 |
| **区域报价校验 + 创建付款链接** | **同一个付款出口** | **X 按出口所在国报价**，两者分离会导致下单必然失败 |
| Stripe 接口 | 同一节点池 | 未配置时直连 |

连接故障触发 6 小时冷却，安全查询最多尝试 3 个出口；付款确认不会自动重放。详见 [付款节点池配置](docs/payment-outbounds.md)。

### 手动部署：构建

```sh
git clone https://github.com/yys9253462-gif/x_gift_bot.git
cd x_gift_bot
npm ci && npm run build        # 前端产物已随仓库提交，可跳过
go build -tags with_quic,with_utls -o bin/xgift ./cmd/xgift
go build -tags with_quic,with_utls -o bin/xgift-web ./cmd/xgift-web
```

> `with_quic,with_utls` 标签是必须的，否则部分代理协议不可用。

### 手动部署：安装服务与密码文件

此流程与仓库原始 unit 配套：程序在 `/opt/xgift/bin`，env 在 `/etc/xgift/site.env`，数据及可写密钥目录在 `/var/lib/xgift`。**不要运行 `xgift setup` 业务向导，不要提前创建空的 `admin-password` 文件**（网页使用排他创建，已有文件会阻止初始化）。首次启动会创建数据库，不能把旧库和新密码混用。

```sh
sudo sh -eu -c '
  id xgift >/dev/null 2>&1 || useradd --system --user-group --home-dir /opt/xgift --shell /usr/sbin/nologin xgift
  install -d -m 755 /opt/xgift /opt/xgift/bin
  install -d -m 700 /etc/xgift
  install -d -m 700 -o xgift -g xgift /var/lib/xgift /var/lib/xgift/secrets
  for name in vault-password setup-password; do
    if [ ! -e "/var/lib/xgift/secrets/$name" ]; then
      (umask 077; openssl rand -hex 32 > "/var/lib/xgift/secrets/$name")
    fi
    chown xgift:xgift "/var/lib/xgift/secrets/$name"
    chmod 600 "/var/lib/xgift/secrets/$name"
  done
'
sudo install -m 755 bin/xgift bin/xgift-web /opt/xgift/bin/
sudo install -m 600 deploy/site.env.example /etc/xgift/site.env
sudoedit /etc/xgift/site.env  # 改 XGIFT_ORIGIN 为实际 HTTPS 域名；确认监听端口空闲
sudo install -m 644 deploy/xgift.service /etc/systemd/system/xgift.service
sudo systemctl daemon-reload
sudo systemctl enable --now xgift
curl -fsS http://127.0.0.1:8787/healthz
```

env 默认示例为 `https://gift.example.com`，密码文件在 xgift 所有的 0700 `secrets/` 内，文件权限 0600；`site.env` 由 root 保管并由 systemd 读取。原 unit 的 `ReadWritePaths=/var/lib/xgift` 允许网页创建管理员文件，不能直接将它移到只读 `/etc/xgift`。改端口或数据路径时需同步调整反代和 unit。复制 env 的命令仅用于首次安装，升级不要覆盖已有配置。

### 手动部署：HTTPS 与管理员初始化

程序强制监听回环地址，`XGIFT_ORIGIN` 必须是 HTTPS origin，不能含路径或尾部 `/`。已有宿主反代请追加独立站点并在校验后重载；容器不能把自己的 `127.0.0.1` 当宿主回环地址，需要自行准备可达的宿主入口/网络，不能直接公开应用端口。

空闲入口使用宿主 Caddy 时，向其实际加载的配置追加以下站点（不要覆盖其他站点；需自行安装 Caddy）：

```caddyfile
gift.example.com {
    encode zstd gzip
    reverse_proxy 127.0.0.1:8787
}
```

```sh
sudo caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
sudo systemctl reload caddy
curl -fsS https://gift.example.com/healthz
```

`deploy/Caddyfile` 是**仅允许 Cloudflare 来源**的另一种模板，不是直接公网通用模板；它要求 Cloudflare 代理、正确来源头和实际加载该配置。直接照抄会使非 Cloudflare 请求返回 403。Nginx/其他反代需自行配置证书、续期、域名、Host 和 HTTPS 转发头；不想手工处理宿主 Nginx 的签发时使用前述安装器。

HTTPS 检查得到 `{"ok":true,...}` 后，用服务器终端本地读取初始化口令（不提交到日志或工单）：

```sh
sudo cat /var/lib/xgift/secrets/setup-password
```

1. 打开 `https://gift.example.com/setup`，输入初始化口令，只设置并确认管理员密码（32–256 UTF-8 字节，不允许首尾空白、换行或空字符）。
2. 点击网页重启，或 `sudo systemctl restart xgift`；重启后 `/setup` 不再开放。
3. 打开 `https://gift.example.com/admin`，使用 `admin` 和自己设置的密码登录，配置 X 凭据、支付卡、查询出口、付款出站、商品和价格。
4. 检查业务配置后，编辑 `/etc/xgift/site.env` 的 `XGIFT_PAYMENTS_ENABLED=true` 并重启。配置不全时启用会导致启动失败；先恢复 `false` 排查。`/healthz` 的 `ok` 只表示服务健康，不证明能下单或付款。

在「兑换码」页生成套餐、数量和批次，复制或下载给用户。保管库密码必须与数据库一起备份；初始化口令与管理员密码不是保管库解密密钥。

---

## 日常维护

业务配置主要在后台完成；域名、监听地址、付款开关与 Turnstile 环境变量仍在 env 调整并重启。CLI 不自动读取 systemd 的 env，也不能依赖默认 `sqlite/vault.db` 指向线上数据。在维护终端先定义以下函数，再执行本节命令（以下按默认一键安装；手动部署将两条路径改为 `/var/lib/xgift/vault.db` 和 `/var/lib/xgift/secrets/vault-password`）：

```sh
xgift() {
  sudo -u xgift /opt/xgift/bin/xgift --db=/opt/xgift/data/vault.db --password-file=/opt/xgift/secrets/vault-password "$@"
}
xgift status                             # 校验必要业务记录，不是全库完整性检查
xgift check                         # 测试代理能否访问 x.com
xgift check-payment-outbounds       # 访问公共探测端点，不创建订单、不付款
xgift resume-payments               # 解除付款暂停
xgift import-chrome                 # macOS：从本机 Chrome 重新导入 Cookie

xgift ops show                      # 查看当前 GraphQL 标识
xgift ops probe                     # 探测标识是否仍被 X 接受
```

更新配置用 `put`（从标准输入读取新值）：

```sh
xgift cards list
echo '新的ct0等JSON' | xgift put --name cookies
echo 'pk_live_新公钥' | xgift put --name stripe-key
echo '{"merchant":"acct_...","currency":"bdt","plans":[...]}' | xgift put --name catalog
```

`--name` 支持：`cookies`、`api-auth`、`card`、`cards`、`proxy`、`payment-outbounds`、`stripe-key`、`catalog`。

> **CLI 参数规则（`cmd/xgift/main.go`）**：解析器会把标志移到位置参数前，所以 `--db=PATH`、`--db PATH` 都可写在子命令前后，例如 `xgift cards list --db=/var/lib/xgift/vault.db`。单横线形式请用 `-db=PATH`；`-db PATH` 不会被预解析器识别为带值标志，会导致 `unexpected argument`。推荐上面的函数固定库与密码路径，其他参数统一使用双横线。

### 抗上游失效

X 更换 `queryId` 时：

```sh
xgift ops show                                  # 看当前生效值和来源
xgift ops set <操作名> <queryId>                # 覆盖，下一笔订单即生效
xgift ops reset                                 # 回到编译进程序的默认值
xgift ops probe                                 # 探测是否仍被接受
```

`<操作名>` 是三者之一：

| 操作名 | 用途 |
|---|---|
| `PremiumGiftingQuery` | 查询账号是否可被赠送 |
| `SubscriptionProductDetailsQuery` | 查询套餐实际价格 |
| `OneTimePurchaseGiftMutation` | 创建赠送订单 |

### 诊断工具

仓库内带了两个只读工具，排查线上问题用：

```sh
XGIFT_PASSWORD_FILE=/opt/xgift/secrets/vault-password go run ./tools/readfail /opt/xgift/data/vault.db
# 加第二个位置参数（如 audit:）才解密并显示匹配前缀的最近三条记录
XGIFT_PASSWORD_FILE=/opt/xgift/secrets/vault-password go run -tags with_quic,with_utls ./tools/priceprobe --db=/opt/xgift/data/vault.db --user=example_user --months=3
```

在源码目录执行，使用有权读取数据和密码的服务器账号；手动部署按前述路径替换，不能把这些工具输出直接公开。两个工具都**不创建订单、不提交付款**，但解密工具打开 vault 时会写自检记录，并非磁盘严格只读；询价需要业务配置和上游可达，且会显示配置元数据。

### 付款卡轮换

付款卡按「卡 × 节点」组合随机轮换：每 3 个连续订单使用同一组合；拒付后立即换组合，先冷却该出口及共享 IP 30 分钟，同一卡在两个不同节点都被拒才进入整卡 30 分钟冷却。补单共用此逻辑；支付方明确 `do_not_try_again` 时该卡永久封锁直到显式解除。

`cards add` 追加或更新（同卡号替换），`cards remove --last4 1234` 移除，`cards rotate` 立即结束当前组合，`cards unblock` 清除冷却与永久封锁。

### 备份、恢复、升级与卸载

一键安装使用 `/opt/xgift/data`、`/opt/xgift/secrets` 和 `/opt/xgift/site.env`；手动部署使用 `/var/lib/xgift`（含 secrets）和 `/etc/xgift/site.env`。备份必须包含两份数据库、WAL 等伴随文件、全部密钥、env 和部署状态；**安装器的 `backups/` 只备份程序/配置，不是数据库备份**。为得到一致副本，先停服务，并确认没有 CLI 付款或补单正在运行：

```sh
sudo systemctl stop xgift
# 默认一键安装；备份文件仅 root 可读
sudo sh -eu -c 'umask 077; tar -czf /root/xgift-backup-$(date +%Y%m%d-%H%M%S).tar.gz -C /opt/xgift data secrets site.env install.conf'
sudo systemctl start xgift
```

手动部署的备份命令改为 `sudo sh -eu -c 'umask 077; tar -czf /root/xgift-backup-$(date +%Y%m%d-%H%M%S).tar.gz -C / var/lib/xgift etc/xgift/site.env etc/systemd/system/xgift.service'`。另存副本到不同故障域并确认能解包；归档含密钥，按敏感资料保护。恢复时停服务，成套恢复数据与原密钥，恢复 xgift 所有权、目录 0700/密钥 0600 后启动；不要只恢复某一份数据库，也不要将旧密文搭配新生成密钥。

一键安装先备份，再运行 `sudo bash /opt/xgift/install.sh --upgrade`（可带 `--ref`）；本机和非 external 的公网健康检查失败会恢复旧程序及服务配置，**不会回滚数据库或本次已安装的依赖、目录、密码**。手动升级先另存旧二进制与 unit，再构建、停服务、备份数据、安装两个新二进制并启动；不要覆盖现有 env 或在线数据库。数据格式发生迁移后，不能假定旧二进制兼容，回退前核对版本或恢复完整备份。

一键卸载 `sudo bash /opt/xgift/install.sh --uninstall` 会停用服务，移除自有 Caddy 标记块/Nginx 站点及相关 hook，保留安装目录、数据、密码、系统依赖与证书；external 的反代入口由你自行移除。手动部署可运行 `sudo systemctl disable --now xgift && sudo rm /etc/systemd/system/xgift.service && sudo systemctl daemon-reload`，再摘除自行配置的反代站点；不删除数据和密钥。

### 验收与排错

```sh
python3 deploy/install_test.py        # 安装器参数/计划回归，非完整安装验收
npm run check                        # TypeScript 检查
# Linux + CGO 编译器 + go.mod 要求的 Go
go test -tags with_quic,with_utls ./...
sudo systemctl --no-pager status xgift
sudo journalctl -u xgift -n 80 --no-pager
curl -fsS http://127.0.0.1:8787/healthz   # 换成 env 的实际端口
curl -fsS https://gift.example.com/healthz
```

- 本机不通：核对 unit 的 env 路径、`XGIFT_ORIGIN` 格式、空闲监听端口、数据/密钥所有权及权限。`site is not initialised` 通常缺少可读初始化口令；不要用创建空管理员文件修复。已有空/损坏管理员文件要先备份并明确其来源再处理。
- 本机正常、公网不通：检查 DNS A/AAAA、安全组 80/443、证书、实际加载的反代配置与 CDN 回源。external 模式安装成功不代表外部接入完成。
- `/setup` 保存失败：确认管理员文件不存在、父目录对 xgift 可写且包含在 unit 的 `ReadWritePaths`；初始化保存后必须重启。
- 付款开关开启后启动失败：暂时恢复 `false`，核查后台业务配置与日志；部署健康检查不会验证 Cookie 有效、区域报价或银行是否接受付款。
- 构建 OOM/上游下载失败：检查可用内存、磁盘、DNS 和下载链路，处理后重跑；不要把内存软限制当作不会 OOM 的承诺。完整安装、签发、升级恢复和卸载仍需独立 Linux 主机实测。

---

## 常见问题

**保管库密码文件丢了怎么办？** 没有原密钥就无法解密原数据；先从成套备份恢复，不要直接删除线上数据库。确认无法恢复后，停服务、保留故障副本，再将旧数据和管理员文件一同移出，使用新密钥重新初始化；原兑换码与订单不会自动恢复。请将数据和 secrets 一起备份。

**X Cookie 过期了？** 后台「X 登录凭据」页直接粘贴新 Cookie 即可（支持整串粘贴自动拆分）。macOS 上也可以用 `xgift import-chrome` 一键刷新。

**想暂停自动充值？** 把对应部署路径的 `site.env` 中 `XGIFT_PAYMENTS_ENABLED` 改为 `false` 并重启服务，自动兑换会被婉拒、兑换码不消耗。此环境开关不等同于付款保护暂停，也不禁止管理员显式确认的手动补单。

**报价金额和实际不符？** 检查「付款出站」配置。X 按出口所在国报价，报价校验与创建链接必须走同一出口。

**付款被拒怎么办？** 明确拒付会显示失败说明并阻止重复提交。网页和 CLI 在同一个 `checkout.lock` 下执行付款，提交间隔至少 30 秒，重启仍保留间隔。普通拒付会先冷却该出口节点及其共享 IP（30 分钟），同一张卡马上换其他节点继续；只有同一张卡在两个不同节点都被拒，才冷却整卡 30 分钟。支付端明确返回 `do_not_try_again` 时只永久封锁被拒的那张卡；仅当所有卡都被永久封锁时才暂停全站。

**付款结果不明怎么办？** 系统宁可标记「待核实」也不会重复扣款。用户用原兑换码点「重新检查并继续兑换」即可自动核对。

**升级程序？** 一键安装先备份，再运行 `sudo bash /opt/xgift/install.sh --upgrade`；手动部署重新构建两个二进制，停服务并备份后替换。不要覆盖现有 env 或线上数据库，详见「备份、恢复、升级与卸载」。

---

## 数据与安全

- `vault.db`：所有敏感信息（Cookie、卡、代理、订单）逐条 AES-256-GCM 加密，密钥由 scrypt 从密码文件派生。没有密码文件谁也读不了。打开时会写一条自检记录验证密码，**密码错误在打开阶段就失败**，不会等到读数据时才报错。
- `site.db`：兑换码索引存摘要和尾号，用户名、状态为明文；可还原的完整兑换码保存在加密 vault 中，历史仅存摘要的兑换码无法还原。请限制两份数据库的文件权限。
- 密钥文件均为 0600；后台使用 HTTPS Basic Auth（密码先哈希再常量时间比较）。
- 接口有限流（按端点分桶、按真实 IP）和同源校验（POST 校验 `Origin`）。
- 响应头带严格 CSP（`default-src 'none'`）、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`、`nosniff`。
- systemd 单元启用 `NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome`、`PrivateTmp`、`UMask=0077`。
- 网页初始化与后台设置不回显完整密钥；安装器会在终端显示初始化口令，诊断工具可能解密审计内容，相关终端记录与输出仍需保密。

### 管理员手动补单

后台运维页使用「预览并补单」查看所有 `review` 订单、每笔金额和当前轮换卡尾号，勾选确认后启动补单。补单与普通付款共用同一套卡 × 节点轮换逻辑。

此操作会发起真实付款，独立于公开充值入口的 `XGIFT_PAYMENTS_ENABLED` 开关；预览、状态查询、部署或服务重启均不会启动付款。预览有效期 10 分钟，卡池或订单内容变化时必须重新预览。

补单在服务器后台串行执行，订单之间至少等待 30 秒。每批每单最多尝试一次；已付款只同步结果，未知付款结果、银行验证、禁止重试指令和不匹配的账单不会重付。旧会话失效时，管理员必须确认已核对原订单未扣款；系统还要求原始明确拒付、对应的失败查询记录，以及再次通过的账号资格和价格检查，才会归档旧订单并生成新链接。每个原会话最多允许 3 次人工重试。

「停止后续订单」会让当前订单完成核实后停止。关闭浏览器不影响任务；服务重启会将运行中的任务标记为中断，不会自动恢复扣款。任务及历史记录保存在加密 vault 的 `admin-recovery:*`，原付款记录保存在 `manual-previous:*`，核验依据保存在 `manual-preflight:*`。页面仅显示卡尾号，不接收卡号或安全码。

「按客户查卡密 / 单独补单」支持输入 X 用户名，跨批次读取绑定订单、解密并验证完整卡密，查看当前付款链接。兑换码列表的「查看卡密 / 补单」打开同一详情。历史仅存哈希的卡密不能还原，但单独补单直接使用订单 ID，不依赖卡密明文。

「仅生成补单链接」与「单独补单」分别创建 `links` 和 `pay` 模式的预览；前者在服务端不调用卡片令牌化或付款确认接口，付款保护暂停时也不解除保护。失效账单替换使用 vault 原子事务保存审计记录并安装新订单，保留人工未扣款确认、历史失败证据和替换次数。

---

## 项目结构

```
cmd/                两个入口：xgift（CLI）与 xgift-web（Web 服务）
internal/
  checkout/         下单流程：资格检查、区域报价、创建订单、付款确认、补单
                   opsprobe.go 实现 GraphQL 标识的在线探测
  site/             Web 服务
                   setup.go / setupcfg.go   管理员初始化与配置校验
                   settings.go              后台设置 API
                   outbounds.go             付款出站管理
  proxy/            内嵌 sing-box，提供付款出口
  vault/            AES-256-GCM + scrypt 的加密存储
  chrome/           macOS 上从本机 Chrome 导入 Cookie
frontend/           React 19 + MUI 7，esbuild 构建
                   AdminSidebar.tsx  侧栏导航
                   Panel.tsx         统一面板组件
                   SettingsPanel.tsx 后台设置面板
                   cookies.ts        粘贴 Cookie 自动解析
                   （产物提交到 internal/site/assets/）
tools/
  priceprobe/       只读：向 X 询价，核对实际币种与金额
  readfail/         只读：查看 vault 记录与失败审计
deploy/             交互式 install.sh、CLI 回归、systemd 单元、Caddyfile、site.env 示例
.github/workflows/  release.yml：打 tag 自动编译并发布预编译产物
docs/               付款节点池说明
```

其他命令：

```sh
npm run check        # TypeScript 类型检查
npm run audit:ui     # Lighthouse 检查
go test ./...        # 全量测试
```

## 发布预编译产物

安装器默认从 Release 取预编译程序，所以**发版时要打 tag 触发 `.github/workflows/release.yml`**：

```sh
git tag -a v0.1.0 -m 'v0.1.0' && git push origin v0.1.0
```

工作流会分别在 `ubuntu-24.04` 与 `ubuntu-24.04-arm` 上原生构建（SQLite 驱动依赖 CGO，不能纯 Go 交叉编译），产出静态链接的 `xgift-linux-{amd64,arm64}`、`xgift-web-linux-{amd64,arm64}` 和 `SHA256SUMS`，并在发布前自检产物确为静态链接。也可在 Actions 页面手动 `workflow_dispatch` 触发。没有对应 Release 时，安装器会自动回退源码编译，功能不受影响，只是慢。

## 许可

MIT，详见 [LICENSE](LICENSE)。
