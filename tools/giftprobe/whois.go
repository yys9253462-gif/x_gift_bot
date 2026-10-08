package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

// whois 报告 vault 里当前凭据对应的 X 账号身份。
// 更换发送账号后必须先确认凭据真的换成了新账号，否则 code 37 无法归因。
// 复用 main.go 已验证可用的请求头构造；X 的 1.1 legacy 接口已全部 404。
func whois() {
	v, err := vault.Open(vaultpath.DB(), vaultpath.PasswordFile(), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开 vault 失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	authRaw, err := v.Get("api-auth")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 api-auth 失败:", err)
		os.Exit(1)
	}
	defer clear(authRaw)
	var auth struct{ Authorization, UserAgent string }
	if json.Unmarshal(authRaw, &auth) != nil {
		fmt.Fprintln(os.Stderr, "api-auth 格式无法解析")
		os.Exit(1)
	}

	cookieRaw, err := v.Get("cookies")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 cookies 失败:", err)
		os.Exit(1)
	}
	defer clear(cookieRaw)
	var state struct {
		Cookies []struct{ Name, Value, Domain string }
	}
	if json.Unmarshal(cookieRaw, &state) != nil {
		fmt.Fprintln(os.Stderr, "cookies 格式无法解析")
		os.Exit(1)
	}
	var jar, csrf string
	for _, c := range state.Cookies {
		if c.Name == "ct0" {
			csrf = c.Value
		}
		jar += c.Name + "=" + c.Value + "; "
	}

	// 与 PremiumGiftingQuery 同构的请求：变量名沿用 X 的 snake_case 习惯。
	vars, err := json.Marshal(map[string]string{"screen_name": ""})
	if err != nil {
		fmt.Fprintln(os.Stderr, "编码变量失败:", err)
		os.Exit(1)
	}
	q := url.Values{"variables": {string(vars)}}
	target := "https://x.com/i/api/graphql/" + opAccountByScreenName + "/AccountByScreenNameGraphQL?" + q.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "构建请求失败:", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", auth.Authorization)
	req.Header.Set("Cookie", jar)
	req.Header.Set("X-Csrf-Token", csrf)
	req.Header.Set("Referer", "https://x.com/")
	req.Header.Set("Origin", "https://x.com")
	if auth.UserAgent != "" {
		req.Header.Set("User-Agent", auth.UserAgent)
	}
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
	req.Header.Set("X-Twitter-Active-User", "yes")
	req.Header.Set("X-Twitter-Client-Language", "en")

	client := &http.Client{Timeout: 25 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "请求失败:", err)
		os.Exit(1)
	}
	defer res.Body.Close()
	fmt.Printf("=== 当前凭据身份 ===\nHTTP %d\n", res.StatusCode)

	raw := make([]byte, 4096)
	n, _ := res.Body.Read(raw)
	var body struct {
		Data struct {
			User struct {
				Result struct {
					RestID string `json:"rest_id"`
					Core   struct {
						Screen string `json:"screen_name"`
						Name   string `json:"name"`
					} `json:"core"`
				} `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if json.Unmarshal(raw[:n], &body) != nil {
		fmt.Printf("  (非 JSON 响应) %.200s\n", string(raw[:min(n, 200)]))
		return
	}
	r := body.Data.User.Result
	fmt.Printf("  rest_id: %s\n", r.RestID)
	fmt.Printf("  screen_name: %s\n", r.Core.Screen)
	fmt.Printf("  name: %s\n", r.Core.Name)
	if r.RestID == "" && r.Core.Screen == "" {
		fmt.Println("  => 凭据无效或接口已变更")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
