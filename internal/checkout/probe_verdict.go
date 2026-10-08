package checkout

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"xgift/internal/vault"
)

// 真实下单前的归因探测。
//
// 为什么需要它：premium_gifting_eligible 是一个布尔值，为 false 时
// 有多种完全不同的原因，而生产链路在 identity(requireEligible=true)
// 处就返回 ErrNotEligible —— 后台只能显示"该账号无法接收"，
// 永远看不到 X 的真实错误码。
//
// 2026-10-08 实测推翻了"false 就是接收方有问题"这个假设：teyircom、
// teyir、ABG19940928、yan_ye95623 四个不同接收方，全部返回同一个
//
//	code 37 / AuthorizationError / "Current user is not eligible to gift"
//
// 也就是说当时的真实原因是发送账号被限，而后台却把锅甩给了接收方。
// 运营看到那句话会去换接收账号，而那完全没用。
//
// 所以这里提供一次真实询问 X 的探测：不查布尔字段，直接打
// useOneTimePurchaseGiftMutation，把 X 的原始 code / message 带回��
//
// 安全性：探测只创建 unpaid 会话，绝不提交付款，并且不写 vault。
// 因此它不消耗创建重试预算（x-create-attempt 不落库），也不产生
// 任何可被误认为已付款的记录。

// ProbeVerdict 是探测结论。
type ProbeVerdict struct {
	// RecipientEligible 为 true 表示 X 接受了为该接收方创建订单的请求。
	RecipientEligible bool `json:"recipient_eligible"`
	// SenderAuthorised 为 false 时说明 X 拒绝的是发送账号而非接收方，
	// 此时换接收账号没有用。
	SenderAuthorised bool `json:"sender_authorised"`
	// XCode 是 X 的 GraphQL 错误码，0 表示没有错误信封。
	XCode int `json:"x_code,omitempty"`
	// XMessage 是 X 的原始消息，不做翻译。
	XMessage string `json:"x_message,omitempty"`
	// Reason 是分类后的中文结论。
	Reason string `json:"reason"`
	// Hint 是给运营的下一步建议。
	Hint string `json:"hint"`
	// SessionID 仅在X 接受下单时存在，且状态一定是 Unpaid（未付款）。
	SessionID string `json:"session_id,omitempty"`
	// Recipient 是探测时读到的接收方 rest_id。
	Recipient string `json:"recipient,omitempty"`
}

