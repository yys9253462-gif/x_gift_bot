package site

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xgift/internal/vault"
)

// Run the real service twice without invoking setupRestart (which exits the process).
// All files and listeners are isolated; no business credentials or payment calls exist.
func TestRunPasswordOnlyRestartWithoutProxy(t *testing.T) {
	dir := t.TempDir()
	passwordPath := filepath.Join(dir, "vault-password")
	setupPath := filepath.Join(dir, "setup-password")
	adminPath := filepath.Join(dir, "admin-password")
	for path, value := range map[string]string{passwordPath: strings.Repeat("v", 32), setupPath: "setup-secret-1234"} {
		if err := os.WriteFile(path, []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	vaultPath := filepath.Join(dir, "vault.db")
	v, err := vault.Open(vaultPath, passwordPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"XGIFT_DATA_DIR": dir, "XGIFT_PASSWORD_FILE": passwordPath,
		"XGIFT_ADMIN_PASSWORD_FILE": adminPath, "XGIFT_SETUP_PASSWORD_FILE": setupPath,
		"XGIFT_ORIGIN": "https://setup-test.invalid", "XGIFT_PAYMENTS_ENABLED": "false",
		"XGIFT_TURNSTILE_SITE_KEY": "", "XGIFT_TURNSTILE_SECRET_FILE": "",
	} {
		t.Setenv(key, value)
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	adminPassword := strings.Repeat("界", 11) // 33 UTF-8 bytes, fewer than 32 characters.
	for phase := 0; phase < 2; phase++ {
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
		func() {
			defer func() {
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Errorf("phase %d shutdown: %v", phase, err)
					} else {
						t.Logf("phase %d: Run stopped cleanly after context cancellation", phase)
					}
				case <-time.After(10 * time.Second):
					t.Error("Run did not stop after context cancellation")
				}
			}()
			base := "http://" + addr
			deadline := time.Now().Add(15 * time.Second)
			for {
				select {
				case err := <-done:
					done <- err
					t.Fatalf("phase %d Run exited before health became ready: %v", phase, err)
				default:
				}
				res, err := client.Get(base + "/healthz")
				if err == nil {
					var health struct {
						OK       bool `json:"ok"`
						Payments bool `json:"payments_enabled"`
					}
					err = json.NewDecoder(res.Body).Decode(&health)
					res.Body.Close()
					if err != nil || res.StatusCode != 200 || !health.OK || health.Payments {
						t.Fatalf("unsafe/unhealthy response: %+v, %v", health, err)
					}
					t.Logf("phase %d: health HTTP 200, payments_enabled=false", phase)
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("health readiness timed out")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if phase == 0 {
				body, _ := json.Marshal(map[string]string{"setup_password": "setup-secret-1234", "admin_password": adminPassword})
				req, _ := http.NewRequest("POST", base+"/api/setup/apply", strings.NewReader(string(body)))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", "https://setup-test.invalid")
				res, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if res.StatusCode != 200 {
					t.Fatalf("setup: %d %s", res.StatusCode, data)
				}
			} else {
				res, err := client.Get(base + "/api/setup/status")
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != 404 {
					t.Fatalf("setup still registered: %d", res.StatusCode)
				}
				req, _ := http.NewRequest("GET", base+"/admin", nil)
				req.SetBasicAuth("admin", adminPassword)
				res, err = client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				res.Body.Close()
				if res.StatusCode != 200 {
					t.Fatalf("admin login: %d", res.StatusCode)
				}
			}
		}()
	}
	v, err = vault.Open(vaultPath, passwordPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := v.Get("proxy"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("default proxy was persisted: %v", err)
	}
	t.Log("proxy remains absent from vault after setup and restart")
	raw, err := os.ReadFile(adminPath)
	if err != nil || string(raw) != adminPassword+"\n" {
		t.Fatalf("admin password changed: %v", err)
	}
}

func TestRunRejectsCorruptProxy(t *testing.T) {
	for _, bootstrap := range []bool{true, false} {
		t.Run(fmt.Sprint("bootstrap=", bootstrap), func(t *testing.T) {
			dir := t.TempDir()
			passwordPath := filepath.Join(dir, "password")
			adminPath := filepath.Join(dir, "admin")
			setupPath := filepath.Join(dir, "setup")
			for path, value := range map[string]string{passwordPath: strings.Repeat("v", 32), setupPath: strings.Repeat("s", 12)} {
				if err := os.WriteFile(path, []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if !bootstrap {
				if err := os.WriteFile(adminPath, []byte(strings.Repeat("a", 32)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(dir, "vault.db")
			v, err := vault.Open(path, passwordPath, true)
			if err != nil {
				t.Fatal(err)
			}
			v.Close()
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = db.Exec("INSERT INTO secrets(name,payload) VALUES ('proxy', ?)", []byte("broken"))
			db.Close()
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range map[string]string{
				"XGIFT_DATA_DIR": dir, "XGIFT_PASSWORD_FILE": passwordPath,
				"XGIFT_ADMIN_PASSWORD_FILE": adminPath, "XGIFT_SETUP_PASSWORD_FILE": setupPath,
				"XGIFT_ORIGIN": "https://setup-test.invalid", "XGIFT_PAYMENTS_ENABLED": "false",
				"XGIFT_TURNSTILE_SITE_KEY": "", "XGIFT_TURNSTILE_SECRET_FILE": "", "XGIFT_LISTEN": "127.0.0.1:0",
			} {
				t.Setenv(key, value)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := Run(ctx); err == nil || err.Error() != "invalid encrypted record" {
				t.Fatalf("corrupt proxy read must fail, got %v", err)
			}
		})
	}
}

func TestSetupPasswordOnly(t *testing.T) {
	for _, tc := range []struct {
		name, setup, password string
		status                int
	}{
		{"success", "setup-secret", strings.Repeat("a", 32), 200},
		{"wrong-token", "wrong", strings.Repeat("a", 32), 401},
		{"short-password", "setup-secret", "short", 400},
		{"min-bytes", "setup-secret", strings.Repeat("a", 32), 200},
		{"max-bytes", "setup-secret", strings.Repeat("a", 256), 200},
		{"over-max-bytes", "setup-secret", strings.Repeat("a", 257), 400},
		{"unicode-valid", "setup-secret", strings.Repeat("界", 11), 200},
		{"unicode-short", "setup-secret", strings.Repeat("界", 10), 400},
		{"unicode-max-bytes", "setup-secret", strings.Repeat("界", 85) + "a", 200},
		{"unicode-over-max", "setup-secret", strings.Repeat("界", 86), 400},
		{"newline", "setup-secret", strings.Repeat("a", 32) + "\n", 400},
		{"nul", "setup-secret", strings.Repeat("a", 32) + "\x00", 400},
		{"padded-password", "setup-secret", " " + strings.Repeat("a", 32), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &server{adminPath: filepath.Join(t.TempDir(), "admin-password"), setupHash: sha256.Sum256([]byte("setup-secret"))}
			s.bootstrap.Store(true)
			body, err := json.Marshal(map[string]string{"setup_password": tc.setup, "admin_password": tc.password})
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/api/setup/apply", strings.NewReader(string(body)))
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
				// Supply a fresh body for the duplicate request. Repeating the very
				// same submission is the retry an operator makes when the first
				// response never arrived, so it has to be answered as the success
				// it was rather than as a conflict.
				r2 := httptest.NewRequest("POST", "/api/setup/apply", strings.NewReader(`{"setup_password":"setup-secret","admin_password":"`+tc.password+`"}`))
				r2.Header.Set("Content-Type", "application/json")
				w = httptest.NewRecorder()
				s.setupApply(w, r2)
				if w.Code != 200 {
					t.Fatalf("duplicate status=%d", w.Code)
				}
				// A different password is not that request and stays a conflict.
				r3 := httptest.NewRequest("POST", "/api/setup/apply", strings.NewReader(`{"setup_password":"setup-secret","admin_password":"`+strings.Repeat("z", len(tc.password))+`"}`))
				r3.Header.Set("Content-Type", "application/json")
				w = httptest.NewRecorder()
				s.setupApply(w, r3)
				if w.Code != 409 {
					t.Fatalf("conflicting duplicate status=%d body=%s", w.Code, w.Body.String())
				}
			}
		})
	}
}

// Execute the shipped script with a minimal DOM and mocked HTTP only; never restart a service.
func TestSetupScriptPasswordBytesAndRestartRetry(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable; run this test where Node.js is installed")
	}
	const script = `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync('assets/setup.html', 'utf8');
assert.ok(!/\bminlength\s*=/i.test(html), 'character minlength blocks UTF-8 passwords');
assert.ok(!/\bmaxlength\s*=/i.test(html), 'character maxlength must not replace byte validation');
async function scenario(password, valid) {
  const ids = {};
  for (const id of ['setup-form','note','submit','setup-password','admin-password','admin-confirm','admin-link']) {
    ids[id] = { value: '', required: true, readOnly: false, disabled: false, textContent: '' };
  }
  let submit;
  ids['setup-form'].addEventListener = (_, handler) => { submit = handler; };
  ids['setup-form'].querySelectorAll = () => [ids['setup-password'],ids['admin-password'],ids['admin-confirm']];
  ids['setup-password'].value = 'setup-secret';
  ids['admin-password'].value = ids['admin-confirm'].value = password;
  const calls = [];
  // Mismatch must not reach the API even when the byte length is valid.
  let restart = 0;
  let redirected = false;
  const sandbox = {TextEncoder, document:{getElementById:id=>ids[id]},
    window:{location:{assign:path=>{ assert.equal(path,'/admin'); redirected=true; }}},
    setTimeout:resolve=>resolve(),
    fetch:async(path,options)=>{
      calls.push(path);
      if(path==='/api/setup/apply') { assert.equal(JSON.parse(options.body).admin_password,password); return {ok:true,json:async()=>({})}; }
      if(path==='/api/setup/restart') { restart++; return {ok:restart>1,json:async()=>({message:'restart failed'})}; }
      assert.equal(path,'/api/setup/status'); return {status:404};
    }};
  vm.runInNewContext(fs.readFileSync('assets/setup.js','utf8'),sandbox);
  // The page reads /api/setup/status once on load to recover a lost apply response.
  await new Promise(resolve=>setImmediate(resolve));
  assert.deepEqual(calls,['/api/setup/status']);
  calls.length=0;
  ids['admin-confirm'].value = password + 'different';
  await submit({preventDefault(){}});
  assert.deepEqual(calls,[]);
  ids['admin-confirm'].value = password;
  await submit({preventDefault(){}});
  if(!valid) { assert.deepEqual(calls,[]); assert.equal(ids.submit.disabled,false); return; }
  assert.deepEqual(calls,['/api/setup/apply','/api/setup/restart']);
  for(const id of ['admin-password','admin-confirm']) {
    assert.equal(ids[id].value,''); assert.equal(ids[id].required,false); assert.equal(ids[id].readOnly,true);
  }
  assert.equal(ids.submit.disabled,false);
  await submit({preventDefault(){}});
  assert.deepEqual(calls,['/api/setup/apply','/api/setup/restart','/api/setup/restart','/api/setup/status']);
  assert.equal(redirected,true);
}
(async()=>{
  for(const [password,valid] of [['a'.repeat(31),false],['a'.repeat(32),true],['a'.repeat(256),true],['a'.repeat(257),false],['界'.repeat(10),false],['界'.repeat(11),true],['界'.repeat(85)+'a',true],['界'.repeat(86),false],[' '+'a'.repeat(32),false],['a'.repeat(32)+'\n',false],['a'.repeat(32)+'\0',false]]) await scenario(password,valid);
  console.log('PASS: UTF-8 boundaries and saved restart retry');
})().catch(error=>{console.error(error);process.exitCode=1;});
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("setup script regression: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// A page reloaded after a lost apply response must recover from /api/setup/status
// and go straight to the restart, and a 409 that is not a proven idempotent
// success must never be reported as one.
func TestSetupScriptRecoversLostApplyResponse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable; run this test where Node.js is installed")
	}
	const script = `
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const source = fs.readFileSync('assets/setup.js','utf8');

function dom() {
  const ids = {};
  for (const id of ['setup-form','note','submit','setup-password','admin-password','admin-confirm','admin-link']) {
    ids[id] = { value: '', required: true, readOnly: false, disabled: false, textContent: '', hidden: true };
  }
  ids['setup-form'].addEventListener = (_, handler) => { ids.submit_handler = handler; };
  ids['setup-form'].querySelectorAll = () => [ids['setup-password'],ids['admin-password'],ids['admin-confirm']];
  ids['setup-password'].value = 'setup-secret';
  return ids;
}

// Scenario 1: the apply response never arrived; the reload says admin_saved.
async function reloadRecoversAndRestarts() {
  const ids = dom();
  const calls = [];
  let restartAttempts = 0;
  let restarted = false;
  const sandbox = {TextEncoder, document:{getElementById:id=>ids[id]},
    window:{location:{assign:path=>{ calls.push('redirect:'+path); }}},
    setTimeout:resolve=>resolve(),
    fetch:async(path,options)=>{
      calls.push(path);
      if(path==='/api/setup/status') {
        // The page only leaves setup mode once the restarted server no longer
        // serves /api/setup/status at all.
        if(restarted) return {status:404};
        return {status:200,ok:true,json:async()=>({bootstrap:true,admin_saved:true,restart_required:true})};
      }
      if(path==='/api/setup/apply') throw new Error('apply must not be repeated after recovery');
      if(path==='/api/setup/restart') {
        restartAttempts++;
        if(restartAttempts===1) return {ok:false,json:async()=>({message:'restart failed'})};
        restarted = true; return {ok:true,json:async()=>({restarting:true})};
      }
      throw new Error('unexpected '+path);
    }};
  vm.runInNewContext(source,sandbox);
  await new Promise(resolve=>setImmediate(resolve));
  assert.ok(ids['admin-password'].readOnly, 'reload did not lock the password fields');
  assert.equal(ids['admin-password'].required,false);
  assert.equal(ids.submit.textContent,'重试重启服务');
  // A failed restart is retryable and never repeats apply.
  await ids.submit_handler({preventDefault(){}});
  assert.deepEqual(calls,['/api/setup/status','/api/setup/restart']);
  assert.match(ids.note.textContent,/重试重启/);
  // The retry succeeds and the page hands over once the route is gone.
  await ids.submit_handler({preventDefault(){}});
  assert.deepEqual(calls,['/api/setup/status','/api/setup/restart','/api/setup/restart','/api/setup/status','redirect:/admin']);
}

// Scenario 2: a 409 on apply must surface as an error, never as success.
async function conflictIsNotSuccess() {
  const ids = dom();
  ids['admin-password'].value = ids['admin-confirm'].value = 'a'.repeat(32);
  const calls = [];
  const sandbox = {TextEncoder, document:{getElementById:id=>ids[id]},
    window:{location:{assign:()=>{ throw new Error('must not redirect'); }}},
    setTimeout:resolve=>resolve(),
    fetch:async(path,options)=>{
      calls.push(path);
      if(path==='/api/setup/status') return {status:200,ok:true,json:async()=>({bootstrap:true,admin_saved:false,restart_required:true})};
      if(path==='/api/setup/apply') return {ok:false,json:async()=>({message:'站点已完成初始化，请重启服务后使用管理后台。'})};
      throw new Error('restart must not be attempted');
    }};
  vm.runInNewContext(source,sandbox);
  await new Promise(resolve=>setImmediate(resolve));
  await ids.submit_handler({preventDefault(){}});
  assert.deepEqual(calls,['/api/setup/status','/api/setup/apply']);
  assert.match(ids.note.textContent,/已完成初始化/);
  assert.equal(ids['admin-password'].readOnly,false,'a conflict must not lock the form as saved');
  assert.notEqual(ids.submit.textContent,'重试重启服务','a conflict must not offer a restart as if the password existed');
  assert.equal(ids.submit.disabled,false,'a conflict must stay retryable');
}

// Scenario 3: the normal first run still applies once and then restarts.
async function firstRun() {
  const ids = dom();
  const password = 'a'.repeat(32);
  ids['admin-password'].value = ids['admin-confirm'].value = password;
  const calls = [];
  let applyCount = 0;
  let restarted = false;
  const sandbox = {TextEncoder, document:{getElementById:id=>ids[id]},
    window:{location:{assign:path=>{ calls.push('redirect:'+path); }}},
    setTimeout:resolve=>resolve(),
    fetch:async(path,options)=>{
      calls.push(path);
      if(path==='/api/setup/status') {
        if(restarted) return {status:404};
        return {status:200,ok:true,json:async()=>({bootstrap:true,admin_saved:false,restart_required:true})};
      }
      if(path==='/api/setup/apply') { applyCount++; assert.equal(JSON.parse(options.body).admin_password,password);
        return {ok:true,json:async()=>({ok:true,already_saved:false,restart_required:true})}; }
      if(path==='/api/setup/restart') { restarted=true; return {ok:true,json:async()=>({restarting:true})}; }
      throw new Error('unexpected '+path);
    }};
  vm.runInNewContext(source,sandbox);
  await new Promise(resolve=>setImmediate(resolve));
  await ids.submit_handler({preventDefault(){}});
  assert.equal(applyCount,1);
  assert.deepEqual(calls,['/api/setup/status','/api/setup/apply','/api/setup/restart','/api/setup/status','redirect:/admin']);
}
(async()=>{
  await reloadRecoversAndRestarts();
  await conflictIsNotSuccess();
  await firstRun();
  console.log('PASS: setup page recovers a lost apply response and never accepts a bare 409');
})().catch(error=>{console.error(error);process.exitCode=1;});
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", script)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("setup recovery regression: %v\n%s", err, output)
	}
	t.Log(string(output))
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
