// 直连探测：用库里的凭据直接请求 X 的定价接口，打印原始响应。
// 只读，不创建订单。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"xgift/internal/vault"
)

func main() {
	db := "/var/lib/xgift/vault.db"
	v, err := vault.Open(db, os.Getenv("XGIFT_PASSWORD_FILE"), false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开保险库失败:", err)
		os.Exit(1)
	}
	defer v.Close()

	rawAuth, _ := v.Get("api-auth")
	var auth struct{ Authorization, UserAgent string }
	json.Unmarshal(rawAuth, &auth)

	rawCk, _ := v.Get("cookies")
	var state struct {
		Cookies []struct{ Name, Value string } `json:"cookies"`
	}
	json.Unmarshal(rawCk, &state)

	h := http.Header{
		"Authorization":             {auth.Authorization},
		"User-Agent":                {auth.UserAgent},
		"Content-Type":              {"application/json"},
		"Origin":                    {"https://x.com"},
		"Referer":                   {"https://x.com/"},
		"X-Twitter-Auth-Type":       {"OAuth2Session"},
		"X-Twitter-Active-User":     {"yes"},
		"X-Twitter-Client-Language": {"en"},
	}
	for _, c := range state.Cookies {
		h.Add("Cookie", c.Name+"="+c.Value)
		if c.Name == "ct0" {
			h.Set("X-Csrf-Token", c.Value)
		}
	}

	vars, _ := json.Marshal(map[string]string{"stripeId": "prod_TJXJtpzqCpI36N"})
	q := url.Values{}
	q.Set("variables", string(vars))
	target := "https://x.com/i/api/graphql/Se1Bp6zcNnuXYXRecV2qLA/useSubscriptionProductDetailsByRestIdQuery?" + q.Encode()

	fmt.Println("=== 直连请求 ===")
	fmt.Println("  URL:", target[:90]+"...")

	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("GET", target, nil)
	req.Header = h
	res, err := client.Do(req)
	if err != nil {
		fmt.Println("  传输错误:", err)
		return
	}
	defer res.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(res.Body, 8192))
	fmt.Printf("  HTTP %d\n", res.StatusCode)
	fmt.Println("  响应体:")
	fmt.Println("  " + string(body))
}
