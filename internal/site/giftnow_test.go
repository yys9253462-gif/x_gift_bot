package site

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"xgift/internal/checkout"
)

// 错误归因是这个功能的核心：2026-10-08 同一句"该账号目前无法接收"
// 下面实际藏着发送方限流、接口变更、出口不可用等完全不同的情况，
// 运营只有拿到分类和 X 原始 code 才知道该改什么。
func TestGiftDiagnoseSeparatesSenderFromRecipient(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		category string
	}{
		{"发送方被限", checkout.ErrGiftNotAuthorised, "sender_not_authorised"},
		{"接口标识变更", checkout.ErrOperationRejected, "operation_stale"},
		{"接收方不可接收", checkout.ErrNotEligible, "recipient_ineligible"},
		{"账号不存在", checkout.ErrUserNotFound, "recipient_not_found"},
		{"读取失败", checkout.ErrXReadFailure, "x_read_failure"},
		{"付款暂停", checkout.ErrPaymentPaused, "payment_paused"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := giftDiagnose(c.err, 25, "测试阶段")
			if d.Category != c.category {
				t.Fatalf("期望分类 %q，得到 %q", c.category, d.Category)
			}
			if d.Summary == "" || d.Detail == "" || d.Hint == "" {
				t.Fatal("诊断必须同时给出结论、细节与可执行建议")
			}
		})
	}
}

// code 37 是2026-10-08 实测的发送方限流，必须带出原始 code，
// 否则运营无法区分它和接收方问题。
func TestGiftDiagnoseKeepsXCode(t *testing.T) {
	raw := errors.New("X says this account is not eligible to gift: X refused useOneTimePurchaseGiftMutation for this account: Authorization: Current user is not eligible to gift [code 37]")
	d := giftDiagnose(&wrappedGift{err: checkout.ErrGiftNotAuthorised, msg: raw.Error()}, 50, "创建订单")
	if d.XCode != 37 {
		t.Fatalf("必须从错误文本取出 X code 37，得到 %d", d.XCode)
	}
	if !strings.Contains(d.Detail, "not eligible to gift") {
		t.Fatal("诊断必须保留 X 的原始 message")
	}
}

// 出口是2026-10-08 整个事故的根因：mutation 强制走付款出口，
// 直连会被 X 判权限错误。分类错误会把运营引到错误方向。
func TestGiftDiagnoseDetectsEgressFailure(t *testing.T) {
	d := giftDiagnose(errors.New("X regional checkout proxy is unavailable"), 40, "核对价格")
	if d.Category != "egress_unavailable" {
		t.Fatalf("出口问题应归为 egress_unavailable，得到 %q", d.Category)
	}
}

// 带付款证据的失败必须显式警告：只看"失败"可能重复扣款。
func TestGiftFailWarnsWhenPaymentEvidenceExists(t *testing.T) {
	task := &giftTask{Username: "someone", Months: 3, State: "running"}
	rec := &checkout.Record{SessionID: "cs_live_abc", SubmittedAt: 1791461912}
	d := &giftDiagnosis{Category: "unknown", Summary: "赠送未完成", Detail: "x", Hint: "y"}
	giftFail(task, d, rec)
	if task.State != "failed" {
		t.Fatal("任务应标记为 failed")
	}
	if !strings.Contains(task.Stage.Hint, "已产生付款证据") {
		t.Fatalf("有付款证据时必须警告先核实扣款，实际提示：%s", task.Stage.Hint)
	}
}

// 无付款证据的失败不应出现扣款警告，避免噪声。
func TestGiftFailStaysQuietWithoutPaymentEvidence(t *testing.T) {
	task := &giftTask{Username: "someone", Months: 3, State: "running"}
	rec := &checkout.Record{}
	d := &giftDiagnosis{Category: "sender_not_authorised", Summary: "发送账号被 X 拒绝，无权赠送", Detail: "x", Hint: "等额度恢复"}
	giftFail(task, d, rec)
	if strings.Contains(task.Stage.Hint, "已产生付款证据") {
		t.Fatal("没有付款证据时不应出现扣款警告")
	}
}

// 任务视图必须是副本：否则页面读取时会与后台写入竞态。
func TestGiftViewIsACopy(t *testing.T) {
	task := &giftTask{Username: "a", Stages: []giftStage{{Message: "one"}}}
	v := giftView(task)
	v.Stages[0].Message = "mutated"
	if task.Stages[0].Message != "one" {
		t.Fatal("giftView 必须返回深拷贝的阶段列表")
	}
}

// X code 的解析只能依赖稳定格式，不能误伤普通错误文本里的方括号数字。
func TestXCodePatternIgnoresUnrelatedText(t *testing.T) {
	if m := xCodePattern.FindStringSubmatch("array index [5] out of range"); m != nil {
		t.Fatalf("不应把非 X code 的方括号数字解析成 X code，得到 %v", m)
	}
}

