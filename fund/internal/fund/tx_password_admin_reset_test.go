package fund

// 交易密码管理员发起重置测试：发起建记录 + 审计、双人审批后用户自设生效、
// 未审批重置被拒、响应无密码哈希、重复发起幂等、鉴权与 404/理由校验、
// 用户侧列表可见管理员发起的记录。审批语义与既有 recoverApprove 完全一致。

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func initiateTxReset(t *testing.T, adminID, uid, reason string) AdminRecoveryRequest {
	t.Helper()
	code, resp := callAdminOp(t, "POST", "/v1/admin/tx-password/reset-request", adminID, map[string]any{"user_id": uid, "reason": reason})
	if code != 200 || !resp.OK {
		t.Fatalf("initiate code=%d resp=%+v", code, resp)
	}
	var rec AdminRecoveryRequest
	if err := json.Unmarshal(resp.Data, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func verifyTxPwd(t *testing.T, uid, pwd string) bool {
	t.Helper()
	code, resp := call(t, "POST", "/v1/tx-password/verify", uid, map[string]any{"password": pwd}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("verify code=%d resp=%+v", code, resp)
	}
	var v struct {
		Verified bool `json:"verified"`
		Locked   bool `json:"locked"`
	}
	if err := json.Unmarshal(resp.Data, &v); err != nil {
		t.Fatal(err)
	}
	return v.Verified
}

func TestTxResetAdminInitiate(t *testing.T) {
	uid := uniq("txr")
	mustWallet(t, uid)
	setTxPwd(t, uid, "111111")
	rec := initiateTxReset(t, "adminA", uid, "用户来电称忘记密码")
	if rec.Status != RecoverPending || rec.UserID != uid {
		t.Fatalf("record = %+v", rec)
	}
	if rec.InitiatedByAdminID == nil || *rec.InitiatedByAdminID != "adminA" {
		t.Fatalf("initiated_by = %+v", rec.InitiatedByAdminID)
	}
	if rec.Reason == nil || *rec.Reason != "用户来电称忘记密码" {
		t.Fatalf("reason = %+v", rec.Reason)
	}
	// 审计：操作人 + 理由
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_audit_log WHERE user_id=$1 AND action='tx_password_admin_reset_request' AND operator_id='adminA' AND detail_masked LIKE '%用户来电称忘记密码%'`, uid).Scan(&n); err != nil || n < 1 {
		t.Fatalf("audit missing: n=%d err=%v", n, err)
	}
}

func TestTxResetAdminResponseHasNoHash(t *testing.T) {
	uid := uniq("txr")
	mustWallet(t, uid)
	code, resp := callAdminOp(t, "POST", "/v1/admin/tx-password/reset-request", "adminA", map[string]any{"user_id": uid, "reason": "r"})
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	if strings.Contains(string(resp.Data), "password_hash") {
		t.Fatalf("response leaks password_hash: %s", resp.Data)
	}
}

func TestTxResetAdminIdempotent(t *testing.T) {
	uid := uniq("txr")
	mustWallet(t, uid)
	r1 := initiateTxReset(t, "adminA", uid, "首次理由")
	r2 := initiateTxReset(t, "adminB", uid, "重复理由应被忽略")
	if r1.RequestID != r2.RequestID {
		t.Fatalf("idempotent violated: %s vs %s", r1.RequestID, r2.RequestID)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_tx_password_recovery WHERE user_id=$1 AND status=0`, uid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("pending count=%d err=%v", n, err)
	}
}

func TestTxResetAdminValidation(t *testing.T) {
	uid := uniq("txr")
	mustWallet(t, uid)
	// 理由必填
	code, _ := callAdminOp(t, "POST", "/v1/admin/tx-password/reset-request", "adminA", map[string]any{"user_id": uid, "reason": "  "})
	if code != 400 {
		t.Fatalf("empty reason code=%d, want 400", code)
	}
	// 不存在用户（无钱包、无密码）404
	code, resp := callAdminOp(t, "POST", "/v1/admin/tx-password/reset-request", "adminA", map[string]any{"user_id": uniq("txr-none"), "reason": "r"})
	if code != 404 {
		t.Fatalf("unknown user code=%d resp=%+v, want 404", code, resp)
	}
	// 无 X-Admin-ID 401
	code, _ = callAdminOp(t, "POST", "/v1/admin/tx-password/reset-request", "", map[string]any{"user_id": uid, "reason": "r"})
	if code != 401 {
		t.Fatalf("no admin id code=%d, want 401", code)
	}
	// 无 secret 401
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(map[string]any{"user_id": uid, "reason": "r"})
	req := httptest.NewRequest("POST", "/v1/admin/tx-password/reset-request", &buf)
	req.Header.Set("X-Admin-ID", "adminA")
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no secret code=%d, want 401", rec.Code)
	}
}

