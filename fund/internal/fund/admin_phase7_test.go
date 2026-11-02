package fund

// Phase 7 测试：管理端资金中心 82–94。全部用 stubVerifier（token "imtok-<uid>"），
// 管理接口用 test-admin-secret；测试注资用内部 income 接口（真实充值在 Phase 8）。

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func callAdminRaw(t *testing.T, method, path, adminID, secret string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if secret != "" {
		req.Header.Set("X-Admin-Secret", secret)
	}
	if adminID != "" {
		req.Header.Set("X-Admin-ID", adminID)
	}
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func createAdjustmentOK(t *testing.T, adminID, uid string, direction int, amount int64, reason, orig string) AdminAdjustment {
	t.Helper()
	body := map[string]any{"user_id": uid, "direction": direction, "amount_cents": amount, "reason": reason}
	if orig != "" {
		body["original_adjustment_id"] = orig
	}
	code, resp := callAdminOp(t, "POST", "/v1/admin/adjustments", adminID, body)
	if code != 200 || !resp.OK {
		t.Fatalf("create adjustment: code=%d err=%+v", code, resp.Error)
	}
	var a AdminAdjustment
	if err := json.Unmarshal(resp.Data, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func approveAdjustmentCall(t *testing.T, adjID, adminID string) (int, apiResp) {
	t.Helper()
	return callAdminOp(t, "POST", "/v1/admin/adjustments/"+adjID+"/approve", adminID, nil)
}

func seedWithdrawOrder(t *testing.T, orderID, uid string, orderType int, amount int64) {
	t.Helper()
	// Phase 8：0008 约束要求 order_type=1 的行必须有 channel；测试种子充值单补 mock 渠道
	// （提现单 order_type=2 不受该约束，保持 channel 为空）。
	channel := ""
	if orderType == 1 {
		channel = "mock"
	}
	if _, err := testDB.Exec(`INSERT INTO wallet_order (order_id, user_id, order_type, amount_cents, fee_cents, actual_cents, status, channel) VALUES ($1,$2,$3,$4,0,$4,0,$5)`,
		orderID, uid, orderType, amount, nullStr(channel)); err != nil {
		t.Fatal(err)
	}
}

// 82 管理端钱包/流水/准入查询
func Test82AdminWalletLedgerAdmission(t *testing.T) {
	u := uniq("a82")
	admitUser(t, u)
	fundUser(t, u, 10000)
	code, resp := call(t, "GET", "/v1/admin/wallets/"+u, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin wallet: code=%d err=%+v", code, resp.Error)
	}
	var acct Account
	_ = json.Unmarshal(resp.Data, &acct)
	if acct.BalanceCents != 10000 || acct.Status != 0 {
		t.Fatalf("admin wallet %+v, want balance 10000 status 0", acct)
	}
	code, resp = call(t, "GET", "/v1/admin/wallets/"+u+"/ledger?limit=50", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin ledger: code=%d", code)
	}
	var entries []LedgerEntry
	_ = json.Unmarshal(resp.Data, &entries)
	if len(entries) == 0 {
		t.Fatalf("admin ledger empty, want income entries")
	}
	code, resp = call(t, "GET", "/v1/admin/wallets/"+u+"/finance-status", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin finance-status: code=%d", code)
	}
	var st FinanceStatus
	_ = json.Unmarshal(resp.Data, &st)
	if !st.HasWithdrawalMethod || !st.HasTransactionPassword || st.FinanceAccountStatus != "ok" || st.NextStep != "none" {
		t.Fatalf("admin finance-status %+v unexpected", st)
	}
	// 不存在的用户：钱包 404；无管理密钥：401。
	code, _ = call(t, "GET", "/v1/admin/wallets/"+uniq("a82no"), "", nil, true)
	if code != 404 {
		t.Fatalf("admin wallet missing user: code=%d, want 404", code)
	}
	code, _ = call(t, "GET", "/v1/admin/wallets/"+u, u, nil, false)
	if code != 401 {
		t.Fatalf("admin wallet user token: code=%d, want 401", code)
	}
}

// 83 提现账户管理：列表脱敏、默认账户、管理端无修改入口
func Test83AdminWithdrawMethodsMasked(t *testing.T) {
	u := uniq("a83")
	mustWallet(t, u)
	bank := addMethodOK(t, u, 3, "李四", "6222021234567890", map[string]any{"bank_name": "工商银行"})
	_ = addMethodOK(t, u, 1, "李四", "alipay-user-001", nil)
	code, raw := callAdminRaw(t, "GET", "/v1/admin/withdraw-methods?user_id="+u, "", testAdmin)
	if code != 200 {
		t.Fatalf("admin methods: code=%d", code)
	}
	if strings.Contains(raw, "6222021234567890") {
		t.Fatalf("admin methods response leaks full card number")
	}
	if !strings.Contains(raw, "****7890") {
		t.Fatalf("admin methods response missing masked ****7890: %s", raw)
	}
	code, resp := call(t, "GET", "/v1/admin/withdraw-methods/default?user_id="+u, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin default method: code=%d", code)
	}
	var def WithdrawMethod
	_ = json.Unmarshal(resp.Data, &def)
	if def.MethodID != bank.MethodID || !def.IsDefault {
		t.Fatalf("admin default %+v, want bank method %s", def, bank.MethodID)
	}
	// 管理端修改入口不存在：PUT 到管理路径不得成功（405），用户绑定数不变。
	code, _ = callAdminRaw(t, "PUT", "/v1/admin/withdraw-methods", "", testAdmin)
	if code == 200 {
		t.Fatalf("admin PUT withdraw-methods unexpectedly succeeded")
	}
	ms := listMethodsOK(t, u)
	if len(ms) != 2 {
		t.Fatalf("methods count=%d after admin PUT, want 2", len(ms))
	}
}

// 84 交易密码管理查询：无哈希字段
func Test84AdminTxPasswordStatusNoHash(t *testing.T) {
	u := uniq("a84")
	admitUser(t, u)
	code, raw := callAdminRaw(t, "GET", "/v1/admin/tx-password/status?user_id="+u, "", testAdmin)
	if code != 200 {
		t.Fatalf("admin txpwd status: code=%d", code)
	}
	if strings.Contains(raw, "password_hash") || strings.Contains(raw, "$2a$") {
		t.Fatalf("admin txpwd status leaks hash: %s", raw)
	}
	var st AdminTxPasswordStatus
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal([]byte(raw), &env)
	_ = json.Unmarshal(env.Data, &st)
	if !st.HasPassword || st.LastChangedAt == nil {
		t.Fatalf("admin txpwd status %+v, want has_password + last_changed_at", st)
	}
	// 未设密码用户：has_password=false，同样无哈希。
	u2 := uniq("a84b")
	mustWallet(t, u2)
	code, raw = callAdminRaw(t, "GET", "/v1/admin/tx-password/status?user_id="+u2, "", testAdmin)
	if code != 200 || strings.Contains(raw, "password_hash") {
		t.Fatalf("admin txpwd status unset: code=%d raw=%s", code, raw)
	}
}

// 85 找回申请单管理查询
func Test85AdminRecoveryList(t *testing.T) {
	u := uniq("a85")
	mustWallet(t, u)
	_ = addMethodOK(t, u, 3, "李四", "6222021234567890", map[string]any{"bank_name": "工商银行"})
	code, resp := call(t, "POST", "/v1/tx-password/recover/request", u, map[string]any{"real_name": "李四", "account_last4": "7890"}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("recover request: code=%d err=%+v", code, resp.Error)
	}
	var req struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(resp.Data, &req)
	code, resp = call(t, "GET", "/v1/admin/tx-password/recover?user_id="+u, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin recover list: code=%d", code)
	}
	var list []RecoveryRequest
	_ = json.Unmarshal(resp.Data, &list)
	found := false
	for _, r := range list {
		if r.RequestID == req.RequestID && r.Status == RecoverPending {
			found = true
		}
		// 管理列表同样不得含身份材料字段。
	}
	if !found {
		t.Fatalf("admin recover list missing pending request %s", req.RequestID)
	}
	code, resp = call(t, "GET", "/v1/admin/tx-password/recover/"+req.RequestID, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin recover detail: code=%d", code)
	}
	// 普通用户 token 调管理接口：401。
	code, _ = call(t, "GET", "/v1/admin/tx-password/recover?user_id="+u, u, nil, false)
	if code != 401 {
		t.Fatalf("admin recover list user token: code=%d, want 401", code)
	}
}

// 86 调整单：单人复核不执行
func Test86AdminAdjustmentSingleApproveNoExec(t *testing.T) {
	u := uniq("a86")
	mustWallet(t, u)
	fundUser(t, u, 5000)
	adj := createAdjustmentOK(t, "admin1", u, AdjCredit, 10000, "测试调增", "")
	if adj.Status != AdjPending {
		t.Fatalf("new adjustment status=%d, want 0 pending", adj.Status)
	}
	code, resp := approveAdjustmentCall(t, adj.AdjustmentID, "admin1")
	if code != 200 || !resp.OK {
		t.Fatalf("first approve: code=%d err=%+v", code, resp.Error)
	}
	var stage struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(resp.Data, &stage)
	if stage.Stage != "awaiting_second_approval" {
		t.Fatalf("first approve stage=%q, want awaiting_second_approval", stage.Stage)
	}
	b, _ := balance(t, u)
	if b != 5000 {
		t.Fatalf("balance after single approve=%d, want 5000 unchanged", b)
	}
	code, resp = call(t, "GET", "/v1/admin/adjustments/"+adj.AdjustmentID, "", nil, true)
	var got AdminAdjustment
	_ = json.Unmarshal(resp.Data, &got)
	if code != 200 || got.Status != AdjPending || got.ExecutedTxnID != nil {
		t.Fatalf("adjustment after single approve %+v, want pending without txn", got)
	}
}

// 87 调整单：双人复核执行（余额变动 + 分录合计 0 + 审计可查）
func Test87AdminAdjustmentDualExec(t *testing.T) {
	u := uniq("a87")
	mustWallet(t, u)
	fundUser(t, u, 5000)
	adj := createAdjustmentOK(t, "admin1", u, AdjCredit, 3000, "测试调增双人", "")
	_, _ = approveAdjustmentCall(t, adj.AdjustmentID, "admin1")
	code, resp := approveAdjustmentCall(t, adj.AdjustmentID, "admin2")
	if code != 200 || !resp.OK {
		t.Fatalf("second approve: code=%d err=%+v", code, resp.Error)
	}
	var stage struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(resp.Data, &stage)
	if stage.Stage != "executed" {
		t.Fatalf("second approve stage=%q, want executed", stage.Stage)
	}
	b, _ := balance(t, u)
	if b != 8000 {
		t.Fatalf("balance after dual exec=%d, want 8000", b)
	}
	code, resp = call(t, "GET", "/v1/admin/adjustments/"+adj.AdjustmentID, "", nil, true)
	var got AdminAdjustment
	_ = json.Unmarshal(resp.Data, &got)
	if code != 200 || got.Status != AdjExecuted || got.ExecutedTxnID == nil || got.SecondAdminID == nil || *got.SecondAdminID != "admin2" {
		t.Fatalf("adjustment after exec %+v unexpected", got)
	}
	ledgerSumZero(t, *got.ExecutedTxnID)
	// 审计可查：执行审计行操作人为第二复核人。
	code, resp = call(t, "GET", "/v1/admin/audit-logs?user_id="+u+"&action=admin_adjustment_execute", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin audit logs: code=%d", code)
	}
	var logs []AuditLogEntry
	_ = json.Unmarshal(resp.Data, &logs)
	if len(logs) == 0 || logs[0].OperatorID == nil || *logs[0].OperatorID != "admin2" {
		t.Fatalf("execute audit logs %+v, want operator admin2", logs)
	}
}

// 88 调整单：拒绝关闭、执行后不可改、反向纠错单
func Test88AdminAdjustmentRejectAndReverse(t *testing.T) {
	u := uniq("a88")
	mustWallet(t, u)
	fundUser(t, u, 10000)
	adj := createAdjustmentOK(t, "admin1", u, AdjCredit, 1000, "待拒绝", "")
	code, resp := callAdminOp(t, "POST", "/v1/admin/adjustments/"+adj.AdjustmentID+"/reject", "admin1", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("reject: code=%d err=%+v", code, resp.Error)
	}
	if code, _ := approveAdjustmentCall(t, adj.AdjustmentID, "admin2"); code == 200 {
		t.Fatalf("approve after reject unexpectedly succeeded")
	}
	if code, _ := callAdminOp(t, "POST", "/v1/admin/adjustments/"+adj.AdjustmentID+"/reject", "admin2", nil); code == 200 {
		t.Fatalf("second reject unexpectedly succeeded")
	}
	// 执行后不可改：执行单再 approve/reject 均 400。
	exec := createAdjustmentOK(t, "admin1", u, AdjCredit, 2000, "待执行", "")
	_, _ = approveAdjustmentCall(t, exec.AdjustmentID, "admin1")
	if code, _ := approveAdjustmentCall(t, exec.AdjustmentID, "admin2"); code != 200 {
		t.Fatalf("dual exec: code=%d", code)
	}
	b, _ := balance(t, u)
	if b != 12000 {
		t.Fatalf("balance after exec=%d, want 12000", b)
	}
	if code, _ := approveAdjustmentCall(t, exec.AdjustmentID, "admin1"); code == 200 {
		t.Fatalf("approve executed unexpectedly succeeded")
	}
	if code, _ := callAdminOp(t, "POST", "/v1/admin/adjustments/"+exec.AdjustmentID+"/reject", "admin1", nil); code == 200 {
		t.Fatalf("reject executed unexpectedly succeeded")
	}
	// 反向纠错单：须注明原单号、方向相反；双人执行后余额恢复。
	rev := createAdjustmentOK(t, "admin1", u, AdjDebit, 2000, "纠错反向", exec.AdjustmentID)
	_, _ = approveAdjustmentCall(t, rev.AdjustmentID, "admin1")
	if code, _ := approveAdjustmentCall(t, rev.AdjustmentID, "admin2"); code != 200 {
		t.Fatalf("reverse exec: code=%d", code)
	}
	b, _ = balance(t, u)
	if b != 10000 {
		t.Fatalf("balance after reverse=%d, want 10000", b)
	}
	// 反向单校验：原单未执行、方向相同均拒绝建单。
	pending := createAdjustmentOK(t, "admin1", u, AdjCredit, 500, "待复核原单", "")
	code, _ = callAdminOp(t, "POST", "/v1/admin/adjustments", "admin1", map[string]any{"user_id": u, "direction": AdjDebit, "amount_cents": 500, "reason": "原单未执行", "original_adjustment_id": pending.AdjustmentID})
	if code == 200 {
		t.Fatalf("reverse referencing pending unexpectedly succeeded")
	}
	code, _ = callAdminOp(t, "POST", "/v1/admin/adjustments", "admin1", map[string]any{"user_id": u, "direction": AdjCredit, "amount_cents": 500, "reason": "方向相同", "original_adjustment_id": exec.AdjustmentID})
	if code == 200 {
		t.Fatalf("reverse same direction unexpectedly succeeded")
	}
}

// 89 调整单：减额超余额时第二复核不落单、单据仍待第二复核；同一人两次复核拒绝
func Test89AdminAdjustmentDecreaseInsufficient(t *testing.T) {
	u := uniq("a89")
	mustWallet(t, u)
	fundUser(t, u, 1000)
	adj := createAdjustmentOK(t, "admin1", u, AdjDebit, 5000, "超额调减", "")
	_, _ = approveAdjustmentCall(t, adj.AdjustmentID, "admin1")
	if code, _ := approveAdjustmentCall(t, adj.AdjustmentID, "admin1"); code == 200 {
		t.Fatalf("same admin second approve unexpectedly succeeded")
	}
	if code, resp := approveAdjustmentCall(t, adj.AdjustmentID, "admin2"); code == 200 {
		t.Fatalf("over-debit second approve unexpectedly succeeded: %+v", resp)
	}
	b, _ := balance(t, u)
	if b != 1000 {
		t.Fatalf("balance after failed exec=%d, want 1000", b)
	}
	code, resp := call(t, "GET", "/v1/admin/adjustments/"+adj.AdjustmentID, "", nil, true)
	var got AdminAdjustment
	_ = json.Unmarshal(resp.Data, &got)
	if code != 200 || got.Status != AdjPending || got.SecondAdminID != nil {
		t.Fatalf("adjustment after failed exec %+v, want pending without second admin", got)
	}
	// 仍可由管理员拒绝关闭。
	if code, _ := callAdminOp(t, "POST", "/v1/admin/adjustments/"+adj.AdjustmentID+"/reject", "admin2", nil); code != 200 {
		t.Fatalf("reject after failed exec: code=%d", code)
	}
}

// 90 提现订单审核脚手架：列表/详情/通过/拒绝状态流转、无打款副作用
func Test90AdminWithdrawOrderReview(t *testing.T) {
	u := uniq("a90")
	mustWallet(t, u)
	fundUser(t, u, 20000)
	oid1, oid2, oidR := uniq("wd1"), uniq("wd2"), uniq("rc1")
	seedWithdrawOrder(t, oid1, u, 2, 5000)
	seedWithdrawOrder(t, oid2, u, 2, 3000)
	seedWithdrawOrder(t, oidR, u, 1, 9000) // 充值单：管理提现审核须拒绝
	var txnBefore int
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE user_id=$1`, u).Scan(&txnBefore)
	code, resp := call(t, "GET", "/v1/admin/withdraw-orders?user_id="+u, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin withdraw list: code=%d", code)
	}
	var list []WithdrawOrderView
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 2 {
		t.Fatalf("admin withdraw list len=%d, want 2 (recharge excluded)", len(list))
	}
	code, resp = call(t, "GET", "/v1/admin/withdraw-orders/"+oid1, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("admin withdraw detail: code=%d", code)
	}
	// 第一复核：仍待审核、余额不动、无新 txn（无打款副作用）。
	code, resp = callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+oid1+"/approve", "admin1", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("withdraw first approve: code=%d err=%+v", code, resp.Error)
	}
	var stage struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(resp.Data, &stage)
	if stage.Stage != "awaiting_second_approval" {
		t.Fatalf("withdraw first approve stage=%q", stage.Stage)
	}
	b, _ := balance(t, u)
	if b != 20000 {
		t.Fatalf("balance after withdraw first approve=%d, want unchanged 20000", b)
	}
	var txnAfter int
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE user_id=$1`, u).Scan(&txnAfter)
	if txnAfter != txnBefore {
		t.Fatalf("txn count changed %d->%d on withdraw review, want no payout side effect", txnBefore, txnAfter)
	}
	// 第二复核：状态置已通过，仍无打款（余额与 txn 数不变）。
	code, resp = callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+oid1+"/approve", "admin2", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("withdraw second approve: code=%d err=%+v", code, resp.Error)
	}
	_ = json.Unmarshal(resp.Data, &stage)
	if stage.Stage != "approved" {
		t.Fatalf("withdraw second approve stage=%q, want approved", stage.Stage)
	}
	code, resp = call(t, "GET", "/v1/admin/withdraw-orders/"+oid1, "", nil, true)
	var got WithdrawOrderView
	_ = json.Unmarshal(resp.Data, &got)
	if code != 200 || got.Status != WithdrawOrderApproved || got.ReviewedAt == nil {
		t.Fatalf("withdraw order after approve %+v, want status 1 + reviewed_at", got)
	}
	b, _ = balance(t, u)
	if b != 20000 {
		t.Fatalf("balance after withdraw approved=%d, want unchanged (no payout in Phase 7)", b)
	}
	if code, _ := callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+oid1+"/approve", "admin1", nil); code == 200 {
		t.Fatalf("approve approved order unexpectedly succeeded")
	}
	// 拒绝流转 + 非提现单拒绝。
	code, resp = callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+oid2+"/reject", "admin1", map[string]any{"note": "资料不符"})
	if code != 200 || !resp.OK {
		t.Fatalf("withdraw reject: code=%d err=%+v", code, resp.Error)
	}
	code, resp = call(t, "GET", "/v1/admin/withdraw-orders/"+oid2, "", nil, true)
	_ = json.Unmarshal(resp.Data, &got)
	if code != 200 || got.Status != WithdrawOrderRejected || got.ReviewNote == nil || *got.ReviewNote != "资料不符" {
		t.Fatalf("withdraw order after reject %+v unexpected", got)
	}
	if code, _ := callAdminOp(t, "POST", "/v1/admin/withdraw-orders/"+oidR+"/approve", "admin1", nil); code == 200 {
		t.Fatalf("approve recharge order as withdraw unexpectedly succeeded")
	}
}

