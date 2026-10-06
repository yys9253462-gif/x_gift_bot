package checkout

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"xgift/internal/vault"
)

func newOpsVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()
	pw := filepath.Join(dir, "password")
	if e := os.WriteFile(pw, []byte(strings.Repeat("p", 32)), 0600); e != nil {
		t.Fatal(e)
	}
	v, e := vault.Open(filepath.Join(dir, "vault.db"), pw, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { v.Close() })
	return v
}

func TestOpsDefaultsWhenNoOverrideExists(t *testing.T) {
	v := newOpsVault(t)
	// No x-graphql-ops record at all: this must resolve to the compiled-in
	// table instead of failing, because every existing deployment starts here.
	got, e := CurrentOps(v)
	if e != nil {
		t.Fatalf("CurrentOps: %v", e)
	}
	if got.PremiumGifting != DefaultOpPremiumGifting || got.ProductDetails != DefaultOpProductDetails || got.OneTimeGiftMutation != DefaultOpOneTimeGiftMutation {
		t.Fatalf("unexpected defaults: %+v", got)
	}
	if got.Overridden {
		t.Fatal("Overridden must be false when no record exists")
	}
}

func TestOpsOverrideTakesEffectWithoutRestart(t *testing.T) {
	v := newOpsVault(t)
	const fresh = "aBcDeFgHiJkLmNoPqRsTuV"
	if e := SaveOps(v, Ops{PremiumGifting: fresh}); e != nil {
		t.Fatalf("SaveOps: %v", e)
	}
	got, e := CurrentOps(v)
	if e != nil {
		t.Fatalf("CurrentOps: %v", e)
	}
	if got.PremiumGifting != fresh {
		t.Fatalf("override not in force: got %q", got.PremiumGifting)
	}
	if !got.Overridden {
		t.Fatal("Overridden must be true once a record exists")
	}
	// Partial updates must not disturb the untouched operations.
	if got.ProductDetails != DefaultOpProductDetails || got.OneTimeGiftMutation != DefaultOpOneTimeGiftMutation {
		t.Fatalf("partial update clobbered other operations: %+v", got)
	}
	// And a client built now must send the override.
	c := &xClient{}
	if c.premiumGiftingOp() != DefaultOpPremiumGifting {
		t.Fatal("a client with no table must fall back to the default")
	}
	c.ops = got
	if c.premiumGiftingOp() != fresh {
		t.Fatalf("client did not adopt the override: %q", c.premiumGiftingOp())
	}
}

func TestOpsValidateRejectsMalformedIdentifiers(t *testing.T) {
	tests := []struct {
		name  string
		ops   Ops
		valid bool
	}{
		{"empty is allowed", Ops{}, true},
		{"typical base64url", Ops{PremiumGifting: "kn8hCE6bHstQV2MtfYDTKg"}, true},
		{"dashes and underscores", Ops{ProductDetails: "a-b_c123456789"}, true},
		{"too short", Ops{PremiumGifting: "short"}, false},
		{"too long", Ops{PremiumGifting: strings.Repeat("a", 65)}, false},
		{"path separator", Ops{PremiumGifting: "aaaaaaaa/bbbb"}, false},
		{"space", Ops{PremiumGifting: "aaaaaaaa bbbb"}, false},
		{"empty string after trim is not a default", Ops{OneTimeGiftMutation: "   "}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := tt.ops.Validate()
			if tt.valid && e != nil {
				t.Fatalf("expected valid, got %v", e)
			}
			if !tt.valid && e == nil {
				t.Fatal("expected rejection, got nil")
			}
		})
	}
}

func TestSaveOpsRefusesToStoreInvalidTable(t *testing.T) {
	v := newOpsVault(t)
	if e := SaveOps(v, Ops{PremiumGifting: "bad/value"}); e == nil {
		t.Fatal("SaveOps must reject a malformed identifier")
	}
	// Nothing may be persisted by a rejected write.
	got, e := CurrentOps(v)
	if e != nil {
		t.Fatal(e)
	}
	if got.PremiumGifting != DefaultOpPremiumGifting {
		t.Fatal("a rejected write left partial state behind")
	}
}

