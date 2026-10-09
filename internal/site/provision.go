package site

// 商城「直接开通」API（partner provision）。
//
// 与 partner.go 里的发码通道的区别，是这条通道的业务目标不同：
//   - 发码通道交付的是一枚兑换码，买家拿到后还要自己去 xg.teyir.com 兑换；
//   - 开通通道直接拿买家的 X 用户名去执行赠送，买家什么都不用做。
//
// 关键事实（读 checkout 包确认）：真正执行赠送的两个函数
//   checkout.Eligibility(ctx, vault, user, port)        // 校验用户名、取 recipient
//   checkout.RunForRecipient(ctx, vault, user, recipient, true, port, months)
// 只吃「X 用户名」和「月数」，**完全不依赖兑换码**。兑换码在 XGift 里
// 只承担"付款凭据"和"幂等锚点"两个职责——而在商城这条链路上，
// 这两件事商城自己的订单体系已经承担了，所以这里不建兑换码。
//
// 幂等账本复用 partner_orders 的思路，但另立一张表：发码与开通是两种
// 不同的交付物，混在一张表里会让"这单到底发了码还是开了通"难以判断。
//
// 赠送是异步长任务（要下单、提交付款、等待结果，可能数分钟），所以接口
// 不阻塞等待完成：首次调用触发后台任务并返回 processing，商城按同一
// order_id 轮询取进度。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"xgift/internal/checkout"
)

// provision 的状态取值。刻意复用 codes 表已有的状态词表
// （processing/review/succeeded），让运营在两处看到同一套语言。
const (
	provisionProcessing = "processing"
	provisionReview     = "review"
	provisionSucceeded  = "succeeded"
	provisionFailed     = "failed"
)

// xUsernamePattern 与 checkout 内部使用的规则一致：X 用户名只允许
// 小写字母、数字、下划线，长度 1–15。这里提前校验是为了给出
// 明确的中文提示，而不是把错误留到赠送阶段才暴露。
var xUsernamePattern = regexp.MustCompile(`^[a-z0-9_]{1,15}$`)

var errProvisionConflict = errors.New("order already provisioned with a different plan")

