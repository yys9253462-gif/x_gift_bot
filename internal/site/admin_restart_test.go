package site

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newRestart builds the request and recorder together as (rec, req). Handing
// back the recorder alone and rebuilding the request from w.Result() silently
// drops the body, which made an earlier version of these tests assert nothing.
func newRestart(body, contentType, auth string) (*httptest.ResponseRecorder, *http.Request) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/restart", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return rec, req
}

// restartServer returns a server whose admin password is synthetic-admin.
// exitProcess is stubbed out so no test can terminate the test binary: any test
// that reaches the exit path records it instead, and can opt into observing it.
func restartServer() *server {
	s := &server{adminHash: sha256.Sum256([]byte("synthetic-admin"))}
	s.exitProcess = func(int) {}
	return s
}

func basicAuth(password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+password))
}

// The restart endpoint is destructive, so the auth boundary is the part that
// matters most: an unauthenticated caller and a wrong password must both be
// refused before anything is scheduled.
func TestAdminRestartRejectsUnauthenticated(t *testing.T) {
	s := restartServer()

	// No Authorization header at all.
	rec, req := newRestart(`{}`, "application/json", "")
	s.admin(s.adminRestart)(rec, req)
	if rec.Code != 401 {
		t.Fatalf("unauthenticated restart allowed: %d %s", rec.Code, rec.Body.String())
	}

	// A caller that never learned the password must not get to restart.
	rec, req = newRestart(`{}`, "application/json", basicAuth("wrong-password"))
	s.admin(s.adminRestart)(rec, req)
	if rec.Code != 401 {
		t.Fatalf("wrong password accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// A non-admin basic username must not pass even with the right password.
func TestAdminRestartRejectsNonAdminUser(t *testing.T) {
	s := restartServer()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/admin/restart", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("someoneelse:synthetic-admin")))
	s.admin(s.adminRestart)(rec, req)
	if rec.Code != 401 {
		t.Fatalf("non-admin username accepted: %d %s", rec.Code, rec.Body.String())
	}
}

// Before the site is initialised the admin routes must stay closed, exactly as
// the middleware does for every other admin endpoint.
func TestAdminRestartRefusesBeforeBootstrap(t *testing.T) {
	s := restartServer()
	s.bootstrap.Store(true)
	rec, req := newRestart(`{}`, "application/json", basicAuth("synthetic-admin"))
	s.admin(s.adminRestart)(rec, req)
	if rec.Code != 503 {
		t.Fatalf("restart allowed before initialisation: %d %s", rec.Code, rec.Body.String())
	}
}

// A malformed body must be rejected. The body check runs after auth, so a
// rejection here proves the payload contract rather than the auth gate.
func TestAdminRestartRejectsBadBody(t *testing.T) {
	s := restartServer()
	for _, body := range []string{`{"unexpected":true}`, `not json`} {
		rec, req := newRestart(body, "application/json", basicAuth("synthetic-admin"))
		s.admin(s.adminRestart)(rec, req)
		if rec.Code != 400 {
			t.Fatalf("body %q accepted: %d %s", body, rec.Code, rec.Body.String())
		}
	}
}

// The {} body is what the admin page actually sends, so it must be accepted,
// and the request must schedule a non-zero exit so systemd revives the service.
// The exit is captured through the exitProcess seam: calling os.Exit here would
// abort the whole test binary and turn one assertion into a package-wide failure.
func TestAdminRestartAcceptsEmptyObjectBodyAndExitsNonZero(t *testing.T) {
	s := restartServer()
	exited := make(chan int, 1)
	s.exitProcess = func(code int) { exited <- code }

	rec, req := newRestart(`{}`, "application/json", basicAuth("synthetic-admin"))
	s.admin(s.adminRestart)(rec, req)

	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"restarting":true`) {
		t.Fatalf("valid restart request rejected: %d %s", rec.Code, rec.Body.String())
	}
	select {
	case code := <-exited:
		// Zero would be read as a clean stop under Restart=on-failure, leaving the
		// site down. The exact value matters, not just "it exited".
		if code == 0 {
			t.Fatal("handler exited 0; Restart=on-failure would treat that as stopped")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler never scheduled the process exit")
	}
}

// A content type other than JSON is refused, matching decodeSetup's contract.
func TestAdminRestartRequiresJSONContentType(t *testing.T) {
	s := restartServer()
	rec, req := newRestart(`{}`, "text/plain", basicAuth("synthetic-admin"))
	s.admin(s.adminRestart)(rec, req)
	if rec.Code != 415 {
		t.Fatalf("non-JSON content type accepted: %d %s", rec.Code, rec.Body.String())
	}
}