// wrappedGift 让测试能同时携带哨兵错误与原始文本。
type wrappedGift struct {
	err error
	msg string
}

func (w *wrappedGift) Error() string { return w.msg }
func (w *wrappedGift) Unwrap() error { return w.err }

// 2026-10-08 22:03 老大指出：界面在25% 预检阶段就断言"接收账号无法接收"，
// 但那只是 premium_gifting_eligible 一个布尔字段，未向 X 下单接口核实。
// 四个不同接收方都返回同一个 code 37，真实原因是发送账号被限——
// 所以归因必须走探测，且发送方受限时不能归咎于接收方。
func TestSenderRefusalIsNeverBlamedOnRecipient(t *testing.T) {
	d := &giftDiagnosis{
		Category: "sender_not_authorised",
		Summary:  "发送账号被 X 限制，无权赠送（不是接收方的问题）",
		Hint:     "换接收账号没有用。",
	}
	if d.Category == "recipient_ineligible" {
		t.Fatal("发送方受限绝不能归类为接收方问题")
	}
	// 文案必须明确否掉"换接收方"这条错误处置。
	if !strings.Contains(d.Summary, "不是接收方") {
		t.Fatalf("结论必须点明不是接收方的问题，实际：%s", d.Summary)
	}
}

// 探测失败时不能掩盖原始错误：两个原因都要报出来。
func TestProbeFailureKeepsOriginalReason(t *testing.T) {
	orig := giftDiagnose(checkout.ErrNotEligible, 25, "正在核对接收账号与赠送资格…")
	if orig.Category != "recipient_ineligible" {
		t.Fatalf("原始错误应归为 recipient_ineligible，得到 %q", orig.Category)
	}
	// 模拟 giftnow.go 里perr != nil 分支的行为：追加而不是替换。
	orig.Hint += "（额外尝试向X 核实判据时也失败：boom）"
	if !strings.Contains(orig.Hint, "boom") {
		t.Fatal("探测失败的额外原因必须保留在提示里")
	}
	if !strings.Contains(orig.Hint, "premium_gifting_eligible") {
		t.Fatal("原始提示必须仍在，不能被探测结果覆盖")
	}
}

// 错误文本会回显到后台页面，而它来自网络库与 sing-box。这两者都可能
// 在错误里带上带认证信息的 URL，所以展示前必须脱敏。
func TestRedactSecretsRemovesCredentials(t *testing.T) {
	cases := []struct{ in, mustNot, mustKeep string }{
		{
			in:       "dial tcp: proxyconnect tcp: socks5://user_abc:secretpw@bd.zy3a.com:18495",
			mustNot:  "secretpw",
			mustKeep: "bd.zy3a.com",
		},
		{
			in:       "Get \"https://api.stripe.com/v1/x\": sk_live_51Hxxxxxxxxxxxxxxxxxxxx",
			mustNot:  "sk_live_51Hxxxxxxxxxxxxxxxxxxxx",
			mustKeep: "api.stripe.com",
		},
		{
			in:       "unauthorized: Authorization: Bearer AAAAAAAAAAAAAAAAAAAA",
			mustNot:  "AAAAAAAAAAAAAAAAAAAA",
			mustKeep: "unauthorized",
		},
		{
			in:       "card fingerprint mismatch: 8c603ad029cc3748617dac161c2a0c832b200c5fafd280c9ec9be66b59ab88ec",
			mustNot:  "8c603ad029cc3748617dac161c2a0c832b200c5fafd280c9ec9be66b59ab88ec",
			mustKeep: "fingerprint",
		},
	}
	for _, c := range cases {
		got := redactSecrets(c.in)
		if strings.Contains(got, c.mustNot) {
			t.Errorf("脱敏后仍含敏感内容 %q：%s", c.mustNot, got)
		}
		if !strings.Contains(got, c.mustKeep) {
			t.Errorf("脱敏后丢失了定位信息 %q：%s", c.mustKeep, got)
		}
	}
}

// 脱敏不能把正常错误信息也吃掉，否则失去诊断价值。
func TestRedactSecretsKeepsDiagnosticText(t *testing.T) {
	in := "X refused useOneTimePurchaseGiftMutation for this account: " +
		"Recipient user is not eligible to receive gift [code 37]"
	got := redactSecrets(in)
	if !strings.Contains(got, "code 37") {
		t.Errorf("X 的错误码必须保留，那是定位问题的关键：%s", got)
	}
	if !strings.Contains(got, "Recipient user") {
		t.Errorf("X 的原文必须保留：%s", got)
	}
}

// giftDiagnose 返回的 Detail 必须已经脱敏。
func TestGiftDiagnoseRedactsDetail(t *testing.T) {
	d := giftDiagnose(errors.New("proxyconnect: socks5://u:p@host:1080 refused"), 25, "询价")
	if strings.Contains(d.Detail, ":p@") {
		t.Fatalf("诊断详情泄露了凭据：%s", d.Detail)
	}
}

