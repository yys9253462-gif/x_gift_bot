// Command whoami reports which X account the vault credentials actually belong
// to.
//
// code 37 is an account-side refusal, so the only useful next question is
// "which account is this?" — the vault stores no screen name, and a stale
// cookie pair looks identical to a fresh one from the outside.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

type auth struct{ Authorization, UserAgent string }
type jar struct {
	Cookies []struct{ Name, Value, Domain string }
}

func main() {
	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	raw, err := v.Get("api-auth")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 api-auth 失败:", err)
		os.Exit(1)
	}
	var a auth
	if json.Unmarshal(raw, &a) != nil {
		fmt.Fprintln(os.Stderr, "api-auth 格式错误")
		os.Exit(1)
	}
	clear(raw)

	raw, err = v.Get("cookies")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 cookies 失败:", err)
		os.Exit(1)
	}
	var j jar
	if json.Unmarshal(raw, &j) != nil {
		fmt.Fprintln(os.Stderr, "cookies 格式错误")
		os.Exit(1)
	}
	clear(raw)

	var authToken, ct0 string
	for _, c := range j.Cookies {
		switch c.Name {
		case "auth_token":
			authToken = c.Value
		case "ct0":
			ct0 = c.Value
		}
	}
	if authToken == "" || ct0 == "" {
		fmt.Fprintln(os.Stderr, "缺少 auth_token 或 ct0")
		os.Exit(1)
	}

	// Fingerprints first: cheap, offline, and they change the moment the
	// operator swaps the account, so the output dates the credentials.
	fmt.Printf("auth_token: %s...%s (len=%d)\n", authToken[:4], authToken[len(authToken)-4:], len(authToken))
	fmt.Printf("ct0       : %s...%s (len=%d)\n", ct0[:4], ct0[len(ct0)-4:], len(ct0))

	// Same GraphQL endpoint the gift flow uses, but asking who we are.
	body := `{"query":"query ViewerQuery { viewer { user_id screen_name created_at } }","variables":{}}`
	req, err := http.NewRequestWithContext(context.Background(), "POST",
		"https://x.com/i/api/graphql/VariantViewerQuery", strings.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", a.Authorization)
	req.Header.Set("User-Agent", a.UserAgent)
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
	req.Header.Set("X-Twitter-Active-User", "yes")
	req.Header.Set("X-Csrf-Token", ct0)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: authToken})
	req.AddCookie(&http.Cookie{Name: "ct0", Value: ct0})

	cl := &http.Client{Timeout: 25 * time.Second}
	res, err := cl.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "请求失败:", err)
		os.Exit(1)
	}
	defer res.Body.Close()
	raw2, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	fmt.Printf("\nHTTP %d  (%d bytes)\n", res.StatusCode, len(raw2))

	var out struct {
		Data struct {
			Viewer *struct {
				UserID     json.Number `json:"user_id"`
				ScreenName string      `json:"screen_name"`
				CreatedAt  string      `json:"created_at"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw2, &out) != nil {
		fmt.Println("响应不是 JSON:", string(raw2[:min(len(raw2), 300)]))
		os.Exit(1)
	}
	if len(out.Errors) > 0 {
		for _, e := range out.Errors {
			fmt.Printf("X 错误: [%d] %s\n", e.Code, e.Message)
		}
		os.Exit(1)
	}
	if out.Data.Viewer == nil {
		fmt.Println("未返回 viewer，凭据可能已失效")
		os.Exit(1)
	}
	v2 := out.Data.Viewer
	fmt.Printf("\n=> 当前凭据的账号: @%s\n", v2.ScreenName)
	fmt.Printf("   user_id   : %s\n", v2.UserID.String())
	if !errors.Is(nil, nil) && v2.CreatedAt != "" {
		fmt.Printf("   注册时间  : %s\n", v2.CreatedAt)
	}
}
