// Command gifttrial runs the real production creation path against the account
// currently stored in the vault, without ever touching Stripe.
//
// It exists to answer one question precisely: does the sending account clear
// X's "Current user is not eligible to gift" (code 37) gate? Every prior
// attempt in this incident burned a retry budget on that same answer, so this
// probe reports the verdict first and never writes to the vault.
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
		fmt.Fprintln(os.Stderr, "usage: gifttrial <x-username>")
		os.Exit(2)
	}
	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	user := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	fmt.Printf("=== 赠送资格实测（不付款）: %s ===\n", user)

	id, err := checkout.Eligibility(ctx, v, user, 0)
	if err != nil {
		fmt.Println("  [1/3] 接收方资格查询失败:", err)
		os.Exit(1)
	}
	fmt.Printf("  [1/3] 接收方资格 OK  rest_id=%s\n", id)

	cat, err := checkout.ReadCatalog(v)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 catalog 失败:", err)
		os.Exit(1)
	}
	plan, err := cat.PlanFor(3)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取套餐失败:", err)
		os.Exit(1)
	}

	start := time.Now()
	q, err := checkout.ProbeQuote(ctx, v, user, plan, 0, id)
	if err != nil {
		fmt.Printf("  [2/3] 区域询价失败(%.1fs): %v\n", time.Since(start).Seconds(), err)
		os.Exit(1)
	}
	fmt.Printf("  [2/3] 区域询价 OK(%.1fs) currency=%s amount=%d oneTime=%v\n",
		time.Since(start).Seconds(), q.Currency, q.Amount, q.OneTime)

	// The decisive step. This reaches X's gift-order creation mutation, which is
	// exactly where code 37 is raised, but stops before any payment is possible.
	r, err := checkout.RecoverWithNewLink(ctx, v, user, id, 0, 3, false, true)
	if err != nil {
		fmt.Printf("  [3/3] 创建赠送订单失败(%.1fs)\n", time.Since(start).Seconds())
		fmt.Println("         错误:", err)
		if errors.Is(err, checkout.ErrGiftNotAuthorised) {
			fmt.Println("\n=> 结论: X 明确拒绝该发送账号创建赠送订单（code 37）。")
			fmt.Println("   这是账号侧资格问题，与接收方、出口、金额、卡片都无关。")
			fmt.Println("   换凭据无法解决，必须在 X 侧解除该账号的赠送限制。")
		}
		os.Exit(1)
	}
	fmt.Printf("  [3/3] 创建订单成功(%.1fs) status=%s session=%v\n",
		time.Since(start).Seconds(), r.Status, r.SessionID != "")
	fmt.Println("\n=> 结论: 发送账号已通过 code 37 关卡，订单已创建但未付款。")
	fmt.Println("   可以继续走付款流程。")
}