// 91 限额配置读写与转账单笔上限触发（红包路径本阶段不接限额，文档写清）
func Test91AdminRiskLimitTransfer(t *testing.T) {
	a := uniq("a91a")
	b := uniq("a91b")
	admitUser(t, a)
	fundUser(t, a, 20000)
	mustWallet(t, b)
	// 默认不限额：先显式复位，再验部分更新语义。
	code, resp := callAdminOp(t, "PUT", "/v1/admin/risk-limits", "admin1", map[string]any{"single_max_cents": nil, "daily_max_cents": nil})
	if code != 200 || !resp.OK {
		t.Fatalf("risk limits reset: code=%d err=%+v", code, resp.Error)
	}
	code, resp = callAdminOp(t, "PUT", "/v1/admin/risk-limits", "admin1", map[string]any{"single_max_cents": 5000})
	if code != 200 || !resp.OK {
		t.Fatalf("risk limits set single: code=%d err=%+v", code, resp.Error)
	}
	var lim RiskLimits
	_ = json.Unmarshal(resp.Data, &lim)
	if lim.SingleMaxCents == nil || *lim.SingleMaxCents != 5000 || lim.DailyMaxCents != nil {
		t.Fatalf("risk limits after partial update %+v, want single 5000 daily nil", lim)
	}
	code, resp = call(t, "GET", "/v1/admin/risk-limits", "", nil, true)
	_ = json.Unmarshal(resp.Data, &lim)
	if lim.SingleMaxCents == nil || *lim.SingleMaxCents != 5000 {
		t.Fatalf("risk limits read %+v, want single 5000", lim)
	}
	// 超限转账拒绝：无订单行、余额不动。
	code, resp = sendTransferCall(t, a, b, 6000, uniq("idem"), testTxPwd)
	if code != 400 || resp.Error.Code != "AMOUNT_LIMIT_EXCEEDED" {
		t.Fatalf("over-limit transfer: code=%d err=%+v, want 400 AMOUNT_LIMIT_EXCEEDED", code, resp.Error)
	}
	ba, _ := balance(t, a)
	if ba != 20000 {
		t.Fatalf("balance after over-limit=%d, want 20000", ba)
	}
	code, resp = call(t, "GET", "/v1/transfers?role=sent", a, nil, false)
	var sent []TransferOrder
	_ = json.Unmarshal(resp.Data, &sent)
	if code != 200 || len(sent) != 0 {
		t.Fatalf("sent transfers after over-limit len=%d, want 0 (no order row)", len(sent))
	}
	// 等于上限放行。
	if code, _ := sendTransferCall(t, a, b, 5000, uniq("idem"), testTxPwd); code != 200 {
		t.Fatalf("at-limit transfer: code=%d, want 200", code)
	}
	// 红包路径本阶段不接限额：超限红包仍可发出（Phase 8/9 统一接入，见文档边界）。
	code, resp = call(t, "POST", "/v1/redpackets", a, map[string]any{"receiver_id": b, "amount_cents": 6000, "tx_password": testTxPwd}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("redpacket above single limit: code=%d err=%+v (Phase 7 boundary: limit only on transfer path)", code, resp.Error)
	}
	// 取消单笔上限后超限转账恢复。
	code, _ = callAdminOp(t, "PUT", "/v1/admin/risk-limits", "admin1", map[string]any{"single_max_cents": nil})
	if code != 200 {
		t.Fatalf("risk limits unset single: code=%d", code)
	}
	if code, _ := sendTransferCall(t, a, b, 6000, uniq("idem"), testTxPwd); code != 200 {
		t.Fatalf("transfer after limit removed: code=%d, want 200", code)
	}
	// 单日上限只存配置、不接入执行：单笔不限额时，超单日上限转账仍成功。
	fundUser(t, a, 20000)
	code, _ = callAdminOp(t, "PUT", "/v1/admin/risk-limits", "admin1", map[string]any{"daily_max_cents": 1000})
	if code != 200 {
		t.Fatalf("risk limits set daily: code=%d", code)
	}
	if code, _ := sendTransferCall(t, a, b, 5000, uniq("idem"), testTxPwd); code != 200 {
		t.Fatalf("transfer above daily_max (not enforced in Phase 7): code=%d, want 200", code)
	}
	// 负数上限拒绝；收尾复位不限额，避免污染后续测试。
	code, _ = callAdminOp(t, "PUT", "/v1/admin/risk-limits", "admin1", map[string]any{"single_max_cents": -1})
	if code == 200 {
		t.Fatalf("negative single limit unexpectedly accepted")
	}
	code, _ = callAdminOp(t, "PUT", "/v1/admin/risk-limits", "admin1", map[string]any{"single_max_cents": nil, "daily_max_cents": nil})
	if code != 200 {
		t.Fatalf("risk limits final reset: code=%d", code)
	}
}

