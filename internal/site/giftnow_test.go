package site

import (
	"errors"
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
