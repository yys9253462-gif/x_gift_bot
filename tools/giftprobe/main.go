// Command giftprobe reports whether an X account can receive a Premium gift.
//
// Three outcomes must stay separate: the account may not exist, may exist but
// be refused, or may be eligible. Collapsing them into "not eligible" hides
// typos from the operator.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"xgift/internal/checkout"
	"xgift/internal/vault"
	"xgift/internal/vaultpath"
)

// queryId 与 internal/checkout/xapi.go 的 DefaultOpPremiumGifting 保持一致。
const opPremiumGifting = "kn8hCE6bHstQV2MtfYDTKg"

// opAccountByScreenName 用于 whois：确认当前凭据对应哪个账号。
const opAccountByScreenName = "Pb6ECdXk-xx56MGzQ"

// opOneTimeGift 与 internal/checkout/xapi.go 的 DefaultOpOneTimeGiftMutation 保持一致。
const opOneTimeGift = "GqTVJ4S1526tLkxj69xIZw"

func main() {
	args := os.Args[1:]
	if len(args) == 2 && args[0] == "create" {
		// create 跳过本地资格判断，直接问 X 的下单接口要真实判据。
		createProbe(args[1])
		return
	}
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: giftprobe <x-username> | giftprobe whois | giftprobe create <x-username>")
		os.Exit(2)
	}
	if args[0] == "whois" {
		whois()
		return
	}
	user := strings.ToLower(strings.TrimPrefix(args[0], "@"))

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

	// X 判定不可接收时，理由不在我们请求的三个字段里，而在同一响应的其他字段
	// （已订阅 / 封禁 / 地区限制 /  就是发送方本人）。先留原文再解析，
	// 这样遇到 false 时能看到原始判据，不必再猜。
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取响应失败:", err)
		os.Exit(1)
	}
	if os.Getenv("GIFTPROBE_RAW") != "" {
		fmt.Printf("--- 原始响应 ---\n%s\n", raw)
	}
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
	if err := json.Unmarshal(raw, &body); err != nil {
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

// createProbe 跳过本地资格判断，直接向 X 的下单接口要一个真实判据。
//
// 为什么需要它：premium_gifting_eligible 是一个布尔值，为false 时
// 有多种完全不同的原因（已是订阅用户、地区限制、账号受限……），
// 而生产链路在 identity() 处就返回 ErrNotEligible，永远看不到 X 的
// 原始错误码。这个探测只创建 unpaid 会话，不碰 Stripe，
// 并且不写 vault，因此不消耗任何重试预算。
func createProbe(user string) {
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
	// rest_id 是纯数字，不能用 base64，也不是用户名。
	// 这里不能用 checkout.Eligibility：它内部走 identity(requireEligible=true)，
	// 会先把 eligible=false 的账号挡掉，那样就永远问不到 X 的真实判据。
	id, err := lookupRestID(auth.Authorization, auth.UserAgent, jar, csrf, user)
	if err != nil {
		fmt.Fprintln(os.Stderr, "读取接收方 rest_id 失败:", err)
		os.Exit(1)
	}

	vars := map[string]string{
		"cancel_url":          "https://x.com/" + user + "/gift-premium",
		"success_url":         "https://x.com/" + user + "/gift-premium/success",
		"external_product_id": plan.ProductID,
		"gift_recipient":      id,
	}
	// mutation 的请求形状照抄 internal/checkout/xapi.go 的 callOnce：
	// POST 到不带 query string 的 URL，variables 放 JSON body，并带 queryId。
	// 变量塞在 URL 上会被X 以406 "GET requests only allow query operations" 拒掉。
	enc, err := json.Marshal(vars)
	if err != nil {
		fmt.Fprintln(os.Stderr, "编码变量失败:", err)
		os.Exit(1)
	}
	body, err := json.Marshal(map[string]any{"variables": json.RawMessage(enc), "queryId": opOneTimeGift})
	if err != nil {
		fmt.Fprintln(os.Stderr, "编码请求体失败:", err)
		os.Exit(1)
	}
	target := "https://x.com/i/api/graphql/" + opOneTimeGift + "/useOneTimePurchaseGiftMutation"

	req, err := http.NewRequestWithContext(context.Background(), "POST", target, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "构建请求失败:", err)
		os.Exit(1)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth.Authorization)
	req.Header.Set("Cookie", jar)
	req.Header.Set("X-Csrf-Token", csrf)
	req.Header.Set("Referer", "https://x.com/"+user+"/gift-premium")
	req.Header.Set("Origin", "https://x.com")
	if auth.UserAgent != "" {
		req.Header.Set("User-Agent", auth.UserAgent)
	}
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
	req.Header.Set("X-Twitter-Active-User", "yes")

	fmt.Printf("=== 下单接口真实判据: %s (rest_id=%s) ===\n", user, id)
	fmt.Println("注意: 只创建 unpaid 会话，不碰 Stripe，不写 vault")

	res, err := (&http.Client{Timeout: 40 * time.Second}).Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "请求失败:", err)
		os.Exit(1)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	fmt.Printf("HTTP %d\n%s\n", res.StatusCode, raw)

	// 会话建出来也没关系：它是 unpaid 的，付款这一步永远由人工决定。
	var resp struct {
		Data struct {
			Gift struct {
				ID     string `json:"session_id"`
				Status string `json:"session_status"`
			} `json:"onetimepurchase_gift"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &resp) == nil && resp.Data.Gift.ID != "" {
		fmt.Printf("\n=> X 接受了下单: session_status=%s\n", resp.Data.Gift.Status)
		fmt.Println("   该会话是 unpaid，未付款；如不需要请直接忽略，它不会自动扣款。")
	}
}

// lookupRestID 只取rest_id，不判断资格。
func lookupRestID(bearer, ua, jar, csrf, user string) (string, error) {
	vars, err := json.Marshal(map[string]string{"screenName": user})
	if err != nil {
		return "", err
	}
	q := url.Values{"variables": {string(vars)}}
	req, err := http.NewRequestWithContext(context.Background(), "GET",
		"https://x.com/i/api/graphql/"+opPremiumGifting+"/PremiumGiftingQuery?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", bearer)
	req.Header.Set("Cookie", jar)
	req.Header.Set("X-Csrf-Token", csrf)
	req.Header.Set("Referer", "https://x.com/"+user+"/gift-premium")
	req.Header.Set("Origin", "https://x.com")
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("X-Twitter-Auth-Type", "OAuth2Session")
	req.Header.Set("X-Twitter-Active-User", "yes")
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var body struct {
		Data struct {
			User struct {
				Result struct {
					ID string `json:"rest_id"`
				} `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", fmt.Errorf("响应无法解析: %s", raw)
	}
	if body.Data.User.Result.ID == "" {
		return "", errors.New("账号不存在或 X 未返回 rest_id")
	}
	return body.Data.User.Result.ID, nil
}
