// 只读对比：同一个 X 账号，在「直连」与「配置的付款出口」下，
// X 报出的 Premium 价格是否不同。
//
// 回答的问题是：换出口能不能改价？
//
//	赠送流程 → X 按下单时的出口国家报价（已知会变）
//	自助订阅 → X 按账号的地区状态报价（本工具验证的假设）
//
// 关键设计：不动任何持久化状态。
//
//	withRegionalExit 的第一条判断是「payment-outbounds 是否存在」：
//	  不存在 → 保持直连
//	  存在   → 启动出口
//	所以本工具用两个独立的 vault 句柄：
//	  句柄 A 读原库（有出口）→ 走出口
//	  句柄 B 读同一文件的副本，副本里删掉 payment-outbounds → 走直连
//	全程不写入任何真实状态，副本用完即删。
//
// 不创建订单、不提交付款、不触碰任何接收方。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

type outcome struct {
	Months   int
	Currency string
	Amount   int64
	Err      error
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: pricemix <x_username>")
		fmt.Fprintln(os.Stderr, "环境变量: XGIFT_DB, XGIFT_PASSWORD_FILE")
		os.Exit(1)
	}
	user := os.Args[1]

	db := envOr("XGIFT_DB", "/var/lib/xgift/vault.db")
	pw := envOr("XGIFT_PASSWORD_FILE", "")

	// ---- 句柄 A：原库（按现状走，即启用付款出口）----
	vA, err := vault.Open(db, pw, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开保险库失败:", err)
		os.Exit(1)
	}
	defer vA.Close()

	cat, err := checkout.ReadCatalog(vA)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取商品目录失败:", err)
		os.Exit(1)
	}

	fmt.Println("=== 商品目录（本地配置）===")
	fmt.Printf("  币种 %s\n", cat.Currency)
	for _, p := range cat.Plans {
		fmt.Printf("  %d 个月: 期望 %.2f %s（商品 %s）\n",
			p.Months, float64(p.Amount)/10000, cat.Currency, p.Product)
	}
	fmt.Println()

	// ---- 出口配置现状 ----
	fmt.Println("=== 付款出站现状 ===")
	outboundCount := 0
	if raw, e := vA.Get("payment-outbounds"); e == nil {
		if nodes, perr := proxy.ParseOutboundPool(raw); perr == nil {
			outboundCount = len(nodes)
			for i, n := range nodes {
				var meta struct{ Type, Tag, Server string }
				json.Unmarshal(n, &meta)
				fmt.Printf("  %d) type=%s tag=%s server=%s\n", i+1, meta.Type, meta.Tag, meta.Server)
			}
		} else {
			fmt.Println("  解析失败:", perr)
		}
		clear(raw)
	} else {
		fmt.Println("  未配置 —— 两次探测都会直连，无法对比")
	}
	fmt.Println()

	// ---- 句柄 B：库的副本，删掉 payment-outbounds → 强制直连 ----
	direct, cleanup, err := openDirectCopy(db, pw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "准备直连副本失败:", err)
		os.Exit(1)
	}
	defer cleanup()

	ctx := context.Background()

	fmt.Println("=== 探测 1：直连（在库副本上，看不到出口配置）===")
	ra := probe(ctx, direct, user, cat)
	printOutcome(ra)
	fmt.Println()

	fmt.Println("=== 探测 2：走配置的付款出口 ===")
	rb := probe(ctx, vA, user, cat)
	printOutcome(rb)
	fmt.Println()

	// ---- 对比 ----
	fmt.Println("=== 结论 ===")
	if len(ra) == 0 || len(rb) == 0 {
		fmt.Println("  数据不足")
		return
	}
	if outboundCount == 0 {
		fmt.Println("  未配置付款出口，两次都是直连，这不构成对比。")
		fmt.Println("  请先在后台「付款出站」配置孟加拉节点再跑。")
		return
	}
	changed := false
	for i := range ra {
		if i >= len(rb) {
			break
		}
		a, b := ra[i], rb[i]
		if a.Err != nil || b.Err != nil {
			fmt.Printf("  %d 个月：直连错误=%v  出口错误=%v\n", a.Months, a.Err, b.Err)
			continue
		}
		same := a.Amount == b.Amount && a.Currency == b.Currency
		if !same {
			changed = true
		}
		mark := "相同"
		if !same {
			mark = "不同"
		}
		fmt.Printf("  %d 个月：直连 %.2f %s  |  出口 %.2f %s   → %s\n",
			a.Months, float64(a.Amount)/10000, a.Currency,
			float64(b.Amount)/10000, b.Currency, mark)
	}
	fmt.Println()
	if changed {
		fmt.Println("  换出口改变了报价 —— 这个账号的定价受出口影响。")
	} else {
		fmt.Println("  换出口没有改变报价。")
		fmt.Println("  自助订阅按账号地区定价，不看你从哪出去；")
		fmt.Println("  想省钱只能改账号的地区设置。")
	}
}

// openDirectCopy 把库复制一份并删掉 payment-outbounds，
// 让 withRegionalExit 走到"保持直连"的分支。
// 原库不被修改。
func openDirectCopy(db, pw string) (*vault.Vault, func(), error) {
	dir, err := os.MkdirTemp("", "pricemix-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }

	// 复制主库（连同 wal/shm，保证一致）
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := db + suffix
		if _, e := os.Stat(src); e != nil {
			continue
		}
		b, e := os.ReadFile(src)
		if e != nil {
			cleanup()
			return nil, nil, e
		}
		if e = os.WriteFile(filepath.Join(dir, filepath.Base(db))+suffix, b, 0600); e != nil {
			cleanup()
			return nil, nil, e
		}
	}
	copyPath := filepath.Join(dir, filepath.Base(db))

	// 删掉 payment-outbounds
	conn, err := sql.Open("sqlite3", copyPath)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if _, err = conn.Exec("DELETE FROM secrets WHERE name='payment-outbounds'"); err != nil {
		conn.Close()
		cleanup()
		return nil, nil, err
	}
	conn.Close()

	v, err := vault.Open(copyPath, pw, false)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return v, func() { v.Close(); cleanup() }, nil
}

func probe(ctx context.Context, v *vault.Vault, user string, cat checkout.Catalog) []outcome {
	var out []outcome
	for _, plan := range cat.Plans {
		p := checkout.Plan{
			Months:    plan.Months,
			Minor:     plan.Amount / 10000, // 目录是 micro，Plan 要主单位
			ProductID: plan.Product,
			Merchant:  cat.Merchant,
			Currency:  cat.Currency,
		}
		c, cancel := context.WithTimeout(ctx, 60*time.Second)
		res, err := checkout.ProbeQuote(c, v, user, p, 0, "")
		cancel()
		o := outcome{Months: plan.Months, Err: err}
		if err == nil {
			o.Currency = res.Currency
			o.Amount = res.Amount
		}
		out = append(out, o)
	}
	return out
}

func printOutcome(list []outcome) {
	for _, o := range list {
		if o.Err != nil {
			fmt.Printf("  %d 个月 → 失败: %v\n", o.Months, o.Err)
			continue
		}
		fmt.Printf("  %d 个月 → %.2f %s\n", o.Months, float64(o.Amount)/10000, o.Currency)
	}
}

func envOr(k, def string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return def
}
