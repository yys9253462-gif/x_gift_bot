// Command getcode prints one plaintext vault record.
//
// It only reads: no site.db row and no vault entry is ever modified.
package main

import (
	"fmt"
	"os"

	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: getcode <vault-key>")
		os.Exit(2)
	}
	// vault.Open takes the password FILE path, not the password itself.
	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()
	raw, err := v.Get(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取失败:", err)
		os.Exit(1)
	}
	defer clear(raw)
	fmt.Println(string(raw))
}
