package checkout

import "testing"

// code 37 有两种含义完全相反的 message，2026-10-08 实测两种都出现过。
// 只看 code 会把接收方问题误报成"发送账号被限"，那会让运营去换接收账号，
// 而真正该改的是发送账号（反之亦然）。
func TestIsSenderRefusalDistinguishesMessageSubject(t *testing.T) {
	sender := []string{
		"Current user is not eligible to gift",
		"current user is not eligible to gift",
		"CURRENT USER IS NOT ELIGIBLE TO GIFT",
	}
	recipient := []string{
		"Recipient user is not eligible to receive gift",
		"recipient user is not eligible to receive gift",
		// 带 recipient 时即使同时出现 current user 也必须判为接收方，
		// 因为 recipient 更具体。
		"Current user is not eligible to gift, recipient user is not eligible to receive gift",
	}
	for _, m := range sender {
		if !isSenderRefusal(m) {
			t.Errorf("应判为发送方问题：%q", m)
		}
	}
	for _, m := range recipient {
		if isSenderRefusal(m) {
			t.Errorf("应判为接收方问题：%q", m)
		}
	}
}

// 无法判定的消息不能默认归给发送方：那种情况下宁可提示不确定，
// 也不能给出"换接收账号没有用"这种可能完全错误的引导。
func TestUnknownMessageIsNotSilentlyBlamedOnSender(t *testing.T) {
	for _, m := range []string{"", "some other refusal", "not eligible to gift"} {
		if isSenderRefusal(m) {
			t.Errorf("无法判定主语时不应判为发送方：%q", m)
		}
	}
}

// xErrorDetail 必须从包装后的错误里取回 X 的原始 code 与 message，
// 页面上的「X 原文」就是靠它。
func TestXErrorDetailExtractsCodeAndMessage(t *testing.T) {
	err := &wrapped{
		msg: "X says this account is not eligible to gift: X refused useOneTimePurchaseGiftMutation for this account: Recipient user is not eligible to receive gift [code 37]",
	}
	code, msg := xErrorDetail(err)
	if code != 37 {
		t.Fatalf("应取到 code 37，得到 %d", code)
	}
	if msg != "Recipient user is not eligible to receive gift" {
		t.Fatalf("应保留 X 原始 message，得到 %q", msg)
	}
}

type wrapped struct{ msg string }

func (w *wrapped) Error() string { return w.msg }
