package site

// Admin settings API: lets an operator keep the site configured from the
// browser after the first-run page is gone. Every handler writes straight to
// the encrypted vault and answers with a redacted snapshot — no full secret is
// ever sent back to the browser.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"xgift/internal/checkout"
)

var errInvalidCredentials = errors.New("stored credentials are unreadable")

// settingsView is the redacted snapshot the admin settings page renders.
type settingsView struct {
	Payments    bool                  `json:"payments"`
	Credentials credentialsView       `json:"credentials"`
	Cards       []checkout.CardStatus `json:"cards"`
	Stripe      stripeView            `json:"stripe"`
	Catalog     catalogView           `json:"catalog"`
	Proxy       proxyView             `json:"proxy"`
}

type credentialsView struct {
	AuthTokenTail   string `json:"auth_token_tail"`
	HasCT0          bool   `json:"has_ct0"`
	AuthorizationOn bool   `json:"authorization_set"`
	UserAgent       string `json:"user_agent"`
}

type stripeView struct {
	Present bool   `json:"present"`
	Tail    string `json:"tail"`
}

type catalogView struct {
	Merchant string                 `json:"merchant"`
	Currency string                 `json:"currency"`
	Plans    []checkout.CatalogPlan `json:"plans"`
}

type proxyView struct {
	Direct bool     `json:"direct"`
	Tags   []string `json:"tags"`
}

// tail returns the last few characters of a secret so an operator can tell two
// values apart without the value ever leaving the vault.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}

func (s *server) adminSettings(w http.ResponseWriter, r *http.Request) {
	view := settingsView{Payments: s.payments, Cards: []checkout.CardStatus{}}

	if raw, err := s.vault.Get("cookies"); err == nil {
		var doc struct {
			Cookies []struct{ Name, Value string } `json:"cookies"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			for _, c := range doc.Cookies {
				switch c.Name {
				case "auth_token":
					view.Credentials.AuthTokenTail = tail(c.Value)
				case "ct0":
					view.Credentials.HasCT0 = c.Value != ""
				}
			}
		}
		clear(raw)
	}
	if raw, err := s.vault.Get("api-auth"); err == nil {
		var headers map[string]string
		if json.Unmarshal(raw, &headers) == nil {
			view.Credentials.UserAgent = headers["UserAgent"]
			view.Credentials.AuthorizationOn = headers["Authorization"] != ""
		}
		clear(raw)
	}
	if raw, err := s.vault.Get("stripe-key"); err == nil {
		view.Stripe.Present = true
		view.Stripe.Tail = tail(string(raw))
		clear(raw)
	}
	if catalog, err := checkout.ReadCatalog(s.vault); err == nil {
		view.Catalog = catalogView{Merchant: catalog.Merchant, Currency: catalog.Currency, Plans: catalog.Plans}
	}
	if raw, err := s.vault.Get("proxy"); err == nil {
		var doc struct {
			Outbounds []struct {
				Type string `json:"type"`
				Tag  string `json:"tag"`
			} `json:"outbounds"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			for _, o := range doc.Outbounds {
				if o.Tag != "" {
					view.Proxy.Tags = append(view.Proxy.Tags, o.Tag)
				}
				if o.Type == "direct" {
					view.Proxy.Direct = true
				}
			}
		}
		clear(raw)
	}
	if cards, err := checkout.CardsStatus(s.vault); err == nil && cards != nil {
		view.Cards = cards
	}
	reply(w, 200, view)
}

