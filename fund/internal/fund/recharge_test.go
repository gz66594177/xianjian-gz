package fund

// Phase 8 测试：人民币充值 95–107。全部用 stubVerifier（token "imtok-<uid>"），
// 管理接口用 test-admin-secret；mock 回调签名密钥为测试夹具 testMockSecret，
// 测试库 xianliao_fund_test（绝不指向生产库）；生产 fund.env 必须不开启 mock 开关。

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

const testMockSecret = "test-mock-secret-phase8"

func mockSign(orderID, channelNo string, amount int64, status string) string {
	mac := hmac.New(sha256.New, []byte(testMockSecret))
	mac.Write(MockCallbackPayload(orderID, channelNo, amount, status))
	return hex.EncodeToString(mac.Sum(nil))
}

func callCallback(t *testing.T, srv *Server, path string, body map[string]any) (int, apiResp) {
	t.Helper()
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", path, &buf)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var resp apiResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func createRechargeOK(t *testing.T, uid string, amount int64, extra map[string]any) RechargeOrder {
	t.Helper()
	body := map[string]any{"amount_cents": amount}
	for k, v := range extra {
		body[k] = v
	}
	code, resp := call(t, "POST", "/v1/recharges", uid, body, false)
	if code != 200 || !resp.OK {
		t.Fatalf("create recharge %s %d: code=%d err=%+v", uid, amount, code, resp.Error)
	}
	var o RechargeOrder
	if err := json.Unmarshal(resp.Data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func paidRecharge(t *testing.T, uid string, amount int64) RechargeOrder {
	t.Helper()
	o := createRechargeOK(t, uid, amount, nil)
	chNo := uniq("ch")
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": amount, "status": "paid",
		"signature": mockSign(o.OrderID, chNo, amount, "paid"),
	})
	if code != 200 || !resp.OK {
		t.Fatalf("paid callback: code=%d err=%+v", code, resp.Error)
	}
	var paid RechargeOrder
	if err := json.Unmarshal(resp.Data, &paid); err != nil {
		t.Fatal(err)
	}
	if paid.Status != RechargePaid || paid.PaidTxnID == nil {
		t.Fatalf("paid order status=%d paidTxn=%v", paid.Status, paid.PaidTxnID)
	}
	return paid
}