// migrateProvision 建立开通账本。追加式迁移，不影响既有数据。
func migrateProvision(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS partner_provisions (
 order_id   TEXT PRIMARY KEY,
 username   TEXT NOT NULL,
 months     INTEGER NOT NULL,
 status     TEXT NOT NULL,
 message    TEXT NOT NULL DEFAULT '',
 progress   INTEGER NOT NULL DEFAULT 0,
 created    INTEGER NOT NULL,
 updated    INTEGER NOT NULL
 );`); err != nil {
		return err
	}
	return nil
}

// provisionRow 是开通账本的一行，也是对外回显的形态。
type provisionRow struct {
	OrderID  string `json:"order_id"`
	Username string `json:"username"`
	Months   int    `json:"months"`
	Status   string `json:"status"`
	Message  string `json:"message"`
	Progress int    `json:"progress"`
	Created  int64  `json:"created"`
	Updated  int64  `json:"updated"`
}

// verifyRequest 是用户名校验请求。商城应在买家付款**之前**调用，
// 把"填错用户名"挡在付款之前——一旦开通完成就撤不回来。
//
// 用 POST 而不是 GET，是因为站点所有需要入参的接口都走 decode()，
// 它要求 application/json 请求体；用 GET + 查询串会撞上 415。
// 与站点其它接口保持一致比"符合 REST 直觉"更重要。
type verifyRequest struct {
	Username string `json:"username"`
}

// partnerVerify 校验 X 用户名是否真实存在、能否接收赠礼。
//
// 这是只读操作，不产生任何订单，也不消耗任何额度。
func (s *server) partnerVerify(w http.ResponseWriter, r *http.Request) {
	var q verifyRequest
	if !decode(w, r, &q) {
		return
	}
	user := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if !xUsernamePattern.MatchString(user) {
		message(w, 400, "X 用户名格式不正确：只能是字母、数字和下划线，且不超过 15 个字符。")
		return
	}
	// 与兑换路径共用同一个出口与同一个校验函数，避免"能收礼"的判定
	// 在两处出现分歧。
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	recipient, err := checkout.Eligibility(ctx, s.vault, user, s.port)
	if err != nil {
		switch {
		case errors.Is(err, checkout.ErrUserNotFound):
			reply(w, 200, map[string]any{"ok": false, "username": user, "reason": "not_found", "message": "该 X 用户不存在，请确认填写的是用户名（@后面的部分），不是显示名称。"})
			return
		case errors.Is(err, checkout.ErrNotEligible):
			reply(w, 200, map[string]any{"ok": false, "username": user, "reason": "not_eligible", "message": "该账号当前无法接收 Premium 赠礼，请确认账号状态正常。"})
			return
		}
		log.Printf("partner verify failed: user=%s: %v", user, err)
		message(w, 503, "暂时无法校验该用户名，请稍后重试。")
		return
	}
	reply(w, 200, map[string]any{"ok": true, "username": user, "recipient": recipient})
}

// provisionRequest 是开通请求体。order_id 由商城提供，作为幂等键。
type provisionRequest struct {
	OrderID  string `json:"order_id"`
	Username string `json:"username"`
	Months   int    `json:"months"`
	// Reference 可选，仅用于运营对账时辨认来源。
	Reference string `json:"reference"`
}

// partnerProvision 按商城订单号执行开通，重复请求返回同一单的进度。
//
// 它不是幂等"立刻返回结果"，而是幂等"指向同一个开通任务"：无论调用
// 多少次，一个 order_id 永远只有一个后台任务、一条账本记录。
func (s *server) partnerProvision(w http.ResponseWriter, r *http.Request) {
	var q provisionRequest
	if !decode(w, r, &q) {
		return
	}
	q.OrderID = strings.TrimSpace(q.OrderID)
	q.Username = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q.Username), "@"))
	if q.OrderID == "" || len(q.OrderID) > 120 {
		message(w, 400, "订单号不能为空且不超过 120 字节。")
		return
	}
	if !xUsernamePattern.MatchString(q.Username) {
		message(w, 400, "X 用户名格式不正确：只能是字母、数字和下划线，且不超过 15 个字符。")
		return
	}
	if q.Months != 3 && q.Months != 6 {
		message(w, 400, "套餐时长仅支持 3 或 6 个月。")
		return
	}
	if len(q.Reference) > 200 {
		message(w, 400, "订单备注过长。")
		return
	}
	// 付款保护暂停时不允许新开通，否则会在系统已知无法付款的情况下
	// 收下订单，把问题留给买家。
	if paused, err := checkout.PaymentPaused(s.vault); err != nil || paused {
		message(w, 503, "充值暂时暂停，请稍后重试。")
		return
	}

	row, created, err := s.claimProvision(r.Context(), q)
	if err != nil {
		if errors.Is(err, errProvisionConflict) {
			message(w, 409, "该订单号已用于其他套餐或用户名，请联系管理员核实。")
			return
		}
		log.Printf("partner provision claim failed: order=%s: %v", q.OrderID, err)
		message(w, 503, "开通任务创建失败，请稍后重试；重复请求不会重复开通。")
		return
	}
	// 只有抢到创建权的请求才启动后台任务。重复调用直接返回当前进度。
	if created {
		s.startProvision(q.OrderID, q.Username, q.Months)
		row.Status = provisionProcessing
		row.Message = "开通任务已创建，正在处理。"
	}
	reply(w, 200, provisionView(row))
}

// provisionView 把账本行转成对外回显，顺带补上给商城的语义说明。
func provisionView(row provisionRow) map[string]any {
	done := row.Status == provisionSucceeded
	out := map[string]any{
		"order_id": row.OrderID,
		"username": row.Username,
		"months":   row.Months,
		"status":   row.Status,
		"message":  row.Message,
		"progress": row.Progress,
		"done":     done,
	}
	// 只有明确失败或需人工介入时才让商城据此停止轮询并告警。
	out["terminal"] = done || row.Status == provisionFailed || row.Status == provisionReview
	return out
}

// claimProvision 用唯一约束决定"谁是这次开通的发起者"，与发码通道
// 同一套思路：不做"先查再写"，避免并发下重复开通两份 Premium。
func (s *server) claimProvision(ctx context.Context, q provisionRequest) (provisionRow, bool, error) {
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return provisionRow{}, false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT OR IGNORE INTO partner_provisions(order_id,username,months,status,message,progress,created,updated) VALUES(?,?,?,?,?,0,?,?)`,
		q.OrderID, q.Username, q.Months, provisionProcessing, "开通任务已创建，正在处理。", now, now)
	if err != nil {
		return provisionRow{}, false, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return provisionRow{}, false, err
	}
	if inserted == 0 {
		row, gerr := provisionByID(tx, q.OrderID)
		if gerr != nil {
			return provisionRow{}, false, gerr
		}
		// 同一订单号换了套餐或换了用户名，说明商城侧数据出了问题，
		// 必须让人看一眼，而不是静默按新参数开通。
		if row.Months != q.Months || row.Username != q.Username {
			return provisionRow{}, false, errProvisionConflict
		}
		if err = tx.Commit(); err != nil {
			return provisionRow{}, false, err
		}
		return row, false, nil
	}
	if err = tx.Commit(); err != nil {
		return provisionRow{}, false, err
	}
	return provisionRow{OrderID: q.OrderID, Username: q.Username, Months: q.Months, Status: provisionProcessing, Created: now, Updated: now}, true, nil
}

