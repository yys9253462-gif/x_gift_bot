package site

// 商城发货 API（partner API）。
//
// 存在的理由：XGift 已有的发码入口是 /api/admin/codes，它使用管理员 Basic
// Auth，只适合运营在后台手工生成一批码。商城（pay.teyir.com 等）需要在
// **支付成功后由服务端自动调用**，两者信任模型完全不同：
//   - 管理员凭据是人的登录口令，不应下发给任何第三方系统；
//   - 商城调用需要一个可单独轮换、可单独吊销、权限仅限"发码/作废"的凭据；
//   - 商城的重试与服务端的超时都要求同一个订单号永远得到同一枚兑换码。
//
// 所以这里引入独立的 API Key 通道与独立的幂等账本（partner_orders）：
//   - 认证：Authorization: Bearer <key>，密钥只存哈希，可比对不可还原；
//   - 幂等：order_id 为幂等键，唯一约束由数据库保证，重复请求返回同一枚码；
//   - 最小权限：只有发码与作废两个动作，不能读写统计、设置或订单列表。
//
// 与既有架构保持一致的部分：仍走 s.vault 存密钥、仍复用 generate 的入库
// 方式（vault 先写密文再写 codes 行）、仍复用 reply/message/decode 的
// 响应约定与 middleware 的限流。

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"
)

// partnerKeyPattern 约束 API Key 的形态：前缀便于识别来源，
// 后面是 48 位十六进制（24 字节随机）。固定长度让比对可以是常数时间。
const partnerKeyPrefix = "xgp_"

var errNoPartnerKey = errors.New("partner API key is not configured")

// partnerOrderRow 是幂等账本的一行。它记录"某个商城订单已经发出哪一枚码"，
// 是重复请求返回同一枚码的唯一依据。
type partnerOrderRow struct {
	OrderID string `json:"order_id"`
	CodeID  string `json:"-"`
	// Hint 是兑换码末 8 位，用于运营比对，不泄露整码。
	Hint    string `json:"hint"`
	Months  int    `json:"months"`
	Created int64  `json:"created"`
}

// migratePartner 建立幂等账本表。迁移是追加式的：已有兑换码不受影响。
func migratePartner(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS partner_orders (
 order_id TEXT PRIMARY KEY,
 code_id  TEXT NOT NULL,
 created  INTEGER NOT NULL
 );`); err != nil {
		return err
	}
	return nil
}

// partnerHash 与管理员口令一样只做单次 SHA-256：密钥本身是高熵随机串，
// 不需要慢哈希，慢哈希只会在每次请求上浪费时间。
func partnerHash(key string) string {
	b := sha256.Sum256([]byte(key))
	return hex.EncodeToString(b[:])
}

// partnerAuthenticated 校验 Bearer 密钥。密钥未配置时一律拒绝，
// 不能因为"没配钥匙"就把接口放开。
func (s *server) partnerAuthenticated(r *http.Request) bool {
	stored, err := s.vault.Get("partner-key")
	if err != nil {
		return false
	}
	defer clear(stored)
	field := strings.TrimSpace(string(stored))
	if field == "" {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const scheme = "Bearer "
	if len(header) <= len(scheme) || !strings.EqualFold(header[:len(scheme)], scheme) {
		return false
	}
	presented := partnerHash(strings.TrimSpace(header[len(scheme):]))
	return subtle.ConstantTimeCompare([]byte(presented), []byte(field)) == 1
}

// partnerKeyValid 校验待保存的 API Key 形态。前缀 + 48 位十六进制。
func partnerKeyValid(key string) bool {
	if !strings.HasPrefix(key, partnerKeyPrefix) {
		return false
	}
	body := key[len(partnerKeyPrefix):]
	if len(body) != 48 {
		return false
	}
	for _, c := range body {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// partner 是 API Key 鉴权中间件。认证失败不区分"没配密钥"与"密钥不对"，
// 避免把服务端配置状态泄露给未授权调用方。
func (s *server) partner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.bootstrapEnabled() {
			message(w, 503, "站点尚未初始化。")
			return
		}
		if !s.partnerAuthenticated(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="XGift Partner"`)
			message(w, 401, "API 密钥无效。")
			return
		}
		next(w, r)
	}
}

// partnerPing 让商城在落配置前自检凭据是否可用，不产生任何副作用。
func (s *server) partnerPing(w http.ResponseWriter, r *http.Request) {
	reply(w, 200, map[string]any{"ok": true, "service": "xgift"})
}

// fulfillRequest 是发货请求体。order_id 由商城提供，作为幂等键。
type fulfillRequest struct {
	OrderID string `json:"order_id"`
	Months  int    `json:"months"`
	// Reference 可选，仅用于运营对账时辨认来源（如商城订单标题）。
	Reference string `json:"reference"`
}