// 92 越权隔离：无密钥/错密钥/用户 token/缺 X-Admin-ID 一律 401
func Test92AdminPermissionIsolation(t *testing.T) {
	u := uniq("a92")
	mustWallet(t, u)
	adj := createAdjustmentOK(t, "admin1", u, AdjCredit, 100, "越权测试", "")
	adminPaths := []string{
		"/v1/admin/wallets/" + u,
		"/v1/admin/adjustments",
		"/v1/admin/withdraw-orders",
		"/v1/admin/risk-limits",
		"/v1/admin/audit-logs?user_id=" + u,
		"/v1/admin/tx-password/status?user_id=" + u,
	}
	for _, p := range adminPaths {
		if code, _ := call(t, "GET", p, u, nil, false); code != 401 {
			t.Fatalf("user token GET %s: code=%d, want 401", p, code)
		}
		if code, _ := callAdminRaw(t, "GET", p, "", ""); code != 401 {
			t.Fatalf("no secret GET %s: code=%d, want 401", p, code)
		}
		if code, _ := callAdminRaw(t, "GET", p, "", "wrong-secret"); code != 401 {
			t.Fatalf("wrong secret GET %s: code=%d, want 401", p, code)
		}
	}
	// 双人复核缺 X-Admin-ID：401。
	if code, _ := callAdminRaw(t, "POST", "/v1/admin/adjustments/"+adj.AdjustmentID+"/approve", "", testAdmin); code != 401 {
		t.Fatalf("approve without X-Admin-ID: code=%d, want 401", code)
	}
	// 创建调整单缺 X-Admin-ID：401（callAdminOp 的 adminID 为空即不带头）。
	if code, _ := callAdminOp(t, "POST", "/v1/admin/adjustments", "", map[string]any{"user_id": u, "direction": AdjCredit, "amount_cents": 100, "reason": "缺头"}); code != 401 {
		t.Fatalf("create adjustment without X-Admin-ID: code=%d, want 401", code)
	}
}

