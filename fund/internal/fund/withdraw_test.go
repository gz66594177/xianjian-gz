package fund

// Phase 9 测试：人民币提现 7 态全流程（108–124）。全部用 stubVerifier（token "imtok-<uid>"），
// 管理接口用 test-admin-secret；测试注资用内部 income 接口（真实充值在 Phase 8，测试注资不走支付渠道）。
// 每项改配置的测试收尾必须复位提现配置（固定 0、费率 0、全部限额 null），避免污染后续测试。

import (
	"encoding/json"
	"testing"
)

func submitWithdrawCall(t *testing.T, uid, methodID string, amount int64, idem string) (int, apiResp) {
	t.Helper()
	body := map[string]any{"withdraw_method_id": methodID, "amount_cents": amount, "tx_password": testTxPwd}
	if idem != "" {
		body["idempotency_key"] = idem
	}
	return call(t, "POST", "/v1/withdrawals", uid, body, false)
}

func submitWithdrawOK(t *testing.T, uid, methodID string, amount int64, idem string) WithdrawOrder {
	t.Helper()
	code, resp := submitWithdrawCall(t, uid, methodID, amount, idem)
	if code != 200 || !resp.OK {
		t.Fatalf("submit withdraw %s %d: code=%d err=%+v", uid, amount, code, resp.Error)
	}
	var o WithdrawOrder
	if err := json.Unmarshal(resp.Data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func withdrawDetail(t *testing.T, uid, orderID string) (int, apiResp) {
	t.Helper()
	return call(t, "GET", "/v1/withdrawals/"+orderID, uid, nil, false)
}

func withdrawDetailOK(t *testing.T, uid, orderID string) WithdrawOrder {
	t.Helper()
	code, resp := withdrawDetail(t, uid, orderID)
	if code != 200 || !resp.OK {
		t.Fatalf("withdraw detail %s: code=%d err=%+v", orderID, code, resp.Error)
	}
	var o WithdrawOrder
	if err := json.Unmarshal(resp.Data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func setWithdrawConfig(t *testing.T, body map[string]any) WithdrawConfig {
	t.Helper()
	code, resp := callAdminOp(t, "PUT", "/v1/admin/withdraw-config", "admin1", body)
	if code != 200 || !resp.OK {
		t.Fatalf("set withdraw config: code=%d err=%+v", code, resp.Error)
	}
	var c WithdrawConfig
	if err := json.Unmarshal(resp.Data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func resetWithdrawConfig(t *testing.T) {
	t.Helper()
	setWithdrawConfig(t, map[string]any{
		"fixed_fee_cents": 0, "rate_bp": 0,
		"min_amount_cents": nil, "single_max_cents": nil, "daily_max_cents": nil, "daily_count_max": nil,
	})
}

func approveWithdraw(t *testing.T, orderID, adminID string) (int, apiResp) {
	t.Helper()
	return callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+orderID+"/approve", adminID, nil)
}

func approveWithdrawDual(t *testing.T, orderID string) {
	t.Helper()
	code, resp := approveWithdraw(t, orderID, "adminA")
	if code != 200 || !resp.OK {
		t.Fatalf("approve1 %s: code=%d err=%+v", orderID, code, resp.Error)
	}
	code, resp = approveWithdraw(t, orderID, "adminB")
	if code != 200 || !resp.OK {
		t.Fatalf("approve2 %s: code=%d err=%+v", orderID, code, resp.Error)
	}
}

func withdrawFlow(t *testing.T, orderID, action, adminID string, body map[string]any) (int, apiResp) {
	t.Helper()
	return callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+orderID+"/"+action, adminID, body)
}

func platformBalance(t *testing.T) int64 {
	t.Helper()
	var b int64
	if err := testDB.QueryRow(`SELECT balance_cents FROM wallet_account WHERE user_id='platform:settlement'`).Scan(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

func freezeRemaining(t *testing.T, freezeID string) (int64, int) {
	t.Helper()
	var rem int64
	var st int
	if err := testDB.QueryRow(`SELECT remaining_cents, status FROM wallet_freeze WHERE freeze_id=$1`, freezeID).Scan(&rem, &st); err != nil {
		t.Fatal(err)
	}
	return rem, st
}

func txnTypeSumZero(t *testing.T, txnType int) {
	t.Helper()
	rows, err := testDB.Query(`SELECT txn_id FROM wallet_txn WHERE type=$1`, txnType)
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
	for _, id := range ids {
		ledgerSumZero(t, id)
	}
}

// 108 提交成功：冻结金额 + 手续费（默认费率 0）、订单待审核、冻结行剩余 = 总额
func Test108WithdrawSubmitSuccess(t *testing.T) {
	u := uniq("w108")
	admitUser(t, u)
	fundUser(t, u, 10000)
	m := listMethodsOK(t, u)[0]
	o := submitWithdrawOK(t, u, m.MethodID, 4000, uniq("wd"))
	if o.Status != 0 || o.FeeCents != 0 || o.ActualCents != 4000 {
		t.Fatalf("order %+v, want status 0 fee 0 actual 4000", o)
	}
	b, f := balance(t, u)
	if b != 6000 || f != 4000 {
		t.Fatalf("balance=%d frozen=%d, want 6000/4000", b, f)
	}
	var freezeID string
	var txnID string
	if err := testDB.QueryRow(`SELECT freeze_id FROM wallet_order WHERE order_id=$1`, o.OrderID).Scan(&freezeID); err != nil {
		t.Fatal(err)
	}
	if err := testDB.QueryRow(`SELECT txn_id FROM wallet_txn WHERE ref_id=$1 AND type=22`, o.OrderID).Scan(&txnID); err != nil {
		t.Fatal(err)
	}
	ledgerSumZero(t, txnID)
	rem, st := freezeRemaining(t, freezeID)
	if rem != 4000 || st != 0 {
		t.Fatalf("freeze remaining=%d status=%d, want 4000/0", rem, st)
	}
}

// 109 余额不足：可用 < 金额 + 手续费时拒绝、余额不动、无订单行
func Test109WithdrawInsufficient(t *testing.T) {
	u := uniq("w109")
	admitUser(t, u)
	fundUser(t, u, 1000)
	m := listMethodsOK(t, u)[0]
	code, _ := submitWithdrawCall(t, u, m.MethodID, 1001, uniq("wd"))
	if code != 400 {
		t.Fatalf("insufficient submit code=%d, want 400", code)
	}
	b, f := balance(t, u)
	if b != 1000 || f != 0 {
		t.Fatalf("balance=%d frozen=%d, want 1000/0", b, f)
	}
	code, resp := call(t, "GET", "/v1/withdrawals", u, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("list code=%d", code)
	}
	var list []WithdrawOrder
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 0 {
		t.Fatalf("list len=%d, want 0 (no order row on insufficient)", len(list))
	}
}

// 110 错密码：403、余额不动、无订单
func Test110WithdrawWrongPassword(t *testing.T) {
	u := uniq("w110")
	admitUser(t, u)
	fundUser(t, u, 5000)
	m := listMethodsOK(t, u)[0]
	code, resp := call(t, "POST", "/v1/withdrawals", u, map[string]any{"withdraw_method_id": m.MethodID, "amount_cents": 1000, "tx_password": "000000"}, false)
	if code != 403 || resp.Error.Code != "TX_PASSWORD_ERROR" {
		t.Fatalf("wrong pwd code=%d err=%+v, want 403 TX_PASSWORD_ERROR", code, resp.Error)
	}
	b, f := balance(t, u)
	if b != 5000 || f != 0 {
		t.Fatalf("balance=%d frozen=%d, want 5000/0", b, f)
	}
}

// 111 未完成准入引导：无方式 / 有方式无密码均 403 引导、不扣款、不建单；补齐后可提
func Test111WithdrawAdmissionGuidance(t *testing.T) {
	u := uniq("w111")
	mustWallet(t, u)
	fundUser(t, u, 5000)
	code, resp := submitWithdrawCall(t, u, "mtd_nonexistent", 1000, uniq("wd"))
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("no-method code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	m := addMethodOK(t, u, MethodAlipay, "Test User", "testuser@alipay.com", nil)
	code, resp = submitWithdrawCall(t, u, m.MethodID, 1000, uniq("wd"))
	if code != 403 || resp.Error.Code != "ADMISSION_NOT_SATISFIED" {
		t.Fatalf("no-pwd code=%d err=%+v, want 403 ADMISSION_NOT_SATISFIED", code, resp.Error)
	}
	b, f := balance(t, u)
	if b != 5000 || f != 0 {
		t.Fatalf("balance=%d frozen=%d, want 5000/0", b, f)
	}
	setTxPwd(t, u, testTxPwd)
	o := submitWithdrawOK(t, u, m.MethodID, 1000, uniq("wd"))
	if o.Status != 0 {
		t.Fatalf("after admission order status=%d, want 0", o.Status)
	}
}

// 112 方式非本人 / 不存在：均 404、不扣款、不建单（不透露他人方式存在性）
func Test112WithdrawMethodOwnership(t *testing.T) {
	a := uniq("w112a")
	b := uniq("w112b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 5000)
	otherMethod := listMethodsOK(t, b)[0]
	code, resp := submitWithdrawCall(t, a, otherMethod.MethodID, 1000, uniq("wd"))
	if code != 404 || resp.Error.Code != "WITHDRAW_METHOD_NOT_FOUND" {
		t.Fatalf("other method code=%d err=%+v, want 404 WITHDRAW_METHOD_NOT_FOUND", code, resp.Error)
	}
	code, _ = submitWithdrawCall(t, a, "mtd_doesnotexist", 1000, uniq("wd"))
	if code != 404 {
		t.Fatalf("missing method code=%d, want 404", code)
	}
	bal, frz := balance(t, a)
	if bal != 5000 || frz != 0 {
		t.Fatalf("balance=%d frozen=%d, want 5000/0", bal, frz)
	}
}

// 113 金额上下限：低于下限 / 超单笔上限均 400、不扣款；收尾复位配置
func Test113WithdrawAmountLimits(t *testing.T) {
	u := uniq("w113")
	admitUser(t, u)
	fundUser(t, u, 100000)
	m := listMethodsOK(t, u)[0]
	setWithdrawConfig(t, map[string]any{"min_amount_cents": 1000, "single_max_cents": 2000})
	code, _ := submitWithdrawCall(t, u, m.MethodID, 500, uniq("wd"))
	if code != 400 {
		t.Fatalf("below-min code=%d, want 400", code)
	}
	code, resp := submitWithdrawCall(t, u, m.MethodID, 5000, uniq("wd"))
	if code != 400 || resp.Error.Code != "AMOUNT_LIMIT_EXCEEDED" {
		t.Fatalf("over-single code=%d err=%+v, want 400 AMOUNT_LIMIT_EXCEEDED", code, resp.Error)
	}
	b, f := balance(t, u)
	if b != 100000 || f != 0 {
		t.Fatalf("balance=%d frozen=%d, want 100000/0", b, f)
	}
	// 等于上限放行
	o := submitWithdrawOK(t, u, m.MethodID, 2000, uniq("wd"))
	if o.Status != 0 {
		t.Fatalf("at-limit order status=%d, want 0", o.Status)
	}
	resetWithdrawConfig(t)
}

// 114 手续费计算：固定 + 费率组合、试算接口一致、手续费 >= 金额时拒绝；收尾复位配置
func Test114WithdrawFeeCalc(t *testing.T) {
	u := uniq("w114")
	admitUser(t, u)
	fundUser(t, u, 100000)
	m := listMethodsOK(t, u)[0]
	setWithdrawConfig(t, map[string]any{"fixed_fee_cents": 100, "rate_bp": 100})
	// 10000 分：100 + floor(10000*100/10000)=100+100=200，实际到账 9800
	code, resp := call(t, "GET", "/v1/withdrawals/quote?amount_cents=10000", u, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("quote code=%d", code)
	}
	var q struct {
		FeeCents    int64 `json:"fee_cents"`
		ActualCents int64 `json:"actual_cents"`
	}
	_ = json.Unmarshal(resp.Data, &q)
	if q.FeeCents != 200 || q.ActualCents != 9800 {
		t.Fatalf("quote fee=%d actual=%d, want 200/9800", q.FeeCents, q.ActualCents)
	}
	o := submitWithdrawOK(t, u, m.MethodID, 10000, uniq("wd"))
	if o.FeeCents != 200 || o.ActualCents != 9800 {
		t.Fatalf("order fee=%d actual=%d, want 200/9800", o.FeeCents, o.ActualCents)
	}
	b, f := balance(t, u)
	if b != 100000-10200 || f != 10200 {
		t.Fatalf("balance=%d frozen=%d, want 89800/10200", b, f)
	}
	// 手续费 >= 金额：固定 5000，提 1000 -> 实际到账 <= 0，拒绝
	setWithdrawConfig(t, map[string]any{"fixed_fee_cents": 5000, "rate_bp": 0})
	code, resp = submitWithdrawCall(t, u, m.MethodID, 1000, uniq("wd"))
	if code != 400 || resp.Error.Code != "FEE_EXCEEDS_AMOUNT" {
		t.Fatalf("fee-exceeds code=%d err=%+v, want 400 FEE_EXCEEDS_AMOUNT", code, resp.Error)
	}
	resetWithdrawConfig(t)
}

// 115 频率限制：单日笔数上限与单日金额上限触发；收尾复位配置
func Test115WithdrawFrequencyLimit(t *testing.T) {
	u := uniq("w115")
	admitUser(t, u)
	fundUser(t, u, 100000)
	m := listMethodsOK(t, u)[0]
	setWithdrawConfig(t, map[string]any{"daily_count_max": 1})
	submitWithdrawOK(t, u, m.MethodID, 1000, uniq("wd"))
	code, _ := submitWithdrawCall(t, u, m.MethodID, 1000, uniq("wd"))
	if code != 400 {
		t.Fatalf("over-count code=%d, want 400", code)
	}
	resetWithdrawConfig(t)

	v := uniq("w115b")
	admitUser(t, v)
	fundUser(t, v, 100000)
	mv := listMethodsOK(t, v)[0]
	setWithdrawConfig(t, map[string]any{"daily_max_cents": 15000})
	submitWithdrawOK(t, v, mv.MethodID, 10000, uniq("wd"))
	code, _ = submitWithdrawCall(t, v, mv.MethodID, 10000, uniq("wd"))
	if code != 400 {
		t.Fatalf("over-daily-amount code=%d, want 400", code)
	}
	resetWithdrawConfig(t)
}

// 116 审核拒绝解冻：提交 -> 单管理员拒绝 -> 全额解冻（金额 + 手续费）、状态 2、type 23 分录合计 0；再拒 400
func Test116WithdrawRejectUnfreeze(t *testing.T) {
	u := uniq("w116")
	admitUser(t, u)
	fundUser(t, u, 10000)
	m := listMethodsOK(t, u)[0]
	setWithdrawConfig(t, map[string]any{"fixed_fee_cents": 100, "rate_bp": 0})
	o := submitWithdrawOK(t, u, m.MethodID, 4000, uniq("wd"))
	b, f := balance(t, u)
	if b != 5900 || f != 4100 {
		t.Fatalf("after submit balance=%d frozen=%d, want 5900/4100", b, f)
	}
	code, resp := callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+o.OrderID+"/reject", "adminA", map[string]any{"note": "资料不符"})
	if code != 200 || !resp.OK {
		t.Fatalf("reject code=%d err=%+v", code, resp.Error)
	}
	got := withdrawDetailOK(t, u, o.OrderID)
	if got.Status != 2 {
		t.Fatalf("after reject status=%d, want 2", got.Status)
	}
	b, f = balance(t, u)
	if b != 10000 || f != 0 {
		t.Fatalf("after reject balance=%d frozen=%d, want 10000/0", b, f)
	}
	var txnID string
	if err := testDB.QueryRow(`SELECT txn_id FROM wallet_txn WHERE ref_id=$1 AND type=23`, o.OrderID).Scan(&txnID); err != nil {
		t.Fatal(err)
	}
	ledgerSumZero(t, txnID)
	code, _ = callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+o.OrderID+"/reject", "adminB", map[string]any{"note": "again"})
	if code != 400 {
		t.Fatalf("re-reject code=%d, want 400", code)
	}
	resetWithdrawConfig(t)
}

// 117 审核通过 -> 处理中 -> 回填凭证 -> 成功：冻结扣减（金额 + 手续费）、平台户同步减少、分录合计 0
func Test117WithdrawApproveProcessPayout(t *testing.T) {
	u := uniq("w117")
	admitUser(t, u)
	fundUser(t, u, 10000)
	m := listMethodsOK(t, u)[0]
	platBefore := platformBalance(t)
	o := submitWithdrawOK(t, u, m.MethodID, 4000, uniq("wd"))
	// 处理中入口在待审核态非法
	code, _ := withdrawFlow(t, o.OrderID, "start-processing", "adminA", nil)
	if code != 409 {
		t.Fatalf("start-from-pending code=%d, want 409", code)
	}
	// 双人复核：同一人两次 400；第一复核后余额与状态不动；第二复核通过
	code, resp := approveWithdraw(t, o.OrderID, "adminA")
	if code != 200 || !resp.OK {
		t.Fatalf("approve1 code=%d err=%+v", code, resp.Error)
	}
	got := withdrawDetailOK(t, u, o.OrderID)
	if got.Status != 0 {
		t.Fatalf("after approve1 status=%d, want 0", got.Status)
	}
	b, f := balance(t, u)
	if b != 6000 || f != 4000 {
		t.Fatalf("after approve1 balance=%d frozen=%d, want 6000/4000", b, f)
	}
	code, _ = approveWithdraw(t, o.OrderID, "adminA")
	if code != 400 {
		t.Fatalf("same-admin approve2 code=%d, want 400", code)
	}
	code, resp = approveWithdraw(t, o.OrderID, "adminB")
	if code != 200 || !resp.OK {
		t.Fatalf("approve2 code=%d err=%+v", code, resp.Error)
	}
	got = withdrawDetailOK(t, u, o.OrderID)
	if got.Status != 1 {
		t.Fatalf("after approve2 status=%d, want 1", got.Status)
	}
	// 处理中：无资金变动
	code, resp = withdrawFlow(t, o.OrderID, "start-processing", "adminA", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("start-processing code=%d err=%+v", code, resp.Error)
	}
	got = withdrawDetailOK(t, u, o.OrderID)
	if got.Status != 3 {
		t.Fatalf("after start status=%d, want 3", got.Status)
	}
	// 无凭证确认 400；有凭证确认成功
	code, _ = withdrawFlow(t, o.OrderID, "confirm-payout", "adminA", map[string]any{"payout_note": "no proof"})
	if code != 400 {
		t.Fatalf("confirm-no-proof code=%d, want 400", code)
	}
	code, resp = withdrawFlow(t, o.OrderID, "confirm-payout", "adminA", map[string]any{"payout_proof_no": "BANK20261102001", "payout_note": "对公户转账"})
	if code != 200 || !resp.OK {
		t.Fatalf("confirm-payout code=%d err=%+v", code, resp.Error)
	}
	got = withdrawDetailOK(t, u, o.OrderID)
	if got.Status != 4 || got.PayoutProofNo == nil || *got.PayoutProofNo != "BANK20261102001" {
		t.Fatalf("after payout %+v, want status 4 proof BANK20261102001", got)
	}
	b, f = balance(t, u)
	if b != 6000 || f != 0 {
		t.Fatalf("after payout balance=%d frozen=%d, want 6000/0", b, f)
	}
	if platformBalance(t) != platBefore-4000 {
		t.Fatalf("platform balance changed unexpectedly: before=%d after=%d", platBefore, platformBalance(t))
	}
	var txnID string
	if err := testDB.QueryRow(`SELECT txn_id FROM wallet_txn WHERE ref_id=$1 AND type=24`, o.OrderID).Scan(&txnID); err != nil {
		t.Fatal(err)
	}
	ledgerSumZero(t, txnID)
}

// 118 处理失败 -> 已退款：失败时冻结不动、退款后全额解冻、type 25 分录合计 0
func Test118WithdrawFailThenRefund(t *testing.T) {
	u := uniq("w118")
	admitUser(t, u)
	fundUser(t, u, 10000)
	m := listMethodsOK(t, u)[0]
	o := submitWithdrawOK(t, u, m.MethodID, 3000, uniq("wd"))
	approveWithdrawDual(t, o.OrderID)
	code, resp := withdrawFlow(t, o.OrderID, "start-processing", "adminA", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("start code=%d err=%+v", code, resp.Error)
	}
	code, resp = withdrawFlow(t, o.OrderID, "fail", "adminA", map[string]any{"failure_reason": "银行退票"})
	if code != 200 || !resp.OK {
		t.Fatalf("fail code=%d err=%+v", code, resp.Error)
	}
	got := withdrawDetailOK(t, u, o.OrderID)
	if got.Status != 5 {
		t.Fatalf("after fail status=%d, want 5", got.Status)
	}
	b, f := balance(t, u)
	if b != 7000 || f != 3000 {
		t.Fatalf("after fail balance=%d frozen=%d, want 7000/3000 (still frozen)", b, f)
	}
	code, resp = withdrawFlow(t, o.OrderID, "refund", "adminA", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("refund code=%d err=%+v", code, resp.Error)
	}
	got = withdrawDetailOK(t, u, o.OrderID)
	if got.Status != 6 {
		t.Fatalf("after refund status=%d, want 6", got.Status)
	}
	b, f = balance(t, u)
	if b != 10000 || f != 0 {
		t.Fatalf("after refund balance=%d frozen=%d, want 10000/0", b, f)
	}
	var txnID string
	if err := testDB.QueryRow(`SELECT txn_id FROM wallet_txn WHERE ref_id=$1 AND type=25`, o.OrderID).Scan(&txnID); err != nil {
		t.Fatal(err)
	}
	ledgerSumZero(t, txnID)
}

// 119 非法状态流转拒绝：各非法边均拒绝、订单状态不动；自动打款骨架 501
func Test119WithdrawIllegalTransitions(t *testing.T) {
	u := uniq("w119")
	admitUser(t, u)
	fundUser(t, u, 10000)
	m := listMethodsOK(t, u)[0]
	o := submitWithdrawOK(t, u, m.MethodID, 2000, uniq("wd"))
	for _, action := range []string{"confirm-payout", "fail", "refund"} {
		code, _ := withdrawFlow(t, o.OrderID, action, "adminA", map[string]any{"payout_proof_no": "X1"})
		if code != 409 {
			t.Fatalf("%s from pending code=%d, want 409", action, code)
		}
	}
	code, _ := withdrawFlow(t, o.OrderID, "auto-payout", "adminA", nil)
	if code != 501 {
		t.Fatalf("auto-payout code=%d, want 501", code)
	}
	approveWithdrawDual(t, o.OrderID)
	code, _ = withdrawFlow(t, o.OrderID, "refund", "adminA", nil)
	if code != 409 {
		t.Fatalf("refund from approved code=%d, want 409", code)
	}
	code, _ = withdrawFlow(t, o.OrderID, "fail", "adminA", nil)
	if code != 409 {
		t.Fatalf("fail from approved code=%d, want 409", code)
	}
	got := withdrawDetailOK(t, u, o.OrderID)
	if got.Status != 1 {
		t.Fatalf("status after illegal attempts=%d, want 1", got.Status)
	}
	// 成功后不可再审
	withdrawFlow(t, o.OrderID, "start-processing", "adminA", nil)
	withdrawFlow(t, o.OrderID, "confirm-payout", "adminA", map[string]any{"payout_proof_no": "BANK119"})
	code, _ = approveWithdraw(t, o.OrderID, "adminA")
	if code != 400 {
		t.Fatalf("approve after success code=%d, want 400", code)
	}
}

// 120 重复提交幂等：同一幂等键两次返回同一单、只冻一次
func Test120WithdrawIdempotentSubmit(t *testing.T) {
	u := uniq("w120")
	admitUser(t, u)
	fundUser(t, u, 10000)
	m := listMethodsOK(t, u)[0]
	idem := uniq("wd")
	o1 := submitWithdrawOK(t, u, m.MethodID, 3000, idem)
	o2 := submitWithdrawOK(t, u, m.MethodID, 3000, idem)
	if o1.OrderID != o2.OrderID {
		t.Fatalf("idem orders differ: %s vs %s", o1.OrderID, o2.OrderID)
	}
	b, f := balance(t, u)
	if b != 7000 || f != 3000 {
		t.Fatalf("balance=%d frozen=%d, want 7000/3000 (single freeze)", b, f)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_order WHERE user_id=$1 AND order_type=2`, u).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("withdraw order rows=%d, want 1", n)
	}
}

// 121 用户权限隔离：他人详情 404、他人列表不含该单、管理接口无密钥 401
func Test121WithdrawPermissionIsolation(t *testing.T) {
	a := uniq("w121a")
	b := uniq("w121b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 5000)
	m := listMethodsOK(t, a)[0]
	o := submitWithdrawOK(t, a, m.MethodID, 1000, uniq("wd"))
	code, _ := withdrawDetail(t, b, o.OrderID)
	if code != 404 {
		t.Fatalf("other detail code=%d, want 404", code)
	}
	code, resp := call(t, "GET", "/v1/withdrawals", b, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("other list code=%d", code)
	}
	var list []WithdrawOrder
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 0 {
		t.Fatalf("other list len=%d, want 0", len(list))
	}
	code, _ = callAdminRaw(t, "GET", "/v1/admin/withdraw-orders/"+o.OrderID, "", "")
	if code != 401 {
		t.Fatalf("admin no-secret code=%d, want 401", code)
	}
	code, resp = call(t, "GET", "/v1/admin/withdraw-orders/"+o.OrderID, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin detail code=%d", code)
	}
	code, _ = call(t, "GET", "/v1/withdrawals/quote?amount_cents=1000", a, nil, false)
	if code != 200 {
		t.Fatalf("quote code=%d, want 200", code)
	}
	code, _ = call(t, "GET", "/v1/withdrawals/quote?amount_cents=1000", "", nil, false)
	if code != 401 {
		t.Fatalf("quote no-token code=%d, want 401", code)
	}
}

// 122 管理端订单查询与审核双人复核 + 手续费配置读写
func Test122WithdrawAdminQueryAndConfig(t *testing.T) {
	u := uniq("w122")
	admitUser(t, u)
	fundUser(t, u, 10000)
	m := listMethodsOK(t, u)[0]
	o := submitWithdrawOK(t, u, m.MethodID, 2500, uniq("wd"))
	code, resp := call(t, "GET", "/v1/admin/withdraw-orders?user_id="+u, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin list code=%d", code)
	}
	var list []WithdrawOrderView
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 1 || list[0].OrderID != o.OrderID {
		t.Fatalf("admin list %+v, want exactly the submitted order", list)
	}
	code, resp = call(t, "GET", "/v1/admin/withdraw-orders?user_id="+u+"&status=4", "", nil, true)
	_ = json.Unmarshal(resp.Data, &list)
	if code != 200 || len(list) != 0 {
		t.Fatalf("admin list status=4 len=%d, want 0", len(list))
	}
	code, resp = call(t, "GET", "/v1/admin/withdraw-orders/"+o.OrderID, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin detail code=%d", code)
	}
	var view WithdrawOrderView
	_ = json.Unmarshal(resp.Data, &view)
	if view.FreezeID == nil || view.FeeCents != 0 || view.AmountCents != 2500 {
		t.Fatalf("admin detail %+v, want freeze_id set fee 0 amount 2500", view)
	}
	// 双人复核：第一人 awaiting（状态仍 0），第二人通过（状态 1）
	code, resp = approveWithdraw(t, o.OrderID, "adminA")
	if code != 200 {
		t.Fatalf("approve1 code=%d", code)
	}
	var stage struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(resp.Data, &stage)
	if stage.Stage != "awaiting_second_approval" {
		t.Fatalf("approve1 stage=%s, want awaiting_second_approval", stage.Stage)
	}
	code, resp = approveWithdraw(t, o.OrderID, "adminB")
	_ = json.Unmarshal(resp.Data, &stage)
	if code != 200 || stage.Stage != "approved" {
		t.Fatalf("approve2 code=%d stage=%s, want approved", code, stage.Stage)
	}
	// 配置读写：非法值 400；写后读回一致；收尾复位
	code, _ = callAdminOp(t, "PUT", "/v1/admin/withdraw-config", "admin1", map[string]any{"fixed_fee_cents": -1})
	if code != 400 {
		t.Fatalf("negative fee code=%d, want 400", code)
	}
	c := setWithdrawConfig(t, map[string]any{"fixed_fee_cents": 50, "rate_bp": 50})
	if c.FixedFeeCents != 50 || c.RateBp != 50 {
		t.Fatalf("config %+v, want fixed 50 rate 50", c)
	}
	code, resp = call(t, "GET", "/v1/admin/withdraw-config", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("config get code=%d", code)
	}
	resetWithdrawConfig(t)
}

// 123 账务守恒：多笔提交 + 拒绝 + 成功 + 失败退款后对账 diff=0，全部 22–25 txn 分录合计为 0
func Test123WithdrawLedgerConservation(t *testing.T) {
	u := uniq("w123")
	admitUser(t, u)
	fundUser(t, u, 50000)
	m := listMethodsOK(t, u)[0]
	o1 := submitWithdrawOK(t, u, m.MethodID, 5000, uniq("wd"))
	o2 := submitWithdrawOK(t, u, m.MethodID, 6000, uniq("wd"))
	o3 := submitWithdrawOK(t, u, m.MethodID, 7000, uniq("wd"))
	callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+o1.OrderID+"/reject", "adminA", map[string]any{"note": "reject"})
	approveWithdrawDual(t, o2.OrderID)
	withdrawFlow(t, o2.OrderID, "start-processing", "adminA", nil)
	withdrawFlow(t, o2.OrderID, "confirm-payout", "adminA", map[string]any{"payout_proof_no": "BANK123"})
	approveWithdrawDual(t, o3.OrderID)
	withdrawFlow(t, o3.OrderID, "start-processing", "adminA", nil)
	withdrawFlow(t, o3.OrderID, "fail", "adminA", map[string]any{"failure_reason": "渠道失败"})
	withdrawFlow(t, o3.OrderID, "refund", "adminA", nil)
	b, f := balance(t, u)
	// 50000 - 6000（成功出账）= 44000 可用，冻结 0（o1 解冻、o3 退款）
	if b != 44000 || f != 0 {
		t.Fatalf("balance=%d frozen=%d, want 44000/0", b, f)
	}
	for _, typ := range []int{TxnWithdrawSubmit, TxnWithdrawReject, TxnWithdrawPayout, TxnWithdrawRefund} {
		txnTypeSumZero(t, typ)
	}
	code, resp := call(t, "POST", "/v1/admin/reconcile", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("reconcile code=%d", code)
	}
	var rec ReconcileResult
	_ = json.Unmarshal(resp.Data, &rec)
	if rec.DiffCents != 0 || rec.Status != 0 {
		t.Fatalf("reconcile diff=%d status=%d, want 0/0", rec.DiffCents, rec.Status)
	}
}

// 124 流程中解绑方式：打款确认被拒（不向失效账户打款），走失败 -> 退款后资金恢复
func Test124WithdrawMethodDeletedBeforePayout(t *testing.T) {
	u := uniq("w124")
	admitUser(t, u)
	fundUser(t, u, 10000)
	m := listMethodsOK(t, u)[0]
	o := submitWithdrawOK(t, u, m.MethodID, 3000, uniq("wd"))
	approveWithdrawDual(t, o.OrderID)
	withdrawFlow(t, o.OrderID, "start-processing", "adminA", nil)
	code, resp := call(t, "DELETE", "/v1/withdraw-methods/"+m.MethodID, u, map[string]any{"tx_password": testTxPwd}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("delete method code=%d err=%+v", code, resp.Error)
	}
	code, _ = withdrawFlow(t, o.OrderID, "confirm-payout", "adminA", map[string]any{"payout_proof_no": "BANK124"})
	if code != 400 {
		t.Fatalf("confirm after method deleted code=%d, want 400", code)
	}
	withdrawFlow(t, o.OrderID, "fail", "adminA", map[string]any{"failure_reason": "方式已解绑"})
	code, resp = withdrawFlow(t, o.OrderID, "refund", "adminA", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("refund code=%d err=%+v", code, resp.Error)
	}
	b, f := balance(t, u)
	if b != 10000 || f != 0 {
		t.Fatalf("after refund balance=%d frozen=%d, want 10000/0", b, f)
	}
}
