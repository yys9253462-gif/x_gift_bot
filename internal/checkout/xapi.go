package checkout

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"xgift/internal/proxy"
	"xgift/internal/vault"
)

// X rebuilds its web client regularly, and every rebuild produces new GraphQL
// operation identifiers. Hard-coding them turns any upstream release into a
// full outage that can only be fixed by recompiling and redeploying, so the
// active table is read from the vault at client construction instead. A new
// client is built per operation, which makes a vault update take effect
// without restarting the service.
const (
	// DefaultOpPremiumGifting and friends are the identifiers compiled into
	// this build. They are a fallback, not a lock: ops show/set/reset changes
	// them at runtime from the encrypted vault.
	DefaultOpPremiumGifting      = "kn8hCE6bHstQV2MtfYDTKg"
	DefaultOpProductDetails      = "Se1Bp6zcNnuXYXRecV2qLA"
	DefaultOpOneTimeGiftMutation = "GqTVJ4S1526tLkxj69xIZw"
)

// GraphQLOpsKey is the vault record holding operator overrides.
const GraphQLOpsKey = "x-graphql-ops"

// Operation names, accepted by the CLI and reported by diagnostics.
const (
	OpPremiumGifting      = "premium_gifting_query"
	OpProductDetails      = "subscription_product_details_query"
	OpOneTimeGiftMutation = "one_time_purchase_gift_mutation"
)

// Operation identifiers are base64url fragments emitted by X's bundle.
var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)

// Ops overrides the built-in operation identifiers. Empty fields fall back to
// the compiled-in defaults, so a partial update is always safe.
type Ops struct {
	PremiumGifting      string `json:"premium_gifting_query,omitempty"`
	ProductDetails      string `json:"subscription_product_details_query,omitempty"`
	OneTimeGiftMutation string `json:"one_time_purchase_gift_mutation,omitempty"`
}

// ResolvedOps reports which identifiers are actually in force.
type ResolvedOps struct {
	PremiumGifting      string `json:"premium_gifting_query"`
	ProductDetails      string `json:"subscription_product_details_query"`
	OneTimeGiftMutation string `json:"one_time_purchase_gift_mutation"`
	Overridden          bool   `json:"overridden"`
}

// Validate rejects a malformed override instead of letting it reach X, where a
// bad identifier would surface as an opaque GraphQL error.
func (o Ops) Validate() error {
	for name, value := range map[string]string{
		OpPremiumGifting:      o.PremiumGifting,
		OpProductDetails:      o.ProductDetails,
		OpOneTimeGiftMutation: o.OneTimeGiftMutation,
	} {
		if value != "" && !operationIDPattern.MatchString(value) {
			return fmt.Errorf("operation %s has an invalid identifier: must be 8-64 characters of [A-Za-z0-9_-]", name)
		}
	}
	return nil
}

func (o Ops) resolve() ResolvedOps {
	r := ResolvedOps{
		PremiumGifting:      DefaultOpPremiumGifting,
		ProductDetails:      DefaultOpProductDetails,
		OneTimeGiftMutation: DefaultOpOneTimeGiftMutation,
	}
	if o.PremiumGifting != "" {
		r.PremiumGifting, r.Overridden = o.PremiumGifting, true
	}
	if o.ProductDetails != "" {
		r.ProductDetails, r.Overridden = o.ProductDetails, true
	}
	if o.OneTimeGiftMutation != "" {
		r.OneTimeGiftMutation, r.Overridden = o.OneTimeGiftMutation, true
	}
	return r
}

// LoadOps reads the override table. A missing record is not an error: it simply
// means the built-in identifiers are in force. A corrupt record is an error, so
// a damaged vault can never silently downgrade to stale defaults.
func LoadOps(v *vault.Vault) (Ops, error) {
	raw, err := v.Get(GraphQLOpsKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Ops{}, nil
		}
		return Ops{}, fmt.Errorf("operation table is unavailable: %w", err)
	}
	defer clear(raw)
	var o Ops
	if err := json.Unmarshal(raw, &o); err != nil {
		return Ops{}, errors.New("operation table is corrupted and cannot be decoded")
	}
	if err := o.Validate(); err != nil {
		return Ops{}, err
	}
	return o, nil
}

// SaveOps replaces the override table after validating it.
func SaveOps(v *vault.Vault, o Ops) error {
	if err := o.Validate(); err != nil {
		return err
	}
	// Normalise "no overrides" to a stored empty object so Reset is explicit.
	plain, err := json.Marshal(o)
	if err != nil {
		return err
	}
	defer clear(plain)
	return v.Put(GraphQLOpsKey, plain)
}

