package site

// Credential validation and vault-record construction shared by the first-run
// setup page and the admin settings API. Secrets are validated here and handed
// straight to the vault; they are never logged, echoed back, or included in any
// error text.

import (
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"xgift/internal/checkout"
)

const (
	// setupMinLength guards the one-shot bootstrap password.
	setupMinLength = 12
	// adminMinLength matches the length the server already requires on disk.
	adminMinLength = 32
	// defaultBearer is the Authorization header the X web client sends.
	defaultBearer = "Bearer AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs%3D1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"
	// defaultUserAgent matches the value the CLI wizard uses when none is given.
	defaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"
)

var (
	// 测试 key 与真实 key 都收：Stripe 账号激活（KYC）往往要几天，
	// 没必要让站点干等在引导模式里 —— 先用 pk_test_ 把配置跑通，
	// 激活后再回管理后台换成 pk_live_ 即可。
	stripeKeyPattern = regexp.MustCompile(`^pk_(live|test)_[A-Za-z0-9]+$`)
	bearerPattern    = regexp.MustCompile(`^Bearer `)
)

// cardForm carries one payment card. The JSON tags are identical to the
// checkout package's internal card type, so the marshalled bytes can be handed
// straight to checkout.AddCardRecords, which re-validates every field.
type cardForm struct {
	Number  string `json:"number"`
	Month   string `json:"exp_month"`
	Year    string `json:"exp_year"`
	CVC     string `json:"cvc"`
	Name    string `json:"billing_name"`
	Email   string `json:"email"`
	Country string `json:"billing_country"`
	Postal  string `json:"billing_postal_code"`
	Line1   string `json:"billing_address_line1"`
	Line2   string `json:"billing_address_line2"`
	City    string `json:"billing_city"`
	State   string `json:"billing_state"`
}

// planForm is one purchasable gift plan. Amount is in major units so the
// operator can type "300" or "4.99" instead of counting minor units by hand.
type planForm struct {
	Months  int    `json:"months"`
	Amount  string `json:"amount"`
	Product string `json:"product"`
}

// credentialForm is the JSON body accepted by POST /api/setup/apply.
type credentialForm struct {
	SetupPassword string `json:"setup_password"`

	AuthToken string `json:"auth_token"`
	CT0       string `json:"ct0"`

	Authorization string `json:"authorization"`
	UserAgent     string `json:"user_agent"`

	Cards []cardForm `json:"cards"`

	StripeKey string `json:"stripe_key"`

	Merchant string     `json:"merchant"`
	Currency string     `json:"currency"`
	Plans    []planForm `json:"plans"`

	ProxyMode string `json:"proxy_mode"`
	ProxyJSON string `json:"proxy_json"`

	AdminPassword string `json:"admin_password"`
}

// validate checks everything that has no reusable validator elsewhere. Card and
// catalog validation is delegated to the checkout package so the web path and
// the CLI path cannot drift apart.
func (f *credentialForm) validate() error {
	if err := checkCookieValue("auth_token", f.AuthToken); err != nil {
		return err
	}
	if err := checkCookieValue("ct0", f.CT0); err != nil {
		return err
	}
	if strings.TrimSpace(f.AdminPassword) != f.AdminPassword || len(f.AdminPassword) < adminMinLength {
		return errors.New("管理员密码至少需要 32 个字符，且首尾不能有空格")
	}
	// 在线收款不是站点的必需能力：兑换码那条链路完全不碰 Stripe，
	// 所以三项全空是合法的「只跑兑换码」模式。
	// 但只要有任意一项填了，就必须填齐 —— 否则站点会变成
	// 「看着能收款、其实下不了单」的坏状态。
	if !f.paymentsConfigured() {
		return nil
	}
	key := strings.TrimSpace(f.StripeKey)
	if key == "" {
		return errors.New("填了商户账号或套餐就必须同时填写 Stripe 公钥")
	}
	if !stripeKeyPattern.MatchString(key) {
		return errors.New("Stripe 公钥格式无效（须为 pk_live_ 或 pk_test_ 开头）")
	}
	if strings.TrimSpace(f.Merchant) == "" {
		return errors.New("填了 Stripe 公钥就必须同时填写商户账号")
	}
	if len(f.Plans) == 0 {
		return errors.New("填了 Stripe 公钥就必须同时填写至少一个套餐")
	}
	return nil
}

// paymentsConfigured reports whether the operator supplied a Stripe setup.
// 全空 = 只跑兑换码；付款开关由它决定，避免出现「开着付款却没有商品」的坏状态。
func (f *credentialForm) paymentsConfigured() bool {
	return strings.TrimSpace(f.StripeKey) != "" ||
		strings.TrimSpace(f.Merchant) != "" ||
		len(f.Plans) != 0
}

func checkCookieValue(name, value string) error {
	if value == "" {
		return errors.New(name + " 不能为空")
	}
	if strings.ContainsAny(value, "\r\n;") {
		return errors.New(name + " 不能包含换行或分号")
	}
	return nil
}

