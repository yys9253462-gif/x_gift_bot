package site

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"xgift/internal/checkout"
)

func postCredentials(t *testing.T, s *server, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/admin/settings/credentials", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.saveCredentials(w, r)
	return w
}

func postRemoveCredentials(t *testing.T, s *server, id string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/admin/settings/credentials/remove", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.removeCredentials(w, r)
	return w
}

// A fresh vault holds no account at all. The first save must materialise
// account #1, mirror it into the legacy cookies record for binary rollback,
// and fill the shared request headers with the documented defaults.
func TestSaveCredentialsAddsFirstAccount(t *testing.T) {
	s := checkFixture(t)
	auth := "auth-" + strings.Repeat("a", 24)
	ct0 := "ct0-" + strings.Repeat("b", 24)

	w := postCredentials(t, s, map[string]string{"auth_token": auth, "ct0": ct0, "label": "主号"})
	if w.Code != 200 {
		t.Fatalf("first save on an empty vault: status=%d body=%s", w.Code, w.Body.String())
	}
	accounts, err := checkout.LoadXAccounts(s.vault)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("pool should hold one account: %+v %v", accounts, err)
	}
	if accounts[0].AuthToken != auth || accounts[0].CT0 != ct0 || accounts[0].Label != "主号" {
		t.Fatalf("account parsed wrong: %+v", accounts[0])
	}
	raw, err := s.vault.Get("cookies")
	if err != nil {
		t.Fatalf("legacy mirror missing: %v", err)
	}
	if !strings.Contains(string(raw), auth) {
		t.Fatalf("legacy mirror does not hold the account: %s", raw)
	}
	clear(raw)
	authorization, userAgent, err := s.readHeaders()
	if err != nil {
		t.Fatalf("readHeaders: %v", err)
	}
	if authorization == "" || userAgent == "" {
		t.Fatalf("api-auth record is incomplete: authorization=%q user_agent=%q", authorization, userAgent)
	}
}

// The pool is the point of the feature: a second account joins without
// disturbing the first, the same auth token refreshes its pair in place, and
// removal drops exactly one account.
func TestSaveCredentialsPoolAddRefreshRemove(t *testing.T) {
	s := checkFixture(t)
	authA := "auth-" + strings.Repeat("a", 24)
	authB := "auth-" + strings.Repeat("d", 24)
	if w := postCredentials(t, s, map[string]string{"auth_token": authA, "ct0": "ct0-" + strings.Repeat("b", 24), "label": "一号"}); w.Code != 200 {
		t.Fatalf("first account: %d %s", w.Code, w.Body.String())
	}
	if w := postCredentials(t, s, map[string]string{"auth_token": authB, "ct0": "ct0-" + strings.Repeat("c", 24), "label": "二号"}); w.Code != 200 {
		t.Fatalf("second account: %d %s", w.Code, w.Body.String())
	}
	accounts, _ := checkout.LoadXAccounts(s.vault)
	if len(accounts) != 2 {
		t.Fatalf("pool should hold two accounts: %+v", accounts)
	}
	// Same auth token refreshes the pair instead of appending a third.
	if w := postCredentials(t, s, map[string]string{"auth_token": authA, "ct0": "ct0-" + strings.Repeat("e", 24)}); w.Code != 200 {
		t.Fatalf("refresh: %d %s", w.Code, w.Body.String())
	}
	accounts, _ = checkout.LoadXAccounts(s.vault)
	if len(accounts) != 2 || accounts[0].CT0 != "ct0-"+strings.Repeat("e", 24) || accounts[0].Label != "一号" {
		t.Fatalf("refresh did not update in place: %+v", accounts[0])
	}
	// Removal drops exactly the addressed account.
	if w := postRemoveCredentials(t, s, accounts[0].ID); w.Code != 200 {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	accounts, _ = checkout.LoadXAccounts(s.vault)
	if len(accounts) != 1 || accounts[0].AuthToken != authB {
		t.Fatalf("wrong account removed: %+v", accounts)
	}
}

// An unreadable cookies record is corruption, not an absent one. The save
// must fail loudly instead of silently replacing whatever was configured.
func TestSaveCredentialsRejectsCorruptStoredCookies(t *testing.T) {
	s := checkFixture(t)
	if err := s.vault.Put("cookies", []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	w := postCredentials(t, s, map[string]string{"auth_token": "auth-" + strings.Repeat("a", 24), "ct0": "ct0-" + strings.Repeat("b", 24)})
	if w.Code != 400 {
		t.Fatalf("corrupt stored record: status=%d body=%s", w.Code, w.Body.String())
	}
}

// Partial submissions are refused: an account needs the full cookie pair.
func TestSaveCredentialsStillRejectsIncompleteSubmission(t *testing.T) {
	s := checkFixture(t)
	if w := postCredentials(t, s, map[string]string{"auth_token": "auth-" + strings.Repeat("a", 24)}); w.Code != 400 {
		t.Fatalf("ct0 missing: status=%d body=%s", w.Code, w.Body.String())
	}
	if w := postCredentials(t, s, map[string]string{"ct0": "ct0-" + strings.Repeat("b", 24)}); w.Code != 400 {
		t.Fatalf("auth_token missing: status=%d body=%s", w.Code, w.Body.String())
	}
}

// The admin settings snapshot reports the pool with per-account status and
// never a full secret.
func TestAdminSettingsReportsAccountPool(t *testing.T) {
	s := checkFixture(t)
	if w := postCredentials(t, s, map[string]string{"auth_token": "auth-" + strings.Repeat("a", 24), "ct0": "ct0-" + strings.Repeat("b", 24), "label": "一号"}); w.Code != 200 {
		t.Fatalf("seed account: %d %s", w.Code, w.Body.String())
	}
	r := httptest.NewRequest("GET", "/api/admin/settings", nil)
	w := httptest.NewRecorder()
	s.adminSettings(w, r)
	if w.Code != 200 {
		t.Fatalf("settings: status=%d body=%s", w.Code, w.Body.String())
	}
	var view settingsView
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Credentials.Accounts) != 1 {
		t.Fatalf("settings should report one account: %+v", view.Credentials.Accounts)
	}
	if view.Credentials.Accounts[0].Label != "一号" || !view.Credentials.Accounts[0].HasCT0 {
		t.Fatalf("account summary wrong: %+v", view.Credentials.Accounts[0])
	}
	if view.Credentials.Accounts[0].Tail != "…"+strings.Repeat("a", 4) {
		t.Fatalf("tail should be redacted: %q", view.Credentials.Accounts[0].Tail)
	}
}
