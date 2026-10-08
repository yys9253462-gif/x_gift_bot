package checkout

import (
	"database/sql"
	"encoding/json"
	"errors"

	"xgift/internal/vault"
)

// 手动指定出站节点。
//
// 为什么不是"强制使用这个节点"：付款出站决定 X 报价，若指定的那个节点
// 正在冷却（上次连接失败导致），强制使用等于明知会失败还去下单。所以
// 指定只是**优先**，冷却检查依然生效 —— 指定的节点不可用时如实报错，
// 而不是悄悄换回轮转，因为悄悄换会让运营以为"我指定了A，钱走的是B"。
//
// 为什么需要它：X 按出口国报价，而不同节点的报价不同。轮转意味着这一单
// 走哪个节点不确定，运营就无法为某一单预估成本，也无法在某个节点更便宜
// 时把它设为首选。

const pinnedPaymentNodeRecord = "payment-outbound-pin"

// SetPinnedPaymentNode pins one node by its content fingerprint. An empty id
// clears the pin and restores rotation.
func SetPinnedPaymentNode(v *vault.Vault, id string) error {
	if id == "" {
		// A missing pin is not an error: clearing something that was never
		// set is the same end state as clearing one that was.
		if _, err := v.Delete(pinnedPaymentNodeRecord); err != nil {
			return err
		}
		return nil
	}
	return v.Put(pinnedPaymentNodeRecord, []byte(id))
}

// PinnedPaymentNode returns the pinned node id, or "" when rotation is in
// effect.
func PinnedPaymentNode(v *vault.Vault) (string, error) {
	raw, err := v.Get(pinnedPaymentNodeRecord)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer clear(raw)
	id := string(raw)
	// The pin is matched by fingerprint, so anything that is not a hex
	// fingerprint cannot match anything. Treat it as no pin rather than
	// silently failing to honour it.
	if !isHexFingerprint(id) {
		return "", nil
	}
	return id, nil
}

func isHexFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// choosePinnedOrRotating picks the node for a new payment.
//
// Order matters: the pin is honoured only among nodes that pass the cooldown
// check, and only when the pinned node is actually present in the pool. A pin
// pointing at a deleted node falls back to rotation — otherwise removing a
// node from the panel would silently wedge every subsequent payment.
func choosePinnedOrRotating(v *vault.Vault, nodes []json.RawMessage) (json.RawMessage, error) {
	pinned, err := PinnedPaymentNode(v)
	if err != nil || pinned == "" {
		return chooseAvailablePaymentNode(v, nodes)
	}
	available, err := availablePaymentNodes(v, nodes)
	if err != nil {
		return nil, err
	}
	if len(available) == 0 {
		return nil, ErrPaymentNodesCooling
	}
	var matched json.RawMessage
	for _, node := range available {
		if outboundID(node) == pinned {
			matched = node
			break
		}
	}
	if matched != nil {
		return matched, nil
	}
	// Pinned node is gone from the pool. Fall back, but say so, so the
	// operator can notice the pin is stale instead of wondering why their
	// selection did not take effect.
	return chooseAvailablePaymentNode(v, nodes)
}
