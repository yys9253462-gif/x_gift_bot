// Command setoutbound replaces the payment outbound pool in the vault.
//
// It writes only the "payment-outbounds" record. Every other vault entry and
// every site.db row is left untouched, and the previous value is printed so it
// can be restored by hand if the new node misbehaves.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

type node struct {
	Type     string `json:"type"`
	Tag      string `json:"tag"`
	Server   string `json:"server"`
	Port     uint16 `json:"server_port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

func main() {
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: setoutbound <type> <tag> <server> <port> <user:pass>")
		os.Exit(2)
	}
	typ, tag, server, portArg, cred := os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5]

	var port uint16
	if _, err := fmt.Sscanf(portArg, "%d", &port); err != nil || port == 0 {
		fmt.Fprintln(os.Stderr, "端口无效")
		os.Exit(2)
	}

	var user, pass string
	for i := 0; i < len(cred); i++ {
		if cred[i] == ':' {
			user, pass = cred[:i], cred[i+1:]
			break
		}
	}
	if user == "" || pass == "" {
		fmt.Fprintln(os.Stderr, "凭据格式应为 user:pass")
		os.Exit(2)
	}

	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	// Print the current value first so a rollback never depends on this tool.
	if prev, err := v.Get("payment-outbounds"); err == nil {
		fmt.Println("改前 payment-outbounds:")
		fmt.Println(" ", string(prev))
		clear(prev)
	} else {
		fmt.Println("改前 payment-outbounds: (不存在)")
	}

	pool := []node{{Type: typ, Tag: tag, Server: server, Port: port, Username: user, Password: pass}}
	b, err := json.Marshal(pool)
	if err != nil {
		fmt.Fprintln(os.Stderr, "序列化失败:", err)
		os.Exit(1)
	}
	defer clear(b)
	if err = v.Put("payment-outbounds", b); err != nil {
		fmt.Fprintln(os.Stderr, "写入失败:", err)
		os.Exit(1)
	}
	fmt.Printf("改后 payment-outbounds: type=%s tag=%s server=%s port=%d\n", typ, tag, server, port)
	fmt.Println("已写入。")
}