// CurrentOps resolves the identifiers in force without contacting X.
func CurrentOps(v *vault.Vault) (ResolvedOps, error) {
	o, err := LoadOps(v)
	if err != nil {
		return ResolvedOps{}, err
	}
	return o.resolve(), nil
}

type Plan struct {
	Months, Minor int
	ProductID     string
	Merchant      string
	Currency      string
}

var ErrNotEligible = errors.New("recipient cannot receive Premium gifts")
var ErrUserNotFound = errors.New("recipient was not found")
var ErrXReadFailure = errors.New("X account or price query failed")

// ErrOperationRejected marks a GraphQL error envelope. X uses one envelope for
// both an operation identifier it no longer recognises and account-level
// refusals such as rate limiting, so this signal alone does not prove which one
// happened. It is still the one failure that an operator can act on without
// recompiling, so it is surfaced separately from transport and HTTP faults.
var ErrOperationRejected = errors.New("X returned a GraphQL error envelope for this operation")

// ErrGiftNotAuthorised means X accepted the request shape but refused it for
// account reasons — most often code 37 / AuthorizationError, "Current user is
// not eligible to gift". Typical causes on X's side: the sending account has no
// verified phone number, is too new or inactive, or is itself restricted.
// It is deliberately separate from ErrOperationRejected: fixing it needs an
// account change, not a new GraphQL operation identifier.
var ErrGiftNotAuthorised = errors.New("X says this account is not eligible to gift")

// Eligibility is read-only: it neither creates a checkout nor submits a payment.
func Eligibility(ctx context.Context, v *vault.Vault, user string, port int) (string, error) {
	c, err := newXClient(v, port)
	if err != nil {
		return "", err
	}
	defer c.close()
	return c.recipient(ctx, user)
}

func (p Plan) Name() string { return fmt.Sprintf("Premium Gift - %d months", p.Months) }

type xClient struct {
	vault        *vault.Vault
	http         *http.Client
	regionalHTTP *http.Client
	headers      http.Header
	ops          ResolvedOps
	// closeRegional 释放为区域询价临时启动的付款出口（若启动过）。
	closeRegional func()
	// probe 为 true 时，本次创建只为向 X 询问判据：不写创建审计、
	// 不占用任何重试预算，也不允许走到付款。探测失败不得影响真实下单。
	probe bool
}

// withRegionalExit points the regional (pricing / checkout) calls at the
// configured payment outbound instead of the account-check exit.
//
// X prices a gift by the country of the requesting exit, so pricing and
// checkout creation MUST leave through the same node the payment will use.
// The account-check exit (site's "proxy" record) is typically direct, and
// using it here silently prices the order in the *server's* country — which
// then fails the amount check inside quote() with a confusing
// "price is not exactly the allowed one-time amount".
func (c *xClient) withRegionalExit(ctx context.Context, recipient string) error {
	raw, err := c.vault.Get("payment-outbounds")
	if errors.Is(err, sql.ErrNoRows) {
		// 没有配置付款出口：保持直连，与结算路径的语义一致。
		return nil
	}
	if err != nil {
		return err
	}
	defer clear(raw)
	nodes, err := proxy.ParseOutboundPool(raw)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}
	// 优先复用已绑定给这个接收方的出口，保证询价与结算同一条线路；
	// 没有绑定记录时退回第一个可用节点。
	var node json.RawMessage
	if route, rerr := selectPaymentRoute(c.vault, recipient); rerr == nil && route != nil && len(route.Outbound) > 0 {
		node = route.Outbound
	} else {
		picked, perr := chooseAvailablePaymentNode(c.vault, nodes)
		if perr != nil {
			return perr
		}
		node = picked
	}
	client, closeFn, err := proxy.OpenOutbound(ctx, node)
	if err != nil {
		return fmt.Errorf("cannot start payment exit for regional pricing: %w", err)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("unexpected X API redirect")
	}
	c.regionalHTTP = client
	c.closeRegional = closeFn
	return nil
}

// The three accessors never return an empty identifier. A client built without
// an operation table — including one constructed directly in tests — must still
// send a usable value rather than an empty path segment to X.

func (c *xClient) premiumGiftingOp() string {
	if c.ops.PremiumGifting != "" {
		return c.ops.PremiumGifting
	}
	return DefaultOpPremiumGifting
}

