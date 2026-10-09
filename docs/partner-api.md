# 商城发货 API（partner）

给外部商城（如 `pay.teyir.com`）用的自动发货接口。提供**两条互相独立的通道**：

| 通道 | 交付物 | 买家要做什么 | 用在哪 |
|---|---|---|---|
| **发码** | 一枚兑换码 | 自己去 `xg.teyir.com` 兑换 | 卖码、囤码、送人 |
| **直接开通** | Premium 直接开在账号上 | **什么都不用做** | 商城卖「X Premium」主力通道 |

两条通道都**不复用管理员凭据**，都用 `Authorization: Bearer <key>` 的独立 API Key，动不了后台设置。

## 一、先配 API Key

在管理后台「设置 → 商城对接」生成密钥，或直接调管理端接口：

```sh
curl -u admin:<后台密码> -X POST https://gift.example.com/api/admin/settings/partner \
  -H 'Content-Type: application/json' \
  -H 'Origin: https://gift.example.com' \
  -d '{"key":"xgp_<48位十六进制>"}'
```

也可以传空对象让服务端生成一枚（密钥只在这一次响应里回显，之后只报 `configured`）：

```sh
curl -u admin:<后台密码> -X POST https://gift.example.com/api/admin/settings/partner/rotate \
  -H 'Content-Type: application/json' -H 'Origin: https://gift.example.com'
```

密钥格式固定为 `xgp_` + 48 位十六进制。服务端只存 **SHA-256 哈希**，明文不落库，丢了只能轮换重发。

## 二、通用约定

**所有 POST 都必须带 `Origin` 头，且要等于 XGift 的站点域名。**

站点对所有 POST 强制校验 `Origin == XGIFT_ORIGIN`（CSRF 防护）。头不对或缺失会直接 403 `请求来源不正确`，**在鉴权之前就被拦掉**。服务端对服务端的调用很容易漏这个头。

```http
Origin: https://xg.teyir.com
```

**套餐月数只接受 `3` 或 `6`**，其它值一律 400。

## 三、探活

```http
GET /api/partner/ping
Authorization: Bearer xgp_...
```

成功返回 `{"ok":true,"service":"xgift"}`。商城侧可用它在启动时校验密钥是否配好了。

---

# 通道 A：直接开通（推荐）

买家在商城填自己的 X 用户名，付款后系统直接把 Premium 开到他账号上。

## A1. 校验用户名（下单前，强烈建议）

```http
POST /api/partner/verify
Authorization: Bearer xgp_...
Content-Type: application/json
Origin: https://xg.teyir.com

{"username":"jack"}
```

**这是只读接口，不产生任何订单，也不花钱。** 商城应当在**买家付款之前**调用它，把「填错用户名」挡在收款之前 —— 一旦开通完成就撤不回来。

成功（用户名有效）：

```json
{"ok": true, "username": "jack", "recipient": "1234567890"}
```

用户名有问题（注意仍是 HTTP 200，靠 `ok` 判断）：

```json
{"ok": false, "username": "jack", "reason": "not_found",
 "message": "该 X 用户不存在，请确认填写的是用户名（@后面的部分），不是显示名称。"}
```

`reason` 取值：
- `not_found` —— 该 X 用户不存在
- `not_eligible` —— 账号存在但当前无法接收 Premium 赠礼

参数格式错误（用户名含非法字符等）返回 **400**。

## A2. 发起开通

```http
POST /api/partner/provision
Authorization: Bearer xgp_...
Content-Type: application/json
Origin: https://xg.teyir.com

{
  "order_id": "20261010-1234",
  "username": "jack",
  "months": 6,
  "reference": "可选备注"
}
```

| 字段 | 必填 | 说明 |
|---|---|---|
| `order_id` | 是 | 商城订单号，最长 120 字节。**幂等键** |
| `username` | 是 | 买家的 X 用户名（不含 `@`）。会自动转小写、自动剥掉 `@` |
| `months` | 是 | `3` 或 `6` |
| `reference` | 否 | 备注，最长 200 字节 |

**开通是异步的**（要下单、提交付款、等结果，可能几分钟），所以接口不阻塞等待，立刻返回：