// saveCredentials replaces the X cookies and request headers. A blank field
// keeps the stored value, so an operator can rotate the cookie without having
// to retype the User-Agent.
func (s *server) saveCredentials(w http.ResponseWriter, r *http.Request) {
	var form struct {
		AuthToken     string `json:"auth_token"`
		CT0           string `json:"ct0"`
		Authorization string `json:"authorization"`
		UserAgent     string `json:"user_agent"`
	}
	if !decodeSetup(w, r, &form) {
		return
	}
	if strings.TrimSpace(form.AuthToken) != "" {
		if err := checkCookieValue("auth_token", strings.TrimSpace(form.AuthToken)); err != nil {
			message(w, 400, err.Error())
			return
		}
	}
	if strings.TrimSpace(form.CT0) != "" {
		if err := checkCookieValue("ct0", strings.TrimSpace(form.CT0)); err != nil {
			message(w, 400, err.Error())
			return
		}
	}
	authToken, ct0, err := s.currentCookiePair()
	if err != nil {
		message(w, 500, "读取现有凭据失败。")
		return
	}
	if v := strings.TrimSpace(form.AuthToken); v != "" {
		authToken = v
	}
	if v := strings.TrimSpace(form.CT0); v != "" {
		ct0 = v
	}
	if authToken == "" || ct0 == "" {
		message(w, 400, "auth_token 与 ct0 不能为空。")
		return
	}
	cookies, err := json.Marshal(map[string]any{"cookies": []map[string]string{
		{"name": "auth_token", "value": authToken, "domain": ".x.com"},
		{"name": "ct0", "value": ct0, "domain": ".x.com"},
	}})
	if err != nil {
		message(w, 500, "构造凭据失败。")
		return
	}
	defer clear(cookies)

	authorization := strings.TrimSpace(form.Authorization)
	userAgent := strings.TrimSpace(form.UserAgent)
	if authorization == "" || userAgent == "" {
		// Fill only the blank field from the stored record, so updating the
		// cookie alone never discards a hand-edited User-Agent.
		storedAuth, storedUA, _ := s.readHeaders()
		if authorization == "" {
			authorization = storedAuth
		}
		if userAgent == "" {
			userAgent = storedUA
		}
	}
	if authorization == "" {
		authorization = defaultBearer
	}
	if !bearerPattern.MatchString(authorization) {
		message(w, 400, "Authorization 必须以 \"Bearer \" 开头。")
		return
	}
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	auth, err := json.Marshal(map[string]string{"Authorization": authorization, "UserAgent": userAgent})
	if err != nil {
		message(w, 500, "构造请求头失败。")
		return
	}
	defer clear(auth)

	if err = s.vault.Put("cookies", cookies); err != nil {
		message(w, 500, "保存 cookies 失败。")
		return
	}
	if err = s.vault.Put("api-auth", auth); err != nil {
		message(w, 500, "保存请求头失败。")
		return
	}
	reply(w, 200, map[string]any{"ok": true})
}