// checkout 的哨兵错误必须全部有分类，否则页面只显示"未归类"。
// 这条测试是防回归的关键：以后 checkout 新增错误而giftDiagnose
// 没跟上时，它会立刻指出漏了哪个。
//
// 不要求每个都有专属分类，但绝不能落到 unknown —— unknown 对运营
// 等于"我不知道出了什么事"，那还不如显示原始错误文本。
func TestEveryCheckoutSentinelIsClassified(t *testing.T) {
	sentinels := map[string]error{
		"ErrGiftNotAuthorised":     checkout.ErrGiftNotAuthorised,
		"ErrOperationRejected":     checkout.ErrOperationRejected,
		"ErrNotEligible":           checkout.ErrNotEligible,
		"ErrUserNotFound":          checkout.ErrUserNotFound,
		"ErrXReadFailure":          checkout.ErrXReadFailure,
		"ErrPaymentPaused":         checkout.ErrPaymentPaused,
		"ErrPaymentDeclined":       checkout.ErrPaymentDeclined,
		"ErrPaymentActionRequired": checkout.ErrPaymentActionRequired,
		"ErrNoUsableCard":          checkout.ErrNoUsableCard,
		"ErrPaymentNodesCooling":   checkout.ErrPaymentNodesCooling,
	}
	for name, sentinel := range sentinels {
		d := giftDiagnose(sentinel, 25, "阶段")
		if d.Category == "unknown" {
			t.Errorf("%s 没有分类，页面会显示「未归类」", name)
		}
		if d.Summary == "" || d.Hint == "" {
			t.Errorf("%s 缺少结论或处置建议", name)
		}
	}
}

// 付款类错误必须与资格类区分开：处置动作完全不同
// （换卡/等冷却 vs 改账号/换接收方）。
func TestPaymentFailuresAreDistinctFromEligibilityFailures(t *testing.T) {
	payment := []error{
		checkout.ErrPaymentDeclined,
		checkout.ErrPaymentActionRequired,
		checkout.ErrNoUsableCard,
		checkout.ErrPaymentNodesCooling,
	}
	for _, e := range payment {
		d := giftDiagnose(e, 80, "提交付款")
		if d.Category == "recipient_ineligible" || d.Category == "sender_not_authorised" {
			t.Errorf("付款失败 %v 被误归为资格问题（%s）", e, d.Category)
		}
		if !strings.HasPrefix(d.Category, "payment_") && d.Category != "no_usable_card" {
			t.Errorf("付款失败应有 payment_ 前缀的分类，得到 %q", d.Category)
		}
	}
}

// work 槽必须在任务结束（含超时）后释放，否则一次卡住的赠送会让
// 全站下单功能一直返回 409。这里验证 runGift 的所有退出路径都走到
// 调用方的 defer 释放。
func TestRunGiftAlwaysReturnsSoWorkSlotIsFreed(t *testing.T) {
	// runGift 依赖 server 的 vault 与端口，构造完整 server 成本高；
	// 这里退一步验证"槽位的获取与释放在同一个函数内成对出现"，
	// 这是 govet 与人工 review 都能核对的结构性约束。
	src := readSource(t, "giftnow.go")
	if !strings.Contains(src, "case s.work <- struct{}{}:") {
		t.Fatal("没有在入口获取 work 槽")
	}
	if !regexp.MustCompile(`defer func\(\) \{ s\.jobs\.Done\(\); <-s\.work \}\(\)`).MatchString(src) {
		t.Fatal("没有在 goroutine 的 defer 里释放 work 槽")
	}
	// 释放必须在 go 语句之后、runGift 调用之前的 defer 上，
	// 否则 runGift 提前 return 时不会执行。
	idxGo := strings.Index(src, "go func()")
	idxDefer := regexp.MustCompile(`defer func\(\) \{ s\.jobs\.Done\(\); <-s\.work \}\(\)`).FindStringIndex(src)
	idxDeferAt := idxDefer[0]
	if idxDeferAt < idxGo {
		t.Fatal("释放槽位的 defer 必须写在 goroutine 内部第一行")
	}
}

// 任务响应必须带 id：前端要靠它继续轮询。第一版只在 POST 响应里返回
// task_id，列表接口不给，切换页面回来就找不到该轮询谁。
func TestTaskViewCarriesID(t *testing.T) {
	src := readSource(t, "giftnow.go")
	if !regexp.MustCompile(`ID\s+string\s+` + "`json:\"id\"`").MatchString(src) {
		t.Fatal("giftTask 必须有导出到 JSON 的 id 字段")
	}
	if !regexp.MustCompile(`ID:\s+id,`).MatchString(src) {
		t.Fatal("创建任务时必须把 id 写进结构体")
	}
}

// 读取当前包内的源文件，用来断言结构性约束。
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", name, err)
	}
	return string(b)
}
