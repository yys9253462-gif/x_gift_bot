package checkout

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testTokenA = "950c88bc34eeceb3524e1d2832e966d7d67ba648"
	testTokenB = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	testCT0A   = "f03741899b6df852de21bac72f6a0565edbc7db9841c2e7770f54b2ec8f48e3cc"
	testCT0B   = "0badc0ffee1234567890abcdef1234567890abcdef1234567890abcdef123456"
)

func legacyCookiesRecord(authToken, ct0 string) string {
	return `{"cookies":[{"name":"auth_token","value":"` + authToken + `","domain":".x.com"},{"name":"ct0","value":"` + ct0 + `","domain":".x.com"}]}`
}

func TestLoadXAccountsFallsBackToLegacyRecord(t *testing.T) {
	v := controlFixture(t)
	if accounts, err := LoadXAccounts(v); err != nil || len(accounts) != 0 {
		t.Fatalf("empty vault should hold no account: %+v %v", accounts, err)
	}
	if err := v.Put("cookies", []byte(legacyCookiesRecord(testTokenA, testCT0A))); err != nil {
		t.Fatal(err)
	}
	accounts, err := LoadXAccounts(v)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("legacy fallback failed: %+v %v", accounts, err)
	}
	if accounts[0].AuthToken != testTokenA || accounts[0].CT0 != testCT0A || accounts[0].ID != accountID(testTokenA) {
		t.Fatalf("legacy account parsed wrong: %+v", accounts[0])
	}
}

func TestUpsertAddsUpdatesAndMirrorsLegacy(t *testing.T) {
	v := controlFixture(t)
	if err := v.Put("cookies", []byte(legacyCookiesRecord(testTokenA, testCT0A))); err != nil {
		t.Fatal(err)
	}
	// Adding a second account materialises the legacy one into the pool.
	added, err := UpsertXAccount(v, "备胎号", testTokenB, testCT0B)
	if err != nil || !added {
		t.Fatalf("add failed: %v %v", added, err)
	}
	accounts, _ := LoadXAccounts(v)
	if len(accounts) != 2 || accounts[0].AuthToken != testTokenA || accounts[1].AuthToken != testTokenB {
		t.Fatalf("pool should hold legacy first: %+v", accounts)
	}
	// Same auth token refreshes the pair instead of duplicating.
	added, err = UpsertXAccount(v, "", testTokenA, "new-ct0-value")
	if err != nil || added {
		t.Fatalf("update reported as add: %v %v", added, err)
	}
	accounts, _ = LoadXAccounts(v)
	if len(accounts) != 2 || accounts[0].CT0 != "new-ct0-value" || accounts[0].Label != "默认账号" {
		t.Fatalf("update did not refresh in place: %+v", accounts[0])
	}
	// account[0] is mirrored into the legacy record for binary rollback.
	raw, err := v.Get("cookies")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "new-ct0-value") {
		t.Fatalf("legacy mirror stale: %s", raw)
	}
	clear(raw)
}

func TestUpsertValidatesCookies(t *testing.T) {
	v := controlFixture(t)
	if _, err := UpsertXAccount(v, "x", "", testCT0A); err == nil {
		t.Fatal("empty auth_token accepted")
	}
	if _, err := UpsertXAccount(v, "x", testTokenA, ""); err == nil {
		t.Fatal("empty ct0 accepted")
	}
	if _, err := UpsertXAccount(v, "x", "token\ninjection", testCT0A); err == nil {
		t.Fatal("newline accepted")
	}
	if _, err := UpsertXAccount(v, "x", testTokenA, "ct0;drop"); err == nil {
		t.Fatal("semicolon accepted")
	}
}

