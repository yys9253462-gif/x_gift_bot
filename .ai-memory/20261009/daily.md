# 2026-10-09

## [23:10] - 只读检查: 商城自动发货对接前置调研

- **文件**: 未修改（只读）
- **决策**: 确认 XGift 现有 `/api/admin/*` 为 Basic Auth 管理接口，无商城专用 API Key 通道；发码逻辑集中在 `site.go:generate`
- **验证**: `git status` 干净；`grep` 路由表；`ssh tokyo` 只读检查 `/opt/ghost/paid-access`

## [23:35] - 方案定案: 新增商城发货 API 契约

- **文件**: 计划新增 `internal/site/partner.go`、`partner_test.go`
- **决策**:
  - 认证：独立 API Key（Bearer），与 admin Basic Auth 分离；密钥哈希存 vault `partner-key`
  - 端点：`GET /api/partner/ping`、`POST /api/partner/fulfill`、`POST /api/partner/revoke`
  - 幂等键 = 商城订单号 `order_id`，落库 `partner_orders` 表，唯一约束保证重复请求返回同一枚码
  - 每单发 1 枚，套餐仅 3/6 个月（与 XGift `months CHECK(3,6)` 一致）
  - 退款策略：不自动作废，`revoke` 仅在码未使用时置 `revoked`
- **验证**: 待单测 + 本地 HTTP 冒烟（计划）

## [00:05] - 实现完成: 商城发货 API（partner 通道）

- **文件**:
  - 新增 `internal/site/partner.go`（全部实现 ~330 行）
  - 新增 `internal/site/partner_test.go`（11 个测试）
  - 修改 `internal/site/site.go`（3 条 partner 路由 + 3 条管理端密钥路由 + 迁移接线 + 独立限流桶）
  - 新增 `docs/partner-api.md`（对接契约文档）
  - 新增 `.artifacts/smoke-test.sh`（真机 HTTPS 端到端冒烟）
- **关键实现决策**:
  - 幂等靠数据库唯一约束定胜负：`INSERT OR IGNORE INTO partner_orders` + `RowsAffected()`
    判胜负（赢家发码、输家回读），**不用「先查再写」**，从根上杜绝并发双发。
  - 批次必须在**同一个事务**里建：`site.db` 是单连接（`SetMaxOpenConns(1)`），
    外层事务未提交时另开事务会直接自锁。故复用 `ensureBatch(tx, "商城订单")`。
  - 密钥只存 SHA-256 哈希于 vault 键 `partner-key`，格式 `xgp_` + 48 位十六进制。
  - `revoke` 只作废 `active`；已 `revoked` 幂等 200；已使用/处理中返回 409 且不改状态。
  - 独立限流桶 `partner:` 600 次/分钟，避免与普通接口互相挤占。
- **踩坑记录**:
  1. 单测 8/11 报 503 —— **测试夹具建表语句漏了 `folder_id`/`copyable` 列**，
     报 `table codes has no column named folder_id`。属测试代码问题，非产品代码问题。
     加了 `log.Printf("partner fulfill failed: ...")` 才定位到，说明**错误日志不能省**。
  2. 冒烟首轮：服务启动即退出，报
     `vault database is missing while the admin password still exists`。
     根因：脚本直接写了一个 65 字节的随机管理员密码，产品据此判定「站点已初始化」，
     但 vault 不存在 → 正确拒绝启动。**正确姿态是先走 bootstrap**：
     初始不建管理员密码文件（或短于 32 字节）→ 进 bootstrap 模式 → 调 `/api/setup/apply`
     → 重启 → 进入正常模式。
  3. 冒烟二轮：所有 POST 返回 403 `请求来源不正确`。
     根因：产品对**所有 POST 强制校验 `Origin == XGIFT_ORIGIN`**（CSRF 防护）。
     **服务端对服务端的调用极易漏这个头**，已写进 `docs/partner-api.md` 显著提示商城侧必配。
  4. 冒烟脚本里 `apt-get install socat` 卡网络超时 3 分钟拖死整轮 ——
     改为**优先 socat，不在则用 Python 标准库写 TLS 终结代理**，两者都不需要联网安装。
- **验证**（全部通过）:
  - `GOOS=linux GOARCH=amd64 go vet ./internal/site/` 无输出
  - `go test ./internal/site/ -run Partner -v -count=1` → 11/11 通过
  - `go test ./... -count=1` → `checkout`/`proxy`/`site`/`vault` 四包全 ok
  - HTTPS 端到端冒烟 13 个环节全绿：bootstrap → 配密钥 → 无密钥 401 →
    正确密钥 ping 200 → 首次发码 200 → **重复请求返回同一枚码（幂等成立）** →
    换套餐 409 → 作废 200 → 重复作废幂等 → 码在 `/api/status` 可查（`status:active`）→
    后台列表出现「商城订单」批次共 2 枚 → 服务端日志无 panic/error
- **边界**: 本轮**只开发并测试 XGift**，未动东京商城生产环境
  （`/opt/ghost/paid-access` 有未提交改动，全程只读，未做任何写操作）。
- **待办**: 商城侧接入（SKU 映射 3/6 个月各一件、每单 1 枚、交付走订单详情+邮件、
  退款不自动作废）待老大另行确认后开展。