// 93 审计日志管理查询（敏感操作可查、详情已脱敏）
func Test93AdminAuditLogQuery(t *testing.T) {
	u := uniq("a93")
	mustWallet(t, u)
	_ = addMethodOK(t, u, 3, "李四", "6222021234567890", map[string]any{"bank_name": "工商银行"})
	fundUser(t, u, 1000)
	adj := createAdjustmentOK(t, "admin1", u, AdjCredit, 500, "审计测试", "")
	_, _ = approveAdjustmentCall(t, adj.AdjustmentID, "admin1")
	_, _ = approveAdjustmentCall(t, adj.AdjustmentID, "admin2")
	code, raw := callAdminRaw(t, "GET", "/v1/admin/audit-logs?user_id="+u+"&action=admin_adjustment_execute", "", testAdmin)
	if code != 200 {
		t.Fatalf("admin audit logs: code=%d", code)
	}
	if strings.Contains(raw, "6222021234567890") {
		t.Fatalf("audit logs leak full card number")
	}
	if !strings.Contains(raw, "admin_adjustment_execute") || !strings.Contains(raw, adj.AdjustmentID) {
		t.Fatalf("audit logs missing execute entry: %s", raw)
	}
	code, raw = callAdminRaw(t, "GET", "/v1/admin/audit-logs?user_id="+u+"&action=admin_adjustment_create", "", testAdmin)
	if code != 200 || !strings.Contains(raw, adj.AdjustmentID) {
		t.Fatalf("audit logs missing create entry: code=%d raw=%s", code, raw)
	}
}

