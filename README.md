# XGift

X（Twitter）Premium 礼品兑换平台。你生成兑换码发给用户，用户在网页上输入兑换码和自己的 X 用户名，系统自动完成 Premium 赠送的下单与付款。

## 这个项目做了什么

在兑换码站点的核心流程之上，重点补了三块**运维友好性**：

**一、全程可在浏览器里配置，不需要 SSH**

上游版本配置靠命令行，改一项就要登服务器。现在从首次安装到日常调整都能在网页完成：

- **首次运行向导**（`/setup`）：打开站点就有引导页，逐项填 X 凭据、支付卡、出站节点、商品价格，完成后自动生成站点配置和后台密码。**初始化完成后这个页面会自动关闭**，不会被后来者探测到。
- **后台设置面板**：密码改完后仍可在「站点设置」随时调整，每个分区独立成页，不再需要命令行。
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

| 兑换页 | 管理页 |
|---|---|
| ![兑换页](docs/screenshots/redeem-light.png) | ![管理页 · 浅色](docs/screenshots/admin-light.png) |

管理页深色模式：

![管理页 · 深色](docs/screenshots/admin-dark.png)

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

### 第一步：准备这些东西

部署前请先准备好以下四样东西，配置向导会逐项询问：

1. **X 登录 Cookie**（`auth_token` 和 `ct0`）：在浏览器登录 x.com 后，按 F12 打开开发者工具 → Application（应用）→ Cookies → `https://x.com`，复制这两项的值。这是系统以你的 X 账号身份发起赠送的凭据。
2. **用于付款的银行卡（一张或多张）**：卡号、有效期、CVC，以及发卡行登记的持卡人姓名、账单邮箱和账单国家（两位代码，如 `BD`）。多张卡会在服务端加密保存并随机轮换。请只填真实信息。
3. **代理（可选）**：服务器能直接访问 x.com 就选「直连」；否则准备一个代理节点。支持 sing-box 的任意 outbound 类型，也可以直接粘贴完整 sing-box 配置。
4. **Stripe 公钥**：X 结账页面使用的 `pk_live_` 开头公钥。

**关于网络路径**（这块容易配错，单独说明）：

| 用途 | 使用哪个出口 | 为什么 |
|---|---|---|
| X 账号与资格检查 | 始终直连 | 与定价无关 |
| **区域报价校验 + 创建付款链接** | **同一个付款出口** | **X 按出口所在国报价**，两者分离会导致下单必然失败 |
| Stripe 接口 | 同一节点池 | 未配置时直连 |

连接故障触发 6 小时冷却，安全查询最多尝试 3 个出口；付款确认不会自动重放。详见 [付款节点池配置](docs/payment-outbounds.md)。

另外需要：一台 Linux 服务器、一个指向该服务器的域名、服务器上安装 Go 1.27+。

### 第二步：构建

```sh
git clone https://github.com/yys9253462-gif/x_gift_bot.git
cd x_gift_bot
npm ci && npm run build        # 前端产物已随仓库提交，可跳过
go build -tags with_quic,with_utls -o bin/xgift ./cmd/xgift
go build -tags with_quic,with_utls -o bin/xgift-web ./cmd/xgift-web
```

> `with_quic,with_utls` 标签是必须的，否则部分代理协议不可用。

### 第三步：启动服务

只需先创建密码文件，然后启动服务，**其余配置在浏览器里完成**：

```sh
./bin/xgift setup          # 生成保管库密码文件（或手工创建 0600 的密码文件）
sudo systemctl link $PWD/deploy/xgift.service
sudo systemctl enable --now xgift
```

> **密码文件务必单独备份**：丢失后所有加密数据无法恢复。

服务起来后浏览器打开你的域名，会自动跳转到**首次运行向导**：

1. **X 凭据** — 可以直接粘贴整串 Cookie，会自动拆分
2. **支付卡** — 卡号会自动校验；多卡可在后台继续追加
3. **出站节点** — 选直连、填节点、或粘贴 sing-box 配置；保存时会校验节点结构（tag 唯一、server 与端口齐全、拒绝 detour）
4. **商品目录** — 用 X Premium 默认目录（3/6 个月），或自定义商户、币种和套餐
5. **站点配置** — 填域名，自动生成配置文件和后台密码（只显示一次，同时存文件）

完成后向导页永久关闭。

### 第四步：配置 HTTPS 反向代理

程序只监听回环地址（启动时会强制校验，不接受对外监听），需要 Caddy 或任意反向代理提供 HTTPS。`deploy/Caddyfile` 是模板，把域名替换后放到 Caddy 配置目录 reload 即可。

验证：

```sh
curl https://你的域名/healthz     # {"ok":true,...} 即成功
```

### 第五步：开始使用

浏览器打开 `https://你的域名/admin`，输入用户名 `admin` 和向导生成的密码。

在「兑换码」页选套餐、数量、批次名生成兑换码，复制或下载发给用户。用户打开站点输入兑换码和 X 用户名即可完成充值。

---

## 日常维护

配置调整都在后台完成。命令行用于检查和排障：

