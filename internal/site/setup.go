package site

// First-run bootstrap: the /setup page and the API that turns a blank vault
// into a configured site. The page exists only while the site is
// uninitialised; once the admin password file is created the server stops
// registering these routes, so the page disappears for good.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// setupBodyLimit is shared with admin JSON settings requests, which may include
// complete node configurations and billing addresses.
const setupBodyLimit = 64 << 10

// setupPage serves the bootstrap form.
func (s *server) setupPage(w http.ResponseWriter, r *http.Request) {
	s.asset("setup.html", "text/html; charset=utf-8")(w, r)
}

// checkSetupPassword compares the submitted bootstrap password in constant
// time. A zero hash means bootstrap was never configured, which always fails.
func (s *server) checkSetupPassword(submitted string) bool {
	s.stateMu.RLock()
	want := s.setupHash
	s.stateMu.RUnlock()
	if want == ([32]byte{}) {
		return false
	}
	got := sha256.Sum256([]byte(submitted))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

// setupStatus lets the page know whether bootstrap is still open.
func (s *server) setupStatus(w http.ResponseWriter, r *http.Request) {
	reply(w, 200, map[string]any{"bootstrap": s.bootstrapEnabled()})
}

// setupApply creates only the administrator password. Business credentials are
// configured later through authenticated admin settings, never during bootstrap.
func (s *server) setupApply(w http.ResponseWriter, r *http.Request) {
	if !s.bootstrapEnabled() {
		message(w, 404, "站点已完成初始化。")
		return
	}
	var f struct {
		SetupPassword string `json:"setup_password"`
		AdminPassword string `json:"admin_password"`
	}
	if !decodeSetup(w, r, &f) {
		return
	}
	if !s.checkSetupPassword(f.SetupPassword) {
		w.Header().Set("Retry-After", "30")
		message(w, 401, "初始化密码不正确。")
		return
	}
	if strings.TrimSpace(f.AdminPassword) != f.AdminPassword || len(f.AdminPassword) < adminMinLength || len(f.AdminPassword) > 256 || strings.ContainsAny(f.AdminPassword, "\r\n\x00") {
		message(w, 400, "后台密码须为 32–256 字节，不能包含首尾空格、换行或空字符。")
		return
	}
	// Only establish the administrator; business settings belong in the authenticated backend.
	if err := s.writeAdminPassword(f.AdminPassword); err != nil {
		if errors.Is(err, errAlreadyInitialised) {
			message(w, 409, "站点已完成初始化，请重启服务后使用管理后台。")
			return
		}
		log.Printf("bootstrap admin password failed: %v", err)
		message(w, 500, "保存管理员密码失败，请重试。")
		return
	}
	log.Printf("bootstrap completed; restart required to leave setup mode")
	reply(w, 200, map[string]any{"ok": true, "restart_required": true})
}

var errAlreadyInitialised = errors.New("admin password file already exists")

// writeAdminPassword creates the admin password file with owner-only
// permissions. It never overwrites: if the file already exists the site is
// genuinely configured and the caller's password must not replace it.
func (s *server) writeAdminPassword(password string) error {
	path := s.adminPath
	if path == "" {
		return errors.New("XGIFT_ADMIN_PASSWORD_FILE is not configured")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if os.IsExist(err) {
			return errAlreadyInitialised
		}
		return err
	}
	defer f.Close()
	if _, err = f.WriteString(password + "\n"); err != nil {
		os.Remove(path)
		return err
	}
	// OpenFile honours the process umask, so set the mode explicitly as well.
	return os.Chmod(path, 0600)
}

// setupRestart hands the process back to systemd. The shipped unit uses
// Restart=on-failure, so a clean exit would be read as "stopped"; exiting
// non-zero restarts under both on-failure and always.
func (s *server) setupRestart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SetupPassword string `json:"setup_password"`
	}
	if !decodeSetup(w, r, &body) {
		return
	}
	if !s.checkSetupPassword(body.SetupPassword) {
		w.Header().Set("Retry-After", "30")
		message(w, 401, "初始化密码不正确。")
		return
	}
	reply(w, 200, map[string]any{"restarting": true})
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.Exit(1)
	}()
}

// decodeSetup mirrors decode but with a larger body budget. Unknown fields are
// still rejected so a renamed form field fails loudly instead of silently
// storing nothing.
func decodeSetup(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		message(w, 415, "请使用 JSON 请求。")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, setupBodyLimit))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		message(w, 400, "请求内容无法解析。")
		return false
	}
	return true
}
