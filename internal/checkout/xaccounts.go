package checkout

// X 账号池：赠送资格由「发送账号」的品质决定（X 按账号判定能否赠送，
// 见 ErrGiftNotAuthorised 的注释），单账号是单点故障——一个号被限流，
// 整条赠送链路停摆。这里把凭据从「一个 cookies 记录」升级为「账号池」，
// 每次下单按 LRU 挑选可用账号，账号级拒绝自动冷却并换号重试。
//
// 存储布局（都在加密 vault 内）：
//   - x-accounts                    账号池，权威数据；不存在时回退旧格式
//   - cookies                       旧版单账号记录；池落地后镜像 account[0]，
//                                  保证旧二进制回退后仍能跑
//   - x-account-cooldown:<id>       账号冷却，{"until":unix,"reason":"..."}
//
// 账号 id 是 auth_token 的内容指纹（sha256 前 12 位），换 ct0 不换 id，
// 页面上只出现尾纹，完整令牌不出 vault。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"xgift/internal/vault"
)

const (
	xAccountsRecord      = "x-accounts"
	xAccountCooldownPrefix = "x-account-cooldown:"
	maxXAccounts         = 32
	xAccountCooldown     = 30 * time.Minute
)

var (
	// ErrNoXAccount means neither the pool nor the legacy record holds an account.
	ErrNoXAccount = errors.New("no X account configured; add one with xgift accounts add or the admin panel")
	// ErrAllXAccountsCooling means every configured account was refused or
	// rejected recently and is inside its cooldown window.
	ErrAllXAccountsCooling = errors.New("all X accounts are cooling down after refusals")
)

// XAccount is one sending account: the two X session cookies plus operator
// metadata. AuthToken and CT0 never leave the vault.
type XAccount struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	AuthToken string `json:"auth_token"`
	CT0       string `json:"ct0"`
	AddedAt   int64  `json:"added_at"`
}

// XAccountInfo is the redacted per-account status the admin panel renders.
type XAccountInfo struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Tail           string `json:"auth_token_tail"`
	HasCT0         bool   `json:"has_ct0"`
	AddedAt        int64  `json:"added_at"`
	CoolingUntil   int64  `json:"cooling_until"`
	CoolingReason  string `json:"cooling_reason,omitempty"`
	CoolingSeconds int64  `json:"cooling_seconds"`
}

// xAccountsDoc is the on-disk shape of the pool record.
type xAccountsDoc struct {
	Accounts []XAccount `json:"accounts"`
}

// accountID derives a stable, non-reversible id from the auth token so the
// same account keeps its identity (and cooldown) across cookie rotations of
// the ct0 only, while the token itself is never displayed.
func accountID(authToken string) string {
	sum := sha256.Sum256([]byte(authToken))
	return hex.EncodeToString(sum[:])[:12]
}

func validateCookieValue(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New(name + " 不能为空")
	}
	if strings.ContainsAny(value, "\r\n;") {
		return errors.New(name + " 不能包含换行或分号")
	}
	return nil
}