func TestRemoveXAccountKeepsOrderAndClearsWhenEmpty(t *testing.T) {
	v := controlFixture(t)
	if _, err := UpsertXAccount(v, "一号", testTokenA, testCT0A); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertXAccount(v, "二号", testTokenB, testCT0B); err != nil {
		t.Fatal(err)
	}
	if err := RemoveXAccount(v, accountID(testTokenA)); err != nil {
		t.Fatal(err)
	}
	accounts, _ := LoadXAccounts(v)
	if len(accounts) != 1 || accounts[0].AuthToken != testTokenB {
		t.Fatalf("wrong account removed: %+v", accounts)
	}
	// The mirror now points at the remaining account.
	raw, _ := v.Get("cookies")
	if !strings.Contains(string(raw), testTokenB) {
		t.Fatalf("mirror did not follow removal: %s", raw)
	}
	clear(raw)
	if err := RemoveXAccount(v, accountID(testTokenB)); err != nil {
		t.Fatal(err)
	}
	if accounts, err := LoadXAccounts(v); err != nil || len(accounts) != 0 {
		t.Fatalf("pool should be empty: %+v %v", accounts, err)
	}
	if _, err := v.Get(xAccountsRecord); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("pool record survived emptying: %v", err)
	}
	if raw, err := v.Get("cookies"); err == nil {
		t.Fatalf("legacy mirror survived emptying: %s", raw)
	}
	if err := RemoveXAccount(v, accountID(testTokenA)); err == nil {
		t.Fatal("removing an unknown account should fail")
	}
}

func TestSelectXAccountRotatesAndHonoursCooldown(t *testing.T) {
	v := controlFixture(t)
	if _, err := UpsertXAccount(v, "一号", testTokenA, testCT0A); err != nil {
		t.Fatal(err)
	}
	if _, err := UpsertXAccount(v, "二号", testTokenB, testCT0B); err != nil {
		t.Fatal(err)
	}
	// LRU: first pick is account one, second rotates to account two.
	first, err := SelectXAccount(v)
	if err != nil || first.AuthToken != testTokenA {
		t.Fatalf("first pick wrong: %+v %v", first, err)
	}
	second, err := SelectXAccount(v)
	if err != nil || second.AuthToken != testTokenB {
		t.Fatalf("second pick did not rotate: %+v %v", second, err)
	}
	// Cooling the LRU-next account forces the only remaining one.
	if err := CoolXAccount(v, accountID(testTokenB), "refused"); err != nil {
		t.Fatal(err)
	}
	third, err := SelectXAccount(v)
	if err != nil || third.AuthToken != testTokenA {
		t.Fatalf("cooled account still selected: %+v %v", third, err)
	}
	// Cooling both leaves nothing selectable.
	if err := CoolXAccount(v, accountID(testTokenA), "refused"); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectXAccount(v); !errors.Is(err, ErrAllXAccountsCooling) {
		t.Fatalf("expected all-cooling error, got %v", err)
	}
	// Expired cooldowns stop excluding an account.
	expired, err := json.Marshal(map[string]any{"until": time.Now().Add(-time.Minute).Unix(), "reason": "refused"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Put(xAccountCooldownPrefix+accountID(testTokenA), expired); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectXAccount(v); err != nil {
		t.Fatalf("expired cooldown still blocks: %v", err)
	}
}

func TestSelectXAccountWithoutAnyAccount(t *testing.T) {
	v := controlFixture(t)
	if _, err := SelectXAccount(v); !errors.Is(err, ErrNoXAccount) {
		t.Fatalf("expected no-account error, got %v", err)
	}
}

func TestXAccountStatusReportsCooldown(t *testing.T) {
	v := controlFixture(t)
	if _, err := UpsertXAccount(v, "一号", testTokenA, testCT0A); err != nil {
		t.Fatal(err)
	}
	if err := CoolXAccount(v, accountID(testTokenA), "code 37"); err != nil {
		t.Fatal(err)
	}
	infos, err := XAccountStatus(v)
	if err != nil || len(infos) != 1 {
		t.Fatalf("status failed: %+v %v", infos, err)
	}
	if infos[0].CoolingSeconds <= 0 || infos[0].CoolingReason != "code 37" || infos[0].Tail != "…a648" {
		t.Fatalf("cooldown not surfaced: %+v", infos[0])
	}
}

func TestIsAccountFaultClassification(t *testing.T) {
	if !IsAccountFault(ErrGiftNotAuthorised) {
		t.Fatal("gift refusal must count as account fault")
	}
	if !IsAccountFault(errAccountAuth) {
		t.Fatal("dead session must count as account fault")
	}
	if IsAccountFault(ErrOperationRejected) {
		t.Fatal("stale identifier must not trigger rotation")
	}
	if IsAccountFault(ErrNotEligible) {
		t.Fatal("recipient-side ineligibility must not trigger rotation")
	}
	if IsAccountFault(nil) {
		t.Fatal("nil is not a fault")
	}
}
