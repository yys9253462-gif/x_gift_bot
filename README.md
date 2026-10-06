# XGift

X（Twitter）Premium 礼品兑换平台。你生成兑换码发给用户，用户在网页上输入兑换码和自己的 X 用户名，系统自动完成 Premium 赠送的下单与付款。

- **兑换页**：用户自助兑换，实时显示处理进度
- **管理后台**：侧边栏导航，生成/停用兑换码、批次文件夹、单笔查询、统计概览、付款节点与卡池状态
- **安全**：凭据逐条 AES-256-GCM 加密存储，付款前逐项校验金额与商户，付款确认只提交一次
- **抗上游失效**：X 的 GraphQL 标识可在线热更新，不需要重新编译

> 本项目 fork 自 [mizorewww/x_gift_bot](https://github.com/mizorewww/x_gift_bot)，上游历史完整保留。

## 截图

以下截图来自本地模拟预览（全部为示例数据）：

| 兑换页 | 管理页 |
|---|---|
| ![兑换页](docs/screenshots/redeem-light.png) | ![管理页 · 浅色](docs/screenshots/admin-light.png) |

管理页深色模式：

![管理页 · 深色](docs/screenshots/admin-dark.png)

## 本地预览（不写任何真实配置）

想先看看界面？只需要 Node.js 22+：

```sh
npm ci
npm run build
npm run preview
```

打开 http://127.0.0.1:4173 是兑换页，http://127.0.0.1:4173/admin 是管理后台。所有数据都是内存示例，不连接真实服务。在兑换页输入 `XG-` 加 48 个字母 `A` 可以演示完整成功流程。

---

## 部署

### 第一步：准备这些东西

部署前请先准备好以下四样东西，配置向导会逐项询问：

1. **X 登录 Cookie**（`auth_token` 和 `ct0`）：在浏览器登录 x.com 后，按 F12 打开开发者工具 → Application（应用）→ Cookies → `https://x.com`，复制这两项的值。这是系统以你的 X 账号身份发起赠送的凭据。
2. **用于付款的银行卡（一张或多张）**：卡号、有效期、CVC，以及发卡行登记的持卡人姓名、账单邮箱和账单国家（两位代码，如 `BD`）。多张卡会在服务端加密保存并随机轮换，新卡可复用同一账单资料。请只填真实信息。
3. **代理（可选）**：服务器能直接访问 x.com 就选「直连」；否则准备一个代理节点。支持 sing-box 的任意 outbound 类型（anytls、socks、http、shadowsocks、vmess、vless、trojan 等），也可以直接粘贴完整 sing-box 配置。
4. **Stripe 公钥**：X 结账页面使用的 `pk_live_` 开头公钥。

**关于网络路径**（这块容易配错，单独说明）：

| 用途 | 使用哪个出口 | 为什么 |
|---|---|---|
| X 账号与资格检查 | 始终直连 | 与定价无关 |
| **区域报价校验 + 创建付款链接** | **同一个 `payment-outbounds` 出口** | **X 按出口所在国报价**，两者分离会导致低价区下单必然失败 |
| Stripe 接口 | 同一节点池 | 未配置时直连 |

连接故障触发 6 小时冷却，安全查询最多尝试 3 个出口；付款确认不会自动重放。详见 [付款节点池配置](docs/payment-outbounds.md)。

另外需要：一台 Linux 服务器、一个指向该服务器的域名、服务器上安装 Go 1.27+（或在自己电脑上构建后上传二进制）。

### 第二步：构建

```sh
git clone https://github.com/yys9253462-gif/x_gift_bot.git
cd x_gift_bot
npm ci && npm run build        # 构建前端（产物已随仓库提交，可跳过）
go build -tags with_quic,with_utls -o bin/xgift ./cmd/xgift
go build -tags with_quic,with_utls -o bin/xgift-web ./cmd/xgift-web
```

> 构建 `with_quic,with_utls` 标签是必须的，否则部分代理协议不可用。

### 第三步：运行配置向导

```sh
./bin/xgift setup
```

向导会一步步引导你完成配置，全程有中文提示：

1. **密码文件** — 自动生成一个随机密码用于加密保管库，保存在你指定的路径（默认 `sqlite/vault-password`，仅本人可读）。**请务必备份这个文件，丢失后所有加密数据无法恢复。**
2. **X 凭据** — 粘贴第一步准备的两个 Cookie。
3. **支付卡** — 输入卡信息和账单信息（卡号会自动校验）。想启用多卡轮换，向导完成后用 `xgift cards add` 追加更多卡，缺少的账单字段会自动继承。
4. **代理** — 选直连、按提示填节点，或粘贴 sing-box 配置；保存前会实际启动验证配置是否有效。
5. **Stripe 公钥** — 粘贴 `pk_live_` 公钥。
6. **商品目录** — 直接回车使用 X Premium 默认目录（3/6 个月套餐），或自定义商户、币种和套餐。
7. **站点配置** — 输入你的域名（如 `https://xp.example.com`），向导会生成 `site.env` 和随机的后台管理员密码（只显示一次，同时保存在文件里）。

完成后运行 `./bin/xgift status`，六条记录全部显示 `verified` 即为成功。

### 第四步：启动网站

把向导生成的 `site.env`、`admin-password`、`vault-password` 放到安全目录（权限 0600），然后：

```sh
sudo systemctl link $PWD/deploy/xgift.service   # 或直接复制到 /etc/systemd/system/
# 编辑 deploy/xgift.service 中的路径使其与你的安装位置一致
sudo systemctl enable --now xgift
```

> **注意**：程序**只监听回环地址**（如 `127.0.0.1:8787`），启动时会强制校验这一点，不接受对外监听。HTTPS 由反向代理提供。

`site.env` 各字段含义：

| 字段 | 说明 |
|---|---|
| `XGIFT_ORIGIN` | 站点完整域名（`https://` 开头） |
| `XGIFT_LISTEN` | 监听地址，只能回环，如 `127.0.0.1:8787` |
| `XGIFT_DATA_DIR` | 数据目录（vault.db、site.db 所在） |
| `XGIFT_PASSWORD_FILE` | 保管库密码文件路径 |
| `XGIFT_ADMIN_PASSWORD_FILE` | 后台密码文件路径 |
| `XGIFT_PAYMENTS_ENABLED` | `true` 开放充值，`false` 暂停（不消耗兑换码） |
| `XGIFT_TURNSTILE_SITE_KEY` | Cloudflare Turnstile 站点密钥（可选，用于公开提交防机器人） |
| `XGIFT_TURNSTILE_SECRET_FILE` | Turnstile 密钥文件路径（可选，**密钥本身不要写进环境变量**） |

> Turnstile 用不上就整段留空。程序采用 fail-closed 策略：配了验证就必须通过，不做静默降级。

### 第五步：配置 HTTPS 反向代理

`deploy/Caddyfile` 是模板，把 `xp.example.com` 替换成你的域名后放到 Caddy 配置目录并 reload 即可。模板默认只放行 Cloudflare 回源 IP，不用 Cloudflare 时删掉 `@cloudflare` 相关段、保留 `reverse_proxy` 即可。

验证：

```sh
curl https://你的域名/healthz     # {"ok":true,...} 即成功
```

### 第六步：开始使用

浏览器打开 `https://你的域名/admin`，输入用户名 `admin` 和向导生成的密码。

管理后台按用途分成三组导航：

| 分组 | 页面 | 用途 |
|---|---|---|
| 日常 | 兑换码 | 选套餐、数量、批次名生成兑换码，复制或下载发给用户 |
| 日常 | 查询 | 按客户用户名或按兑换码直接查订单 |
| 配置 | X 登录凭据 | 更新 `auth_token` / `ct0`（支持**粘贴整串 Cookie 自动拆分**） |
| 配置 | 支付卡 | 卡池增删、解封 |
| 配置 | 商品与价格 | 套餐时长、金额、商户与商品标识 |
| 配置 | 付款出站 | 决定 X 报价区域的出口节点 |
| 配置 | 查询出口 | 账号资格查询的出口 |
| 运维 | 运维 | 付款节点状态、手动补单、统计概览 |

用户打开 `https://你的域名`，输入兑换码和 X 用户名即可完成充值。

---

## 日常维护

```sh
./bin/xgift status                        # 检查所有加密记录是否完好
./bin/xgift check                         # 测试代理能否访问 x.com
./bin/xgift check-payment-outbounds       # 探测付款出口（不发真实请求）
./bin/xgift import-chrome                 # macOS：从本机 Chrome 重新导入 X Cookie
./bin/xgift resume-payments               # 解除付款暂停
```

更新配置用 `put`（从标准输入读取新值）：

```sh
./bin/xgift cards list                                     # 查看卡池和当前轮换组合（只显示尾号）
echo '{"number":"...","exp_month":"05","exp_year":"2031","cvc":"123"}' | ./bin/xgift cards add
echo '新的ct0等JSON' | ./bin/xgift put --name cookies      # 还有 card / cards / proxy / api-auth
echo 'pk_live_新公钥' | ./bin/xgift put --name stripe-key
echo '{"merchant":"acct_...","currency":"bdt","plans":[...]}' | ./bin/xgift put --name catalog
```

`put` 支持的记录名：`cookies`、`api-auth`、`card`、`cards`、`proxy`、`payment-outbounds`、`stripe-key`、`catalog`。

> **CLI 参数位置有讲究**：全局标志必须写在子命令**之前**，且用等号形式，例如
> `xgift cards list -db=/var/lib/xgift/vault.db`（写成 `-db PATH` 会报 `unexpected argument`）。

### 付款卡轮换规则

付款卡按「卡 × 节点」组合随机轮换：每 3 个连续订单使用同一组合；任意订单被拒后立即换组合，被拒的那张卡进入 30 分钟冷却（其他卡继续轮换），补单也走同一逻辑。支付方明确 `do_not_try_again` 时该卡永久封锁直到显式解除。

`cards add` 追加或更新（同卡号替换），`cards remove --last4 1234` 移除，`cards rotate` 立即结束当前组合，`cards unblock` 清除冷却与永久封锁。

### 抗上游失效

X 会不定期更换 GraphQL 的查询标识（`queryId`），届时相关请求会失败。系统把这部分做成可在线更新，**不需要重新编译**：

```sh
./bin/xgift ops show                                  # 查看当前生效的标识及其来源
./bin/xgift ops set <操作名> <queryId>                # 覆盖某个标识，下一笔订单即生效
./bin/xgift ops reset                                 # 删除覆盖值，回到编译进程序的默认值
./bin/xgift ops probe                                 # 探测标识是否仍被 X 接受（不下单、不付款）
```

`<操作名>` 是以下三个之一：

| 操作名 | 用途 |
|---|---|
| `PremiumGiftingQuery` | 查询账号是否可被赠送 |
| `SubscriptionProductDetailsQuery` | 查询套餐实际价格 |
| `OneTimePurchaseGiftMutation` | 创建赠送订单 |

`ops probe` 用一个不存在的用户名去探测：X 正常运行该查询时会返回「用户不存在」而非「操作不存在」，以此区分**标识失效**与**账号问题**——避免你在网络故障时误改配置。该命令不创建订单、不提交付款。

界面上也会区分这两类错误：「X 拒绝了本次下单（账号不具备赠送资格）」和「标识可能已失效」是不同的提示。

---

## 常见问题

**密码文件丢了怎么办？** 无法恢复。加密数据全部作废，需要删除 `vault.db` 后重新运行 `xgift setup`。请把它和数据库一起备份。

**X Cookie 过期了？** 管理后台「X 登录凭据」页可以直接粘贴整串 Cookie（支持请求头原文、浏览器扩展导出的 JSON 数组、含无关项的整段文本），会自动拆分填入。macOS 上也可以用 `./bin/xgift import-chrome` 一键刷新。

**想暂停充值？** 管理后台修改，或把 `site.env` 里 `XGIFT_PAYMENTS_ENABLED` 改为 `false` 并重启服务。用户兑换会被婉拒，兑换码不消耗。

**报价金额和实际不符？** 检查「付款出站」配置。X 按出口所在国报价，报价校验与创建链接必须走同一出口，否则金额对不上会导致下单被拒。

**付款被拒怎么办？** 明确拒付会显示失败说明并阻止重复提交，不再显示自动核实。网页和 CLI 在同一个 `checkout.lock` 下执行付款，提交间隔至少 30 秒，重启仍保留间隔。付款按「卡 × 节点」组合随机轮换，每 3 个连续订单使用同一组合；普通拒付会立即结束当前组合，并**先冷却该出口节点及其共享 IP（30 分钟）**，同一张卡马上换其他节点继续付款；只有同一张卡在两个不同节点都被拒，才冷却整卡 30 分钟。支付端明确返回 `do_not_try_again` 时只永久封锁被拒的那张卡；仅当所有卡都被永久封锁时才暂停全站，可用 `xgift cards unblock` 显式解除。

**付款结果不明怎么办？** 系统宁可标记「待核实」也不会重复扣款。用户用原兑换码点「重新检查并继续兑换」即可自动核对，确认成功后自动补上状态。

**升级程序？** 重新构建两个二进制，备份数据目录和密码文件，替换后 `systemctl restart xgift`。不要覆盖线上数据库。

---

## 数据与安全

- `vault.db`：所有敏感信息（Cookie、卡、代理、订单）逐条 AES-256-GCM 加密，密钥由 scrypt 从密码文件派生。没有密码文件谁也读不了。
- `site.db`：兑换码只存摘要和尾号，不存明文；用户名、状态为明文。请限制文件权限。
- 所有密钥文件均为 0600（仅本人可读）；后台使用 HTTPS Basic Auth（密码先哈希再常量时间比较）。
- 接口有限流（按端点分桶、按真实 IP）和同源校验（POST 校验 `Origin`）。
- 响应头带严格 CSP（`default-src 'none'`）、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`、`nosniff`。
- systemd 单元启用 `NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome`、`PrivateTmp`、`UMask=0077`。

### 管理员手动补单

登录 `/admin` → 运维页，使用「预览并补单」查看所有 `review` 订单、每笔金额和当前轮换卡尾号，勾选确认后点击「确认付款并启动补单」。补单与普通付款共用同一套卡 × 节点轮换逻辑：被拒订单会换到新组合，同一批次的后续订单继续沿用新组合。

此操作会发起真实付款，独立于公开充值入口的 `XGIFT_PAYMENTS_ENABLED` 开关；预览、状态查询、部署或服务重启均不会启动付款。预览有效期为 10 分钟，卡池配置或订单内容发生变化时必须重新预览。

补单在服务器后台串行执行，订单之间至少等待 30 秒，并保留全局付款间隔。每批每单最多尝试一次；已付款只同步结果，未知付款结果、银行验证、禁止重试指令和不匹配的账单不会重付。有效会话会复用并重新核验商户、客户、套餐、金额及未收款/未授权金额证据。旧会话失效时，管理员必须确认已核对原订单未扣款；系统还要求原始明确拒付、对应的失败查询记录，以及再次通过的账号资格和价格检查，才会归档旧订单并生成新链接。每个原会话最多允许 3 次人工重试。

「停止后续订单」会让当前订单完成核实后停止。关闭浏览器不影响任务；服务重启会将运行中的任务标记为中断，不会自动恢复扣款。重新预览后，结果不明的原付款仍被排除。任务及历史记录保存在加密 vault 的 `admin-recovery:*`，原付款记录保存在 `manual-previous:*`，核验依据保存在 `manual-preflight:*`，诊断错误保存在 `manual-recovery-error:*`。页面仅显示卡尾号，不接收卡号或安全码。

管理页的「按客户查卡密 / 单独补单」支持输入 X 用户名，跨批次读取绑定订单、解密并验证完整卡密，查看当前付款链接。兑换码列表的「查看卡密 / 补单」打开同一详情。历史仅存哈希的卡密不能还原，但单独补单直接使用订单 ID，不依赖卡密明文。

「仅生成补单链接」与「单独补单」分别创建 `links` 和 `pay` 模式的预览；前者在服务端不调用卡片令牌化或付款确认接口，付款保护暂停时也不解除保护。两种模式都只处理预览绑定的客户。失效账单替换使用 vault 原子事务保存 `replacement-original:<旧会话>` 审计并安装新订单记录，保留人工未扣款确认、历史失败证据、前一个会话和替换次数。有效未支付链接会直接复用；生成新链接后可以在客户详情查看。服务器重启不会自动继续任务。

---

## 项目结构

```
cmd/                两个入口：xgift（CLI）与 xgift-web（Web 服务）
internal/
  checkout/         下单流程：资格检查、区域报价、创建订单、付款确认、补单
  site/             Web 服务：路由、后台接口、兑换码与批次、统计
  proxy/            内嵌 sing-box，提供付款出口
  vault/            AES-256-GCM + scrypt 的加密存储
  chrome/           macOS 上从本机 Chrome 导入 Cookie
frontend/           React 19 + MUI 7，esbuild 构建，产物提交到 internal/site/assets/
deploy/             systemd 单元、Caddyfile、site.env 示例
docs/               付款节点池说明与截图
```

其他命令：

```sh
npm run check        # TypeScript 类型检查
npm run audit:ui     # Lighthouse 检查
go test ./...        # 全量测试
```

## 许可

MIT，详见 [LICENSE](LICENSE)。上游项目：[mizorewww/x_gift_bot](https://github.com/mizorewww/x_gift_bot)。
