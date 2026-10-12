package checkout

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"xgift/internal/vault"
)

type Record struct {
	ReplacementCount  int          `json:"replacement_count,omitempty"`
	PreviousSession   string       `json:"previous_session,omitempty"`
	ManualRecovery    bool         `json:"manual_recovery,omitempty"`
	CreationRetryable bool         `json:"creation_retryable,omitempty"`
	CreationAttempts  int          `json:"creation_attempts,omitempty"`
	PreflightSaved    bool         `json:"preflight_saved,omitempty"`
	PaymentMethod     string       `json:"payment_method,omitempty"`
	CardFingerprint   string       `json:"card_fingerprint,omitempty"`
	ConfirmParameters string       `json:"confirm_parameters,omitempty"`
	ConfirmKey        string       `json:"confirm_key,omitempty"`
	SubmittedAt       int64        `json:"submitted_at,omitempty"`
	RecoveryAttempts  int          `json:"recovery_attempts,omitempty"`
	LastError         *stripeError `json:"last_error,omitempty"`

	Username    string `json:"username"`
	RecipientID string `json:"recipient_id"`
	Months      int    `json:"months"`
	Amount      int    `json:"amount_minor"`
	Currency    string `json:"currency"`
	ProductID   string `json:"product_id"`
	SessionID   string `json:"session_id"`
	URL         string `json:"url"`
	Status      string `json:"status"`
	Created     int64  `json:"created"`
}

var sessionPattern = regexp.MustCompile(`^cs_live_[A-Za-z0-9]+$`)
var sessionPathPattern = regexp.MustCompile(`^/[A-Za-z]/pay/`)

func sessionURL(link, id string) bool {
	u, e := url.Parse(link)
	return e == nil && sessionPattern.MatchString(id) && u.Scheme == "https" && u.Host == "checkout.stripe.com" && u.User == nil && u.RawQuery == "" && sessionPathPattern.MatchString(u.Path) && u.Path[2:] == "/pay/"+id
}
func save(v *vault.Vault, r *Record) error {
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return v.Put("checkout:"+r.RecipientID, b)
}
func Run(ctx context.Context, v *vault.Vault, user string, pay bool, port, months int) (*Record, error) {
	return run(ctx, v, user, "", pay, port, months)
}

// RunForRecipient pins a redemption to the identity checked before reserving its code.
func RunForRecipient(ctx context.Context, v *vault.Vault, user, expectedRecipient string, pay bool, port, months int) (*Record, error) {
	if expectedRecipient == "" {
		return nil, errors.New("expected recipient is required")
	}
	return run(ctx, v, user, expectedRecipient, pay, port, months)
}