// The probe is the operator's only way to tell a rebuilt upstream from an
// ordinary failure. Each branch must be classified correctly, otherwise the
// tool misleads exactly when it is needed — and, worse, sends an operator to
// edit configuration when the real fault is the network.
func TestGraphQLErrorEnvelopeIsFlaggedNotCalledStale(t *testing.T) {
	v := newOpsVault(t)
	c := &xClient{vault: v, headers: http.Header{}, http: &http.Client{Transport: mockXTransport(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"errors":[{"message":"Unknown operation"}]}`), nil
	})}}
	c.regionalHTTP = c.http
	c.ops = ResolvedOps{PremiumGifting: DefaultOpPremiumGifting}

	_, err := c.identity(context.Background(), ProbeUser, false)
	if !errors.Is(err, ErrOperationRejected) {
		t.Fatalf("a GraphQL error envelope must surface as ErrOperationRejected, got %v", err)
	}
	// The point of the whole feature: this must be distinguishable from a
	// transport fault, otherwise the operator cannot tell what to fix.
	if errors.Is(err, ErrXReadFailure) {
		t.Fatal("ErrOperationRejected must not also satisfy ErrXReadFailure")
	}
}

// X reports an account-level gifting refusal inside the same GraphQL error
// envelope it uses for a renamed operation. Classifying that as "identifier may
// be stale" sends an operator to re-scrape operation IDs while the real problem
// is the account, so code 37 must have its own error.
func TestGiftingAuthorisationRefusalIsNotCalledStale(t *testing.T) {
	v := newOpsVault(t)
	c := &xClient{vault: v, headers: http.Header{}, http: &http.Client{Transport: mockXTransport(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{"data":{},"errors":[{"code":37,"extensions":{"code":37,"kind":"Permissions","name":"AuthorizationError","source":"Client"},"kind":"Permissions","locations":[{"column":3,"line":2}],"message":"Authorization: Current user is not eligible to gift","name":"AuthorizationError","path":["onetimepurchase_gift"],"source":"Client"}]}`), nil
	})}}
	c.regionalHTTP = c.http
	c.ops = ResolvedOps{PremiumGifting: DefaultOpPremiumGifting}

	_, err := c.identity(context.Background(), ProbeUser, false)
	if !errors.Is(err, ErrGiftNotAuthorised) {
		t.Fatalf("code 37 must surface as ErrGiftNotAuthorised, got %v", err)
	}
	if errors.Is(err, ErrOperationRejected) {
		t.Fatal("an account refusal must not be reported as a stale operation identifier")
	}
	if !strings.Contains(err.Error(), "not eligible to gift") {
		t.Fatalf("the upstream message must be preserved, got %v", err)
	}
}

func TestTransportFailureIsNotMistakenForAStaleIdentifier(t *testing.T) {
	v := newOpsVault(t)
	c := &xClient{vault: v, headers: http.Header{}, http: &http.Client{Transport: mockXTransport(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: proxy refused the connection")
	})}}
	c.regionalHTTP = c.http
	c.ops = ResolvedOps{PremiumGifting: DefaultOpPremiumGifting}

	_, err := c.identity(context.Background(), ProbeUser, false)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	// This is the failure mode that would send an operator to update the
	// identifiers when the network is what is actually broken.
	if errors.Is(err, ErrOperationRejected) {
		t.Fatal("a transport failure must never be reported as an operation rejection")
	}
}

func TestProbeUserIsNotARealAccount(t *testing.T) {
	// A regression guard: the probe must never be pointed at a real recipient.
	if ProbeUser == "" || !strings.HasPrefix(ProbeUser, "xgift_ops_probe") {
		t.Fatalf("probe user changed and may address a real account: %q", ProbeUser)
	}
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"fixture"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
