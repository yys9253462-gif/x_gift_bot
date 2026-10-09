package site

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupPasswordOnly(t *testing.T) {
	for _, tc := range []struct {
		name, setup, password string
		status                int
	}{
		{"success", "setup-secret", strings.Repeat("a", 32), 200},
		{"wrong-token", "wrong", strings.Repeat("a", 32), 401},
		{"short-password", "setup-secret", "short", 400},
		{"padded-password", "setup-secret", " " + strings.Repeat("a", 32), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{adminPath: filepath.Join(t.TempDir(), "admin-password"), setupHash: sha256.Sum256([]byte("setup-secret"))}
			s.bootstrap.Store(true)
			r := httptest.NewRequest("POST", "/api/setup/apply", strings.NewReader(`{"setup_password":"`+tc.setup+`","admin_password":"`+tc.password+`"}`))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.setupApply(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.status == 200 {
				// A nil vault proves setup has no business configuration dependency.
				data, err := os.ReadFile(s.adminPath)
				if err != nil || string(data) != tc.password+"\n" {
					t.Fatalf("password not saved: %v", err)
				}
				// Supply a fresh body for the duplicate request.
				r2 := httptest.NewRequest("POST", "/api/setup/apply", strings.NewReader(`{"setup_password":"setup-secret","admin_password":"`+tc.password+`"}`))
				r2.Header.Set("Content-Type", "application/json")
				w = httptest.NewRecorder()
				s.setupApply(w, r2)
				if w.Code != 409 {
					t.Fatalf("duplicate status=%d", w.Code)
				}
			}
		})
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestBootstrapGateClosesSiteUntilInitialised(t *testing.T) {
	var s server
	s.bootstrap.Store(true)
	h := s.bootstrapGate(okHandler())

	open := []string{"/setup", "/setup.js", "/api/setup/status", "/api/setup/apply", "/api/setup/restart", "/healthz", "/favicon.svg"}
	for _, path := range open {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d, want 200 while bootstrapping", path, rec.Code)
		}
	}

	// A fresh deploy must not be probeable for codes or admin data before it
	// has ever been configured.
	closed := []string{"/", "/app.js", "/admin", "/admin.js", "/api/redeem", "/api/admin/settings", "/api/security"}
	for _, path := range closed {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s returned %d, want 503 while bootstrapping", path, rec.Code)
		}
	}
}

func TestBootstrapGatePassesEverythingOnceConfigured(t *testing.T) {
	var s server
	s.bootstrap.Store(false)
	h := s.bootstrapGate(okHandler())
	for _, path := range []string{"/", "/setup", "/api/redeem", "/api/admin/settings"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s returned %d, want 200 after configuration", path, rec.Code)
		}
	}
}

func TestCheckSetupPassword(t *testing.T) {
	var s server
	// An unconfigured hash must fail even against an empty submission.
	if s.checkSetupPassword("") {
		t.Fatal("empty hash accepted a password")
	}
	s.stateMu.Lock()
	s.setupHash = sha256.Sum256([]byte("correct-horse"))
	s.stateMu.Unlock()
	if s.checkSetupPassword("wrong") {
		t.Fatal("wrong password accepted")
	}
	if !s.checkSetupPassword("correct-horse") {
		t.Fatal("the configured password was rejected")
	}
}

func TestWriteAdminPasswordRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin-password")
	s := &server{adminPath: path}
	first := strings.Repeat("a", adminMinLength)
	if err := s.writeAdminPassword(first); err != nil {
		t.Fatalf("first write: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions are %v, want 0600", info.Mode().Perm())
	}
	// A second attempt must not replace an already-configured admin password.
	if err := s.writeAdminPassword(strings.Repeat("b", adminMinLength)); !errors.Is(err, errAlreadyInitialised) {
		t.Fatalf("second write returned %v, want errAlreadyInitialised", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != first {
		t.Fatal("the stored admin password was overwritten")
	}
}

func TestMajorToMinor(t *testing.T) {
	cases := map[string]int{"300": 30000, "4.99": 499, "4.9": 490, "0.05": 5, "1200": 120000}
	for in, want := range cases {
		got, err := majorToMinor(in)
		if err != nil || got != want {
			t.Fatalf("majorToMinor(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "abc", "4.999", "-5", "1e3"} {
		if _, err := majorToMinor(in); err == nil {
			t.Fatalf("majorToMinor(%q) was accepted", in)
		}
	}
}

func validForm() credentialForm {
	return credentialForm{
		SetupPassword: strings.Repeat("s", setupMinLength),
		AuthToken:     "token-value",
		CT0:           "ct0-value",
		StripeKey:     "pk_live_abc123",
		Merchant:      "acct_1Ika5JA3KZ32dPo1",
		Currency:      "USD",
		Plans:         []planForm{{Months: 3, Amount: "300", Product: "prod_TJXJtpzqCpI36N"}},
		AdminPassword: strings.Repeat("a", adminMinLength),
	}
}

func TestCredentialFormValidateRejectsBadInput(t *testing.T) {
	base := validForm()
	if err := base.validate(); err != nil {
		t.Fatalf("a valid form was rejected: %v", err)
	}

	short := validForm()
	short.AdminPassword = strings.Repeat("a", adminMinLength-1)
	if err := short.validate(); err == nil {
		t.Fatal("a short admin password was accepted")
	}

	padded := validForm()
	padded.AdminPassword = " " + strings.Repeat("a", adminMinLength)
	if err := padded.validate(); err == nil {
		t.Fatal("an admin password with a leading space was accepted")
	}

	badKey := validForm()
	badKey.StripeKey = "sk_live_abc"
	if err := badKey.validate(); err == nil {
		t.Fatal("a non-publishable Stripe key was accepted")
	}

	// Stripe 账号激活往往要几天，测试 key 必须先能用，否则站点只能干等。
	testKey := validForm()
	testKey.StripeKey = "pk_test_abc123"
	if err := testKey.validate(); err != nil {
		t.Fatalf("a test-mode publishable key was rejected: %v", err)
	}

	typoKey := validForm()
	typoKey.StripeKey = "pk_live_" // 只有前缀、没有密钥本体
	if err := typoKey.validate(); err == nil {
		t.Fatal("a bare pk_live_ prefix with no key body was accepted")
	}

	emptyToken := validForm()
	emptyToken.AuthToken = ""
	if err := emptyToken.validate(); err == nil {
		t.Fatal("an empty auth_token was accepted")
	}

	// A cookie value carrying a semicolon would break the header encoding.
	smuggled := validForm()
	smuggled.CT0 = "abc; injected=1"
	if err := smuggled.validate(); err == nil {
		t.Fatal("a ct0 value containing a semicolon was accepted")
	}
}

func TestAPIHeadersFallBackToDefaults(t *testing.T) {
	var blank credentialForm
	raw, err := blank.apiAuthRecord()
	if err != nil {
		t.Fatalf("blank headers: %v", err)
	}
	if !strings.Contains(string(raw), defaultUserAgent) || !strings.Contains(string(raw), defaultBearer) {
		t.Fatalf("defaults were not applied: %s", raw)
	}

	custom := credentialForm{Authorization: "Bearer custom-token", UserAgent: "Test/1.0"}
	raw, err = custom.apiAuthRecord()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Bearer custom-token") || !strings.Contains(string(raw), "Test/1.0") {
		t.Fatalf("explicit headers were dropped: %s", raw)
	}

	bad := credentialForm{Authorization: "Basic abc"}
	if _, err = bad.apiAuthRecord(); err == nil {
		t.Fatal("a non-Bearer Authorization header was accepted")
	}
}

func TestMarshalCardsNormalisesInput(t *testing.T) {
	raw, err := marshalCards([]cardForm{{
		Number:  "4242 4242-4242 4242",
		Month:   "9",
		Year:    " 2029 ",
		CVC:     " 123 ",
		Name:    " Test Holder ",
		Email:   " holder@example.com ",
		Country: "us",
	}})
	if err != nil {
		t.Fatalf("marshalCards: %v", err)
	}
	got := string(raw)
	for _, want := range []string{`"number":"4242424242424242"`, `"exp_month":"09"`, `"exp_year":"2029"`, `"cvc":"123"`, `"billing_name":"Test Holder"`, `"billing_country":"US"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("normalised payload missing %s: %s", want, got)
		}
	}
	if _, err = marshalCards(nil); err == nil {
		t.Fatal("an empty card list was accepted")
	}
}

func TestProxyRecordDefaultsToDirect(t *testing.T) {
	var blank credentialForm
	raw, err := blank.proxyRecord()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"type":"direct"`) {
		t.Fatalf("default proxy is not direct: %s", raw)
	}

	pasted := credentialForm{ProxyMode: "json", ProxyJSON: "  {\"outbounds\":[]}  "}
	raw, err = pasted.proxyRecord()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"outbounds":[]}` {
		t.Fatalf("pasted proxy was not trimmed verbatim: %s", raw)
	}

	unknown := credentialForm{ProxyMode: "socks"}
	if _, err = unknown.proxyRecord(); err == nil {
		t.Fatal("an unknown proxy mode was accepted")
	}
	blankPaste := credentialForm{ProxyMode: "json"}
	if _, err = blankPaste.proxyRecord(); err == nil {
		t.Fatal("an empty pasted proxy config was accepted")
	}
}

func TestCatalogRecordValidatesThroughCheckout(t *testing.T) {
	good := credentialForm{
		Merchant: "acct_1Ika5JA3KZ32dPo1",
		Currency: "USD",
		Plans:    []planForm{{Months: 3, Amount: "300", Product: "prod_TJXJtpzqCpI36N"}},
	}
	raw, err := good.catalogRecord()
	if err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
	if !strings.Contains(string(raw), `"amount":30000`) {
		t.Fatalf("amount was not converted to minor units: %s", raw)
	}

	badMerchant := good
	badMerchant.Merchant = "not-an-account"
	if _, err = badMerchant.catalogRecord(); err == nil {
		t.Fatal("an invalid merchant was accepted")
	}

	twoPlans := good
	twoPlans.Plans = append(twoPlans.Plans, planForm{Months: 3, Amount: "600", Product: "prod_other"})
	if _, err = twoPlans.catalogRecord(); err == nil {
		t.Fatal("duplicate plan durations were accepted")
	}
}

// 只跑兑换码是完全合法的用法：Stripe 全空时整表应当被接受，
// 且不产出 catalog 记录（写了空记录比不写更难排查）。
func TestStripeFieldsAreOptionalForRedeemOnlySites(t *testing.T) {
	form := validForm()
	form.StripeKey = ""
	form.Merchant = ""
	form.Plans = nil

	if err := form.validate(); err != nil {
		t.Fatalf("a redeem-only form was rejected: %v", err)
	}
	if form.paymentsConfigured() {
		t.Fatal("a blank Stripe setup was reported as configured")
	}
	raw, err := form.catalogRecord()
	if err != nil {
		t.Fatalf("blank catalog should be skipped, got: %v", err)
	}
	if raw != nil {
		t.Fatalf("blank Stripe setup still produced a catalog record: %s", raw)
	}
}

// 但「配了一半」必须拦下：无论缺哪一项，上线后都会变成
// 看着能收款、实际下不了单的坏状态。
func TestPartialStripeSetupIsRejected(t *testing.T) {
	// 有公钥、无商户/套餐
	keyOnly := validForm()
	keyOnly.StripeKey = "pk_test_abc123"
	keyOnly.Merchant = ""
	keyOnly.Plans = nil
	if err := keyOnly.validate(); err == nil {
		t.Fatal("a Stripe key without merchant or plans was accepted")
	}
	if !keyOnly.paymentsConfigured() {
		t.Fatal("a half-filled Stripe setup should count as configured")
	}

	// 有套餐、无公钥（这一条曾因早退分支被漏放）
	plansOnly := validForm()
	plansOnly.StripeKey = ""
	plansOnly.Merchant = ""
	plansOnly.Plans = []planForm{{Months: 3, Amount: "300", Product: "prod_x"}}
	if !plansOnly.paymentsConfigured() {
		t.Fatal("plans without a key should still count as configured")
	}
	if err := plansOnly.validate(); err == nil {
		t.Fatal("plans without a Stripe key were accepted")
	}

	// 有商户、无公钥
	merchantOnly := validForm()
	merchantOnly.StripeKey = ""
	merchantOnly.Currency = "usd"
	merchantOnly.Plans = nil
	if err := merchantOnly.validate(); err == nil {
		t.Fatal("a merchant without a Stripe key was accepted")
	}
}