func rechargeDetail(t *testing.T, uid, orderID string) RechargeOrder {
	t.Helper()
	code, resp := call(t, "GET", "/v1/recharges/"+orderID, uid, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("recharge detail: code=%d err=%+v", code, resp.Error)
	}
	var o RechargeOrder
	if err := json.Unmarshal(resp.Data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func auditCount(t *testing.T, uid, action string) int {
	t.Helper()
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_audit_log WHERE user_id=$1 AND action=$2`, uid, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 95 创建订单：固定档位 / 自定义金额 / 非法金额拒绝 / 真实渠道未签约前拒绝 / 创建幂等
func Test95RechargeCreate(t *testing.T) {
	u := uniq("r95")
	mustWallet(t, u)
	fixed := createRechargeOK(t, u, 10000, nil) // 档位 ¥100
	if fixed.Status != RechargePending || fixed.Channel != "mock" || fixed.FeeCents != 0 || fixed.ActualCents != 10000 {
		t.Fatalf("fixed tier order: %+v", fixed)
	}
	custom := createRechargeOK(t, u, 12345, nil) // 自定义 ¥123.45
	if custom.Status != RechargePending || custom.ActualCents != 12345 {
		t.Fatalf("custom order: %+v", custom)
	}
	for _, amt := range []int64{0, -100} {
		code, _ := call(t, "POST", "/v1/recharges", u, map[string]any{"amount_cents": amt}, false)
		if code != 400 {
			t.Fatalf("amount %d: code=%d, want 400", amt, code)
		}
	}
	for _, ch := range []string{"alipay", "wechat"} {
		code, resp := call(t, "POST", "/v1/recharges", u, map[string]any{"amount_cents": 1000, "channel": ch}, false)
		if code != 400 || resp.Error.Code != "CHANNEL_NOT_AVAILABLE" {
			t.Fatalf("channel %s: code=%d err=%+v, want 400 CHANNEL_NOT_AVAILABLE", ch, code, resp.Error)
		}
	}
	key := uniq("idem")
	a := createRechargeOK(t, u, 5000, map[string]any{"idempotency_key": key})
	b := createRechargeOK(t, u, 5000, map[string]any{"idempotency_key": key})
	if a.OrderID != b.OrderID {
		t.Fatalf("idempotent create gave %s vs %s", a.OrderID, b.OrderID)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_order WHERE user_id=$1 AND order_type=1 AND client_idempotency_key=$2`, u, key).Scan(&n); err != nil || n != 1 {
		t.Fatalf("idempotent create rows=%d err=%v, want 1", n, err)
	}
}

// 96 回调验签通过入账：余额、分录合计 0、txn type 20、订单已支付
func Test96RechargeCallbackPaid(t *testing.T) {
	u := uniq("r96")
	mustWallet(t, u)
	o := createRechargeOK(t, u, 20000, nil)
	chNo := uniq("ch")
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(20000), "status": "paid",
		"signature": mockSign(o.OrderID, chNo, 20000, "paid"),
	})
	if code != 200 || !resp.OK {
		t.Fatalf("paid callback: code=%d err=%+v", code, resp.Error)
	}
	var paid RechargeOrder
	_ = json.Unmarshal(resp.Data, &paid)
	if paid.Status != RechargePaid || paid.PaidTxnID == nil || paid.ChannelOrderNo == nil || *paid.ChannelOrderNo != chNo {
		t.Fatalf("paid order: %+v", paid)
	}
	ledgerSumZero(t, *paid.PaidTxnID)
	var txnType int
	if err := testDB.QueryRow(`SELECT type FROM wallet_txn WHERE txn_id=$1`, *paid.PaidTxnID).Scan(&txnType); err != nil || txnType != TxnRecharge {
		t.Fatalf("paid txn type=%d err=%v, want 20", txnType, err)
	}
	b, _ := balance(t, u)
	if b != 20000 {
		t.Fatalf("balance=%d, want 20000", b)
	}
}

// 97 验签失败拒绝入账 + 未知订单回调 404（均不透露、不动资金）
func Test97RechargeCallbackBadSignature(t *testing.T) {
	u := uniq("r97")
	mustWallet(t, u)
	o := createRechargeOK(t, u, 5000, nil)
	chNo := uniq("ch")
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(5000), "status": "paid",
		"signature": "deadbeef",
	})
	if code != 401 || resp.Error.Code != "SIGNATURE_INVALID" {
		t.Fatalf("bad signature: code=%d err=%+v, want 401 SIGNATURE_INVALID", code, resp.Error)
	}
	// 篡改金额后签名不再匹配（签名覆盖金额字段）
	code, _ = callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(5001), "status": "paid",
		"signature": mockSign(o.OrderID, chNo, 5000, "paid"),
	})
	if code != 401 {
		t.Fatalf("tampered amount: code=%d, want 401", code)
	}
	// 未知订单 + 有效签名（签名覆盖 body 的渠道单号）：404，不入账
	unkCh := uniq("ch")
	code, _ = callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": "rch_nonexistent", "channel_order_no": unkCh, "amount_cents": int64(100), "status": "paid",
		"signature": mockSign("rch_nonexistent", unkCh, 100, "paid"),
	})
	if code != 404 {
		t.Fatalf("unknown order: code=%d, want 404", code)
	}
	after := rechargeDetail(t, u, o.OrderID)
	if after.Status != RechargePending {
		t.Fatalf("order status=%d, want 0 pending", after.Status)
	}
	b, _ := balance(t, u)
	if b != 0 {
		t.Fatalf("balance=%d, want 0", b)
	}
}