func run(ctx context.Context, v *vault.Vault, user, expectedRecipient string, pay bool, port, months int) (*Record, error) {
	if pay {
		paused, err := PaymentPaused(v)
		if err != nil {
			return nil, err
		}
		if paused {
			return nil, ErrPaymentPaused
		}
	}
	user = strings.ToLower(strings.TrimPrefix(user, "@"))
	if !regexp.MustCompile(`^[a-z0-9_]{1,15}$`).MatchString(user) {
		return nil, errors.New("invalid username")
	}
	catalog, e := ReadCatalog(v)
	if e != nil {
		return nil, e
	}
	plan, e := catalog.PlanFor(months)
	if e != nil {
		return nil, e
	}
	progress(ctx, 25, "正在核对接收账号与赠送资格…")
	var (
		x         *xClient
		recipient string
		r         Record
	)
	// 账号池故障转移：X 按「发送账号」判定赠送资格（code 37），会话失效
	// （401）同样是账号级问题。身份核对、订单记录校验与询价都发生在
	// session 创建之前，此刻换号没有任何已创建状态，可安全重试；session
	// 一旦创建，订单与发送账号绑定，之后不再换号。
	for attempt := 0; ; attempt++ {
		x, e = newXClient(v, port)
		if e != nil {
			return nil, e
		}
		r = Record{}
		recipient, e = x.recipient(ctx, user)
		if e != nil {
			if fault := x.account; fault != nil && IsAccountFault(e) && attempt < maxXAccounts {
				x.close()
				if cerr := CoolXAccount(v, fault.ID, accountFaultReason(e)); cerr != nil {
					return nil, cerr
				}
				continue
			}
			x.close()
			return nil, e
		}
		stop, gerr := guardOrder(v, user, recipient, expectedRecipient, plan, pay, &r)
		if gerr != nil || stop != nil {
			x.close()
			return stop, gerr
		}
		progress(ctx, 40, "正在核对套餐时长和价格…")
		// 询价与创建订单必须走付款出口：X 按请求出口所在国定价，若沿用账号
		// 查询出口（通常直连）就会按服务器所在国报价，随后在 quote() 里因
		// 金额不符被拒，而错误信息只说"price is not exactly the allowed amount"。
		// 此时 recipient 已确定，可以按它选到与结算相同的节点。
		if e = x.withRegionalExit(ctx, recipient); e != nil {
			x.close()
			return nil, e
		}
		if e = x.quote(ctx, user, plan); e != nil {
			if fault := x.account; fault != nil && IsAccountFault(e) && attempt < maxXAccounts {
				x.close()
				if cerr := CoolXAccount(v, fault.ID, accountFaultReason(e)); cerr != nil {
					return nil, cerr
				}
				continue
			}
			x.close()
			return nil, e
		}
		break
	}
	defer x.close()
	if r.SessionID == "" {
		if r.Status == "" {
			r = Record{Username: user, RecipientID: recipient, Months: plan.Months, Amount: plan.Minor, Currency: strings.ToUpper(plan.Currency), ProductID: plan.ProductID, Status: "creating", Created: time.Now().Unix()}
		}
		for r.CreationAttempts < maxAttempts {
			if e = ctx.Err(); e != nil {
				return &r, e
			}
			// Reserve each creation attempt durably. A lost response can leave an
			// unused external session, but only the selected saved session is paid.
			r.CreationAttempts++
			r.CreationRetryable = false
			if e = save(v, &r); e != nil {
				return &r, e
			}
			progress(ctx, 50, "正在创建专属赠送订单…")
			r.SessionID, r.URL, e = x.create(ctx, user, recipient, plan)
			if e == nil {
				break
			}
			var retry *temporaryError
			if errors.As(e, &retry) && r.CreationAttempts < maxAttempts {
				r.CreationRetryable = true
				if saveErr := save(v, &r); saveErr != nil {
					return &r, saveErr
				}
			}
			if IsAccountFault(e) && x.account != nil {
				// 账号在创建阶段被 X 拒绝（资格查询没拦住、mutations 才报的
				// code 37）。冷却该账号让后续订单换号；本订单没有 session，
				// 标记可重试后交给下一次运行或人工重跑，用池里其他账号继续。
				if cerr := CoolXAccount(v, x.account.ID, accountFaultReason(e)); cerr != nil {
					return &r, cerr
				}
				r.CreationRetryable = true
				if saveErr := save(v, &r); saveErr != nil {
					return &r, saveErr
				}
				return &r, e
			}
			if stop := waitRetry(ctx, e, r.CreationAttempts); stop != nil {
				return &r, stop
			}
		}
		r.Status = "created"
		if e = save(v, &r); e != nil {
			return &r, e
		}
	}
	paymentFlow := paymentRead
	if pay {
		paymentFlow = paymentPay
	}
	s, e := newStripe(ctx, v, r.RecipientID, paymentFlow)
	if e != nil {
		return &r, e
	}
	defer s.close()
	progress(ctx, 60, "正在核验订单金额和收款方…")
	page, e := s.page(ctx, &r, true)
	if e != nil {
		return &r, e
	}
	if e = page.guard(&r, plan, false); e != nil {
		return &r, e
	}
	if page.PaymentStatus == "paid" && page.Status == "complete" {
		r.Status = "succeeded"
		return &r, save(v, &r)
	}
	if e = page.guard(&r, plan, true); e != nil {
		return &r, e
	}
	if !pay {
		return &r, nil
	}
	c, e := s.paymentCard()
	if e != nil {
		return &r, e
	}
	r.CardFingerprint = cardFingerprint(c)
	progress(ctx, 70, "正在准备付款，请勿重复提交…")
	method, e := s.tokenize(ctx, &r, c)
	if e != nil {
		if cardTokenizationRejected(e) {
			// The card itself was rejected before any submit: end the batch so the
			// next attempt rotates to another card instead of retrying this one.
			_ = markPaymentDecline(v, r.RecipientID)
		}
		return &r, e
	}
	// Refresh after card creation; confirmation also pins the expected amount server-side.
	page, e = s.page(ctx, &r, true)
	if e != nil {
		return &r, e
	}
	if e = page.guard(&r, plan, true); e != nil {
		return &r, e
	}
	return submitAndObserve(ctx, v, &r, s, page, method, plan, "submitting")
}