```sh
./bin/xgift status                        # 检查所有加密记录是否完好
./bin/xgift check                         # 测试代理能否访问 x.com
./bin/xgift check-payment-outbounds       # 探测付款出口（不发真实请求）
./bin/xgift resume-payments               # 解除付款暂停
./bin/xgift import-chrome                 # macOS：从本机 Chrome 重新导入 Cookie

./bin/xgift ops show                      # 查看当前 GraphQL 标识
./bin/xgift ops probe                     # 探测标识是否仍被 X 接受
```

更新配置用 `put`（从标准输入读取新值）：

```sh
./bin/xgift cards list
echo '新的ct0等JSON' | ./bin/xgift put --name cookies
echo 'pk_live_新公钥' | ./bin/xgift put --name stripe-key
echo '{"merchant":"acct_...","currency":"bdt","plans":[...]}' | ./bin/xgift put --name catalog
```

`--name` 支持：`cookies`、`api-auth`、`card`、`cards`、`proxy`、`payment-outbounds`、`stripe-key`、`catalog`。

> **CLI 参数位置有讲究**：全局标志必须写在子命令**之前**且用等号形式，例如
> `xgift cards list -db=/var/lib/xgift/vault.db`（写成 `-db PATH` 会报 `unexpected argument`）。

### 抗上游失效

X 更换 `queryId` 时：

```sh
./bin/xgift ops show                                  # 看当前生效值和来源
./bin/xgift ops set <操作名> <queryId>                # 覆盖，下一笔订单即生效
./bin/xgift ops reset                                 # 回到编译进程序的默认值
./bin/xgift ops probe                                 # 探测是否仍被接受
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
go run ./tools/readfail    /path/to/vault.db          # 列出 vault 记录，解密指定前缀的审计内容
go run ./tools/priceprobe                             # 从当前出口向 X 询价，看真实币种与金额
```

两个工具都**不创建订单、不提交付款**。

### 付款卡轮换

付款卡按「卡 × 节点」组合随机轮换：每 3 个连续订单使用同一组合；任意订单被拒后立即换组合，被拒的那张卡进入 30 分钟冷却（其他卡继续轮换），补单也走同一逻辑。支付方明确 `do_not_try_again` 时该卡永久封锁直到显式解除。

`cards add` 追加或更新（同卡号替换），`cards remove --last4 1234` 移除，`cards rotate` 立即结束当前组合，`cards unblock` 清除冷却与永久封锁。

---

## 常见问题

**密码文件丢了怎么办？** 无法恢复。加密数据全部作废，需要删除 `vault.db` 后重新初始化。请把它和数据库一起备份。

**X Cookie 过期了？** 后台「X 登录凭据」页直接粘贴新 Cookie 即可（支持整串粘贴自动拆分）。macOS 上也可以用 `./bin/xgift import-chrome` 一键刷新。

**想暂停充值？** 后台调整，或把 `site.env` 里 `XGIFT_PAYMENTS_ENABLED` 改为 `false` 并重启服务。用户兑换会被婉拒，兑换码不消耗。

**报价金额和实际不符？** 检查「付款出站」配置。X 按出口所在国报价，报价校验与创建链接必须走同一出口。

**付款被拒怎么办？** 明确拒付会显示失败说明并阻止重复提交。网页和 CLI 在同一个 `checkout.lock` 下执行付款，提交间隔至少 30 秒，重启仍保留间隔。普通拒付会先冷却该出口节点及其共享 IP（30 分钟），同一张卡马上换其他节点继续；只有同一张卡在两个不同节点都被拒，才冷却整卡 30 分钟。支付端明确返回 `do_not_try_again` 时只永久封锁被拒的那张卡；仅当所有卡都被永久封锁时才暂停全站。

**付款结果不明怎么办？** 系统宁可标记「待核实」也不会重复扣款。用户用原兑换码点「重新检查并继续兑换」即可自动核对。

**升级程序？** 重新构建两个二进制，备份数据目录和密码文件，替换后 `systemctl restart xgift`。不要覆盖线上数据库。

---

## 数据与安全

- `vault.db`：所有敏感信息（Cookie、卡、代理、订单）逐条 AES-256-GCM 加密，密钥由 scrypt 从密码文件派生。没有密码文件谁也读不了。打开时会写一条自检记录验证密码，**密码错误在打开阶段就失败**，不会等到读数据时才报错。
- `site.db`：兑换码只存摘要和尾号，不存明文；用户名、状态为明文。请限制文件权限。
- 密钥文件均为 0600；后台使用 HTTPS Basic Auth（密码先哈希再常量时间比较）。
- 接口有限流（按端点分桶、按真实 IP）和同源校验（POST 校验 `Origin`）。
- 响应头带严格 CSP（`default-src 'none'`）、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`、`nosniff`。
- systemd 单元启用 `NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome`、`PrivateTmp`、`UMask=0077`。
- 配置向导与后台设置的所有密钥**不落日志、不回显、不进错误信息**。

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
                   setup.go / setupcfg.go   首次运行向导
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

MIT，详见 [LICENSE](LICENSE)。
