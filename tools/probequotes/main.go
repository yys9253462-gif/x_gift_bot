// Command probequotes reports the live X gifting capability of the account in
// the vault, for both a recipient and the sending account itself.
//
// Read only: it creates no order and submits no payment.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: probequotes <x-username>")
		os.Exit(2)
	}
	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	user := os.Args[1]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Printf("=== 目标账号: %s ===\n", user)
	recipient, err := checkout.Eligibility(ctx, v, user, 0)
	if err != nil {
		fmt.Println("资格查询失败:", err)
		os.Exit(1)
	}
	fmt.Printf("  可接收赠送: 是\n  rest_id: %s\n", recipient)

	// 直接问 X：这个发送账号自己能不能创建赠送订单。
	// 这是与"接收方资格"完全不同的两个开关，也是 code 37 的真正来源。
	fmt.Println("\n=== 发送方账号能力探测 ===")
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
	res, err := checkout.ProbeQuote(ctx, v, user, plan, 0, recipient)
	if err != nil {
		fmt.Printf("  区域询价失败(%.1fs): %v\n", time.Since(start).Seconds(), err)
		os.Exit(1)
	}
	fmt.Printf("  区域询价 OK(%.1fs) currency=%s amount=%d oneTime=%v\n",
		time.Since(start).Seconds(), res.Currency, res.Amount, res.OneTime)
	if res.Currency == plan.Currency && res.Amount == int64(plan.Minor)*10000 {
		fmt.Println("  金额校验: 通过")
	} else {
		fmt.Println("  金额校验: 不一致")
	}
}
