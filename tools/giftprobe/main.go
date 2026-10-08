// Command giftprobe reports whether an X account can receive a Premium gift.
//
// Three outcomes must stay separate: the account may not exist, may exist but
// be refused, or may be eligible. Collapsing them into "not eligible" hides
// typos from the operator.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

// queryId 与 internal/checkout/xapi.go 的 DefaultOpPremiumGifting 保持一致。
const opPremiumGifting = "kn8hCE6bHstQV2MtfYDTKg"

// opAccountByScreenName 用于 whois：确认当前凭据对应哪个账号。
const opAccountByScreenName = "Pb6ECdXk-xx56MGzQ"

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: giftprobe <x-username> | giftprobe whois")
		os.Exit(2)
	}
	if os.Args[1] == "whois" {
		whois()
		return
	}
	user := strings.ToLower(strings.TrimPrefix(os.Args[1], "@"))

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
	bearer := auth.Authorization
	ua := auth.UserAgent

	cookieRaw, err := v.Get("cookies")
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取 cookies 失败:", err)
		os.Exit(1)
	}
	defer clear(cookieRaw)
	var cookieState struct {
		Cookies []struct{ Name, Value, Domain string }
	}
	if json.Unmarshal(cookieRaw, &cookieState) != nil {
		fmt.Fprintln(os.Stderr, "cookies 格式无法解析")
		os.Exit(1)
	}
	var jar, csrf string
	for _, c := range cookieState.Cookies {
		if c.Name == "ct0" {
			csrf = c.Value
		}
		jar += c.Name + "=" + c.Value + "; "
	}

	vars, err := json.Marshal(map[string]string{"screenName": user})
	if err != nil {
		fmt.Fprintln(os.Stderr, "编码变量失败:", err)
		os.Exit(1)
	}
	q := url.Values{"variables": {string(vars)}}
	target := "https://x.com/i/api/graphql/" + opPremiumGifting + "/PremiumGiftingQuery?" + q.Encode()

	req, err := http.NewRequestWithContext(context.Background(), "GET", target, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "构建请求失败:", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", bearer)
	req.Header.Set("Cookie", jar)
	req.Header.Set("X-Csrf-Token", csrf)
	req.Header.Set("Referer", "https://x.com/"+user+"/gift-premium")
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
	req.Header.Set("X-Twitter-Active-User", "yes")
	client := &http.Client{Timeout: 30 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "请求失败:", err)
		os.Exit(1)
	}
	defer res.Body.Close()
	fmt.Printf("=== 目标账号: %s ===\nHTTP %d\n", user, res.StatusCode)

	var body struct {
		Data struct {
			User struct {
				Result struct {
					ID       string `json:"rest_id"`
					Screen   string `json:"screen_name"`
					Eligible *bool  `json:"premium_gifting_eligible"`
				} `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		fmt.Fprintln(os.Stderr, "解析响应失败:", err)
		os.Exit(1)
	}
	r := body.Data.User.Result
	fmt.Printf("  rest_id: %s\n", r.ID)
	fmt.Printf("  screen_name: %s\n", r.Screen)
	if r.Eligible != nil {
		fmt.Printf("  premium_gifting_eligible: %v\n", *r.Eligible)
	}
	fmt.Println()
	switch {
	case r.ID == "":
		fmt.Println("=> 结论: 账号不存在（X 返回空 rest_id，资格字段缺失）")
	case r.Eligible == nil:
		fmt.Println("=> 结论: 账号存在，但 X 未返回资格字段（无法判定，勿当成不可赠送）")
	case !*r.Eligible:
		fmt.Println("=> 结论: 账号存在，X 明确判定该接收方不可接收赠送")
	default:
		fmt.Println("=> 结论: X 返回该接收方可接收赠送")
	}
}
