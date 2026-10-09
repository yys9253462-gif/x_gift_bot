package site

// 商城「直接开通」API 的测试。
//
// 与发码通道相比，这条通道的风险等级更高：开通一旦成功，Premium 就落在
// 买家账号上了，事后无法收回。所以这里重点覆盖三类行为：
//   1. 参数把关：用户名格式、套餐月数、订单号长度必须在建任务之前就挡掉；
//   2. 幂等：同一订单号只能产生一个开通任务，重复调用返回同一单的进度；
//   3. 冲突：同一订单号换用户名或换套餐必须拒绝，不能静默按新参数开通。
//
// 注意：这里不测"真的去 X 下单付款"——那需要真实的 X 凭据与支付卡。
// 送出之前的全部判断（资格、暂停、参数）都能覆盖，实际赠送属集成测试范畴。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// provisionFixture 复用 partnerFixture，并补上开通账本表。
func provisionFixture(t *testing.T) *server {
	t.Helper()
	s := partnerFixture(t)
	if err := migrateProvision(s.db); err != nil {
		t.Fatal(err)
	}
	return s
}

// provisionPost 直接调用开通处理器，绕过网络但保留中间件与解析约定。
func provisionPost(t *testing.T, s *server, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	// verify 与 provision 的状态查询用 GET，其余用 POST。
	if strings.HasSuffix(path, "?status") {
		path = strings.TrimSuffix(path, "?status")
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		s.partner(s.provisionStatus)(w, r)
		return w
	}
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	switch path {
	case "/api/partner/provision":
		s.partner(s.partnerProvision)(w, r)
	case "/api/partner/verify":
		s.partner(s.partnerVerify)(w, r)
	default:
		t.Fatalf("unknown path %q", path)
	}
	return w
}

// TestProvisionRejectsBadInput 覆盖建任务之前就应当挡掉的参数。
// 这些判断放在最前面是有意的：一旦任务建起来，后台就会真的去花钱。
func TestProvisionRejectsBadInput(t *testing.T) {
	s := provisionFixture(t)
	key := configurePartner(t, s)

	cases := []struct {
		name string
		body string
	}{
		{"订单号为空", `{"order_id":"","username":"jack","months":3}`},
		{"订单号过长", `{"order_id":"` + strings.Repeat("x", 121) + `","username":"jack","months":3}`},
		{"用户名为空", `{"order_id":"P-1","username":"","months":3}`},
		{"用户名含大写以外的非法字符", `{"order_id":"P-2","username":"jack doe","months":3}`},
		{"用户名带 @ 前缀后仍非法", `{"order_id":"P-3","username":"@jack!","months":3}`},
		{"用户名过长", `{"order_id":"P-4","username":"` + strings.Repeat("a", 16) + `","months":3}`},
		{"套餐不支持", `{"order_id":"P-5","username":"jack","months":12}`},
		{"套餐为 0", `{"order_id":"P-6","username":"jack","months":0}`},
		{"备注过长", `{"order_id":"P-7","username":"jack","months":3,"reference":"` + strings.Repeat("y", 201) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := provisionPost(t, s, "/api/partner/provision", key, tc.body)
			if w.Code != 400 {
				t.Fatalf("期望 400，实际 %d：%s", w.Code, w.Body.String())
			}
		})
	}
}

// TestProvisionAcceptsAtPrefix 前端常把 @ 一起复制过来，必须能容忍。
func TestProvisionAcceptsAtPrefix(t *testing.T) {
	s := provisionFixture(t)
	key := configurePartner(t, s)
	// 把付款保护置为暂停，让请求停在"建任务之前"的那道闸上。
	// 这样既能证明 @ 被正确剥离、用户名格式检查通过（否则是 400），
	// 又不会真的去调用 X。
	if err := s.vault.Put("payment-control", []byte(`{"paused":true,"reason":"test"}`)); err != nil {
		t.Fatal(err)
	}
	w := provisionPost(t, s, "/api/partner/provision", key, `{"order_id":"P-AT","username":"@Jack_99","months":6}`)
	if w.Code != 503 {
		t.Fatalf("@ 前缀应被接受并走到暂停判断，期望 503，实际 %d：%s", w.Code, w.Body.String())
	}
}