// partnerFulfill 按商城订单号发码，重复请求返回同一枚码。
//
// 幂等的实现刻意不做"先查再写"：两个并发请求会同时查不到再同时写，
// 于是发两枚码。这里改成数据库唯一约束定胜负——先 INSERT 抢锁，
// 唯一的那个赢家负责生成码；抢输的那个回读已存在的记录并返回。
func (s *server) partnerFulfill(w http.ResponseWriter, r *http.Request) {
	var q fulfillRequest
	if !decode(w, r, &q) {
		return
	}
	q.OrderID = strings.TrimSpace(q.OrderID)
	if q.OrderID == "" || len(q.OrderID) > 120 {
		message(w, 400, "订单号不能为空且不超过 120 字节。")
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
	code, row, err := s.fulfillOrder(r, q.OrderID, q.Months)
	if err != nil {
		if errors.Is(err, errFulfillConflict) {
			message(w, 409, "该订单号已用于其他套餐，请联系管理员核实。")
			return
		}
		log.Printf("partner fulfill failed: order=%s: %v", q.OrderID, err)
		message(w, 503, "发码失败，请稍后重试；重复请求不会重复发放。")
		return
	}
	reply(w, 200, map[string]any{
		"order_id": row.OrderID,
		"code":     code,
		"months":   row.Months,
		"hint":     row.Hint,
		"created":  row.Created,
	})
}

// errFulfillConflict 表示同一订单号被要求发不同套餐的码。
var errFulfillConflict = errors.New("order already fulfilled with a different plan")

// partnerBatchName 是商城自动发码固定归入的批次名。固定名称让运营在后台
// 一眼就能把机器发的码与人工批次分开。
const partnerBatchName = "商城订单"

// fulfillOrder 是幂等发码的核心。返回值里的 code 只在本次真正生成时给出；
// 已存在时通过回读密文取回同一枚码。
func (s *server) fulfillOrder(r *http.Request, orderID string, months int) (string, partnerOrderRow, error) {
	now := time.Now().Unix()
	codeID := token(16)
	code := "XG-" + strings.ToUpper(token(24))

	// 先把密文写进 vault，再写账本行：中断最多留下一条读不到的密文，
	// 绝不会出现"账本里有一枚码、但它的密文不存在"的悬挂状态。
	if err := s.vault.Put("redemption:"+codeID, []byte(code)); err != nil {
		return "", partnerOrderRow{}, err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return "", partnerOrderRow{}, err
	}
	defer tx.Rollback()
	// 批次必须在同一个事务里建：site.db 是单连接（SetMaxOpenConns(1)），
	// 在外层事务未提交时另开一个事务会直接自锁。
	batch, err := ensureBatch(tx, partnerBatchName)
	if err != nil {
		return "", partnerOrderRow{}, err
	}
	// INSERT OR IGNORE + 检查受影响行数：赢家写 1 行，输家写 0 行。
	res, err := tx.Exec(`INSERT OR IGNORE INTO partner_orders(order_id,code_id,created) VALUES(?,?,?)`, orderID, codeID, now)
	if err != nil {
		return "", partnerOrderRow{}, err
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return "", partnerOrderRow{}, err
	}
	if inserted == 0 {
		// 已经发过：回读既有记录，保证返回同一枚码。
		existing, gerr := partnerOrderByID(tx, orderID)
		if gerr != nil {
			return "", partnerOrderRow{}, gerr
		}
		if existing.Months != months {
			return "", existing, errFulfillConflict
		}
		plain, perr := s.vault.Get("redemption:" + existing.CodeID)
		if perr != nil {
			return "", existing, perr
		}
		defer clear(plain)
		if err = tx.Commit(); err != nil {
			return "", existing, err
		}
		return string(plain), existing, nil
	}
	if _, err = tx.Exec(`INSERT INTO codes(id,hash,hint,batch,months,status,created,updated,folder_id,copyable) VALUES(?,?,?,?,?,'active',?,?,?,1)`, codeID, hash(code), code[len(code)-8:], partnerBatchName, months, now, now, batch); err != nil {
		return "", partnerOrderRow{}, err
	}
	if err = tx.Commit(); err != nil {
		return "", partnerOrderRow{}, err
	}
	return code, partnerOrderRow{OrderID: orderID, CodeID: codeID, Hint: code[len(code)-8:], Months: months, Created: now}, nil
}

// partnerOrderByID 读取账本行，并顺带取出码的时长（挂在 codes 表上）。
func partnerOrderByID(tx *sql.Tx, orderID string) (partnerOrderRow, error) {
	var row partnerOrderRow
	err := tx.QueryRow(`SELECT p.order_id,p.code_id,p.created,c.hint,c.months FROM partner_orders p JOIN codes c ON c.id=p.code_id WHERE p.order_id=?`, orderID).
		Scan(&row.OrderID, &row.CodeID, &row.Created, &row.Hint, &row.Months)
	return row, err
}

// revokeRequest 是作废请求体。order_id 指向商城订单对应的那枚码。
type revokeRequest struct {
	OrderID string `json:"order_id"`
}

// partnerRevoke 按商城订单号作废对应兑换码。
//
// 只作废仍为 active 的码：已经进入 processing/succeeded 的码意味着买家
// 已经拿去兑换了，撤销它只会让账实不符，必须交人工核实。刻意不提供
// "强制作废"参数——那等于给商城一个绕过业务约束的后门。
func (s *server) partnerRevoke(w http.ResponseWriter, r *http.Request) {
	var q revokeRequest
	if !decode(w, r, &q) {
		return
	}
	q.OrderID = strings.TrimSpace(q.OrderID)
	if q.OrderID == "" || len(q.OrderID) > 120 {
		message(w, 400, "订单号不能为空且不超过 120 字节。")
		return
	}
	row, err := partnerOrderByIDQuery(s.db, q.OrderID)
	if errors.Is(err, sql.ErrNoRows) {
		message(w, 404, "没有这个订单号的发码记录。")
		return
	}
	if err != nil {
		message(w, 503, "无法读取发码记录。")
		return
	}
	res, err := s.db.Exec("UPDATE codes SET status='revoked',message='商城订单已作废',updated=? WHERE id=? AND status='active'", time.Now().Unix(), row.CodeID)
	if err != nil {
		message(w, 503, "作废失败，请稍后重试。")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		message(w, 503, "作废失败，请稍后重试。")
		return
	}
	if n != 1 {
		// 码已被使用或已作废。两种都不该静默成功：前者需人工核对，
		// 后者重复作废应当幂等返回。
		var status string
		if e := s.db.QueryRow("SELECT status FROM codes WHERE id=?", row.CodeID).Scan(&status); e != nil {
			message(w, 503, "无法读取兑换码状态。")
			return
		}
		if status == "revoked" {
			reply(w, 200, map[string]any{"order_id": q.OrderID, "revoked": true, "already_revoked": true})
			return
		}
		reply(w, 409, map[string]any{"order_id": q.OrderID, "revoked": false, "status": status, "message": "兑换码已被使用或正在处理，无法作废，请人工核实。"})
		return
	}
	reply(w, 200, map[string]any{"order_id": q.OrderID, "revoked": true})
}

// partnerOrderByIDQuery 是 partnerOrderByID 在无事务场景下的等价读取。
func partnerOrderByIDQuery(db *sql.DB, orderID string) (partnerOrderRow, error) {
	var row partnerOrderRow
	err := db.QueryRow(`SELECT p.order_id,p.code_id,p.created,c.hint,c.months FROM partner_orders p JOIN codes c ON c.id=p.code_id WHERE p.order_id=?`, orderID).
		Scan(&row.OrderID, &row.CodeID, &row.Created, &row.Hint, &row.Months)
	return row, err
}

// setPartnerKey 由管理员设置/轮换 API Key。写入的是哈希，明文只在响应里
// 回显一次，之后无法再取回。
func (s *server) setPartnerKey(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Key string `json:"key"`
	}
	if !decode(w, r, &f) {
		return
	}
	key := strings.TrimSpace(f.Key)
	if !partnerKeyValid(key) {
		message(w, 400, "API 密钥须为 "+partnerKeyPrefix+" 开头、后接 48 位十六进制。")
		return
	}
	if err := s.vault.Put("partner-key", []byte(partnerHash(key))); err != nil {
		message(w, 500, "保存 API 密钥失败。")
		return
	}
	reply(w, 200, map[string]any{"ok": true, "tail": tail(key)})
}

// rotatePartnerKey 生成一枚新密钥并保存其哈希，一次性回显明文。
func (s *server) rotatePartnerKey(w http.ResponseWriter, r *http.Request) {
	key := partnerKeyPrefix + token(24)
	if err := s.vault.Put("partner-key", []byte(partnerHash(key))); err != nil {
		message(w, 500, "生成 API 密钥失败。")
		return
	}
	reply(w, 200, map[string]any{"ok": true, "key": key, "tail": tail(key)})
}

// partnerKeyStatus 只回显密钥是否已配置与尾号，不返回哈希。
func (s *server) partnerKeyStatus(w http.ResponseWriter, r *http.Request) {
	stored, err := s.vault.Get("partner-key")
	if err != nil {
		reply(w, 200, map[string]any{"configured": false})
		return
	}
	defer clear(stored)
	configured := strings.TrimSpace(string(stored)) != ""
	reply(w, 200, map[string]any{"configured": configured})
}

var _ = errNoPartnerKey