func (c *xClient) productDetailsOp() string {
	if c.ops.ProductDetails != "" {
		return c.ops.ProductDetails
	}
	return DefaultOpProductDetails
}

func (c *xClient) oneTimeGiftOp() string {
	if c.ops.OneTimeGiftMutation != "" {
		return c.ops.OneTimeGiftMutation
	}
	return DefaultOpOneTimeGiftMutation
}

func newXClient(v *vault.Vault, port int) (*xClient, error) {
	// Resolve the operation table before anything else so an invalid override
	// fails loudly at construction instead of on a live payment path.
	ops, e := LoadOps(v)
	if e != nil {
		return nil, e
	}
	raw, e := v.Get("api-auth")
	if e != nil {
		return nil, errors.New("X API authentication metadata is missing or unreadable")
	}
	defer clear(raw)
	var auth struct{ Authorization, UserAgent string }
	if json.Unmarshal(raw, &auth) != nil || !strings.HasPrefix(auth.Authorization, "Bearer ") {
		return nil, errors.New("invalid X API authentication metadata")
	}
	raw, e = v.Get("cookies")
	if e != nil {
		return nil, e
	}
	defer clear(raw)
	var state struct {
		Cookies []struct{ Name, Value, Domain string }
	}
	if e = json.Unmarshal(raw, &state); e != nil {
		return nil, e
	}
	h := http.Header{"Authorization": {auth.Authorization}, "User-Agent": {auth.UserAgent}, "Content-Type": {"application/json"}, "Origin": {"https://x.com"}, "X-Twitter-Auth-Type": {"OAuth2Session"}, "X-Twitter-Active-User": {"yes"}, "X-Twitter-Client-Language": {"en"}}
	found := map[string]bool{}
	for _, c := range state.Cookies {
		if c.Domain != ".x.com" && c.Domain != "x.com" {
			return nil, errors.New("unexpected cookie domain")
		}
		if c.Name != "auth_token" && c.Name != "ct0" {
			return nil, errors.New("unexpected cookie name")
		}
		if found[c.Name] || c.Value == "" || strings.ContainsAny(c.Value, "\r\n;") {
			return nil, errors.New("invalid X cookie")
		}
		found[c.Name] = true
		h.Add("Cookie", c.Name+"="+c.Value)
		if c.Name == "ct0" {
			h.Set("X-Csrf-Token", c.Value)
		}
	}
	if !found["ct0"] || !found["auth_token"] {
		return nil, errors.New("required X cookies missing")
	}
	// Account checks connect directly. Regional pricing and checkout creation
	// must share the configured exit because X prices depend on its country.
	// DisableKeepAlives：这两个 client 随 xClient 存活，但每个 xClient 只服务
	// 一次下单/询价就被 close，保留 keep-alive 只会多占连接（同类 bug 实测
	// 5760 条/天，约 12 天打满 ulimit 65535）。
	newClient := func(proxy func(*http.Request) (*url.URL, error)) *http.Client {
		return &http.Client{Transport: &http.Transport{Proxy: proxy, DisableKeepAlives: true, TLSHandshakeTimeout: 15 * time.Second}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected X API redirect") }}
	}
	p, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	return &xClient{http: newClient(nil), regionalHTTP: newClient(http.ProxyURL(p)), headers: h, vault: v, ops: ops.resolve()}, nil
}
func (c *xClient) close() {
	c.http.CloseIdleConnections()
	if c.regionalHTTP != nil {
		c.regionalHTTP.CloseIdleConnections()
	}
	if c.closeRegional != nil {
		c.closeRegional()
		c.closeRegional = nil
	}
}
func (c *xClient) call(ctx context.Context, user, name, id string, variables any, mutation bool, out any) error {
	if mutation {
		return c.callOnce(ctx, user, name, id, variables, true, out)
	}
	return retrySafe(ctx, func() error { return c.callOnce(ctx, user, name, id, variables, false, out) })
}
func (c *xClient) callOnce(ctx context.Context, user, name, id string, variables any, mutation bool, out any) (callErr error) {
	// A fixed-operation audit is encrypted before sending, then completed even on
	// transport/body failures. Never store request headers or cookies here.
	var audit struct {
		Operation   string `json:"operation"`
		Variables   any    `json:"variables"`
		StartedAt   int64  `json:"started_at"`
		FinishedAt  int64  `json:"finished_at,omitempty"`
		Phase       string `json:"phase"`
		HTTP        int    `json:"http_status,omitempty"`
		Body        string `json:"body,omitempty"`
		Cause       string `json:"cause,omitempty"`
		Failure     string `json:"failure,omitempty"`
		ContentType string `json:"content_type,omitempty"`
		RequestID   string `json:"request_id,omitempty"`
		EdgeID      string `json:"edge_id,omitempty"`
	}
	audit.Operation, audit.Variables, audit.StartedAt, audit.Phase = name, variables, time.Now().Unix(), "request_pending"
	// A probe writes no audit record on purpose: an operator asking X "why was
	// this refused" must not consume the creation retry budget of the order
	// they are trying to place. The budget exists to stop a real order from
	// being recreated forever; a diagnostic never creates anything.
	probe := c.probe
	if !mutation {
		defer func() {
			if callErr == nil {
				return
			}
			audit.FinishedAt, audit.Failure = time.Now().Unix(), callErr.Error()
			if len(audit.Body) > 32<<10 {
				audit.Body = audit.Body[:32<<10]
			}
			b, err := json.Marshal(audit)
			defer clear(b)
			if err != nil || c.vault.Put(fmt.Sprintf("x-read-failure:%s:%s:%d", user, name, time.Now().UnixNano()), b) != nil {
				callErr = fmt.Errorf("%w: could not preserve failure details", ErrXReadFailure)
				return
			}
			// A GraphQL error envelope is kept distinct from an ordinary read
			// failure on purpose. Collapsing the two would leave an operator no
			// way to tell "our identifier went stale, update the config" from
			// "X was unhappy", which is the whole reason the operation table is
			// hot-updatable in the first place.
			if errors.Is(callErr, ErrOperationRejected) {
				callErr = fmt.Errorf("%w: %w", ErrOperationRejected, callErr)
			} else {
				callErr = fmt.Errorf("%w: %w", ErrXReadFailure, callErr)
			}
		}()
	}
	if mutation && !probe {
		key := fmt.Sprintf("x-create-attempt:%s:%d", user, time.Now().UnixNano())
		persist := func() error {
			b, e := json.Marshal(audit)
			if e != nil {
				return e
			}
			defer clear(b)
			return c.vault.Put(key, b)
		}
		if e := persist(); e != nil {
			return errors.New("could not preserve X creation attempt before sending")
		}
		defer func() {
			audit.FinishedAt = time.Now().Unix()
			if callErr != nil {
				audit.Failure = callErr.Error()
			}
			if e := persist(); e != nil {
				callErr = errors.New("could not preserve X creation outcome; automatic recovery blocked")
			}
		}()
	}
	target := "https://x.com/i/api/graphql/" + id + "/" + name
	method := "GET"
	var body []byte
	if mutation {
		method = "POST"
		merr := error(nil)
		body, merr = json.Marshal(map[string]any{"variables": variables, "queryId": id})
		if merr != nil {
			return fmt.Errorf("cannot encode X mutation body: %w", merr)
		}
	} else {
		b, merr := json.Marshal(variables)
		if merr != nil {
			// 不要静默发出空 variables —— 那会让 X 回一个更费解的错误。
			return fmt.Errorf("cannot encode X query variables: %w", merr)
		}
		q := url.Values{"variables": {string(b)}}
		if name == "useSubscriptionProductDetailsByRestIdQuery" {
			q.Set("features", `{"subscriptions_marketing_page_fetch_promotions":true}`)
		}
		target += "?" + q.Encode()
	}
	req, e := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if e != nil {
		return e
	}
	req.Header = c.headers.Clone()
	req.Header.Set("Referer", "https://x.com/"+user+"/gift-premium")
	client := c.http
	if mutation || name == "useSubscriptionProductDetailsByRestIdQuery" {
		if c.regionalHTTP == nil {
			return errors.New("X regional checkout proxy is unavailable")
		}
		client = c.regionalHTTP
	}
	res, e := client.Do(req)
	if e != nil {
		audit.Phase, audit.Cause = "transport_failed", e.Error()
		return temporary(fmt.Errorf("X %s request failed", name))
	}
	audit.HTTP, audit.Phase = res.StatusCode, "response_received"
	audit.ContentType, audit.RequestID, audit.EdgeID = res.Header.Get("Content-Type"), res.Header.Get("X-Request-ID"), res.Header.Get("CF-Ray")
	defer res.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	defer clear(raw)
	if len(raw) > 2<<20 {
		audit.Body = string(raw[:2<<20])
		audit.Phase = "response_too_large"
		return errors.New("X response exceeded size limit")
	}
	audit.Body = string(raw)
	if e != nil {
		audit.Phase, audit.Cause = "response_read_failed", e.Error()
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			return xHTTPFailure(errors.New("X error response could not be read"), res.StatusCode, res.Header.Get("Retry-After"), mutation)
		}
		return temporary(errors.New("X response could not be read"))
	}
	audit.Phase = "response_validation"
	if res.StatusCode != 200 {
		return xHTTPFailure(fmt.Errorf("X %s returned HTTP %d; no payment attempted", name, res.StatusCode), res.StatusCode, res.Header.Get("Retry-After"), mutation)
	}
	var envelope struct {
		Errors []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Name    string `json:"name"`
			Kind    string `json:"kind"`
		} `json:"errors"`
	}
	if e = json.Unmarshal(raw, &envelope); e != nil {
		return errors.New("X returned non-JSON data")
	}
	if len(envelope.Errors) > 0 {
		first := envelope.Errors[0]
		// X answers an unknown or renamed operation with HTTP 200 and a GraphQL
		// error envelope, so this is the signal that separates "upstream
		// rebuilt and our identifier may be stale" from every other failure.
		//
		// But an account-level refusal arrives in the same envelope shape: a
		// gifting account that fails X's requirements returns
		//   code 37 / AuthorizationError / "Current user is not eligible to gift"
		// Collapsing both into "identifier may be stale" sends the operator to
		// re-scrape operation IDs for a problem that is actually account state,
		// so keep them distinguishable.
		if strings.Contains(strings.ToLower(first.Message+first.Name+first.Kind), "authorization") ||
			first.Code == 37 {
			return fmt.Errorf("%w: X refused %s for this account: %s [code %d]",
				ErrGiftNotAuthorised, name, first.Message, first.Code)
		}
		return fmt.Errorf("%w: X %s rejected the operation: %s [code %d]",
			ErrOperationRejected, name, first.Message, first.Code)
	}
	if e = json.Unmarshal(raw, out); e != nil {
		return e
	}
	audit.Phase = "response_decoded"
	return nil
}