// 98 重复回调幂等：同一支付回调两次只入账一次
func Test98RechargeCallbackIdempotent(t *testing.T) {
	u := uniq("r98")
	mustWallet(t, u)
	o := createRechargeOK(t, u, 8000, nil)
	chNo := uniq("ch")
	body := map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(8000), "status": "paid",
		"signature": mockSign(o.OrderID, chNo, 8000, "paid"),
	}
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", body)
	if code != 200 || !resp.OK {
		t.Fatalf("first callback: code=%d err=%+v", code, resp.Error)
	}
	var first RechargeOrder
	_ = json.Unmarshal(resp.Data, &first)
	code, resp = callCallback(t, testSrv, "/v1/payments/callback/mock", body)
	if code != 200 || !resp.OK {
		t.Fatalf("replay callback: code=%d err=%+v", code, resp.Error)
	}
	var second RechargeOrder
	_ = json.Unmarshal(resp.Data, &second)
	if second.PaidTxnID == nil || *second.PaidTxnID != *first.PaidTxnID {
		t.Fatalf("replay txn %v vs %v", second.PaidTxnID, first.PaidTxnID)
	}
	b, _ := balance(t, u)
	if b != 8000 {
		t.Fatalf("balance=%d, want 8000 (credited once)", b)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE user_id=$1 AND type=20 AND ref_id=$2`, u, o.OrderID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("type20 txn rows=%d err=%v, want 1", n, err)
	}
}

// 99 金额不一致的回调拒绝入账并记审计（订单不动、资金不动）
func Test99RechargeCallbackAmountMismatch(t *testing.T) {
	u := uniq("r99")
	mustWallet(t, u)
	o := createRechargeOK(t, u, 10000, nil)
	chNo := uniq("ch")
	// 签名按回调金额算（验签通过），但回调金额 ≠ 订单金额 → 业务层拒绝
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(9999), "status": "paid",
		"signature": mockSign(o.OrderID, chNo, 9999, "paid"),
	})
	if code != 400 || resp.Error.Code != "CALLBACK_AMOUNT_MISMATCH" {
		t.Fatalf("amount mismatch: code=%d err=%+v, want 400 CALLBACK_AMOUNT_MISMATCH", code, resp.Error)
	}
	after := rechargeDetail(t, u, o.OrderID)
	if after.Status != RechargePending || after.PaidTxnID != nil {
		t.Fatalf("order after mismatch: %+v", after)
	}
	b, _ := balance(t, u)
	if b != 0 {
		t.Fatalf("balance=%d, want 0", b)
	}
	if auditCount(t, u, "recharge_callback_amount_mismatch") < 1 {
		t.Fatalf("amount mismatch audit row missing")
	}
}

// 100 支付失败回调：订单已失败、不入账；失败后支付回调为非法流转
func Test100RechargeCallbackFailed(t *testing.T) {
	u := uniq("r100")
	mustWallet(t, u)
	o := createRechargeOK(t, u, 6000, nil)
	chNo := uniq("ch")
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(6000), "status": "failed",
		"signature": mockSign(o.OrderID, chNo, 6000, "failed"), "failure_reason": "user cancelled",
	})
	if code != 200 || !resp.OK {
		t.Fatalf("failed callback: code=%d err=%+v", code, resp.Error)
	}
	var failed RechargeOrder
	_ = json.Unmarshal(resp.Data, &failed)
	if failed.Status != RechargeFailed || failed.PaidTxnID != nil {
		t.Fatalf("failed order: %+v", failed)
	}
	b, _ := balance(t, u)
	if b != 0 {
		t.Fatalf("balance=%d, want 0", b)
	}
	// 失败后重放失败回调：幂等返回同一状态
	code, _ = callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(6000), "status": "failed",
		"signature": mockSign(o.OrderID, chNo, 6000, "failed"),
	})
	if code != 200 {
		t.Fatalf("failed replay: code=%d, want 200", code)
	}
	// 失败后支付回调：非法流转 409，仍失败、无入账
	code, resp = callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(6000), "status": "paid",
		"signature": mockSign(o.OrderID, chNo, 6000, "paid"),
	})
	if code != 409 || resp.Error.Code != "INVALID_TRANSITION" {
		t.Fatalf("failed->paid: code=%d err=%+v, want 409 INVALID_TRANSITION", code, resp.Error)
	}
	if rechargeDetail(t, u, o.OrderID).Status != RechargeFailed {
		t.Fatalf("order left failed state after illegal transition")
	}
}

// 101 退款回调扣回与重复退款幂等（仅 mock 渠道演练）
func Test101RechargeRefund(t *testing.T) {
	u := uniq("r101")
	mustWallet(t, u)
	paid := paidRecharge(t, u, 15000)
	chNo := *paid.ChannelOrderNo
	body := map[string]any{
		"order_id": paid.OrderID, "channel_order_no": chNo, "amount_cents": int64(15000), "status": "refunded",
		"signature": mockSign(paid.OrderID, chNo, 15000, "refunded"),
	}
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", body)
	if code != 200 || !resp.OK {
		t.Fatalf("refund callback: code=%d err=%+v", code, resp.Error)
	}
	var refunded RechargeOrder
	_ = json.Unmarshal(resp.Data, &refunded)
	if refunded.Status != RechargeRefunded || refunded.RefundTxnID == nil {
		t.Fatalf("refunded order: %+v", refunded)
	}
	ledgerSumZero(t, *refunded.RefundTxnID)
	var txnType int
	if err := testDB.QueryRow(`SELECT type FROM wallet_txn WHERE txn_id=$1`, *refunded.RefundTxnID).Scan(&txnType); err != nil || txnType != TxnRechargeRefund {
		t.Fatalf("refund txn type=%d err=%v, want 21", txnType, err)
	}
	b, _ := balance(t, u)
	if b != 0 {
		t.Fatalf("balance=%d, want 0 after refund", b)
	}
	// 重复退款回调：幂等，不再扣款
	code, _ = callCallback(t, testSrv, "/v1/payments/callback/mock", body)
	if code != 200 {
		t.Fatalf("refund replay: code=%d, want 200", code)
	}
	b, _ = balance(t, u)
	if b != 0 {
		t.Fatalf("balance=%d after refund replay, want 0", b)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE user_id=$1 AND type=21 AND ref_id=$2`, u, paid.OrderID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("type21 txn rows=%d err=%v, want 1", n, err)
	}
}