// currentCookiePair reads the stored auth_token and ct0.
func (s *server) currentCookiePair() (string, string, error) {
	raw, err := s.vault.Get("cookies")
	if err != nil {
		return "", "", err
	}
	defer clear(raw)
	var doc struct {
		Cookies []struct{ Name, Value string } `json:"cookies"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return "", "", errInvalidCredentials
	}
	var authToken, ct0 string
	for _, c := range doc.Cookies {
		switch c.Name {
		case "auth_token":
			authToken = c.Value
		case "ct0":
			ct0 = c.Value
		}
	}
	return authToken, ct0, nil
}

// readHeaders returns the stored Authorization header and User-Agent.
func (s *server) readHeaders() (string, string, error) {
	raw, err := s.vault.Get("api-auth")
	if err != nil {
		return "", "", err
	}
	defer clear(raw)
	var headers map[string]string
	if json.Unmarshal(raw, &headers) != nil {
		return "", "", errInvalidCredentials
	}
	return headers["Authorization"], headers["UserAgent"], nil
}

// addCards appends to the card pool, reusing the checkout validator.
func (s *server) addCards(w http.ResponseWriter, r *http.Request) {
	var form struct {
		Cards []cardForm `json:"cards"`
	}
	if !decodeSetup(w, r, &form) {
		return
	}
	raw, err := marshalCards(form.Cards)
	if err != nil {
		message(w, 400, err.Error())
		return
	}
	defer clear(raw)
	// AddCardRecords merges and returns the new *total*, so the number of cards
	// actually added has to be derived; reporting the total under the key
	// "added" would tell the operator they just added the whole library.
	before, err := checkout.CardsStatus(s.vault)
	if err != nil {
		message(w, 500, "读取现有支付卡失败。")
		return
	}
	total, err := checkout.AddCardRecords(s.vault, raw)
	if err != nil {
		message(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]any{"added": total - len(before), "total": total})
}

func (s *server) removeCard(w http.ResponseWriter, r *http.Request) {
	var form struct {
		Last4 string `json:"last4"`
	}
	if !decode(w, r, &form) {
		return
	}
	last4 := strings.TrimSpace(form.Last4)
	if len(last4) != 4 {
		message(w, 400, "请提供卡片尾号（4 位）。")
		return
	}
	// RemoveCardRecord also returns the remaining count, not how many it dropped.
	before, err := checkout.CardsStatus(s.vault)
	if err != nil {
		message(w, 500, "读取现有支付卡失败。")
		return
	}
	remaining, err := checkout.RemoveCardRecord(s.vault, last4)
	if err != nil {
		message(w, 400, err.Error())
		return
	}
	reply(w, 200, map[string]any{"removed": len(before) - remaining, "total": remaining})
}

func (s *server) unblockCards(w http.ResponseWriter, r *http.Request) {
	unblocked, err := checkout.UnblockPaymentCards(s.vault)
	if err != nil {
		message(w, 500, "解封失败。")
		return
	}
	reply(w, 200, map[string]any{"unblocked": unblocked})
}

func (s *server) saveStripe(w http.ResponseWriter, r *http.Request) {
	var form struct {
		StripeKey string `json:"stripe_key"`
	}
	if !decode(w, r, &form) {
		return
	}
	key := strings.TrimSpace(form.StripeKey)
	if !stripeKeyPattern.MatchString(key) {
		message(w, 400, "Stripe 公钥格式无效（须为 pk_live_ 或 pk_test_ 开头）。")
		return
	}
	if err := s.vault.Put("stripe-key", []byte(key)); err != nil {
		message(w, 500, "保存失败。")
		return
	}
	reply(w, 200, map[string]any{"ok": true, "tail": tail(key)})
}

func (s *server) saveCatalog(w http.ResponseWriter, r *http.Request) {
	var f struct {
		Merchant string     `json:"merchant"`
		Currency string     `json:"currency"`
		Plans    []planForm `json:"plans"`
	}
	if !decodeSetup(w, r, &f) {
		return
	}
	full := credentialForm{Merchant: f.Merchant, Currency: f.Currency, Plans: f.Plans}
	raw, err := full.catalogRecord()
	if err != nil {
		message(w, 400, err.Error())
		return
	}
	defer clear(raw)
	if err = s.vault.Put("catalog", raw); err != nil {
		message(w, 500, "保存套餐失败。")
		return
	}
	reply(w, 200, map[string]any{"ok": true})
}

// saveOutbounds stores the payment outbound pool. The checkout reads this
// record for regional pricing and checkout creation, so it is the one that
// decides whether X quotes a low-price region.
func (s *server) saveOutbounds(w http.ResponseWriter, r *http.Request) {
	var f outboundsForm
	if !decodeSetup(w, r, &f) {
		return
	}
	raw, err := f.outboundsRecord()
	if err != nil {
		message(w, 400, err.Error())
		return
	}
	defer clear(raw)
	if err = s.vault.Put("payment-outbounds", raw); err != nil {
		message(w, 500, "保存出站节点失败。")
		return
	}
	// 出站池在下一次付款时生效，无需重启；但已绑定节点的在途订单仍走原出口。
	reply(w, 200, map[string]any{"ok": true, "restart_required": false})
}

// outboundsStatus reports the payment pool as the checkout sees it.
func (s *server) outboundsStatus(w http.ResponseWriter, r *http.Request) {
	view, err := describeOutbounds(s.vault)
	if err != nil {
		message(w, 500, "读取出站配置失败："+err.Error())
		return
	}
	reply(w, 200, view)
}

// saveProxy stores a proxy configuration. The embedded sing-box is started at
// boot, so the change only takes effect after a restart; the response says so
// rather than pretending the running process changed.
func (s *server) saveProxy(w http.ResponseWriter, r *http.Request) {
	var f credentialForm
	if !decodeSetup(w, r, &f) {
		return
	}
	raw, err := f.proxyRecord()
	if err != nil {
		message(w, 400, err.Error())
		return
	}
	defer clear(raw)
	if err = s.vault.Put("proxy", raw); err != nil {
		message(w, 500, "保存代理配置失败。")
		return
	}
	reply(w, 200, map[string]any{"ok": true, "restart_required": true})
}
