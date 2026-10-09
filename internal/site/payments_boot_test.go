package site

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xgift/internal/vault"
)

// paymentBootFixture prepares a fully initialised site (admin password present,
// so not bootstrap) with the vault written by the caller.
func paymentBootFixture(t *testing.T, seed func(v *vault.Vault)) string {
	t.Helper()
	dir := t.TempDir()
	passwordPath := filepath.Join(dir, "password")
	adminPath := filepath.Join(dir, "admin")
	setupPath := filepath.Join(dir, "setup")
	for path, value := range map[string]string{
		passwordPath: strings.Repeat("v", 32),
		adminPath:    strings.Repeat("a", 40),
		setupPath:    strings.Repeat("s", 12),
	} {
		if err := os.WriteFile(path, []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	v, err := vault.Open(filepath.Join(dir, "vault.db"), passwordPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(v)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"XGIFT_DATA_DIR": dir, "XGIFT_PASSWORD_FILE": passwordPath,
		"XGIFT_ADMIN_PASSWORD_FILE": adminPath, "XGIFT_SETUP_PASSWORD_FILE": setupPath,
		"XGIFT_ORIGIN": "https://setup-test.invalid", "XGIFT_PAYMENTS_ENABLED": "true",
		"XGIFT_TURNSTILE_SITE_KEY": "", "XGIFT_TURNSTILE_SECRET_FILE": "",
	} {
		t.Setenv(key, value)
	}
	return dir
}

// A configured site whose payment card pool is still empty must boot: the
// operator finishes the settings from the admin page, and a service that exits
// leaves them with no way in at all. Only the public payment entry point is
// closed, and /healthz has to say so.
func TestRunBootsWithIncompletePaymentConfiguration(t *testing.T) {
	paymentBootFixture(t, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()
	t.Setenv("XGIFT_LISTEN", addr)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Run did not stop after context cancellation")
		}
	}()
	base := "http://" + addr
	deadline := time.Now().Add(20 * time.Second)
	var health struct {
		OK       bool `json:"ok"`
		Payments bool `json:"payments_enabled"`
	}
	for {
		select {
		case err := <-done:
			t.Fatalf("Run exited although payment configuration is merely incomplete: %v", err)
		default:
		}
		res, err := client.Get(base + "/healthz")
		if err == nil {
			if e := json.NewDecoder(res.Body).Decode(&health); e != nil {
				t.Fatalf("health body: %v", e)
			}
			res.Body.Close()
			if res.StatusCode != 200 || !health.OK {
				t.Fatalf("health: %d %+v", res.StatusCode, health)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("health readiness timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if health.Payments {
		t.Fatal("health reported payments_enabled=true with no payment card configured")
	}
	// The admin backend stays reachable so the missing cards can be added.
	req, _ := http.NewRequest("GET", base+"/admin", nil)
	req.SetBasicAuth("admin", strings.Repeat("a", 40))
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("admin login with incomplete payments: %d", res.StatusCode)
	}
	// Public redemption must be refused instead of starting an order.
	body, _ := json.Marshal(map[string]string{"code": "XG-" + strings.Repeat("A", 48), "username": "someone"})
	req, _ = http.NewRequest("POST", base+"/api/redeem", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://setup-test.invalid")
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("public redeem with incomplete payments: %d %s", res.StatusCode, data)
	}
}

// The admin gift path submits real payments, so a card pool that was never
// configured must keep it shut too — not just the public redemption form.
func TestAdminGiftRefusesWhilePaymentConfigIncomplete(t *testing.T) {
	dir := paymentBootFixture(t, func(v *vault.Vault) {
		if err := v.Put("catalog", []byte(`{"merchant":"acct_TESTMERCHANT","currency":"usd","plans":[{"months":3,"amount":30000,"product":"prod_TEST3MO"}]}`)); err != nil {
			t.Fatal(err)
		}
	})
	s := &server{vault: mustOpenVault(t, dir), payments: true, work: make(chan struct{}, 1), checks: make(chan struct{}, 4), limits: map[string]limit{}}
	s.paymentBlocked.Store(true)
	body, _ := json.Marshal(map[string]any{"username": "someone", "months": 3})
	req := httptest.NewRequest("POST", "/api/admin/gift", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.giftNow(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("admin gift with an incomplete card pool: %d %s", w.Code, w.Body.String())
	}
	if len(s.work) != 0 {
		t.Fatal("a rejected gift must not take the payment worker")
	}
}

func mustOpenVault(t *testing.T, dir string) *vault.Vault {
	t.Helper()
	v, err := vault.Open(filepath.Join(dir, "vault.db"), filepath.Join(dir, "password"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

// Corrupt data is the opposite of an unfinished configuration: the operator
// must find out instead of the site coming up and reading garbage records.
func TestRunStillFailsOnCorruptPaymentRecord(t *testing.T) {
	paymentBootFixture(t, nil)
	path := filepath.Join(os.Getenv("XGIFT_DATA_DIR"), "vault.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO secrets(name,payload) VALUES ('cards', ?)", []byte("broken")); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("XGIFT_LISTEN", "127.0.0.1:0")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = Run(ctx)
	if err == nil {
		t.Fatal("corrupt card record must not be treated as an unconfigured site")
	}
	if !strings.Contains(err.Error(), "invalid encrypted record") {
		t.Fatalf("corruption was masked: %v", err)
	}
}