// 102 退款时余额不足：整体失败、原订单与余额不动、记审计
func Test102RechargeRefundInsufficient(t *testing.T) {
	u := uniq("r102")
	mustWallet(t, u)
	paid := paidRecharge(t, u, 10000)
	// 花掉 8000，只剩 2000，不够退 10000
	code, _ := call(t, "POST", "/v1/ledger/expense", u, map[string]any{"amount_cents": 8000, "idempotency_key": uniq("idem")}, false)
	if code != 200 {
		t.Fatalf("expense: code=%d", code)
	}
	chNo := *paid.ChannelOrderNo
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": paid.OrderID, "channel_order_no": chNo, "amount_cents": int64(10000), "status": "refunded",
		"signature": mockSign(paid.OrderID, chNo, 10000, "refunded"),
	})
	if code != 409 || resp.Error.Code != "REFUND_INSUFFICIENT" {
		t.Fatalf("refund insufficient: code=%d err=%+v, want 409 REFUND_INSUFFICIENT", code, resp.Error)
	}
	after := rechargeDetail(t, u, paid.OrderID)
	if after.Status != RechargePaid || after.RefundTxnID != nil {
		t.Fatalf("order after failed refund: %+v", after)
	}
	b, _ := balance(t, u)
	if b != 2000 {
		t.Fatalf("balance=%d, want 2000 unchanged", b)
	}
	if auditCount(t, u, "recharge_refund_failed_insufficient") < 1 {
		t.Fatalf("refund insufficient audit row missing")
	}
}