// TestProvisionRequiresAuth 没密钥一律 401，且不得因此建出任务。
func TestProvisionRequiresAuth(t *testing.T) {
	s := provisionFixture(t)
	configurePartner(t, s)

	for _, path := range []string{"/api/partner/provision", "/api/partner/verify"} {
		w := provisionPost(t, s, path, "", `{"order_id":"P-AUTH","username":"jack","months":3}`)
		if w.Code != 401 {
			t.Fatalf("%s 无密钥应 401，实际 %d", path, w.Code)
		}
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM partner_provisions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("未授权的请求不得建出任务，实际有 %d 条", n)
	}
}

// TestProvisionClaimIsIdempotent 同一订单号重复调用只能有一条账本记录，
// 且第二次调用必须复用同一任务而不是新建。
func TestProvisionClaimIsIdempotent(t *testing.T) {
	s := provisionFixture(t)
	key := configurePartner(t, s)

	body := `{"order_id":"P-IDEM","username":"jack","months":6}`
	// 先手动建任务（跳过真实赠送），模拟"任务已存在"的状态。
	row, created, err := s.claimProvision(httptest.NewRequest(http.MethodPost, "/", nil).Context(), provisionRequest{OrderID: "P-IDEM", Username: "jack", Months: 6})
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("首次认领应当 created=true")
	}
	if row.OrderID != "P-IDEM" {
		t.Fatalf("订单号不符：%s", row.OrderID)
	}
	// 再次认领：必须 created=false，且仍指向同一条。
	_, created2, err := s.claimProvision(httptest.NewRequest(http.MethodPost, "/", nil).Context(), provisionRequest{OrderID: "P-IDEM", Username: "jack", Months: 6})
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatal("重复认领必须 created=false，否则会重复开通")
	}
	var n int
	if err = s.db.QueryRow("SELECT COUNT(*) FROM partner_provisions WHERE order_id=?", "P-IDEM").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("同一订单号只能有 1 条记录，实际 %d 条", n)
	}
	_ = body
	_ = key
}

// TestProvisionClaimConflictsOnChange 同一订单号换用户名或换套餐必须报冲突。
// 这是防止"商城侧数据错了却悄悄按新参数开通"的最后一道闸。
func TestProvisionClaimConflictsOnChange(t *testing.T) {
	s := provisionFixture(t)
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()

	if _, _, err := s.claimProvision(ctx, provisionRequest{OrderID: "P-CONF", Username: "jack", Months: 6}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		req  provisionRequest
	}{
		{"换用户名", provisionRequest{OrderID: "P-CONF", Username: "jill", Months: 6}},
		{"换套餐", provisionRequest{OrderID: "P-CONF", Username: "jack", Months: 3}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := s.claimProvision(ctx, tc.req)
			if !errorsIs(err, errProvisionConflict) {
				t.Fatalf("期望冲突错误，实际 %v", err)
			}
		})
	}
	// 参数完全一致时应当放行（返回已存在记录，created=false）。
	row, created, err := s.claimProvision(ctx, provisionRequest{OrderID: "P-CONF", Username: "jack", Months: 6})
	if err != nil {
		t.Fatalf("参数一致不应报错：%v", err)
	}
	if created {
		t.Fatal("参数一致时不应重复创建")
	}
	if row.Months != 6 || row.Username != "jack" {
		t.Fatalf("回读的记录不符：%+v", row)
	}
}

