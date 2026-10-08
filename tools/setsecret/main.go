// Command setsecret writes one plaintext value into the encrypted vault.
//
// It exists because operational fixes regularly need a single vault record
// written outside the web UI — a Stripe publishable key, an outbound proxy, a
// rotated card. Doing that through the settings page requires a browser and a
// full admin session, which is not always available when a payment is already
// in flight.
//
// Every value is validated against the shape the production code expects, so a
// malformed value is rejected here rather than half way through a payment.
package main

import (
	"fmt"
	"os"
	"regexp"

	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

var shapes = map[string]*regexp.Regexp{
	// stripe.go requires exactly this prefix and nothing else.
	"stripe-key": regexp.MustCompile(`^pk_live_[A-Za-z0-9]+$`),
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: setsecret <key> <value>")
		os.Exit(2)
	}
	name, value := os.Args[1], os.Args[2]

	if shape, ok := shapes[name]; ok && !shape.MatchString(value) {
		fmt.Fprintf(os.Stderr, "拒绝: %s 不符合 %s\n", name, shape)
		os.Exit(1)
	}
	if name == "stripe-key" && len(value) < 20 {
		fmt.Fprintln(os.Stderr, "拒绝: Stripe 公钥明显过短")
		os.Exit(1)
	}

	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	// Show what is being replaced so the change can be traced afterwards.
	if old, e := v.Get(name); e == nil {
		fmt.Printf("改前 %s: %s\n", name, summarise(string(old)))
		clear(old)
	} else {
		fmt.Printf("改前 %s: (不存在，本次新建)\n", name)
	}

	if err = v.Put(name, []byte(value)); err != nil {
		fmt.Fprintln(os.Stderr, "写入失败:", err)
		os.Exit(1)
	}
	back, err := v.Get(name)
	if err != nil {
		fmt.Fprintln(os.Stderr, "写入后校验失败:", err)
		os.Exit(1)
	}
	if string(back) != value {
		fmt.Fprintln(os.Stderr, "写入后校验不一致")
		os.Exit(1)
	}
	clear(back)
	fmt.Printf("改后 %s: %s\n已写入并校验通过。\n", name, summarise(value))
}

// summarise keeps the identifying head of a secret so it is recognisable in a
// log without ever printing enough of it to be useful to an attacker.
func summarise(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:8] + "..." + fmt.Sprint(len(s)) + "chars"
}
