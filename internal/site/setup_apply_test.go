package site

import (
	"crypto/sha256"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func applyRequest(t *testing.T, s *server, setup, password string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"setup_password": setup, "admin_password": password})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/setup/apply", strings.NewReader(string(body)))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.setupApply(w, r)
	return w
}

func statusBody(t *testing.T, s *server) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	s.setupStatus(w, httptest.NewRequest("GET", "/api/setup/status", nil))
	var body map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatalf("invalid JSON %q: %v", w.Body.String(), e)
	}
	return w.Code, body
}

func setupServer(t *testing.T) *server {
	t.Helper()
	s := &server{adminPath: filepath.Join(t.TempDir(), "admin-password"), setupHash: sha256.Sum256([]byte("setup-secret"))}
	s.bootstrap.Store(true)
	return s
}

// A response that never reached the browser leaves the operator staring at the
// form with no idea the password was stored. The page must be able to recover
// that state instead of retrying apply and bouncing off 409.
func TestSetupStatusReportsSavedAdmin(t *testing.T) {
	s := setupServer(t)
	code, body := statusBody(t, s)
	if code != 200 {
		t.Fatalf("status=%d", code)
	}
	if body["bootstrap"] != true || body["admin_saved"] != false || body["restart_required"] != false {
		t.Fatalf("fresh bootstrap status=%v", body)
	}
	if w := applyRequest(t, s, "setup-secret", strings.Repeat("a", 32)); w.Code != 200 {
		t.Fatalf("apply=%d %s", w.Code, w.Body.String())
	}
	code, body = statusBody(t, s)
	if code != 200 || body["admin_saved"] != true || body["restart_required"] != true || body["bootstrap"] != true {
		t.Fatalf("status after apply=%d %v", code, body)
	}
}

// Repeating the same submission must be answered as success, because it is
// exactly the same request that already took effect.
func TestSetupApplyIsIdempotentForTheSamePassword(t *testing.T) {
	s := setupServer(t)
	password := strings.Repeat("a", 32)
	if w := applyRequest(t, s, "setup-secret", password); w.Code != 200 {
		t.Fatalf("first apply=%d %s", w.Code, w.Body.String())
	}
	w := applyRequest(t, s, "setup-secret", password)
	if w.Code != 200 {
		t.Fatalf("idempotent retry=%d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	if body["ok"] != true || body["already_saved"] != true || body["restart_required"] != true {
		t.Fatalf("retry body=%v", body)
	}
	raw, err := os.ReadFile(s.adminPath)
	if err != nil || string(raw) != password+"\n" {
		t.Fatalf("admin password changed by the retry: %v %q", err, raw)
	}
}

// A different password is not the request that succeeded. Treating it as one
// would confirm success for a password that was never stored.
func TestSetupApplyRefusesConflictingRetry(t *testing.T) {
	s := setupServer(t)
	if w := applyRequest(t, s, "setup-secret", strings.Repeat("a", 32)); w.Code != 200 {
		t.Fatalf("first apply=%d %s", w.Code, w.Body.String())
	}
	if w := applyRequest(t, s, "setup-secret", strings.Repeat("b", 32)); w.Code != 409 {
		t.Fatalf("conflicting retry=%d %s", w.Code, w.Body.String())
	}
	// A wrong setup password must never confirm anything either.
	if w := applyRequest(t, s, "wrong", strings.Repeat("a", 32)); w.Code != 401 {
		t.Fatalf("wrong setup password=%d %s", w.Code, w.Body.String())
	}
	raw, err := os.ReadFile(s.adminPath)
	if err != nil || string(raw) != strings.Repeat("a", 32)+"\n" {
		t.Fatalf("admin password was overwritten: %v", err)
	}
}