// TestProvisionStatusUnknownOrder 查不存在的订单要明确 404，而不是空响应。
func TestProvisionStatusUnknownOrder(t *testing.T) {
	s := provisionFixture(t)
	key := configurePartner(t, s)

	r := httptest.NewRequest(http.MethodGet, "/api/partner/provision?order_id=NOPE", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.partner(s.provisionStatus)(w, r)
	if w.Code != 404 {
		t.Fatalf("期望 404，实际 %d：%s", w.Code, w.Body.String())
	}
}

// TestProvisionStatusReportsProgress 任务存在时应当返回当前状态与是否终态。
func TestProvisionStatusReportsProgress(t *testing.T) {
	s := provisionFixture(t)
	key := configurePartner(t, s)
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()

	if _, _, err := s.claimProvision(ctx, provisionRequest{OrderID: "P-PROG", Username: "jack", Months: 3}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/api/partner/provision?order_id=P-PROG", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.partner(s.provisionStatus)(w, r)
	if w.Code != 200 {
		t.Fatalf("期望 200，实际 %d：%s", w.Code, w.Body.String())
	}
	body := partnerBody(t, w)
	if body["status"] != provisionProcessing {
		t.Fatalf("刚建的任务应为 processing，实际 %v", body["status"])
	}
	// 刚建的任务尚未完成，商城据此知道还要继续轮询。
	if body["done"] != false {
		t.Fatalf("未完成任务 done 应为 false，实际 %v", body["done"])
	}
	if body["terminal"] != false {
		t.Fatalf("未完成任务 terminal 应为 false，实际 %v", body["terminal"])
	}
	if body["months"].(float64) != 3 {
		t.Fatalf("月份不符：%v", body["months"])
	}
}

// TestProvisionSucceededIsTerminal 成功后必须报 done 与 terminal，
// 商城据此停止轮询并发交付邮件。
func TestProvisionSucceededIsTerminal(t *testing.T) {
	s := provisionFixture(t)
	key := configurePartner(t, s)
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()

	if _, _, err := s.claimProvision(ctx, provisionRequest{OrderID: "P-DONE", Username: "jack", Months: 6}); err != nil {
		t.Fatal(err)
	}
	s.updateProvision("P-DONE", provisionSucceeded, "Premium 已成功开通。", 100)

	r := httptest.NewRequest(http.MethodGet, "/api/partner/provision?order_id=P-DONE", nil)
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	s.partner(s.provisionStatus)(w, r)
	body := partnerBody(t, w)
	if body["done"] != true {
		t.Fatalf("成功后 done 应为 true，实际 %v", body["done"])
	}
	if body["terminal"] != true {
		t.Fatalf("成功后 terminal 应为 true，实际 %v", body["terminal"])
	}
	if body["progress"].(float64) != 100 {
		t.Fatalf("成功后进度应为 100，实际 %v", body["progress"])
	}
}

// TestUpdateProvisionDoesNotDowngradeTerminal 迟到的进度回调不得把
// 已完成的任务改回处理中——那会让商城把已开通的订单误判为进行中。
func TestUpdateProvisionDoesNotDowngradeTerminal(t *testing.T) {
	s := provisionFixture(t)
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()

	if _, _, err := s.claimProvision(ctx, provisionRequest{OrderID: "P-LATE", Username: "jack", Months: 3}); err != nil {
		t.Fatal(err)
	}
	s.updateProvision("P-LATE", provisionSucceeded, "已开通。", 100)
	// 模拟一个迟到的进度回调。
	s.updateProvision("P-LATE", provisionProcessing, "正在处理。", 40)

	row, err := provisionByIDQuery(s.db, "P-LATE")
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != provisionSucceeded {
		t.Fatalf("终态不应被改回，实际 %s", row.Status)
	}
	if row.Progress != 100 {
		t.Fatalf("进度不应被回退，实际 %d", row.Progress)
	}
}

// TestVerifyRejectsMalformedUsername 校验接口的格式检查必须与开通一致，
// 否则商城会先被告知"可以"，付款后开通才失败。
func TestVerifyRejectsMalformedUsername(t *testing.T) {
	s := provisionFixture(t)
	key := configurePartner(t, s)

	cases := []string{
		`{"username":""}`,
		`{"username":"jack doe"}`,
		`{"username":"jack!"}`,
		`{"username":"` + strings.Repeat("a", 16) + `"}`,
	}
	for _, body := range cases {
		w := provisionPost(t, s, "/api/partner/verify", key, body)
		if w.Code != 400 {
			t.Fatalf("非法用户名应 400，实际 %d：%s", w.Code, w.Body.String())
		}
	}
}

// errorsIs 是 errors.Is 的局部包装，避免为本文件再引入一个导入。
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

var _ = partnerBody
