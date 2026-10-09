# 商城发货 API（partner）

给外部商城（如 `pay.teyir.com`）用的自动发货接口：商城下单并完成支付后，调用本接口取一枚兑换码，直接在订单详情页和邮件里交给买家。

这条通道**不复用管理员凭据**，`Authorization: Bearer <key>` 走独立的 API Key，只能发码和作废，动不了后台设置。

## 一、先配 API Key

在管理后台「设置 → 商城对接」生成密钥，或直接调管理端接口：

```sh
curl -u admin:<后台密码> -X POST https://gift.example.com/api/admin/settings/partner \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://gift.example.com' \
  -d '{"key":"xgp_<48位十六进制>"}'
```

也可以传空字符串让服务端生成一枚（密钥只在这一次响应里回显，之后只报 `configured`）：

```sh
curl -u admin:<后台密码> -X POST https://gift.example.com/api/admin/settings/partner/rotate \
  -H 'Content-Type: application/json' -H 'Origin: https://gift.example.com'
```

密钥格式固定为 `xgp_` + 48 位十六进制。服务端只存 **SHA-256 哈希**，明文不落库，丢了只能轮换重发。

## 二、三个接口

### 1. 探活

```http
GET /api/partner/ping
Authorization: Bearer xgp_...
```

成功返回 `{"ok":true,"service":"xgift"}`。商城侧可用它在启动时校验密钥是否配好了。

### 2. 发码（幂等）

```http
POST /api/partner/fulfill
Authorization: Bearer xgp_...
Content-Type: application/json
Origin: https://gift.example.com

{"order_id":"20261009-1234","months":6,"reference":"买家备注"}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `order_id` | 是 | 商城订单号，最长 120 字节。**幂等键**，重复调用必须传同一个值 |
| `months` | 是 | 只接受 `3` 或 `6`，其它值 400 |
| `reference` | 否 | 备注，最长 200 字节，仅作记录 |

成功返回：

```json
{"code":"XG-........","hint":"........","months":6,"order_id":"20261009-1234","reused":false}
```

`reused` 为 `true` 表示这单之前已经发过码，这次是**原样回读同一枚**，商城侧据此判断「不要重复发邮件」。

### 3. 作废

```http
POST /api/partner/revoke
Authorization: Bearer xgp_...
Content-Type: application/json
Origin: https://gift.example.com

{"order_id":"20261009-1234"}
```

- 码还没被使用（`active`）→ 置为 `revoked`，返回 `{"ok":true,"status":"revoked"}`
- 码已经被用过或处理中 → **409**，状态不变（不允许退款把一个正在兑换的单子打断）
- 已经作废过 → **200** `{"ok":true,"status":"already_revoked"}`，重复调用安全

## 三、幂等怎么保证的

`order_id` 落在 `partner_orders` 表并加了唯一约束。发码用 `INSERT OR IGNORE` 让数据库**定胜负**：

- 抢到插入的那次请求负责生成并写码；
- 没抢到的（`RowsAffected()==0`）回读已有记录，把同一枚码返回。

所以商城网络超时后**放心直接重试**，不会重复发码。同一个 `order_id` 换 `months` 重调返回 **409**，因为那意味着商城侧订单数据出错了，应当人工核查而不是悄悄换套餐。

## 四、商城侧必须注意的三件事

**1. POST 必须带 `Origin` 头，且要等于 XGift 的站点域名。**

站点对所有 POST 强制校验 `Origin == XGIFT_ORIGIN`（CSRF 防护，既有行为）。头不对或缺失会直接 403 `请求来源不正确`，**在鉴权之前就被拦掉**。服务端对服务端的调用很容易漏这个头，务必在 HTTP 客户端里显式设置。

**2. 交付渠道由商城负责。**

XGift 只把码交还给商城，**不会**替商城发邮件。「订单详情页 + 邮件」都在商城侧实现。

**3. 退款不自动作废。**

已按约定：商城退款时**不要**自动调 `revoke`。已经发出的码是否要作废由人工核查后决定，避免买家把码用掉再退款。真要作废，用 `revoke` 单独处理——它只对未被使用的码生效。

## 五、限流与错误码

`/api/partner/*` 单独限流 **600 次/分钟**，与普通接口互不挤占。大促期间按订单量估算即可，正常不会碰到。

| 状态码 | 含义 | 商城侧建议动作 |
|---|---|---|
| 200 | 成功 | 正常交付 |
| 401 | 密钥无效或未配置 | 告警，检查密钥 |
| 400 | 参数不合法（`months` 不是 3/6、`order_id` 超长等） | 不要重试，查代码 |
| 403 | `Origin` 不匹配 | 不要重试，修配置 |
| 409 | 同 `order_id` 换套餐，或作废一个已使用的码 | **不要重试**，转人工 |
| 503 | 站点未初始化 | 检查 XGift 部署 |

## 六、本地验证

`.artifacts/smoke-test.sh` 会编译真实二进制、起 HTTPS 服务并跑完整流程（bootstrap → 配密钥 → 发码 → 重试幂等 → 换套餐冲突 → 作废 → 公开接口查码 → 后台核对）。需要 WSL 或 Linux，因为项目用了 `syscall.Flock`。

```sh
wsl -d Ubuntu -- bash -c "bash /mnt/f/github/x_gift_bot/.artifacts/smoke-test.sh"
```
