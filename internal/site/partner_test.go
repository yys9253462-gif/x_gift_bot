package site

// 商城发货 API 的测试。
//
// 重点覆盖三类容易出事的行为：
//   1. 鉴权：没配密钥、密钥错、密钥对但格式不对 —— 都不能放行；
//   2. 幂等：同一订单号重复请求只能发一枚码，且必须返回同一枚；
//   3. 作废：只作废未使用的码，已使用的码必须明确拒绝而不是静默成功。
//
// 这些是商城对接里唯一会造成真金白银损失的地方，所以每条都单独断言。

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"xgift/internal/vault"
)

// partnerFixture 搭一个带 vault、site.db 和已建表的最小 server。
func partnerFixture(t *testing.T) *server {
	t.Helper()
	s := checkFixture(t)
	db, err := sql.Open("sqlite3", t.TempDir()+"/site.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec(`CREATE TABLE codes (
 id TEXT PRIMARY KEY, hash TEXT NOT NULL UNIQUE, hint TEXT NOT NULL, batch TEXT NOT NULL,
 months INTEGER NOT NULL CHECK(months IN (3,6)),
 status TEXT NOT NULL CHECK(status IN ('active','processing','succeeded','review','revoked')),
 username TEXT NOT NULL DEFAULT '', recipient_id TEXT UNIQUE,
 message TEXT NOT NULL DEFAULT '', created INTEGER NOT NULL, updated INTEGER NOT NULL, progress INTEGER NOT NULL DEFAULT 0,
 folder_id TEXT REFERENCES folders(id) ON DELETE SET NULL, copyable INTEGER NOT NULL DEFAULT 0);`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TABLE folders (id TEXT PRIMARY KEY, name TEXT NOT NULL COLLATE NOCASE UNIQUE, created INTEGER NOT NULL, updated INTEGER NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	if err = migratePartner(db); err != nil {
		t.Fatal(err)
	}
	s.db = db
	return s
}

// configurePartner 写入一枚密钥的哈希，返回明文密钥。
func configurePartner(t *testing.T, s *server) string {
	t.Helper()
	key := partnerKeyPrefix + strings.Repeat("a", 48)
	if err := s.vault.Put("partner-key", []byte(partnerHash(key))); err != nil {
		t.Fatal(err)
	}
	return key
}

func partnerPost(t *testing.T, s *server, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	switch path {
	case "/api/partner/fulfill":
		s.partner(s.partnerFulfill)(w, r)
	case "/api/partner/revoke":
		s.partner(s.partnerRevoke)(w, r)
	case "/api/partner/ping":
		s.partner(s.partnerPing)(w, r)
	default:
		t.Fatalf("unknown path %q", path)
	}
	return w
}

func partnerBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON %q: %v", w.Body.String(), err)
	}
	return body
}

// 没配密钥时接口必须关闭，否则一个误删密钥的站点会变成裸奔的发码机。
func TestPartnerRejectsWithoutConfiguredKey(t *testing.T) {
	s := partnerFixture(t)
	w := partnerPost(t, s, "/api/partner/fulfill", partnerKeyPrefix+strings.Repeat("a", 48), `{"order_id":"o1","months":3}`)
	if w.Code != 401 {
		t.Fatalf("unconfigured key: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPartnerRejectsWrongAndMalformedKeys(t *testing.T) {
	s := partnerFixture(t)
	good := configurePartner(t, s)
	cases := []struct {
		name, key string
	}{
		{"wrong key", partnerKeyPrefix + strings.Repeat("b", 48)},
		{"no prefix", strings.Repeat("a", 48)},
		{"empty", ""},
		{"basic instead of bearer", good},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/partner/fulfill", strings.NewReader(`{"order_id":"o1","months":3}`))
			r.Header.Set("Content-Type", "application/json")
			switch c.name {
			case "basic instead of bearer":
				r.Header.Set("Authorization", "Basic "+c.key)
			case "empty":
				// 不设置 Authorization
			default:
				r.Header.Set("Authorization", "Bearer "+c.key)
			}
			w := httptest.NewRecorder()
			s.partner(s.partnerFulfill)(w, r)
			if w.Code != 401 {
				t.Fatalf("%s: status=%d body=%s", c.name, w.Code, w.Body.String())
			}
		})
	}
	// 正确的密钥必须放行，否则上面的断言可能只是"全都拒绝"的假阳性。
	if w := partnerPost(t, s, "/api/partner/fulfill", good, `{"order_id":"o1","months":3}`); w.Code != 200 {
		t.Fatalf("valid key rejected: status=%d body=%s", w.Code, w.Body.String())
	}
}

// 同一订单号重复请求必须返回同一枚码，且库里只有一枚。
func TestPartnerFulfillIsIdempotent(t *testing.T) {
	s := partnerFixture(t)
	key := configurePartner(t, s)
	body := `{"order_id":"shop-1001","months":6}`

	first := partnerPost(t, s, "/api/partner/fulfill", key, body)
	if first.Code != 200 {
		t.Fatalf("first: status=%d body=%s", first.Code, first.Body.String())
	}
	code1, _ := partnerBody(t, first)["code"].(string)
	if !codePattern.MatchString(code1) {
		t.Fatalf("first code malformed: %q", code1)
	}

	second := partnerPost(t, s, "/api/partner/fulfill", key, body)
	if second.Code != 200 {
		t.Fatalf("second: status=%d body=%s", second.Code, second.Body.String())
	}
	code2, _ := partnerBody(t, second)["code"].(string)
	if code1 != code2 {
		t.Fatalf("idempotency broken: %q != %q", code1, code2)
	}

	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM codes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("issued %d codes for one order, want 1", count)
	}
}

// 同一订单号换套餐必须报冲突，不能悄悄再发一枚。
func TestPartnerFulfillConflictsOnPlanChange(t *testing.T) {
	s := partnerFixture(t)
	key := configurePartner(t, s)
	if w := partnerPost(t, s, "/api/partner/fulfill", key, `{"order_id":"shop-1002","months":3}`); w.Code != 200 {
		t.Fatalf("first: status=%d body=%s", w.Code, w.Body.String())
	}
	w := partnerPost(t, s, "/api/partner/fulfill", key, `{"order_id":"shop-1002","months":6}`)
	if w.Code != 409 {
		t.Fatalf("plan change: status=%d body=%s", w.Code, w.Body.String())
	}
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM codes").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("conflict still issued a code: count=%d", count)
	}
}

// 时长只允许 3 或 6：与 codes 表的 CHECK 约束保持一致，
// 否则会变成一条数据库错误而不是清晰的 400。
func TestPartnerFulfillRejectsUnsupportedMonths(t *testing.T) {
	s := partnerFixture(t)
	key := configurePartner(t, s)
	for _, months := range []string{"1", "12", "0", "3.5"} {
		w := partnerPost(t, s, "/api/partner/fulfill", key, `{"order_id":"shop-1003-`+months+`","months":`+months+`}`)
		if w.Code != 400 {
			t.Fatalf("months=%s: status=%d body=%s", months, w.Code, w.Body.String())
		}
	}
}

// 作废只能作用于尚未使用的码。
func TestPartnerRevokeOnlyAffectsActiveCode(t *testing.T) {
	s := partnerFixture(t)
	key := configurePartner(t, s)
	if w := partnerPost(t, s, "/api/partner/fulfill", key, `{"order_id":"shop-2001","months":3}`); w.Code != 200 {
		t.Fatalf("fulfill: status=%d", w.Code)
	}
	w := partnerPost(t, s, "/api/partner/revoke", key, `{"order_id":"shop-2001"}`)
	if w.Code != 200 || partnerBody(t, w)["revoked"] != true {
		t.Fatalf("revoke: status=%d body=%s", w.Code, w.Body.String())
	}
	var status string
	if err := s.db.QueryRow("SELECT status FROM codes").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "revoked" {
		t.Fatalf("status=%q want revoked", status)
	}
	// 重复作废应当幂等成功。
	w = partnerPost(t, s, "/api/partner/revoke", key, `{"order_id":"shop-2001"}`)
	if w.Code != 200 || partnerBody(t, w)["already_revoked"] != true {
		t.Fatalf("second revoke: status=%d body=%s", w.Code, w.Body.String())
	}
}

// 已被买家兑换的码不能作废：那代表钱已经变成了服务，撤销只会账实不符。
func TestPartnerRevokeRefusesUsedCode(t *testing.T) {
	s := partnerFixture(t)
	key := configurePartner(t, s)
	if w := partnerPost(t, s, "/api/partner/fulfill", key, `{"order_id":"shop-2002","months":6}`); w.Code != 200 {
		t.Fatalf("fulfill: status=%d", w.Code)
	}
	if _, err := s.db.Exec("UPDATE codes SET status='succeeded', username='buyer'"); err != nil {
		t.Fatal(err)
	}
	w := partnerPost(t, s, "/api/partner/revoke", key, `{"order_id":"shop-2002"}`)
	if w.Code != 409 {
		t.Fatalf("used code: status=%d body=%s", w.Code, w.Body.String())
	}
	var status string
	if err := s.db.QueryRow("SELECT status FROM codes").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" {
		t.Fatalf("used code was changed to %q", status)
	}
}

func TestPartnerRevokeUnknownOrder(t *testing.T) {
	s := partnerFixture(t)
	key := configurePartner(t, s)
	if w := partnerPost(t, s, "/api/partner/revoke", key, `{"order_id":"never-issued"}`); w.Code != 404 {
		t.Fatalf("unknown order: status=%d body=%s", w.Code, w.Body.String())
	}
}

// 商城发出去的那枚码必须真的能兑换：vault 里存着明文，
// 且 hint 与明文末 8 位一致。这条把"发码"和"兑换"接起来，防止只写账本。
func TestPartnerFulfilledCodeIsRedeemable(t *testing.T) {
	s := partnerFixture(t)
	key := configurePartner(t, s)
	w := partnerPost(t, s, "/api/partner/fulfill", key, `{"order_id":"shop-3001","months":3}`)
	if w.Code != 200 {
		t.Fatalf("fulfill: status=%d body=%s", w.Code, w.Body.String())
	}
	body := partnerBody(t, w)
	code, _ := body["code"].(string)
	hint, _ := body["hint"].(string)
	if !codePattern.MatchString(code) {
		t.Fatalf("issued code malformed: %q", code)
	}
	if hint != code[len(code)-8:] {
		t.Fatalf("hint %q is not the tail of %q", hint, code)
	}
	var id, digest string
	if err := s.db.QueryRow("SELECT id,hash FROM codes").Scan(&id, &digest); err != nil {
		t.Fatal(err)
	}
	plain, err := s.vault.Get("redemption:" + id)
	if err != nil {
		t.Fatalf("vault record missing: %v", err)
	}
	defer clear(plain)
	if string(plain) != code {
		t.Fatalf("vault holds %q, responded %q", plain, code)
	}
	if hash(code) != digest {
		t.Fatal("stored hash does not match the issued code")
	}
	var months int
	if err := s.db.QueryRow("SELECT months FROM codes WHERE id=?", id).Scan(&months); err != nil {
		t.Fatal(err)
	}
	if months != 3 {
		t.Fatalf("months=%d want 3", months)
	}
}

// 密钥校验函数本身的形态约束。
func TestPartnerKeyValid(t *testing.T) {
	if !partnerKeyValid(partnerKeyPrefix + strings.Repeat("0", 48)) {
		t.Fatal("valid key rejected")
	}
	for _, bad := range []string{"", "xgp_", partnerKeyPrefix + strings.Repeat("0", 47), partnerKeyPrefix + strings.Repeat("0", 49), partnerKeyPrefix + strings.Repeat("g", 48), "xgq_" + strings.Repeat("0", 48)} {
		if partnerKeyValid(bad) {
			t.Fatalf("invalid key accepted: %q", bad)
		}
	}
}

// 端到端的幂等账本行数断言：重复请求不能撑大 partner_orders。
func TestPartnerLedgerDoesNotGrowOnRetries(t *testing.T) {
	s := partnerFixture(t)
	key := configurePartner(t, s)
	for i := 0; i < 5; i++ {
		if w := partnerPost(t, s, "/api/partner/fulfill", key, `{"order_id":"shop-4001","months":6}`); w.Code != 200 {
			t.Fatalf("attempt %d: status=%d body=%s", i+1, w.Code, w.Body.String())
		}
	}
	var rows int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM partner_orders").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("ledger has %d rows for one order", rows)
	}
}

var _ = vault.Vault{}
