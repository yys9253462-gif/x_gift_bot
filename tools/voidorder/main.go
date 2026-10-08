// Command voidorder removes an abandoned checkout so the recipient can be
// gifted again.
//
// It exists for a specific leftover: an order that never reached X's create
// step (so it holds no session, no payment evidence and no money) but still
// occupies the recipient's slot. While that record exists, checkout.go refuses
// to start another order for the same recipient, and site.go rejects the
// redemption code with 409 — the recipient is permanently blocked.
//
// Safety rules, all checked before anything is written:
//   - the order must be unsubmitted (no session, no payment method, nothing
//     submitted), so no money can be affected;
//   - the order must not be succeeded;
//   - the matching redemption code is only moved to "revoked", never deleted,
//     so the audit trail survives.
//
// The vault record is removed inside a transaction together with the code
// update, so a crash cannot leave the two disagreeing.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: voidorder <recipient-rest-id>")
		os.Exit(2)
	}
	recipient := os.Args[1]

	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	key := "checkout:" + recipient
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

	fmt.Printf("订单: user=@%s status=%s attempts=%d months=%d\n",
		r.Username, r.Status, r.CreationAttempts, r.Months)
	fmt.Printf("付款证据: session=%q url=%q payment=%q submitted=%d\n",
		r.SessionID, r.URL, r.PaymentMethod, r.SubmittedAt)

	// Never void an order that reached the payment provider.
	if r.SessionID != "" || r.URL != "" || r.PaymentMethod != "" ||
		r.ConfirmParameters != "" || r.ConfirmKey != "" || r.SubmittedAt != 0 || r.PreflightSaved {
		fmt.Fprintln(os.Stderr, "拒绝: 订单含付款证据，作废可能造成重复扣款或漏记")
		os.Exit(1)
	}
	if r.Status == "succeeded" {
		fmt.Fprintln(os.Stderr, "拒绝: 订单已成功，不可作废")
		os.Exit(1)
	}
	fmt.Printf("安全检查通过: 未提交过付款 (status=%s)\n", r.Status)

	// Move the redemption code to revoked and drop the checkout record together.
	site, err := sql.Open("sqlite3", "file:"+siteDB()+"?_busy_timeout=5000")
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 site.db 失败:", err)
		os.Exit(1)
	}
	defer site.Close()

	tx, err := site.Begin()
	if err != nil {
		fmt.Fprintln(os.Stderr, "开启事务失败:", err)
		os.Exit(1)
	}
	res, err := tx.Exec(
		"UPDATE codes SET status='revoked',message=?,updated=? WHERE recipient_id=? AND status IN ('active','processing','review')",
		"订单已作废（未提交付款），如需赠送请使用新兑换码。", time.Now().Unix(), recipient)
	if err != nil {
		_ = tx.Rollback()
		fmt.Fprintln(os.Stderr, "更新兑换码失败:", err)
		os.Exit(1)
	}
	touched, _ := res.RowsAffected()

	// The vault has no delete verb, and writing an empty payload would leave an
	// undecryptable record behind — worse than a stale one, because every read
	// would fail. Deleting the row needs no vault key: the payload is already
	// ciphertext, so removing it is a pure storage operation and cannot damage
	// anything else in the file.
	rawDB, err := sql.Open("sqlite3", "file:"+vaultpath.DB()+"?_busy_timeout=5000")
	if err != nil {
		_ = tx.Rollback()
		fmt.Fprintln(os.Stderr, "打开 vault.db 失败:", err)
		os.Exit(1)
	}
	defer rawDB.Close()
	if _, err = rawDB.Exec("DELETE FROM secrets WHERE name=?", key); err != nil {
		_ = tx.Rollback()
		fmt.Fprintln(os.Stderr, "删除订单记录失败:", err)
		os.Exit(1)
	}
	if err = tx.Commit(); err != nil {
		fmt.Fprintln(os.Stderr, "提交事务失败:", err)
		os.Exit(1)
	}

	fmt.Printf("已作废: 兑换码 %d 条置为 revoked，订单记录已清空\n", touched)
	fmt.Println("该接收方现在可以重新赠送。")
}

// siteDB 返回站点库路径。它和保险库不在同一个文件里，所以单独一个变量；
// 默认值与 vaultpath 保持一致，同一部署目录下。
func siteDB() string {
	if p := os.Getenv("XGIFT_SITE_DB"); p != "" {
		return p
	}
	return "/var/lib/xgift/site.db"
}
