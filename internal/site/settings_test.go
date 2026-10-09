package site

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
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

// A fresh vault has no cookies record at all. That is an empty old value to
// merge into, not a read failure: the very first save of auth_token/ct0 must
// succeed or the operator can never configure the site.
func TestSaveCredentialsOnEmptyVault(t *testing.T) {
	s := checkFixture(t)
	auth := "auth-" + strings.Repeat("a", 24)
	ct0 := "ct0-" + strings.Repeat("b", 24)

	w := postCredentials(t, s, map[string]string{"auth_token": auth, "ct0": ct0})
	if w.Code != 200 {
		t.Fatalf("first save on an empty vault: status=%d body=%s", w.Code, w.Body.String())
	}
	storedAuth, storedCT0, err := s.currentCookiePair()
	if err != nil {
		t.Fatalf("currentCookiePair: %v", err)
	}
	if storedAuth != auth || storedCT0 != ct0 {
		t.Fatalf("cookies record=%q/%q want %q/%q", storedAuth, storedCT0, auth, ct0)
	}
	authorization, userAgent, err := s.readHeaders()
	if err != nil {
		t.Fatalf("readHeaders: %v", err)
	}
	if authorization == "" || userAgent == "" {
		t.Fatalf("api-auth record is incomplete: authorization=%q user_agent=%q", authorization, userAgent)
	}

	// Rotating one cookie must keep the stored partner value.
	w = postCredentials(t, s, map[string]string{"ct0": "ct0-" + strings.Repeat("c", 24)})
	if w.Code != 200 {
		t.Fatalf("cookie rotation: status=%d body=%s", w.Code, w.Body.String())
	}
	storedAuth, storedCT0, err = s.currentCookiePair()
	if err != nil {
		t.Fatalf("currentCookiePair after rotation: %v", err)
	}
	if storedAuth != auth || storedCT0 != "ct0-"+strings.Repeat("c", 24) {
		t.Fatalf("rotation lost a value: %q/%q", storedAuth, storedCT0)
	}
}

// An unreadable record is corruption, not an absent one. Reporting it as an
// empty value would silently overwrite whatever the operator had configured.
func TestSaveCredentialsRejectsCorruptStoredCookies(t *testing.T) {
	s := checkFixture(t)
	if err := s.vault.Put("cookies", []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	if err := s.vault.Put("api-auth", []byte("not-json")); err != nil {
		t.Fatal(err)
	}
	w := postCredentials(t, s, map[string]string{"auth_token": "auth-" + strings.Repeat("a", 24), "ct0": "ct0-" + strings.Repeat("b", 24)})
	if w.Code != 500 {
		t.Fatalf("corrupt stored record: status=%d body=%s", w.Code, w.Body.String())
	}
}

// Partial submissions must still be refused; only the missing record changed.
func TestSaveCredentialsStillRejectsIncompleteSubmission(t *testing.T) {
	s := checkFixture(t)
	if w := postCredentials(t, s, map[string]string{"auth_token": "auth-" + strings.Repeat("a", 24)}); w.Code != 400 {
		t.Fatalf("ct0 missing: status=%d body=%s", w.Code, w.Body.String())
	}
}
