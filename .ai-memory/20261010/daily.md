# 2026-10-10

## [00:20] - 上线完成: 瑞典兑换站更新到含商城发货接口的版本

- **目标**: 瑞典服务器（se，13.63.71.232）跑兑换站，东京服务器跑商城；本轮把新功能上线到瑞典。
- **服务器现状（只读核查）**:
  - 服务 `xgift.service`，用户 `xgift`，`ExecStart=/opt/xgift/bin/xgift-web`
  - 站点域名 `https://xg.teyir.com`，监听 `127.0.0.1:8787`，Caddy 做反代
  - 源码目录 `/opt/xgift-src`，`main` 分支（**注意**：早期脚本写过 `dev` 分支，本轮实际以 `main` 为准）
  - Go 1.27.1 已装，构建自 `main` 分支
  - 仓库**没有任何 Release**，只有 `v0.1.0`/`v0.2.0` 标签 → 安装器会回退源码编译，
    因此更新必须走「服务器上拉源码 + 本地编译 + 替换二进制」这条路
- **上线步骤**:
  1. 本地提交并推送：`99155f9 feat: 新增商城发货 API，支持外部商城自动发码`
  2. 备份：旧二进制 → `xgift-web.bak-update-<时间戳>`；`site.env.bak-<时间戳>`；
     停服后把 `site.db`/`vault.db` 拷到 `/root/xgift-db-backup-<时间戳>/`
  3. 服务器拉取：`/opt/xgift-src` 从 `46dc528` 快进到 `99155f9`
  4. 先在服务器跑单测：`go test ./internal/site/ -run Partner` → ok（说明新代码在本机可编译可测）
  5. 编译：`CGO_ENABLED=1 go build -tags with_quic,with_utls -o bin/xgift-web ./cmd/xgift-web`
  6. 替换二进制 + `systemctl restart xgift`
- **踩坑**: `cp` 覆盖运行中的二进制报 `Text file busy` ——
  **必须 `systemctl stop` 之后再替换**，不能边跑边换。
- **验证（全部通过）**:
  - `strings` 确认新二进制已含 `api/partner/fulfill`
  - `/healthz` → `{"ok":true,"payments_enabled":true}`（付款开关保持开启，旧业务未受影响）
  - `/api/setup/status` → 404（仍处已初始化状态，未被误重置）
  - 无密钥 `/api/partner/ping` → 401；正确密钥 → `{"ok":true,"service":"xgift"}`
  - 发码 → 重复请求返回**同一枚码**；换套餐 → 409；作废 → 200；重复作废 → 幂等
  - 后台列表出现「商城订单」批次，码已真实入库
  - **跨机验证**：从东京服务器带密钥调用 `https://xg.teyir.com/api/partner/fulfill` → 200 并取到码，
    延迟约 0.78s。**这正是后续商城对接的核心链路，已打通。**
  - 服务日志无 panic/error
- **密钥**: 商城 API 密钥已生成，存于瑞典 `/root/xgift-partner-key.txt`（0600）。
  前缀 `xgp_`。**不写入本文件**，按密钥管理规矩不落库到仓库。
- **清理**: 验证用的 `SE-TEST-001`、`CROSS-TEST-001` 两笔测试单均已作废，不干扰真实业务。
- **遗留**: 商城侧（东京 `pay.teyir.com`）的代码接入尚未开始 —— 属于下一阶段，
  需要商城侧改代码：下单支付后调 fulfill、把码显示在订单详情并邮件发给买家、退款不自动作废。