```json
{"order_id":"20261010-1234","username":"jack","months":6,
 "status":"processing","message":"开通任务已创建，正在处理。",
 "progress":0,"done":false,"terminal":false}
```

## A3. 轮询结果（必须做）

```http
GET /api/partner/provision?order_id=20261010-1234
Authorization: Bearer xgp_...
```

商城按 `order_id` 轮询，直到 `terminal` 为 `true`：

| 字段 | 含义 |
|---|---|
| `status` | `processing` 处理中 / `succeeded` 已开通 / `review` 需人工 / `failed` 已失败 |
| `done` | 是否**成功**开通（只有 `succeeded` 才为 `true`） |
| `terminal` | 是否**可以停止轮询**（`done` 或 `review` 或 `failed`） |
| `progress` | 0–100 进度 |

**关键：`terminal=true` 但 `done=false` 表示开通没成功，不要给买家发「已开通」的邮件，要走人工或退款流程。**

## A4. 幂等是怎么保证的

`order_id` 落在 `partner_provisions` 表并加了唯一约束，用 `INSERT OR IGNORE` 让数据库**定胜负**：

- 抢到插入的那次请求负责发起后台开通任务；
- 没抢到的直接返回**同一单的当前进度**。

所以商城网络超时后**放心直接重试**，不会重复开通两份 Premium。

**同一个 `order_id` 换了用户名或换了套餐 → 409**。这意味着商城侧订单数据出错了，必须人工核查，而不是悄悄按新参数开通。

## A5. 退款边界（重要）

**Premium 一旦开在买家账号上就无法收回。** 因此：

- 商城侧应当**禁止对已成功开通的订单自动退款**，一律转人工
- XGift **不提供**「撤销已开通赠送」的接口 —— 这是刻意不做的，防止商城绕过业务约束

---

# 通道 B：发码

交付物是一枚兑换码，买家自己拿去 `xg.teyir.com` 兑换。适合卖码、囤码、送人。

## B1. 发码（幂等）

```http
POST /api/partner/fulfill
Authorization: Bearer xgp_...
Content-Type: application/json
Origin: https://xg.teyir.com

{"order_id":"20261009-1234","months":6,"reference":"买家备注"}
```

成功返回：

```json
{"code":"XG-........","hint":"........","months":6,"order_id":"20261009-1234","reused":false}
```

`reused` 为 `true` 表示这单之前已经发过码，这次是**原样回读同一枚**。

## B2. 作废

```http
POST /api/partner/revoke
Authorization: Bearer xgp_...
Content-Type: application/json
Origin: https://xg.teyir.com

{"order_id":"20261009-1234"}
```

- 码还没被使用（`active`）→ 置为 `revoked`，返回 `{"ok":true,"status":"revoked"}`
- 已经被用过或处理中 → **409**，状态不变
- 已经作废过 → **200** `{"ok":true,"status":"already_revoked"}`

---

## 四、错误码

| 状态码 | 含义 | 商城侧建议动作 |
|---|---|---|
| 200 | 成功 | 正常处理 |
| 400 | 参数不合法 | 不要重试，查代码 |
| 401 | 密钥无效或未配置 | 告警，检查密钥 |
| 403 | `Origin` 不匹配 | 不要重试，修配置 |
| 404 | 查不到该订单号的记录 | 不要重试，查是否传错订单号 |
| 409 | 同订单号换了套餐/用户名，或作废已使用的码 | **不要重试**，转人工 |
| 503 | 站点未初始化 / 付款暂停 / 上游暂时异常 | 可稍后重试 |

## 五、限流

`/api/partner/*` 单独限流 **600 次/分钟**，与普通接口互不挤占。轮询开通进度也在这个桶里，正常不会碰到。

## 六、本地验证

`.artifacts/smoke-test.sh` 会编译真实二进制、起 HTTPS 服务并跑完整流程。需要 WSL 或 Linux，因为项目用了 `syscall.Flock`。

```sh
wsl -d Ubuntu -- bash -c "bash /mnt/f/github/x_gift_bot/.artifacts/smoke-test.sh"
```