// cookiesRecord builds the vault payload read by checkout's X client.
func (f *credentialForm) cookiesRecord() ([]byte, error) {
	return json.Marshal(map[string]any{"cookies": []map[string]string{
		{"name": "auth_token", "value": f.AuthToken, "domain": ".x.com"},
		{"name": "ct0", "value": f.CT0, "domain": ".x.com"},
	}})
}

// apiAuthRecord builds the vault payload holding the Authorization header and
// the User-Agent sent with every GraphQL call.
func (f *credentialForm) apiAuthRecord() ([]byte, error) {
	authorization := strings.TrimSpace(f.Authorization)
	if authorization == "" {
		authorization = defaultBearer
	}
	if !bearerPattern.MatchString(authorization) {
		return nil, errors.New("Authorization 必须以 \"Bearer \" 开头")
	}
	userAgent := strings.TrimSpace(f.UserAgent)
	if userAgent == "" {
		userAgent = defaultUserAgent
	}
	return json.Marshal(map[string]string{"Authorization": authorization, "UserAgent": userAgent})
}

// cardsRecord normalises the submitted cards and marshals them for
// checkout.AddCardRecords.
func (f *credentialForm) cardsRecord() ([]byte, error) {
	return marshalCards(f.Cards)
}

// marshalCards cleans up operator input before it reaches the checkout package.
// The Luhn, expiry and completeness checks stay in checkout.AddCardRecords so
// the web form and the CLI wizard cannot drift apart; here we only normalise
// separators, month padding and country casing.
func marshalCards(cards []cardForm) ([]byte, error) {
	if len(cards) == 0 {
		return nil, errors.New("至少需要填写一张支付卡")
	}
	for i := range cards {
		c := &cards[i]
		c.Number = strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(c.Number))
		c.Month = strings.TrimSpace(c.Month)
		if len(c.Month) == 1 {
			c.Month = "0" + c.Month
		}
		c.Year = strings.TrimSpace(c.Year)
		c.CVC = strings.TrimSpace(c.CVC)
		c.Name = strings.TrimSpace(c.Name)
		c.Email = strings.TrimSpace(c.Email)
		c.Country = strings.ToUpper(strings.TrimSpace(c.Country))
	}
	return json.Marshal(cards)
}

// proxyRecord turns the operator's proxy choice into the stored sing-box
// configuration. Only "direct" and a pasted configuration are offered on the
// web form; the guided AnyTLS wizard stays CLI-only.
func (f *credentialForm) proxyRecord() ([]byte, error) {
	switch f.ProxyMode {
	case "", "direct":
		return []byte(`{"outbounds":[{"type":"direct","tag":"direct"}]}`), nil
	case "json":
		raw := []byte(strings.TrimSpace(f.ProxyJSON))
		if len(raw) == 0 {
			return nil, errors.New("请粘贴代理配置 JSON")
		}
		return raw, nil
	default:
		return nil, errors.New("代理方式无效")
	}
}

// catalogRecord builds and validates the catalog payload. Returns nil when the
// operator left the whole Stripe setup blank (redeem-code-only mode).
func (f *credentialForm) catalogRecord() ([]byte, error) {
	merchant := strings.TrimSpace(f.Merchant)
	currency := strings.ToLower(strings.TrimSpace(f.Currency))
	if !f.paymentsConfigured() {
		return nil, nil
	}
	if merchant == "" {
		return nil, errors.New("请填写 Stripe 商户账号")
	}
	if currency == "" {
		return nil, errors.New("请填写币种")
	}
	if len(f.Plans) < 1 || len(f.Plans) > 2 {
		return nil, errors.New("套餐数量必须是 1 或 2")
	}
	catalog := checkout.Catalog{Merchant: merchant, Currency: currency}
	for _, p := range f.Plans {
		minor, err := majorToMinor(p.Amount)
		if err != nil {
			return nil, err
		}
		catalog.Plans = append(catalog.Plans, checkout.CatalogPlan{
			Months:  p.Months,
			Amount:  minor,
			Product: strings.TrimSpace(p.Product),
		})
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		return nil, err
	}
	// Reuse the checkout validator so the web form and the CLI agree exactly.
	if _, err = checkout.ParseCatalog(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// majorToMinor converts a major-unit amount such as "300" or "4.99" into the
// minor units Stripe expects.
func majorToMinor(s string) (int, error) {
	s = strings.TrimSpace(s)
	if !regexp.MustCompile(`^[0-9]+(\.[0-9]{1,2})?$`).MatchString(s) {
		return 0, errors.New("金额必须是数字，最多两位小数")
	}
	whole, frac, _ := strings.Cut(s, ".")
	minor, err := strconv.Atoi(whole)
	if err != nil {
		return 0, errors.New("金额超出范围")
	}
	minor *= 100
	if frac != "" {
		if len(frac) == 1 {
			frac += "0"
		}
		cents, _ := strconv.Atoi(frac)
		minor += cents
	}
	return minor, nil
}