func TestTxResetAdminDualApproveThenUserReset(t *testing.T) {
	uid := uniq("txr")
	mustWallet(t, uid)
	setTxPwd(t, uid, "111111")
	rec := initiateTxReset(t, "adminA", uid, "管理员发起")
	// 未审批时用户重置被拒
	code, _ := call(t, "POST", "/v1/tx-password/recover/reset", uid, map[string]any{"request_id": rec.RequestID, "new_password": "222222", "confirm_password": "222222"}, false)
	if code != 400 {
		t.Fatalf("reset before approval code=%d, want 400", code)
	}
	// 第一名审批 -> 待第二名
	code, resp := callAdminOp(t, "POST", "/v1/admin/tx-password/recover/approve", "adminA", map[string]any{"request_id": rec.RequestID})
	if code != 200 {
		t.Fatalf("approve1 code=%d resp=%+v", code, resp)
	}
	var stage struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(resp.Data, &stage)
	if stage.Stage != "awaiting_second_approval" {
		t.Fatalf("stage=%s, want awaiting_second_approval", stage.Stage)
	}
	// 同一人第二次审批被拒（审批语义未弱化）
	code, _ = callAdminOp(t, "POST", "/v1/admin/tx-password/recover/approve", "adminA", map[string]any{"request_id": rec.RequestID})
	if code != 400 {
		t.Fatalf("same admin approve2 code=%d, want 400", code)
	}
	// 第二名不同管理员审批 -> 通过
	code, resp = callAdminOp(t, "POST", "/v1/admin/tx-password/recover/approve", "adminB", map[string]any{"request_id": rec.RequestID})
	if code != 200 {
		t.Fatalf("approve2 code=%d resp=%+v", code, resp)
	}
	// 用户自设新密码生效，旧密码失效
	code, _ = call(t, "POST", "/v1/tx-password/recover/reset", uid, map[string]any{"request_id": rec.RequestID, "new_password": "222222", "confirm_password": "222222"}, false)
	if code != 200 {
		t.Fatalf("user reset code=%d, want 200", code)
	}
	if verifyTxPwd(t, uid, "111111") {
		t.Fatalf("old password still valid after reset")
	}
	if !verifyTxPwd(t, uid, "222222") {
		t.Fatalf("new password not valid after reset")
	}
	// 用户侧列表可见该记录（已使用状态）
	code, resp = call(t, "GET", "/v1/tx-password/recover/requests", uid, nil, false)
	if code != 200 {
		t.Fatalf("user list code=%d", code)
	}
	var list []RecoveryRequest
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range list {
		if r.RequestID == rec.RequestID && r.Status == RecoverUsed {
			found = true
		}
	}
	if !found {
		t.Fatalf("admin-initiated record not visible in user list: %+v", list)
	}
}

func TestTxResetAdminReject(t *testing.T) {
	uid := uniq("txr")
	mustWallet(t, uid)
	rec := initiateTxReset(t, "adminA", uid, "可疑申请")
	code, _ := callAdminOp(t, "POST", "/v1/admin/tx-password/recover/reject", "adminB", map[string]any{"request_id": rec.RequestID})
	if code != 200 {
		t.Fatalf("reject code=%d, want 200", code)
	}
	code, _ = call(t, "POST", "/v1/tx-password/recover/reset", uid, map[string]any{"request_id": rec.RequestID, "new_password": "222222", "confirm_password": "222222"}, false)
	if code != 400 {
		t.Fatalf("reset after reject code=%d, want 400", code)
	}
}