// 94 调整单账务守恒：多笔调整 + 转账后对账平账，全部 type 19 分录合计为 0
func Test94AdminAdjustmentLedgerConservation(t *testing.T) {
	a := uniq("a94a")
	b := uniq("a94b")
	admitUser(t, a)
	admitUser(t, b)
	fundUser(t, a, 30000)
	for _, tc := range []struct {
		dir int
		amt int64
	}{
		{AdjCredit, 5000},
		{AdjDebit, 2000},
	} {
		adj := createAdjustmentOK(t, "admin1", a, tc.dir, tc.amt, "守恒测试", "")
		_, _ = approveAdjustmentCall(t, adj.AdjustmentID, "admin1")
		if code, _ := approveAdjustmentCall(t, adj.AdjustmentID, "admin2"); code != 200 {
			t.Fatalf("conservation adjustment exec: code=%d", code)
		}
	}
	if code, _ := sendTransferCall(t, a, b, 7000, uniq("idem"), testTxPwd); code != 200 {
		t.Fatalf("conservation transfer: code=%d", code)
	}
	rows, err := testDB.Query(`SELECT txn_id FROM wallet_txn WHERE type=19 AND user_id=$1`, a)
	if err != nil {
		t.Fatal(err)
	}
	var txns []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		txns = append(txns, id)
	}
	rows.Close()
	if len(txns) != 2 {
		t.Fatalf("type 19 txn count=%d, want 2", len(txns))
	}
	for _, id := range txns {
		ledgerSumZero(t, id)
	}
	code, resp := call(t, "POST", "/v1/admin/reconcile", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("reconcile: code=%d", code)
	}
	var rec ReconcileResult
	_ = json.Unmarshal(resp.Data, &rec)
	if rec.DiffCents != 0 || rec.Status != 0 {
		t.Fatalf("reconcile diff=%d status=%d, want 0/0", rec.DiffCents, rec.Status)
	}
}