// startProvision 在后台执行开通。s.work 是全局单并发信号量，
// 与网页兑换共用——赠送要真的花钱，不允许并发跑多笔。
func (s *server) startProvision(orderID, user string, months int) {
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		select {
		case s.work <- struct{}{}:
			defer func() { <-s.work }()
		case <-s.ctx.Done():
			s.updateProvision(orderID, provisionReview, "服务正在关闭，开通未开始，请重试。", 0)
			return
		}
		ctx, cancel := context.WithTimeout(s.ctx, 240*time.Second)
		defer cancel()
		ctx = checkout.WithProgress(ctx, func(percent int, msg string) {
			s.updateProvision(orderID, provisionProcessing, msg, percent)
		})
		s.runProvision(ctx, orderID, user, months)
	}()
}

// runProvision 是开通的实际执行体。判定顺序刻意与 redeem 保持一致，
// 让两处的失败语义相同：先校验资格，再执行赠送。
func (s *server) runProvision(ctx context.Context, orderID, user string, months int) {
	recipient, err := checkout.Eligibility(ctx, s.vault, user, s.port)
	if err != nil {
		switch {
		case errors.Is(err, checkout.ErrUserNotFound):
			s.updateProvision(orderID, provisionFailed, "该 X 用户不存在，开通未执行。请核实用户名后重试。", 0)
		case errors.Is(err, checkout.ErrNotEligible):
			s.updateProvision(orderID, provisionFailed, "该账号当前无法接收 Premium 赠礼，开通未执行。", 0)
		default:
			log.Printf("provision order %s eligibility failed: %v", orderID, err)
			s.updateProvision(orderID, provisionReview, "资格校验未通过或上游异常，需人工核实；请勿重复提交。", 0)
		}
		return
	}

	record, err := checkout.RunForRecipient(ctx, s.vault, user, recipient, true, s.port, months)
	if err != nil {
		stage := "before_order"
		if record != nil {
			stage = record.Status
		}
		s.persistProvisionFailure(orderID, recipient, months, stage, err)
		// 付款阶段的错误要分清"明确未提交"与"结果不明"，前者可以重试，
		// 后者必须人工核实——重复提交可能导致重复扣款。
		switch {
		case errors.Is(err, checkout.ErrPaymentPaused):
			s.updateProvision(orderID, provisionReview, "付款已暂停，开通未提交。请稍后重新发起。", 0)
		case errors.Is(err, checkout.ErrGiftNotAuthorised):
			s.updateProvision(orderID, provisionFailed, "X 拒绝了本次赠礼资格，开通未完成。", 0)
		case record != nil && record.SubmittedAt != 0:
			s.updateProvision(orderID, provisionReview, "付款结果正在核实，系统不会重复扣款；请保留订单等待人工处理。", 0)
		default:
			s.updateProvision(orderID, provisionReview, "开通未完成，需人工核实处理阶段；请勿重复提交。", 0)
		}
		return
	}

	// 走到这里说明付款已提交并拿到结果。付款被拒仍算未开通。
	if checkout.IsPaymentDeclined(record) {
		s.updateProvision(orderID, provisionFailed, "支付被拒绝，开通未完成。请检查支付卡状态。", 0)
		return
	}
	s.updateProvision(orderID, provisionSucceeded, "Premium 已成功开通。", 100)
	log.Printf("provision order %s succeeded for user %s (%d months)", orderID, user, months)
}