// A 403 on a read-only X query can be transient. Retry within the existing
// bounded budget and Retry-After rules, without changing credentials or proxy.
// Never apply this exception to checkout creation or Stripe confirmation.
func xHTTPFailure(err error, status int, retryAfter string, mutation bool) error {
	if status == http.StatusForbidden && !mutation {
		return httpFailure(err, http.StatusTooManyRequests, retryAfter)
	}
	return httpFailure(err, status, retryAfter)
}

func (c *xClient) recipient(ctx context.Context, user string) (string, error) {
	return c.identity(ctx, user, true)
}
func (c *xClient) identity(ctx context.Context, user string, requireEligible bool) (string, error) {
	var r struct {
		Data struct {
			User struct {
				Result struct {
					ID       string `json:"rest_id"`
					Eligible bool   `json:"premium_gifting_eligible"`
					Core     struct {
						Screen string `json:"screen_name"`
					} `json:"core"`
				} `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if e := c.call(ctx, user, "PremiumGiftingQuery", c.premiumGiftingOp(), map[string]string{"screenName": user}, false, &r); e != nil {
		return "", e
	}
	u := r.Data.User.Result
	if u.ID == "" {
		return "", ErrUserNotFound
	}
	if !strings.EqualFold(u.Core.Screen, user) {
		return "", errors.New("recipient identity or gift eligibility could not be verified")
	}
	if requireEligible && !u.Eligible {
		return "", ErrNotEligible
	}
	return u.ID, nil
}

// QuoteResult is what X actually returned for a product, in the region implied
// by the current payment exit. It exists so an operator can see the real price
// instead of only "matches / does not match".
type QuoteResult struct {
	ProductID string
	Currency  string
	Amount    int64 // amount_local_micro as returned by X
	OneTime   bool
}

// ProbeQuote is read-only: it asks X for the product price through the
// configured payment exit and returns what came back. It creates no checkout,
// submits no payment and touches no recipient — but it does use the same exit
// and credentials as a real order, so the region it reports is the region a
// real order would be priced in.
func ProbeQuote(ctx context.Context, v *vault.Vault, user string, p Plan, port int, recipient string) (QuoteResult, error) {
	c, err := newXClient(v, port)
	if err != nil {
		return QuoteResult{}, err
	}
	defer c.close()
	// 与下单路径一致：探针也走付款出口，否则探出来的价格和真实下单不同。
	if err = c.withRegionalExit(ctx, recipient); err != nil {
		return QuoteResult{}, err
	}
	return c.probeQuote(ctx, user, p)
}

func (c *xClient) probeQuote(ctx context.Context, user string, p Plan) (QuoteResult, error) {
	var r struct {
		Data struct {
			Product struct {
				ID     string `json:"rest_id"`
				Prices []struct {
					Amount   int64  `json:"amount_local_micro"`
					Currency string `json:"currency_code"`
					Type     string `json:"price_type"`
				} `json:"prices"`
			} `json:"web_subscription_product_details_by_rest_id"`
		} `json:"data"`
	}
	if e := c.call(ctx, user, "useSubscriptionProductDetailsByRestIdQuery", c.productDetailsOp(), map[string]string{"stripeId": p.ProductID}, false, &r); e != nil {
		return QuoteResult{}, e
	}
	product := r.Data.Product
	if product.ID != p.ProductID || len(product.Prices) != 1 {
		return QuoteResult{}, errors.New("unexpected X product or price list")
	}
	price := product.Prices[0]
	return QuoteResult{
		ProductID: product.ID,
		Currency:  price.Currency,
		Amount:    price.Amount,
		OneTime:   price.Type == "OneTime",
	}, nil
}

func (c *xClient) quote(ctx context.Context, user string, p Plan) error {
	var r struct {
		Data struct {
			Product struct {
				ID     string `json:"rest_id"`
				Prices []struct {
					Amount   int64  `json:"amount_local_micro"`
					Currency string `json:"currency_code"`
					Type     string `json:"price_type"`
				} `json:"prices"`
			} `json:"web_subscription_product_details_by_rest_id"`
		} `json:"data"`
	}
	if e := c.call(ctx, user, "useSubscriptionProductDetailsByRestIdQuery", c.productDetailsOp(), map[string]string{"stripeId": p.ProductID}, false, &r); e != nil {
		return e
	}
	product := r.Data.Product
	if product.ID != p.ProductID || len(product.Prices) != 1 {
		return errors.New("unexpected X product or price list")
	}
	price := product.Prices[0]
	if price.Type != "OneTime" || !strings.EqualFold(price.Currency, p.Currency) || price.Amount != int64(p.Minor)*10000 {
		return errors.New("X price is not exactly the allowed one-time amount")
	}
	return nil
}
func (c *xClient) create(ctx context.Context, user, recipient string, p Plan) (string, string, error) {
	var r struct {
		Data struct {
			Gift struct {
				ID     string `json:"session_id"`
				URL    string `json:"session_url"`
				Status string `json:"session_status"`
			} `json:"onetimepurchase_gift"`
		} `json:"data"`
	}
	variables := map[string]string{"cancel_url": "https://x.com/" + user + "/gift-premium", "success_url": "https://x.com/" + user + "/gift-premium/success", "external_product_id": p.ProductID, "gift_recipient": recipient}
	if e := c.call(ctx, user, "useOneTimePurchaseGiftMutation", c.oneTimeGiftOp(), variables, true, &r); e != nil {
		return "", "", e
	}
	s := r.Data.Gift
	if s.Status != "Unpaid" {
		return "", "", errors.New("X checkout status is not Unpaid; payment was not submitted")
	}
	if !sessionPattern.MatchString(s.ID) {
		return "", "", errors.New("X checkout session ID is missing or not a live session; payment was not submitted")
	}
	if !sessionURL(s.URL, s.ID) {
		return "", "", errors.New("X checkout URL is unsupported or does not match its session; payment was not submitted")
	}
	return s.ID, s.URL, nil
}

// createProbe 与 create 发出完全相同的请求，唯一区别是不写创建审计、
// 不占用重试预算。它只用来向 X 询问判据，返回的会话一定是 Unpaid，
// 调用方不得据此付款。
//
// 存在的理由：premium_gifting_eligible 只是一个布尔值，为 false 时生产链路
// 在 identity() 处就返回 ErrNotEligible，运营看不到 X 的原始错误码。
// 2026-10-08 因此误判过：四个不同接收方都返回 code 37，真实原因是发送账号
// 被限，而界面却显示"该账号无法接收 Premium 赠送"。
func (c *xClient) createProbe(ctx context.Context, user, recipient string, p Plan) (string, string, error) {
	prev := c.probe
	c.probe = true
	defer func() { c.probe = prev }()
	return c.create(ctx, user, recipient, p)
}
