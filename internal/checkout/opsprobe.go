package checkout

import (
	"context"
	"errors"
	"fmt"

	"xgift/internal/vault"
)

// ProbeUser is intentionally not a real account. PremiumGiftingQuery is a
// read-only operation, and a name that cannot resolve still proves the
// identifier is live: X answers "no such user" with a populated data envelope,
// while a renamed operation is answered with an error envelope. That difference
// is the whole probe.
const ProbeUser = "xgift_ops_probe_no_such_user"

// ProbeReport is the outcome for a single operation.
type ProbeReport struct {
	Operation string `json:"operation"`
	// Identifier is the value actually in force right now.
	Identifier string `json:"identifier"`
	// OK means X executed the query and answered.
	OK bool `json:"ok"`
	// GraphQLError means X returned an error envelope. It does NOT by itself
	// prove the identifier is stale: X uses the same envelope for an unknown
	// operation and for account-level refusals such as rate limiting.
	GraphQLError bool `json:"graphql_error,omitempty"`
	// Inconclusive means the probe could not reach a verdict, typically a
	// transport, credential or proxy fault. Never reported as a stale
	// identifier, so an operator is not sent to edit configuration when the
	// real problem is the network.
	Inconclusive bool   `json:"inconclusive,omitempty"`
	Probeable    bool   `json:"probeable"`
	Explanation  string `json:"explanation"`
}

// ProbeIdentifier reports whether the identifier currently in force for
// PremiumGiftingQuery is still accepted by X.
//
// It creates no checkout, submits no payment, and never touches a real
// recipient. It still needs a working proxy path and valid cookies.
func ProbeIdentifier(ctx context.Context, v *vault.Vault, port int) (ProbeReport, error) {
	c, err := newXClient(v, port)
	if err != nil {
		return ProbeReport{}, err
	}
	defer c.close()

	r := ProbeReport{Operation: "PremiumGiftingQuery", Identifier: c.premiumGiftingOp(), Probeable: true}
	_, idErr := c.identity(ctx, ProbeUser, false)
	switch {
	case idErr == nil:
		r.OK = true
		r.Explanation = "标识有效：X 正常响应并解析了该用户名"
	case errors.Is(idErr, ErrUserNotFound):
		// The expected outcome. X executed the operation and found no account,
		// which is only possible when the identifier is recognised.
		r.OK = true
		r.Explanation = "标识有效：X 正常执行查询，仅因探测用户名不存在而返回空结果"
	case errors.Is(idErr, ErrOperationRejected):
		r.GraphQLError = true
		r.Explanation = "X 返回 GraphQL 错误信封。两种可能：① 标识已随 X 前端重新发布而失效（去 x.com 抓新的 queryId 后用 xgift ops set 更新）；② 账号被限流或拒绝（换个时间重试，先别改配置）"
	case errors.Is(idErr, ErrXReadFailure):
		r.Inconclusive = true
		r.Explanation = "无法判定：X 读取失败但不是 GraphQL 错误，请先检查网络、代理与 Cookie"
	default:
		r.Inconclusive = true
		r.Explanation = fmt.Sprintf("无法判定：%v", idErr)
	}
	return r, nil
}

// ProbeSummary describes what can and cannot be verified without spending
// money or creating a session.
type ProbeSummary struct {
	Identifier                      string `json:"identifier"`
	PremiumGiftingQuery             string `json:"premium_gifting_query"`
	SubscriptionProductDetailsQuery string `json:"subscription_product_details_query"`
	OneTimePurchaseGiftMutation     string `json:"one_time_purchase_gift_mutation"`
	Overridden                      bool   `json:"overridden"`
}

// Describe returns the identifiers in force plus the honest reason one of them
// cannot be probed in isolation.
func Describe(v *vault.Vault) (ProbeSummary, error) {
	ops, err := CurrentOps(v)
	if err != nil {
		return ProbeSummary{}, err
	}
	return ProbeSummary{
		Identifier:                      ops.PremiumGifting,
		PremiumGiftingQuery:             ops.PremiumGifting,
		SubscriptionProductDetailsQuery: ops.ProductDetails,
		OneTimePurchaseGiftMutation:     ops.OneTimeGiftMutation,
		Overridden:                      ops.Overridden,
	}, nil
}