// updateProvision 更新进度。刻意不覆盖终态：一个已经 succeeded 的任务
// 不该被后来迟到的进度回调改回 processing。
func (s *server) updateProvision(orderID, status, msg string, progress int) {
	if _, err := s.db.Exec("UPDATE partner_provisions SET status=?,message=?,progress=MAX(progress,?),updated=? WHERE order_id=? AND status IN ('processing','review')",
		status, msg, progress, time.Now().Unix(), orderID); err != nil {
		log.Printf("provision order %s progress could not be saved: %v", orderID, err)
	}
}

// persistProvisionFailure 把失败详情加密留档。与兑换路径一致：
// 上游原始错误可能含账号信息，只进 vault，不进日志。
func (s *server) persistProvisionFailure(orderID, recipient string, months int, stage string, err error) {
	payload, merr := json.Marshal(map[string]any{
		"order_id":     orderID,
		"recipient_id": recipient,
		"months":       months,
		"stage":        stage,
		"error":        err.Error(),
		"observed_at":  time.Now().Unix(),
	})
	if merr != nil {
		log.Printf("provision order %s failure details could not be encoded: %v", orderID, merr)
		return
	}
	defer clear(payload)
	if perr := s.vault.Put("provision-failure:"+orderID+":"+strconv.FormatInt(time.Now().UnixNano(), 10), payload); perr != nil {
		log.Printf("provision order %s failure details could not be persisted: %v", orderID, perr)
	}
	log.Printf("provision order %s stopped at stage %s; upstream details remain encrypted", orderID, stage)
}

// provisionStatus 让商城轮询开通进度。只读，不产生副作用。
func (s *server) provisionStatus(w http.ResponseWriter, r *http.Request) {
	orderID := strings.TrimSpace(r.URL.Query().Get("order_id"))
	if orderID == "" || len(orderID) > 120 {
		message(w, 400, "订单号不能为空且不超过 120 字节。")
		return
	}
	row, err := provisionByIDQuery(s.db, orderID)
	if errors.Is(err, sql.ErrNoRows) {
		message(w, 404, "没有这个订单号的开通记录。")
		return
	}
	if err != nil {
		message(w, 503, "无法读取开通记录。")
		return
	}
	reply(w, 200, provisionView(row))
}

// provisionByID 在事务内读取账本行。
func provisionByID(tx *sql.Tx, orderID string) (provisionRow, error) {
	var row provisionRow
	err := tx.QueryRow(`SELECT order_id,username,months,status,message,progress,created,updated FROM partner_provisions WHERE order_id=?`, orderID).
		Scan(&row.OrderID, &row.Username, &row.Months, &row.Status, &row.Message, &row.Progress, &row.Created, &row.Updated)
	return row, err
}

// provisionByIDQuery 是 provisionByID 在无事务场景下的等价读取。
func provisionByIDQuery(db *sql.DB, orderID string) (provisionRow, error) {
	var row provisionRow
	err := db.QueryRow(`SELECT order_id,username,months,status,message,progress,created,updated FROM partner_provisions WHERE order_id=?`, orderID).
		Scan(&row.OrderID, &row.Username, &row.Months, &row.Status, &row.Message, &row.Progress, &row.Created, &row.Updated)
	return row, err
}