// 103 订单状态非法流转拒绝：待支付退款、已支付再失败、已退款再支付
func Test103RechargeIllegalTransition(t *testing.T) {
	u := uniq("r103")
	mustWallet(t, u)
	pending := createRechargeOK(t, u, 3000, nil)
	chNo := uniq("ch")
	code, resp := callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": pending.OrderID, "channel_order_no": chNo, "amount_cents": int64(3000), "status": "refunded",
		"signature": mockSign(pending.OrderID, chNo, 3000, "refunded"),
	})
	if code != 409 || resp.Error.Code != "INVALID_TRANSITION" {
		t.Fatalf("pending->refunded: code=%d err=%+v, want 409", code, resp.Error)
	}
	paid := paidRecharge(t, u, 4000)
	code, resp = callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": paid.OrderID, "channel_order_no": *paid.ChannelOrderNo, "amount_cents": int64(4000), "status": "failed",
		"signature": mockSign(paid.OrderID, *paid.ChannelOrderNo, 4000, "failed"),
	})
	if code != 409 || resp.Error.Code != "INVALID_TRANSITION" {
		t.Fatalf("paid->failed: code=%d err=%+v, want 409", code, resp.Error)
	}
	if rechargeDetail(t, u, paid.OrderID).Status != RechargePaid {
		t.Fatalf("paid order left paid state after illegal failed callback")
	}
	// 已退款后再支付：非法
	paid2 := paidRecharge(t, u, 2000)
	chNo2 := *paid2.ChannelOrderNo
	code, _ = callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": paid2.OrderID, "channel_order_no": chNo2, "amount_cents": int64(2000), "status": "refunded",
		"signature": mockSign(paid2.OrderID, chNo2, 2000, "refunded"),
	})
	if code != 200 {
		t.Fatalf("refund for setup: code=%d", code)
	}
	code, resp = callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": paid2.OrderID, "channel_order_no": chNo2, "amount_cents": int64(2000), "status": "paid",
		"signature": mockSign(paid2.OrderID, chNo2, 2000, "paid"),
	})
	if code != 409 || resp.Error.Code != "INVALID_TRANSITION" {
		t.Fatalf("refunded->paid: code=%d err=%+v, want 409", code, resp.Error)
	}
}

// 104 用户权限隔离：他人查不到订单、无 token 401
func Test104RechargePermissionIsolation(t *testing.T) {
	a := uniq("r104a")
	b := uniq("r104b")
	mustWallet(t, a)
	mustWallet(t, b)
	o := createRechargeOK(t, a, 7000, nil)
	code, _ := call(t, "GET", "/v1/recharges/"+o.OrderID, b, nil, false)
	if code != 404 {
		t.Fatalf("other user detail: code=%d, want 404", code)
	}
	code, resp := call(t, "GET", "/v1/recharges", b, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("other user list: code=%d", code)
	}
	if strings.Contains(string(resp.Data), o.OrderID) {
		t.Fatalf("other user list leaks order %s", o.OrderID)
	}
	// 坏 token 须发原始请求（call 助手会再拼 imtok- 前缀，无法表达无效 token）
	req := httptest.NewRequest("GET", "/v1/recharges/"+o.OrderID, nil)
	req.Header.Set("Authorization", "Bearer imtok-bad")
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("bad token detail: code=%d, want 401", rec.Code)
	}
}

// 105 管理端订单查询：列表过滤（用户/状态/渠道）、详情、排除提现单、无密钥 401
func Test105RechargeAdminQuery(t *testing.T) {
	u := uniq("r105")
	mustWallet(t, u)
	pending := createRechargeOK(t, u, 9000, nil)
	paid := paidRecharge(t, u, 11000)
	seedWithdrawOrder(t, uniq("wd"), u, 2, 9000)
	code, resp := callAdminOp(t, "GET", "/v1/admin/recharges?user_id="+u, "admin1", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("admin list: code=%d err=%+v", code, resp.Error)
	}
	if !strings.Contains(string(resp.Data), pending.OrderID) || !strings.Contains(string(resp.Data), paid.OrderID) {
		t.Fatalf("admin list missing recharge orders: %s", string(resp.Data))
	}
	// 状态过滤：只要已支付
	code, resp = callAdminOp(t, "GET", "/v1/admin/recharges?user_id="+u+"&status=1", "admin1", nil)
	if code != 200 || !strings.Contains(string(resp.Data), paid.OrderID) || strings.Contains(string(resp.Data), pending.OrderID) {
		t.Fatalf("admin status filter: code=%d data=%s", code, string(resp.Data))
	}
	// 详情 + 提现单详情应 400（非充值单）
	code, resp = callAdminOp(t, "GET", "/v1/admin/recharges/"+paid.OrderID, "admin1", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("admin detail: code=%d err=%+v", code, resp.Error)
	}
	var wdID string
	if err := testDB.QueryRow(`SELECT order_id FROM wallet_order WHERE user_id=$1 AND order_type=2 ORDER BY created_at DESC LIMIT 1`, u).Scan(&wdID); err != nil {
		t.Fatal(err)
	}
	code, _ = callAdminOp(t, "GET", "/v1/admin/recharges/"+wdID, "admin1", nil)
	if code != 400 {
		t.Fatalf("admin detail of withdraw order: code=%d, want 400", code)
	}
	// 提现单不在充值列表里（按 order_type 过滤）
	code, resp = callAdminOp(t, "GET", "/v1/admin/recharges?user_id="+u, "admin1", nil)
	if strings.Contains(string(resp.Data), wdID) {
		t.Fatalf("admin recharge list leaks withdraw order %s", wdID)
	}
	// 无密钥 401
	code, _ = callAdminRaw(t, "GET", "/v1/admin/recharges?user_id="+u, "", "")
	if code != 401 {
		t.Fatalf("admin list without secret: code=%d, want 401", code)
	}
}

