// Command unfreeze unblocks a single order frozen in the "creating" state.
//
// An order is frozen when CreationAttempts was persisted but the retry permit
// (CreationRetryable) was not: that happens when the process dies while
// x.create is in flight, because the permit is only written back when x.create
// returns a temporary error. checkout.go then refuses every future attempt.
//
// A second, different freeze appears once the budget is spent. code 37 is a
// permanent account-side refusal, not a temporary error, so it never writes the
// permit back yet still consumes one attempt per try. Three probes of the same
// answer leave the order unable to run again even after the operator fixes the
// account. --reset-budget clears the counter for that case.
//
// This tool flips those flags and nothing else. It refuses to touch an order
// that shows any payment evidence, so it can never authorise a second charge on
// a session that was already submitted.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"xgift/internal/checkout"
	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

func main() {
	resetBudget := false
	var recipient string
	for _, a := range os.Args[1:] {
		if a == "--reset-budget" {
			resetBudget = true
			continue
		}
		if recipient != "" {
			fmt.Fprintln(os.Stderr, "usage: unfreeze [--reset-budget] <recipient-id>")
			os.Exit(2)
		}
		recipient = a
	}
	if recipient == "" {
		fmt.Fprintln(os.Stderr, "usage: unfreeze [--reset-budget] <recipient-id>")
		os.Exit(2)
	}
	key := "checkout:" + recipient
	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	raw, err := v.Get(key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取订单失败:", err)
		os.Exit(1)
	}
	var r checkout.Record
	if json.Unmarshal(raw, &r) != nil {
		clear(raw)
		fmt.Fprintln(os.Stderr, "订单记录无法解析")
		os.Exit(1)
	}
	clear(raw)

	fmt.Printf("改前: status=%s attempts=%d retryable=%v session=%q url=%q\n",
		r.Status, r.CreationAttempts, r.CreationRetryable, r.SessionID, r.URL)

	if r.Status != "creating" {
		fmt.Fprintln(os.Stderr, "拒绝: 该订单不在 creating 状态")
		os.Exit(1)
	}
	// Never unfreeze an order that carries payment evidence: unfreezing such an
	// order could let a session that was already submitted be charged again.
	if r.SessionID != "" || r.URL != "" || r.PaymentMethod != "" ||
		r.ConfirmParameters != "" || r.ConfirmKey != "" || r.SubmittedAt != 0 || r.PreflightSaved {
		fmt.Fprintln(os.Stderr, "拒绝: 订单含付款证据，解冻可能导致重复扣款")
		os.Exit(1)
	}
	if r.CreationAttempts >= 3 && !resetBudget {
		fmt.Fprintln(os.Stderr, "拒绝: 重试预算已耗尽（如确认要重新尝试，加 --reset-budget）")
		os.Exit(1)
	}
	if r.CreationRetryable && !resetBudget {
		fmt.Fprintln(os.Stderr, "该订单未被冻结，无需处理")
		os.Exit(0)
	}

	if resetBudget {
		// Reset to 1, not 0: checkout.go requires CreationAttempts >= 1 as proof
		// that an attempt was actually reserved. Zero trips the same guard.
		fmt.Printf("重置重试预算: %d -> 1\n", r.CreationAttempts)
		r.CreationAttempts = 1
	}
	r.CreationRetryable = true
	b, err := json.Marshal(&r)
	if err != nil {
		fmt.Fprintln(os.Stderr, "序列化失败:", err)
		os.Exit(1)
	}
	defer clear(b)
	if err = v.Put(key, b); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
		fmt.Fprintln(os.Stderr, "写回失败:", err)
		os.Exit(1)
	}
	fmt.Printf("改后: status=%s attempts=%d retryable=%v\n", r.Status, r.CreationAttempts, r.CreationRetryable)
	fmt.Println("已解冻，可以重新提交补单。")
}