// ProbeGiftEligibility 向 X 询问"能否为该接收方创建订单"。
//
// port 为 0 表示由调用方决定是否启动区域出口；探测必须走区域出口，
// 否则 X 会按服务器所在地定价与判权限，与真实下单不一致。
func ProbeGiftEligibility(ctx context.Context, v *vault.Vault, user string, port, months int) (ProbeVerdict, error) {
	user = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(user), "@"))
	if !regexp.MustCompile(`^[a-z0-9_]{1,15}$`).MatchString(user) {
		return ProbeVerdict{}, errors.New("invalid username")
	}
	cat, e := ReadCatalog(v)
	if e != nil {
		return ProbeVerdict{}, e
	}
	plan, e := cat.PlanFor(months)
	if e != nil {
		return ProbeVerdict{}, e
	}
	c, e := newXClient(v, port)
	if e != nil {
		return ProbeVerdict{}, e
	}
	defer c.close()

	// 探测要问的是"X 真实的下单判据"，所以必须走与真实下单相同的区域出口。
	if e = c.withRegionalExit(ctx, user); e != nil {
		return ProbeVerdict{}, e
	}

	// 这里不能用 c.recipient()：它带 requireEligible，会把 false 的账号
	// 在本地挡掉，那样就回到"看不到 X 原始错误码"的老问题。
	id, e := c.identity(ctx, user, false)
	if e != nil {
		if errors.Is(e, ErrUserNotFound) {
			return ProbeVerdict{
				RecipientEligible: false,
				SenderAuthorised:  true,
				Reason:            "接收账号不存在或 X 未返回 rest_id",
				Hint:              "检查用户名拼写：X 用户名最多 15 个字母数字下划线，不含 @。",
			}, nil
		}
		return ProbeVerdict{}, e
	}

	verdict := ProbeVerdict{Recipient: id, SenderAuthorised: true}
	// 直接调用底层 create：它对 X 的错误信封做完整分类，
	// 包括 code 37 → ErrGiftNotAuthorised。
	sessionID, _, e := c.createProbe(ctx, user, id, plan)
	switch {
	case e == nil:
		verdict.RecipientEligible = true
		verdict.SessionID = sessionID
		verdict.Reason = "X 接受为该接收方创建订单（会话未付款）"
		verdict.Hint = "该账号可以接收赠送，可以继续下单付款。"
	case errors.Is(e, ErrGiftNotAuthorised):
		// 🔴 code 37 有两种含义完全相反的 message，必须看原文区分：
		//
		//   "Current user is not eligible to gift"    → 发送账号（current user = 我们）
		//   "Recipient user is not eligible ..."      → 接收账号
		//
		// 实测两者都是 code 37 / AuthorizationError / Permissions。
		// xapi.go 只按 code 分类，把接收方问题也归成了 ErrGiftNotAuthorised，
		// 所以这里不能只看哨兵错误类型，必须读 message。
		if code, msg := xErrorDetail(e); code != 0 {
			verdict.XCode, verdict.XMessage = code, msg
		}
		if isSenderRefusal(verdict.XMessage) {
			verdict.SenderAuthorised = false
			verdict.Reason = "发送账号被 X 拒绝，无权赠送（不是接收方的问题）"
			verdict.Hint = "X 的原文说的是「当前用户没有赠送资格」，current user 指发送账号。" +
				"换接收账号没有用。常见原因：赠送额度或频率超限、账号过新或未完成手机验证、账号本身受限。"
		} else {
			verdict.Reason = "接收账号被 X 拒绝，无法接收赠送（已向 X 核实）"
			verdict.Hint = "X 的原文明确指向接收账号：" + verdict.XMessage +
				"。换接收账号有用。若确认该账号确实可接收，可先用 giftprobe 复核，再检查 X 侧是否有其它限制。"
		}
	case errors.Is(e, ErrOperationRejected):
		verdict.Reason = "X 拒绝了请求本身（不是账号资格问题）"
		verdict.Hint = "X 返回 GraphQL 错误信封但内容不是权限拒绝，通常是 operation ID 已变更。" +
			"需要更新 internal/checkout/xapi.go 里的 DefaultOp* 常量并重新部署。"
		if code, msg := xErrorDetail(e); code != 0 {
			verdict.XCode, verdict.XMessage = code, msg
		}
	default:
		return ProbeVerdict{}, e
	}
	return verdict, nil
}

// isSenderRefusal 判断 X 的拒绝指向发送账号还是接收账号。
//
// X 用同一个 code 37 / AuthorizationError 表达两种相反的结论，区别只在
// 消息主语：current user 是我们（发送方），recipient user 是对方。
// 2026-10-08 实测两种消息都真实出现过：
//
//	Current user is not eligible to gift         → 四个不同接收方都返回这条
//	Recipient user is not eligible to receive gift → 换到另一个接收方时返回这条
func isSenderRefusal(message string) bool {
	m := strings.ToLower(message)
	// 先判接收方，因为它更具体；命中就一定不是发送方。
	if strings.Contains(m, "recipient") {
		return false
	}
	// 必须出现 current user 才算发送方。只有 "not eligible to gift" 而没有
	// 主语时无法判定归属 —— 那就不能给出"换接收账号没有用"这种引导，
	// 因为它可能完全指错方向。
	return strings.Contains(m, "current user")
}

// xErrorDetail 从包装后的错误文本里取回 X 的 code 与 message。
// xapi.go 用 fmt.Errorf("...[code %d]") 包装，这里解析那个稳定格式。
func xErrorDetail(err error) (int, string) {
	var s string
	if err != nil {
		s = err.Error()
	}
	var code int
	if m := xCodeRe.FindStringSubmatch(s); m != nil {
		fmt.Sscanf(m[1], "%d", &code)
	}
	msg := s
	if i := strings.LastIndex(s, ": "); i > 0 {
		// 去掉前置的哨兵错误名，保留 X 的原始描述。
		tail := s[i+2:]
		if j := strings.Index(tail, ": "); j >= 0 && j+2 < len(tail) {
			msg = tail[j+2:]
		} else {
			msg = tail
		}
	}
	if k := strings.Index(msg, " [code "); k > 0 {
		msg = msg[:k]
	}
	return code, msg
}

var xCodeRe = regexp.MustCompile(`\[code (\d+)\]`)