// 106 Mock 开关关闭时 mock 回调入口不可用（404）；真实渠道回调骨架 501；未知渠道 404
func Test106RechargeMockSwitchOff(t *testing.T) {
	offSrv := NewServer(Config{Addr: "127.0.0.1:0", AdminSecret: testAdmin, TokenVerifier: stubVerifier{}}, testDB)
	u := uniq("r106")
	mustWallet(t, u)
	o := createRechargeOK(t, u, 5000, nil)
	chNo := uniq("ch")
	code, _ := callCallback(t, offSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": o.OrderID, "channel_order_no": chNo, "amount_cents": int64(5000), "status": "paid",
		"signature": mockSign(o.OrderID, chNo, 5000, "paid"),
	})
	if code != 404 {
		t.Fatalf("mock-off callback: code=%d, want 404", code)
	}
	// 真实渠道回调：验签骨架未配置 → 501，绝不入账
	for _, ch := range []string{"alipay", "wechat"} {
		code, resp := callCallback(t, testSrv, "/v1/payments/callback/"+ch, map[string]any{"order_id": o.OrderID})
		if code != 501 || resp.Error.Code != "CHANNEL_NOT_CONFIGURED" {
			t.Fatalf("channel %s callback: code=%d err=%+v, want 501 CHANNEL_NOT_CONFIGURED", ch, code, resp.Error)
		}
	}
	code, _ = callCallback(t, testSrv, "/v1/payments/callback/unknownpay", map[string]any{"order_id": o.OrderID})
	if code != 404 {
		t.Fatalf("unknown channel callback: code=%d, want 404", code)
	}
	if rechargeDetail(t, u, o.OrderID).Status != RechargePending {
		t.Fatalf("order changed by disabled/skeleton callbacks")
	}
}

// 107 账务守恒：多笔充值 + 退款后对账 diff=0，全部 20/21 txn 分录合计为 0
func Test107RechargeLedgerConservation(t *testing.T) {
	u := uniq("r107")
	mustWallet(t, u)
	p1 := paidRecharge(t, u, 20000)
	_ = paidRecharge(t, u, 30000)
	p3 := paidRecharge(t, u, 10000)
	chNo := *p3.ChannelOrderNo
	code, _ := callCallback(t, testSrv, "/v1/payments/callback/mock", map[string]any{
		"order_id": p3.OrderID, "channel_order_no": chNo, "amount_cents": int64(10000), "status": "refunded",
		"signature": mockSign(p3.OrderID, chNo, 10000, "refunded"),
	})
	if code != 200 {
		t.Fatalf("refund: code=%d", code)
	}
	b, _ := balance(t, u)
	if b != 50000 {
		t.Fatalf("balance=%d, want 50000 (20000+30000+10000-10000)", b)
	}
	ledgerSumZero(t, *p1.PaidTxnID)
	code, resp := call(t, "POST", "/v1/admin/reconcile", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("reconcile: code=%d err=%+v", code, resp.Error)
	}
	var rec ReconcileResult
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.DiffCents != 0 || rec.Status != 0 {
		t.Fatalf("reconcile diff=%d status=%d, want 0/0", rec.DiffCents, rec.Status)
	}
	rows, err := testDB.Query(`SELECT txn_id FROM wallet_txn WHERE user_id=$1 AND type IN (20,21)`, u)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 4 {
		t.Fatalf("20/21 txn count=%d, want 4 (3 paid + 1 refund)", len(ids))
	}
	for _, id := range ids {
		ledgerSumZero(t, id)
	}
}
