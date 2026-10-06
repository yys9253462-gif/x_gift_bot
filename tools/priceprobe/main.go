// 一次性只读探针：从当前配置的付款出口向 X 询价，看真实返回的币种与金额。
// 不创建 checkout、不提交付款、不触碰任何接收方。
//
// 关键：X 的区域询价必须走付款出口（regionalHTTP），而 newXClient 需要
// 一个已经在本机监听的代理端口。OpenOutbound 内部自己选端口且不对外暴露，
// 所以这里照它的做法：先占一个空闲端口，再用它启动 sing-box。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

func main() {
	db := flag.String("db", "/var/lib/xgift/vault.db", "encrypted SQLite record store")
	months := flag.Int("months", 3, "plan months to quote")
	user := flag.String("user", "yan_ye95623", "query context account")
	nodeIndex := flag.Int("node", 0, "which outbound node to use (0-based)")
	flag.Parse()

	v, err := vault.Open(*db, os.Getenv("XGIFT_PASSWORD_FILE"), false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开保险库失败: %v\n", err)
		os.Exit(1)
	}
	defer v.Close()

	// 1) 付款出口现状
	netStatus, err := checkout.PaymentNetworkStatus(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取出站状态失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("付款出口: mode=%s nodes=%d available=%d cooling=%d\n",
		netStatus.Mode, netStatus.Nodes, netStatus.Available, netStatus.Cooling)

	// 2) catalog
	cat, err := checkout.ReadCatalog(v)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\ncatalog 未配置或不可读: %v\n", err)
		fmt.Fprintln(os.Stderr, "→ 询价需要商品 ID 与期望币种。")
		os.Exit(1)
	}
	plan, err := cat.PlanFor(*months)
	if err != nil {
		fmt.Fprintf(os.Stderr, "catalog 里没有 %d 个月的套餐: %v\n", *months, err)
		os.Exit(1)
	}
	fmt.Printf("catalog: merchant=%s currency=%s\n", cat.Merchant, cat.Currency)
	fmt.Printf("待询价: %d 个月 期望币种=%s 期望金额=%d(最小单位) product=%s\n",
		plan.Months, plan.Currency, plan.Minor, plan.ProductID)

	// 3) 启动付款出口，拿到本地端口
	raw, err := v.Get("payment-outbounds")
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n未配置付款出口，X 会按服务器所在国报价: %v\n", err)
		os.Exit(1)
	}
	defer clearBytes(raw)
	nodes, err := proxy.ParseOutboundPool(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "出站池解析失败: %v\n", err)
		os.Exit(1)
	}
	if len(nodes) == 0 {
		fmt.Fprintln(os.Stderr, "\n出站池为空，X 会按服务器所在国报价。先配一个节点。")
		os.Exit(1)
	}
	node := nodes[*nodeIndex]
	var meta struct{ Tag, Type string }
	json.Unmarshal(node, &meta)
	fmt.Printf("付款出口节点: %s (%s)\n", meta.Tag, meta.Type)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 4) 询价。ProbeQuote 内部会按付款出口启动区域通道，与真实下单一致。
	fmt.Println()
	got, err := checkout.ProbeQuote(ctx, v, *user, plan, 0, "")
	if err != nil {
		fmt.Printf("询价失败: %v\n", err)
		os.Exit(2)
	}

	minor := float64(got.Amount) / 1e6
	fmt.Println("=== X 实际返回 ===")
	fmt.Printf("  商品 ID : %s\n", got.ProductID)
	fmt.Printf("  币种    : %s\n", got.Currency)
	fmt.Printf("  金额    : %d (amount_local_micro)\n", got.Amount)
	fmt.Printf("  主单位  : %.2f\n", minor)
	fmt.Printf("  一次性  : %v\n", got.OneTime)

	fmt.Println("\n=== 与 catalog 期望值比对 ===")
	// X 返回的币种大小写不固定（实测返回 "Bdt"），而下单路径用的是
	// strings.EqualFold，所以这里也必须不区分大小写，否则会误报不匹配。
	matchCurrency := strings.EqualFold(got.Currency, plan.Currency)
	matchAmount := got.Amount == int64(plan.Minor)*10000
	fmt.Printf("  币种一致 : %v (期望 %s)\n", matchCurrency, plan.Currency)
	fmt.Printf("  金额一致 : %v (期望 %d)\n", matchAmount, int64(plan.Minor)*10000)
	if matchCurrency && matchAmount && got.OneTime {
		fmt.Println("\n结论: 完全匹配 —— 可以直接下单")
	} else {
		fmt.Println("\n结论: 不匹配 —— 按上面实际值改 catalog，否则下单会被拒")
		fmt.Printf("建议: 把 %d 个月的金额改成 %.2f（币种 %s）\n", plan.Months, minor, got.Currency)
	}
}

func clearBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