// guardOrder replays every pre-session record check for one recipient and
// plan. It is a pure function of vault state, so the account failover loop
// can re-run it after switching accounts without side effects. A non-nil
// stop means the caller must return it verbatim: either an order that is
// already finished, or a state that forbids another submission.
func guardOrder(v *vault.Vault, user, recipient, expectedRecipient string, plan Plan, pay bool, r *Record) (stop *Record, err error) {
	if expectedRecipient != "" && recipient != expectedRecipient {
		return nil, errors.New("recipient identity changed; refusing to create or pay an order")
	}
	// Public manual checkouts must never be taken over by saved-card payment.
	if raw, err := v.Get("public-checkout:" + recipient); err == nil {
		clear(raw)
		return nil, ErrPublicLinkConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Fail closed on legacy records rather than silently bypassing an earlier attempt.
	if _, err := v.Get("checkout:" + user); err == nil {
		return nil, errors.New("legacy checkout requires migration before creating another order")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	raw, err := v.Get("checkout:" + recipient)
	if err == nil {
		if json.Unmarshal(raw, r) != nil || r.RecipientID != recipient || r.Months != plan.Months || r.Amount != plan.Minor || r.Currency != strings.ToUpper(plan.Currency) || r.ProductID != plan.ProductID {
			return nil, errors.New("existing checkout differs from this recipient or plan; refusing another order")
		}
		if r.Status == "creating" {
			if r.SessionID != "" || r.URL != "" || r.PaymentMethod != "" || r.ConfirmParameters != "" || r.ConfirmKey != "" || r.SubmittedAt != 0 || r.PreflightSaved {
				return nil, errors.New("creation record contains inconsistent payment evidence")
			}
			if !r.CreationRetryable || r.CreationAttempts < 1 || r.CreationAttempts >= maxAttempts {
				return r, errors.New("creation retry budget is unavailable or exhausted; automatic recovery blocked")
			}
		}
		if r.Status != "creating" && !sessionURL(r.URL, r.SessionID) {
			return nil, errors.New("existing checkout URL or session is invalid")
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if r.Status == "succeeded" {
		return r, nil
	}
	if r.Status != "" && r.Status != "created" && r.Status != "creating" {
		return r, fmt.Errorf("existing checkout status is %s; no payment will be resubmitted", r.Status)
	}
	if !unsubmitted(r) {
		return r, errors.New("order contains payment evidence; refusing another submission")
	}
	if pay {
		if err = CheckPaymentConfiguration(v); err != nil {
			return r, err
		}
	}
	return nil, nil
}

func submitAndObserve(ctx context.Context, v *vault.Vault, r *Record, s *stripeClient, page *paymentPage, method string, plan Plan, status string) (*Record, error) {
	if len(page.raw) == 0 {
		return r, errors.New("missing original preflight response")
	}
	if err := reservePaymentSlot(ctx, v, r); err != nil {
		return r, err
	}
	if err := v.Put("stripe-preflight:"+r.SessionID, page.raw); err != nil {
		return r, err
	}
	r.PreflightSaved = true
	r.PaymentMethod = method
	r.ConfirmParameters = confirmationForm(r, page, method, plan).Encode()
	r.ConfirmKey = idempotency(r, "confirm")
	r.SubmittedAt = time.Now().Unix()
	r.Status = status
	return confirmAndObserve(ctx, v, r, s, plan)
}
func confirmAndObserve(ctx context.Context, v *vault.Vault, r *Record, s *stripeClient, plan Plan) (*Record, error) {
	r.LastError = nil
	var e error
	if e = save(v, r); e != nil {
		return r, e
	}
	progress(ctx, 80, "正在提交付款，请勿重复提交…")
	_, confirmErr := s.confirm(ctx, r)
	progress(ctx, 90, "已尝试提交付款，正在核实最终结果…")
	if confirmErr != nil {
		var se *stripeError
		if errors.As(confirmErr, &se) {
			r.LastError = se
		}
		if IsPaymentDeclined(r) {
			r.Status = "declined"
		}
		// Save the response before any polling so interruptions cannot erase a
		// definite decline or leave it falsely labelled as still submitting.
		if e = save(v, r); e != nil {
			return r, e
		}
	}
	// Poll the result endpoint: init is no longer available once a session completes.
	// This loop only reads status; confirmation is never resubmitted.
observe:
	for i := 0; i < 20; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				break observe
			case <-time.After(2 * time.Second):
			}
		}
		state, readErr := s.poll(ctx, r, plan)
		if readErr == nil {
			if state == "succeeded" {
				r.Status = "succeeded"
				r.LastError = nil
				if e = save(v, r); e != nil {
					return r, e
				}
				return r, paymentOutcome(v, r)
			}
			if state == "requires_action" {
				r.Status = "requires_action"
				if e = save(v, r); e != nil {
					return r, e
				}
				if e = markAuthenticationRequired(v, r); e != nil {
					return r, e
				}
				return r, errors.New("bank authentication is required; use this existing checkout")
			}
		}
		if IsPaymentDeclined(r) {
			r.Status = "declined"
			if e = save(v, r); e != nil {
				return r, e
			}
			if e = paymentOutcome(v, r); e != nil {
				return r, e
			}
			return r, confirmErr
		}
		if ctx.Err() != nil {
			break
		}
	}
	r.Status = "unknown"
	if e = save(v, r); e != nil {
		return r, e
	}
	if confirmErr != nil {
		return r, confirmErr
	}
	return r, errors.New("Stripe has not confirmed payment; automatic retry is blocked")
}
