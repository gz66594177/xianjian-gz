package fund

// 人工余额修改测试（单人直连入账，无双人复核）：
// 覆盖单人一次入账分毫不差、减少不足 400、理由枚举与备注规则、金额/方向校验、
// 审计日志、重复提交幂等、鉴权、列表与单查。双人复核不在本功能测试范围内。

import (
	"encoding/json"
	"strings"
	"testing"
)

func seedIncome(t *testing.T, uid string, amount int64) {
	t.Helper()
	code, _ := call(t, "POST", "/v1/ledger/income", uid, map[string]any{"amount_cents": amount, "idempotency_key": uniq("seed")}, false)
	if code != 200 {
		t.Fatalf("seed income %s %d: code=%d", uid, amount, code)
	}
}

func postBalAdj(t *testing.T, adminID string, body map[string]any) (int, apiResp) {
	t.Helper()
	return callAdminOp(t, "POST", "/v1/admin/balance-adjustments", adminID, body)
}

func decodeBalAdj(t *testing.T, resp apiResp) BalanceAdjustment {
	t.Helper()
	var a BalanceAdjustment
	if err := json.Unmarshal(resp.Data, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestBalanceAdjustCreditExact(t *testing.T) {
	u := uniq("baladj")
	mustWallet(t, u)
	seedIncome(t, u, 10000)
	code, resp := postBalAdj(t, "admin1", map[string]any{
		"user_id": u, "direction": 1, "amount_cents": 2500,
		"reason_code": "activity_reward", "note": "", "idempotency_key": uniq("baladj"),
	})
	if code != 200 || !resp.OK {
		t.Fatalf("credit: code=%d err=%+v", code, resp.Error)
	}
	a := decodeBalAdj(t, resp)
	if a.Status != 1 || a.ExecutedTxnID == "" {
		t.Fatalf("credit record %+v, want status=1 with txn", a)
	}
	if a.ReasonCode != "activity_reward" {
		t.Fatalf("reason_code=%s, want activity_reward", a.ReasonCode)
	}
	bal, _ := balance(t, u)
	if bal != 12500 {
		t.Fatalf("balance=%d, want 12500", bal)
	}
	ledgerSumZero(t, a.ExecutedTxnID)
}

func TestBalanceAdjustDebitExactAndInsufficient(t *testing.T) {
	u := uniq("baladj")
	mustWallet(t, u)
	seedIncome(t, u, 10000)
	code, resp := postBalAdj(t, "admin1", map[string]any{
		"user_id": u, "direction": 2, "amount_cents": 3000,
		"reason_code": "correction", "note": "多记一笔，冲回",
	})
	if code != 200 || !resp.OK {
		t.Fatalf("debit: code=%d err=%+v", code, resp.Error)
	}
	if bal, _ := balance(t, u); bal != 7000 {
		t.Fatalf("balance=%d, want 7000", bal)
	}
	// 减少超可用余额：400，余额不动，不留有效单据。
	code, _ = postBalAdj(t, "admin1", map[string]any{
		"user_id": u, "direction": 2, "amount_cents": 7001,
		"reason_code": "correction", "note": "超额",
	})
	if code != 400 {
		t.Fatalf("over-debit code=%d, want 400", code)
	}
	if bal, _ := balance(t, u); bal != 7000 {
		t.Fatalf("balance after failed debit=%d, want 7000", bal)
	}
	code, resp = call(t, "GET", "/v1/admin/balance-adjustments?user_id="+u, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("list: code=%d", code)
	}
	var list []BalanceAdjustment
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("list len=%d, want 1 (failed debit must not leave a record)", len(list))
	}
}

func TestBalanceAdjustReasonAndAmountValidation(t *testing.T) {
	u := uniq("baladj")
	mustWallet(t, u)
	seedIncome(t, u, 5000)
	bad := []map[string]any{
		{"user_id": u, "direction": 1, "amount_cents": 100, "reason_code": "not_a_reason"},
		{"user_id": u, "direction": 1, "amount_cents": 100, "reason_code": "other", "note": ""},
		{"user_id": u, "direction": 1, "amount_cents": 0, "reason_code": "activity_reward"},
		{"user_id": u, "direction": 1, "amount_cents": -50, "reason_code": "activity_reward"},
		{"user_id": u, "direction": 3, "amount_cents": 100, "reason_code": "activity_reward"},
		{"user_id": u, "direction": 1, "amount_cents": 100, "reason_code": ""},
	}
	for i, b := range bad {
		if code, _ := postBalAdj(t, "admin1", b); code != 400 {
			t.Fatalf("case %d code=%d, want 400", i, code)
		}
	}
	// 其他 + 备注通过；其余理由备注可选。
	code, resp := postBalAdj(t, "admin1", map[string]any{
		"user_id": u, "direction": 1, "amount_cents": 100,
		"reason_code": "other", "note": "客户特殊情况说明",
	})
	if code != 200 || !resp.OK {
		t.Fatalf("other+note: code=%d err=%+v", code, resp.Error)
	}
	if got := decodeBalAdj(t, resp); got.Note != "客户特殊情况说明" {
		t.Fatalf("note=%q, want it stored", got.Note)
	}
}

func TestBalanceAdjustIdempotent(t *testing.T) {
	u := uniq("baladj")
	mustWallet(t, u)
	seedIncome(t, u, 8000)
	key := uniq("baladj-key")
	body := map[string]any{
		"user_id": u, "direction": 1, "amount_cents": 2000,
		"reason_code": "internal_test", "idempotency_key": key,
	}
	code, resp := postBalAdj(t, "admin1", body)
	if code != 200 || !resp.OK {
		t.Fatalf("first: code=%d err=%+v", code, resp.Error)
	}
	first := decodeBalAdj(t, resp)
	code, resp = postBalAdj(t, "admin1", body)
	if code != 200 || !resp.OK {
		t.Fatalf("repeat: code=%d err=%+v", code, resp.Error)
	}
	second := decodeBalAdj(t, resp)
	if second.AdjustmentID != first.AdjustmentID || second.ExecutedTxnID != first.ExecutedTxnID {
		t.Fatalf("repeat returned different record: %s vs %s", second.AdjustmentID, first.AdjustmentID)
	}
	if bal, _ := balance(t, u); bal != 10000 {
		t.Fatalf("balance=%d, want 10000 (credited once)", bal)
	}
}

func TestBalanceAdjustAuthAndLookup(t *testing.T) {
	u := uniq("baladj")
	mustWallet(t, u)
	seedIncome(t, u, 1000)
	// 无 X-Admin-ID：401；无 secret：401。
	if code, _ := postBalAdj(t, "", map[string]any{"user_id": u, "direction": 1, "amount_cents": 100, "reason_code": "activity_reward"}); code != 401 {
		t.Fatalf("no admin id code=%d, want 401", code)
	}
	if code, _ := callAdminRaw(t, "POST", "/v1/admin/balance-adjustments", "admin1", ""); code != 401 {
		t.Fatalf("no secret code=%d, want 401", code)
	}
	code, resp := callAdminOp(t, "POST", "/v1/admin/balance-adjustments", "admin1", map[string]any{"user_id": u, "direction": 1, "amount_cents": 100, "reason_code": "compensation"})
	if code != 200 || !resp.OK {
		t.Fatalf("authed create: code=%d err=%+v", code, resp.Error)
	}
	a := decodeBalAdj(t, resp)
	code, resp = call(t, "GET", "/v1/admin/balance-adjustments/"+a.AdjustmentID, "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("get: code=%d", code)
	}
	if got := decodeBalAdj(t, resp); got.AdjustmentID != a.AdjustmentID {
		t.Fatalf("get id mismatch")
	}
	code, _ = call(t, "GET", "/v1/admin/balance-adjustments/baladj_missing", "", nil, true)
	if code != 404 {
		t.Fatalf("get missing code=%d, want 404", code)
	}
	// 未建钱包用户与平台户：400。
	if code, _ := postBalAdj(t, "admin1", map[string]any{"user_id": "nouser_" + uniq("x"), "direction": 1, "amount_cents": 100, "reason_code": "activity_reward"}); code != 400 {
		t.Fatalf("no-wallet code=%d, want 400", code)
	}
	if code, _ := postBalAdj(t, "admin1", map[string]any{"user_id": "platform:settlement", "direction": 1, "amount_cents": 100, "reason_code": "activity_reward"}); code != 400 {
		t.Fatalf("platform code=%d, want 400", code)
	}
}

func TestBalanceAdjustAuditLog(t *testing.T) {
	u := uniq("baladj")
	mustWallet(t, u)
	seedIncome(t, u, 6000)
	code, resp := postBalAdj(t, "admin9", map[string]any{
		"user_id": u, "direction": 2, "amount_cents": 1500,
		"reason_code": "compensation", "note": "赔付",
	})
	if code != 200 || !resp.OK {
		t.Fatalf("create: code=%d err=%+v", code, resp.Error)
	}
	a := decodeBalAdj(t, resp)
	code, resp = call(t, "GET", "/v1/admin/audit-logs?user_id="+u+"&action=balance_adjustment", "", nil, true)
	if code != 200 || !resp.OK {
		t.Fatalf("audit list: code=%d", code)
	}
	var logs []AuditLogEntry
	if err := json.Unmarshal(resp.Data, &logs); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range logs {
		if l.DetailMasked != nil && strings.Contains(*l.DetailMasked, a.AdjustmentID) && l.OperatorID != nil && *l.OperatorID == "admin9" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit log for %s by admin9 not found in %d rows", a.AdjustmentID, len(logs))
	}
}
