// Command giftlive runs the real production checkout for a recipient, all the
// way through payment.
//
// It exists because the operator needs one command that exercises the same
// path the site uses — regional exit, price check, X order creation, Stripe
// tokenisation and submission — instead of a chain of probes that each stop
// one step earlier. Every probe used while diagnosing this incident stopped at
// the create boundary, which is exactly why the real failure stayed invisible.
//
// The sending account always comes from the vault; only the recipient is an
// argument, because that is the side this command varies.
//
// Safety: it refuses to touch an order that already carries payment evidence,
// and it prints the record state after every step so a partial run is obvious.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: giftlive <recipient-x-username>")
		fmt.Fprintln(os.Stderr, "  发送方账号取自 vault 中保存的凭据")
		os.Exit(2)
	}
	// This is the RECIPIENT. The sender is whichever account the vault holds.
	recipientUser := os.Args[1]

	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	fmt.Printf("=== 真实下单：vault 账号赠送 @%s ===\n", recipientUser)
	start := time.Now()

	// Resolve the recipient first so the record identity can be checked before
	// anything is written or paid.
	id, err := checkout.Eligibility(ctx, v, recipientUser, 0)
	if err != nil {
		fmt.Println("接收方资格查询失败:", err)
		os.Exit(1)
	}
	fmt.Printf("  接收方 @%s rest_id=%s  (%.1fs)\n", recipientUser, id, time.Since(start).Seconds())

	// user = the recipient's X username; expectedRecipient = its verified id.
	r, err := checkout.RunForRecipient(ctx, v, recipientUser, id, true, 0, 3)
	if err != nil {
		fmt.Printf("下单失败(%.1fs)\n", time.Since(start).Seconds())
		fmt.Println("  错误:", err)
		if r != nil {
			fmt.Printf("  订单状态: status=%s attempts=%d session=%q payment=%q submitted=%d\n",
				r.Status, r.CreationAttempts, r.SessionID, r.PaymentMethod, r.SubmittedAt)
		}
		switch {
		case errors.Is(err, checkout.ErrGiftNotAuthorised):
			fmt.Println("  => X 拒绝该发送账号创建赠送订单（code 37）")
		case errors.Is(err, checkout.ErrNotEligible):
			fmt.Println("  => 接收方不可接收")
		}
		os.Exit(1)
	}

	fmt.Printf("下单完成(%.1fs)\n", time.Since(start).Seconds())
	fmt.Printf("  status          = %s\n", r.Status)
	fmt.Printf("  session_id      = %s\n", r.SessionID)
	fmt.Printf("  payment_method  = %s\n", r.PaymentMethod)
	fmt.Printf("  submitted_at    = %d\n", r.SubmittedAt)
	if u := checkout.CheckoutLink(r); u != "" {
		fmt.Printf("  checkout_url    = %s\n", u)
	}
	if r.Status == "succeeded" {
		fmt.Println("\n=> 赠送成功，Premium 已激活。")
	} else {
		fmt.Printf("\n=> 当前状态 %s，未成功。\n", r.Status)
	}
}