// LoadXAccounts reads the pool. A vault that predates the pool falls back to
// the legacy single-account cookies record, so existing deployments keep
// working without a migration step; the fallback account is materialised on
// the first write.
func LoadXAccounts(v *vault.Vault) ([]XAccount, error) {
	raw, err := v.Get(xAccountsRecord)
	if err == nil {
		defer clear(raw)
		var doc xAccountsDoc
		if json.Unmarshal(raw, &doc) != nil {
			return nil, errors.New("x-accounts record is corrupt; fix it with xgift accounts add")
		}
		return doc.Accounts, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	legacy, lerr := loadLegacyAccount(v)
	if lerr != nil {
		return nil, lerr
	}
	if legacy == nil {
		return nil, nil
	}
	return []XAccount{*legacy}, nil
}

// loadLegacyAccount converts the old single-account cookies record.
func loadLegacyAccount(v *vault.Vault) (*XAccount, error) {
	raw, err := v.Get("cookies")
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	var doc struct {
		Cookies []struct{ Name, Value string } `json:"cookies"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil, errors.New("legacy cookies record is corrupt; rewrite it with xgift accounts add")
	}
	var authToken, ct0 string
	for _, c := range doc.Cookies {
		switch c.Name {
		case "auth_token":
			authToken = c.Value
		case "ct0":
			ct0 = c.Value
		}
	}
	if authToken == "" || ct0 == "" {
		return nil, nil
	}
	return &XAccount{ID: accountID(authToken), Label: "默认账号", AuthToken: authToken, CT0: ct0}, nil
}

// XAccountStatus reports every account with its cooldown state. Cooldown
// records that expired are reported as available; nothing is deleted here.
func XAccountStatus(v *vault.Vault) ([]XAccountInfo, error) {
	accounts, err := LoadXAccounts(v)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	out := make([]XAccountInfo, 0, len(accounts))
	for _, a := range accounts {
		info := XAccountInfo{
			ID:      a.ID,
			Label:   a.Label,
			Tail:    tailOf(a.AuthToken),
			HasCT0:  a.CT0 != "",
			AddedAt: a.AddedAt,
		}
		raw, cerr := v.Get(xAccountCooldownPrefix + a.ID)
		if cerr != nil && !errors.Is(cerr, sql.ErrNoRows) {
			return nil, cerr
		}
		if cerr == nil {
			var cd struct {
				Until  int64  `json:"until"`
				Reason string `json:"reason"`
			}
			if json.Unmarshal(raw, &cd) == nil && cd.Until > now {
				info.CoolingUntil = cd.Until
				info.CoolingReason = cd.Reason
				info.CoolingSeconds = cd.Until - now
			}
			clear(raw)
		}
		out = append(out, info)
	}
	return out, nil
}

func tailOf(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 4 {
		return s
	}
	return "…" + s[len(s)-4:]
}

// LRU state is in-memory on purpose: cooldowns persist in the vault, but
// perfect fairness across restarts is not worth a vault write per order.
var (
	accountUseMu   sync.Mutex
	accountUseSeq  = map[string]uint64{}
	accountUseTick uint64
)

// SelectXAccount picks the least recently used account that is not cooling
// down. Callers get a private copy; the vault stays the source of truth.
func SelectXAccount(v *vault.Vault) (*XAccount, error) {
	accounts, err := LoadXAccounts(v)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrNoXAccount
	}
	now := time.Now().Unix()
	available := make([]XAccount, 0, len(accounts))
	for _, a := range accounts {
		until, cerr := accountCooldownUntil(v, a.ID)
		if cerr != nil {
			return nil, cerr
		}
		if until > now {
			continue
		}
		available = append(available, a)
	}
	if len(available) == 0 {
		return nil, ErrAllXAccountsCooling
	}
	accountUseMu.Lock()
	defer accountUseMu.Unlock()
	best := 0
	for i := range available {
		if accountUseSeq[available[i].ID] < accountUseSeq[available[best].ID] {
			best = i
		}
	}
	accountUseTick++
	accountUseSeq[available[best].ID] = accountUseTick
	chosen := available[best]
	return &chosen, nil
}

func accountCooldownUntil(v *vault.Vault, id string) (int64, error) {
	raw, err := v.Get(xAccountCooldownPrefix + id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer clear(raw)
	var cd struct {
		Until int64 `json:"until"`
	}
	if json.Unmarshal(raw, &cd) != nil {
		return 0, nil
	}
	return cd.Until, nil
}

// CoolXAccount takes one account out of rotation for xAccountCooldown. It is
// only ever called with an account-level refusal, never with a transport or
// payment failure — those say nothing about the account.
func CoolXAccount(v *vault.Vault, id, reason string) error {
	b, err := json.Marshal(map[string]any{"until": time.Now().Add(xAccountCooldown).Unix(), "reason": reason})
	if err != nil {
		return err
	}
	defer clear(b)
	return v.Put(xAccountCooldownPrefix+id, b)
}

// IsAccountFault classifies errors that justify account rotation. Only two
// families qualify: X's explicit account-level refusal (code 37 and friends)
// and a dead session (HTTP 401). A stale GraphQL identifier or a rate limit is
// deliberately excluded — rotating accounts would hide the real problem.
func IsAccountFault(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrGiftNotAuthorised) || errors.Is(err, errAccountAuth)
}

// accountFaultReason is the short operator-facing label stored on the cooldown
// record so the admin panel can say why an account is resting.
func accountFaultReason(err error) string {
	if errors.Is(err, errAccountAuth) {
		return "会话失效"
	}
	return "赠送被拒"
}

// UpsertXAccount adds an account or refreshes the cookies of an existing one
// (same auth token). The first write materialises the legacy record into the
// pool, and account[0] is always mirrored back into the legacy cookies record
// so a rolled-back binary still finds a working account.
func UpsertXAccount(v *vault.Vault, label, authToken, ct0 string) (added bool, err error) {
	authToken, ct0 = strings.TrimSpace(authToken), strings.TrimSpace(ct0)
	if err = validateCookieValue("auth_token", authToken); err != nil {
		return false, err
	}
	if err = validateCookieValue("ct0", ct0); err != nil {
		return false, err
	}
	label = strings.TrimSpace(label)
	accounts, err := LoadXAccounts(v)
	if err != nil {
		return false, err
	}
	id := accountID(authToken)
	for i := range accounts {
		if accounts[i].ID == id {
			accounts[i].AuthToken, accounts[i].CT0 = authToken, ct0
			if label != "" {
				accounts[i].Label = label
			}
			return false, writeXAccounts(v, accounts)
		}
	}
	if len(accounts) >= maxXAccounts {
		return false, fmt.Errorf("账号池已满（%d 个）", maxXAccounts)
	}
	if label == "" {
		label = fmt.Sprintf("账号 %d", len(accounts)+1)
	}
	accounts = append(accounts, XAccount{ID: id, Label: label, AuthToken: authToken, CT0: ct0, AddedAt: time.Now().Unix()})
	return true, writeXAccounts(v, accounts)
}

// RemoveXAccount drops one account. Removing the last account clears the pool
// and the mirrored legacy record, returning the vault to its pristine state.
func RemoveXAccount(v *vault.Vault, id string) error {
	accounts, err := LoadXAccounts(v)
	if err != nil {
		return err
	}
	kept := make([]XAccount, 0, len(accounts))
	found := false
	for _, a := range accounts {
		if a.ID == id {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return errors.New("账号不存在")
	}
	if len(kept) == 0 {
		if _, err = v.Delete(xAccountsRecord); err != nil {
			return err
		}
		_, err = v.Delete("cookies")
		return err
	}
	return writeXAccounts(v, kept)
}

func writeXAccounts(v *vault.Vault, accounts []XAccount) error {
	b, err := json.Marshal(xAccountsDoc{Accounts: accounts})
	if err != nil {
		return err
	}
	defer clear(b)
	if err = v.Put(xAccountsRecord, b); err != nil {
		return err
	}
	return mirrorLegacyCookies(v, accounts[0])
}

// mirrorLegacyCookies keeps the single-account record in sync with account[0]
// purely for rollback safety: an older binary reads only that record.
func mirrorLegacyCookies(v *vault.Vault, a XAccount) error {
	b, err := json.Marshal(map[string]any{"cookies": []map[string]string{
		{"name": "auth_token", "value": a.AuthToken, "domain": ".x.com"},
		{"name": "ct0", "value": a.CT0, "domain": ".x.com"},
	}})
	if err != nil {
		return err
	}
	defer clear(b)
	return v.Put("cookies", b)
}
