package checkout

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"xgift/internal/proxy"
	"xgift/internal/vault"
)

// 与 ops_test.go 的 newOpsVault 同构，不重复造一个。
func pinTestVault(t *testing.T) *vault.Vault { return newOpsVault(t) }

// coolNode 让一个节点进入冷却，走项目自己的写入路径。
func coolNode(t *testing.T, v *vault.Vault, node json.RawMessage) {
	t.Helper()
	paymentRouteMu.Lock()
	defer paymentRouteMu.Unlock()
	if err := coolPaymentNodeLocked(v, node, "test", 30*time.Minute); err != nil {
		t.Fatalf("设置冷却失败：%v", err)
	}
}

func twoNodes() []json.RawMessage {
	a := json.RawMessage(`{"type":"socks","tag":"a","server":"a.example.com","server_port":1080}`)
	b := json.RawMessage(`{"type":"socks","tag":"b","server":"b.example.com","server_port":1080}`)
	return []json.RawMessage{a, b}
}

// 指定节点后，新路由必须选中它，而不是轮转到别的。
func TestPinnedNodeIsChosenForNewRoutes(t *testing.T) {
	v := pinTestVault(t)
	nodes := twoNodes()
	if err := SetPinnedPaymentNode(v, outboundID(nodes[1])); err != nil {
		t.Fatalf("指定失败：%v", err)
	}
	got, err := choosePinnedOrRotating(v, nodes)
	if err != nil {
		t.Fatalf("选路失败：%v", err)
	}
	if outboundID(got) != outboundID(nodes[1]) {
		t.Fatalf("指定了节点 b，却选到了 a")
	}
}

// 清掉指定后要回到轮转。
func TestClearedPinRestoresRotation(t *testing.T) {
	v := pinTestVault(t)
	nodes := twoNodes()
	if err := SetPinnedPaymentNode(v, outboundID(nodes[1])); err != nil {
		t.Fatal(err)
	}
	if err := SetPinnedPaymentNode(v, ""); err != nil {
		t.Fatal(err)
	}
	pinned, err := PinnedPaymentNode(v)
	if err != nil || pinned != "" {
		t.Fatalf("清空后仍读到指定 %q（err=%v）", pinned, err)
	}
	if _, err := choosePinnedOrRotating(v, nodes); err != nil {
		t.Fatalf("轮转失败：%v", err)
	}
}

// 🔴 指定一个已被冷却的节点时，不能拿它去下单 —— 那明知会失败还花钱。
// 正确行为：退回轮转（在可用节点里选），而不是硬用指定的。
func TestPinnedNodeIsIgnoredWhileCooling(t *testing.T) {
	v := pinTestVault(t)
	nodes := twoNodes()
	if err := SetPinnedPaymentNode(v, outboundID(nodes[0])); err != nil {
		t.Fatal(err)
	}
	// 让节点 a 进入冷却。用项目自己的冷却函数，不要手写记录 ——
	// 记录是 nodeCooldown 的 JSON，第一版直接Put 一个裸字符串，
	// 读取时报 "invalid node cooldown record"，测试失败却与被测行为无关。
	coolNode(t, v, nodes[0])
	got, err := choosePinnedOrRotating(v, nodes)
	if err != nil {
		t.Fatalf("应退回轮转而不是报错：%v", err)
	}
	if outboundID(got) == outboundID(nodes[0]) {
		t.Fatal("选中正在冷却的节点 —— 会明知失败还提交付款")
	}
}

// 指定的节点被从池子里删掉后，必须退回轮转，而不是卡死。
// 否则"删除节点"这个操作会让后续所有付款都失败。
func TestPinnedNodeMissingFromPoolFallsBack(t *testing.T) {
	v := pinTestVault(t)
	nodes := twoNodes()
	if err := SetPinnedPaymentNode(v, outboundID(nodes[1])); err != nil {
		t.Fatal(err)
	}
	// 池子里只剩 a，但指定的是 b —— 模拟"删掉了被指定的节点"。
	only := []json.RawMessage{nodes[0]}
	got, err := choosePinnedOrRotating(v, only)
	if err != nil {
		t.Fatalf("指定的节点不在池中时应退回轮转，却报错：%v", err)
	}
	if outboundID(got) != outboundID(nodes[0]) {
		t.Fatal("应退回选择剩下的节点 a")
	}
}

// 池里没有任何可用节点时必须报错，不能返回一个非法节点。
func TestPinnedWithNoAvailableNodesFails(t *testing.T) {
	v := pinTestVault(t)
	nodes := twoNodes()
	if err := SetPinnedPaymentNode(v, outboundID(nodes[0])); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		coolNode(t, v, n)
	}
	if _, err := choosePinnedOrRotating(v, nodes); !errors.Is(err, ErrPaymentNodesCooling) {
		t.Fatalf("应返回 ErrPaymentNodesCooling，得到 %v", err)
	}
}

// 指纹格式不对时按"未指定"处理，而不是静默不生效。
func TestMalformedPinTreatedAsUnset(t *testing.T) {
	v := pinTestVault(t)
	if err := v.Put(pinnedPaymentNodeRecord, []byte("not-a-fingerprint")); err != nil {
		t.Fatal(err)
	}
	pinned, err := PinnedPaymentNode(v)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if pinned != "" {
		t.Fatalf("非法指纹应按未指定处理，得到 %q", pinned)
	}
}

// 导出的 OutboundID 必须与内部用的是同一个值 —— 界面指定节点靠它匹配，
// 不一致会导致"指定了却不生效"这种静默失败。
func TestExportedOutboundIDMatchesInternal(t *testing.T) {
	nodes := twoNodes()
	if OutboundID(nodes[0]) != outboundID(nodes[0]) {
		t.Fatal("导出的 OutboundID 与内部不一致")
	}
	// 且必须通过生产解析，否则界面上列出来的节点是无效的。
	if _, err := proxy.ParseOutboundPool([]byte(`[{"type":"socks","tag":"a","server":"a.example.com","server_port":1080}]`)); err != nil {
		t.Fatalf("测试节点本身不合法：%v", err)
	}
}
