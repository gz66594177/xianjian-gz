package fund

// Phase 2 测试：IM token 鉴权、交易密码安全找回（身份核验 + 双人复核）、
// 提现方式边界（递补默认、删唯一、去重、格式校验）、准入引导。
// 全部用 stubVerifier（token "imtok-<uid>"），真实 IM 联调另见部署后的集成脚本。

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func callAdminOp(t *testing.T, method, path, adminID string, body any) (int, apiResp) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-Admin-Secret", testAdmin)
	if adminID != "" {
		req.Header.Set("X-Admin-ID", adminID)
	}
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	var resp apiResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func setTxPwd(t *testing.T, uid, pwd string) {
	t.Helper()
	code, _ := call(t, "POST", "/v1/tx-password/set", uid, map[string]any{"password": pwd, "confirm_password": pwd}, false)
	if code != 200 {
		t.Fatalf("set tx password %s: code=%d", uid, code)
	}
}

func addMethodOK(t *testing.T, uid string, typ int, name, acct string, extra map[string]any) WithdrawMethod {
	t.Helper()
	body := map[string]any{"type": typ, "real_name": name, "account_no": acct}
	for k, v := range extra {
		body[k] = v
	}
	code, resp := call(t, "POST", "/v1/withdraw-methods", uid, body, false)
	if code != 200 || !resp.OK {
		t.Fatalf("add method %s: code=%d err=%+v", uid, code, resp.Error)
	}
	var m WithdrawMethod
	if err := json.Unmarshal(resp.Data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func listMethodsOK(t *testing.T, uid string) []WithdrawMethod {
	t.Helper()
	code, resp := call(t, "GET", "/v1/withdraw-methods", uid, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("list methods %s: code=%d", uid, code)
	}
	var ms []WithdrawMethod
	if err := json.Unmarshal(resp.Data, &ms); err != nil {
		t.Fatal(err)
	}
	return ms
}

func countDefaults(ms []WithdrawMethod) int {
	n := 0
	for _, m := range ms {
		if m.IsDefault {
			n++
		}
	}
	return n
}

// 16 IM token 鉴权：无 token 401、伪造 token 401、平台户 token 403、IM 不可达 503（绝不 fail-open）
func Test16IMAuth(t *testing.T) {
	uid := uniq("u16")
	mustWallet(t, uid)
	// 无 token
	req := httptest.NewRequest("GET", "/v1/wallet/balance", nil)
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no token code=%d, want 401", rec.Code)
	}
	// 伪造 token
	req = httptest.NewRequest("GET", "/v1/wallet/balance", nil)
	req.Header.Set("Authorization", "Bearer imtok-bad")
	rec = httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("bad token code=%d, want 401", rec.Code)
	}
	// 平台户 token
	code, _ := call(t, "GET", "/v1/wallet/balance", PlatformUserID, nil, false)
	if code != 403 {
		t.Fatalf("platform token code=%d, want 403", code)
	}
	// IM 不可达 -> 503（用 downVerifier 的独立服务实例）
	cfg := Config{Addr: "127.0.0.1:0", DatabaseURL: testSrv.cfg.DatabaseURL, RedisAddr: testSrv.cfg.RedisAddr, AdminSecret: testAdmin, AESKey: testSrv.cfg.AESKey, TokenVerifier: downVerifier{}}
	srv := NewServer(cfg, testDB)
	req = httptest.NewRequest("GET", "/v1/wallet/balance", nil)
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("verifier down code=%d, want 503", rec.Code)
	}
	// Bearer 格式错误（非 Bearer 方案）-> 401
	req = httptest.NewRequest("GET", "/v1/wallet/balance", nil)
	req.Header.Set("Authorization", "Token imtok-"+uid)
	rec = httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("non-bearer code=%d, want 401", rec.Code)
	}
	// 旧 X-Internal-Secret 头不再被接受
	req = httptest.NewRequest("GET", "/v1/wallet/balance", nil)
	req.Header.Set("X-User-ID", uid)
	req.Header.Set("X-Internal-Secret", "test-internal-secret")
	rec = httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("legacy internal secret code=%d, want 401", rec.Code)
	}
}

// 17 token 冒用隔离：A 的方式 B 删/改不到，且 B 的列表里看不到 A 的任何信息
func Test17TokenImpersonation(t *testing.T) {
	uidA := uniq("u17a")
	uidB := uniq("u17b")
	mustWallet(t, uidA)
	mustWallet(t, uidB)
	setTxPwd(t, uidA, "123456")
	setTxPwd(t, uidB, "654321")
	mA := addMethodOK(t, uidA, 3, "张三", "6222021234567890", map[string]any{"bank_name": "工商银行"})
	// B 用自己的正确密码删 A 的方式 -> 找不到（不透露归属），且 A 的方式仍在
	code, _ := call(t, "DELETE", "/v1/withdraw-methods/"+mA.MethodID, uidB, map[string]any{"tx_password": "654321"}, false)
	if code != 400 {
		t.Fatalf("cross-user delete code=%d, want 400", code)
	}
	ms := listMethodsOK(t, uidA)
	if len(ms) != 1 || ms[0].MethodID != mA.MethodID {
		t.Fatalf("A method lost after B delete attempt: %+v", ms)
	}
	// B 的列表为空，不含 A 的脱敏账号
	msB := listMethodsOK(t, uidB)
	if len(msB) != 0 {
		t.Fatalf("B list leaked: %+v", msB)
	}
	// B 改 A 的方式 -> 400
	code, _ = call(t, "PUT", "/v1/withdraw-methods/"+mA.MethodID, uidB, map[string]any{"real_name": "李四", "tx_password": "654321"}, false)
	if code != 400 {
		t.Fatalf("cross-user update code=%d, want 400", code)
	}
}

// 18 找回全流程：身份核验 -> 双人复核 -> 本人重置 -> 一次性
func Test18RecoveryFullFlow(t *testing.T) {
	uid := uniq("u18")
	mustWallet(t, uid)
	addMethodOK(t, uid, 3, "李四", "6222021234567890", map[string]any{"bank_name": "工商银行"})
	setTxPwd(t, uid, "123456")
	// 身份核验失败：实名错、后 4 位错
	code, _ := call(t, "POST", "/v1/tx-password/recover/request", uid, map[string]any{"real_name": "王五", "account_last4": "7890"}, false)
	if code != 400 {
		t.Fatalf("wrong name request code=%d, want 400", code)
	}
	code, _ = call(t, "POST", "/v1/tx-password/recover/request", uid, map[string]any{"real_name": "李四", "account_last4": "0000"}, false)
	if code != 400 {
		t.Fatalf("wrong last4 request code=%d, want 400", code)
	}
	// 正确申请
	code, resp := call(t, "POST", "/v1/tx-password/recover/request", uid, map[string]any{"real_name": "李四", "account_last4": "7890"}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("recover request code=%d err=%+v", code, resp.Error)
	}
	var reqBody struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(resp.Data, &reqBody); err != nil || reqBody.RequestID == "" {
		t.Fatalf("recover request id missing: %s", string(resp.Data))
	}
	reqID := reqBody.RequestID
	// 待复核时重复申请 -> 返回同一单
	code, resp = call(t, "POST", "/v1/tx-password/recover/request", uid, map[string]any{"real_name": "李四", "account_last4": "7890"}, false)
	if code != 200 {
		t.Fatalf("duplicate pending request code=%d, want 200", code)
	}
	var dup struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(resp.Data, &dup)
	if dup.RequestID != reqID {
		t.Fatalf("duplicate pending returned %s, want %s", dup.RequestID, reqID)
	}
	// 未双人通过前重置 -> 400
	code, _ = call(t, "POST", "/v1/tx-password/recover/reset", uid, map[string]any{"request_id": reqID, "new_password": "111111", "confirm_password": "111111"}, false)
	if code != 400 {
		t.Fatalf("reset before approval code=%d, want 400", code)
	}
	// 管理员 1 approve -> 待第二人
	code, resp = callAdminOp(t, "POST", "/v1/admin/tx-password/recover/approve", "admin1", map[string]any{"request_id": reqID})
	if code != 200 {
		t.Fatalf("approve1 code=%d err=%+v", code, resp.Error)
	}
	var stage struct {
		Stage string `json:"stage"`
	}
	_ = json.Unmarshal(resp.Data, &stage)
	if stage.Stage != "awaiting_second_approval" {
		t.Fatalf("approve1 stage=%s, want awaiting_second_approval", stage.Stage)
	}
	// 同一人再批 -> 400
	code, _ = callAdminOp(t, "POST", "/v1/admin/tx-password/recover/approve", "admin1", map[string]any{"request_id": reqID})
	if code != 400 {
		t.Fatalf("same-admin second approve code=%d, want 400", code)
	}
	// 无 X-Admin-ID -> 401
	code, _ = callAdminOp(t, "POST", "/v1/admin/tx-password/recover/approve", "", map[string]any{"request_id": reqID})
	if code != 401 {
		t.Fatalf("approve without admin id code=%d, want 401", code)
	}
	// 管理员 2 approve -> approved
	code, resp = callAdminOp(t, "POST", "/v1/admin/tx-password/recover/approve", "admin2", map[string]any{"request_id": reqID})
	if code != 200 {
		t.Fatalf("approve2 code=%d err=%+v", code, resp.Error)
	}
	_ = json.Unmarshal(resp.Data, &stage)
	if stage.Stage != "approved" {
		t.Fatalf("approve2 stage=%s, want approved", stage.Stage)
	}
	// 本人重置：两次不一致 -> 400；正确 -> 成功；旧密码失效、新密码生效
	code, _ = call(t, "POST", "/v1/tx-password/recover/reset", uid, map[string]any{"request_id": reqID, "new_password": "111111", "confirm_password": "222222"}, false)
	if code != 400 {
		t.Fatalf("reset mismatch code=%d, want 400", code)
	}
	code, _ = call(t, "POST", "/v1/tx-password/recover/reset", uid, map[string]any{"request_id": reqID, "new_password": "111111", "confirm_password": "111111"}, false)
	if code != 200 {
		t.Fatalf("reset code=%d, want 200", code)
	}
	code, resp = call(t, "POST", "/v1/tx-password/verify", uid, map[string]any{"password": "123456"}, false)
	if code != 200 {
		t.Fatalf("verify old after reset code=%d", code)
	}
	var v struct {
		Verified bool `json:"verified"`
	}
	_ = json.Unmarshal(resp.Data, &v)
	if v.Verified {
		t.Fatalf("old password still verifies after reset")
	}
	code, resp = call(t, "POST", "/v1/tx-password/verify", uid, map[string]any{"password": "111111"}, false)
	if code != 200 {
		t.Fatalf("verify new after reset code=%d", code)
	}
	_ = json.Unmarshal(resp.Data, &v)
	if !v.Verified {
		t.Fatalf("new password does not verify after reset")
	}
	// 重置一次性：复用 -> 400
	code, _ = call(t, "POST", "/v1/tx-password/recover/reset", uid, map[string]any{"request_id": reqID, "new_password": "222222", "confirm_password": "222222"}, false)
	if code != 400 {
		t.Fatalf("reset reuse code=%d, want 400", code)
	}
	// 用户查自己的申请单：状态为已使用（2），且不含实名/后 4 位等身份材料
	code, resp = call(t, "GET", "/v1/tx-password/recover/requests", uid, nil, false)
	if code != 200 {
		t.Fatalf("list own requests code=%d", code)
	}
	if bytes.Contains(resp.Data, []byte("李四")) || bytes.Contains(resp.Data, []byte("7890")) {
		t.Fatalf("recovery list leaked identity material: %s", string(resp.Data))
	}
	var list []RecoveryRequest
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 1 || list[0].Status != RecoverUsed {
		t.Fatalf("own requests unexpected: %+v", list)
	}
}

// 19 找回拒绝与过期
func Test19RecoveryRejectAndExpiry(t *testing.T) {
	uid := uniq("u19")
	mustWallet(t, uid)
	addMethodOK(t, uid, 3, "赵六", "6222021234567890", map[string]any{"bank_name": "工商银行"})
	setTxPwd(t, uid, "123456")
	_, resp := call(t, "POST", "/v1/tx-password/recover/request", uid, map[string]any{"real_name": "赵六", "account_last4": "7890"}, false)
	var r struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(resp.Data, &r)
	// 拒绝后重置 -> 400；管理查询状态为已拒绝（3）
	code, _ := callAdminOp(t, "POST", "/v1/admin/tx-password/recover/reject", "admin1", map[string]any{"request_id": r.RequestID})
	if code != 200 {
		t.Fatalf("reject code=%d", code)
	}
	code, _ = call(t, "POST", "/v1/tx-password/recover/reset", uid, map[string]any{"request_id": r.RequestID, "new_password": "111111", "confirm_password": "111111"}, false)
	if code != 400 {
		t.Fatalf("reset after reject code=%d, want 400", code)
	}
	code, resp = callAdminOp(t, "GET", "/v1/admin/tx-password/recover/"+r.RequestID, "admin1", nil)
	if code != 200 {
		t.Fatalf("admin get rejected code=%d", code)
	}
	var rec RecoveryRequest
	_ = json.Unmarshal(resp.Data, &rec)
	if rec.Status != RecoverRejected {
		t.Fatalf("rejected status=%d, want 3", rec.Status)
	}
	// 过期：第二单，人为置过期后 approve 与 reset 均拒绝，查询为已过期（4）
	_, resp = call(t, "POST", "/v1/tx-password/recover/request", uid, map[string]any{"real_name": "赵六", "account_last4": "7890"}, false)
	_ = json.Unmarshal(resp.Data, &r)
	if _, err := testDB.Exec(`UPDATE wallet_tx_password_recovery SET expires_at=now()-interval '1 hour' WHERE request_id=$1`, r.RequestID); err != nil {
		t.Fatal(err)
	}
	code, _ = callAdminOp(t, "POST", "/v1/admin/tx-password/recover/approve", "admin1", map[string]any{"request_id": r.RequestID})
	if code != 400 {
		t.Fatalf("approve expired code=%d, want 400", code)
	}
	code, resp = callAdminOp(t, "GET", "/v1/admin/tx-password/recover/"+r.RequestID, "admin1", nil)
	if code != 200 {
		t.Fatalf("admin get expired code=%d", code)
	}
	_ = json.Unmarshal(resp.Data, &rec)
	if rec.Status != RecoverExpired {
		t.Fatalf("expired status=%d, want 4", rec.Status)
	}
}

// 20 提现方式边界：改默认、删默认递补、删唯一、设默认幂等
func Test20MethodEdges(t *testing.T) {
	uid := uniq("u20")
	mustWallet(t, uid)
	setTxPwd(t, uid, "123456")
	m1 := addMethodOK(t, uid, 1, "张三", "zhangsan@alipay.example", nil)
	m2 := addMethodOK(t, uid, 2, "张三", "wx12345", nil)
	m3 := addMethodOK(t, uid, 3, "张三", "6222021234567890", map[string]any{"bank_name": "工商银行"})
	// 首绑自动默认 m1；改默认到 m2
	if ms := listMethodsOK(t, uid); countDefaults(ms) != 1 || ms[0].MethodID != m1.MethodID || !ms[0].IsDefault {
		t.Fatalf("first method not auto default: %+v", ms)
	}
	code, _ := call(t, "POST", "/v1/withdraw-methods/"+m2.MethodID+"/set-default", uid, map[string]any{"tx_password": "123456"}, false)
	if code != 200 {
		t.Fatalf("set default m2 code=%d", code)
	}
	ms := listMethodsOK(t, uid)
	if countDefaults(ms) != 1 {
		t.Fatalf("after set-default defaults=%d, want 1", countDefaults(ms))
	}
	for _, m := range ms {
		if m.IsDefault && m.MethodID != m2.MethodID {
			t.Fatalf("default is %s, want %s", m.MethodID, m2.MethodID)
		}
	}
	// 取消 m2 的默认 -> 自动递补最早绑定的 m1
	code, _ = call(t, "PUT", "/v1/withdraw-methods/"+m2.MethodID, uid, map[string]any{"is_default": false, "tx_password": "123456"}, false)
	if code != 200 {
		t.Fatalf("unset default code=%d", code)
	}
	ms = listMethodsOK(t, uid)
	if countDefaults(ms) != 1 {
		t.Fatalf("after unset defaults=%d, want 1", countDefaults(ms))
	}
	for _, m := range ms {
		if m.IsDefault && m.MethodID != m1.MethodID {
			t.Fatalf("promoted default is %s, want %s (earliest)", m.MethodID, m1.MethodID)
		}
	}
	// 删默认 m1 -> 递补最早剩余 m2
	code, resp := call(t, "DELETE", "/v1/withdraw-methods/"+m1.MethodID, uid, map[string]any{"tx_password": "123456"}, false)
	if code != 200 {
		t.Fatalf("delete default code=%d", code)
	}
	var del DeleteMethodResult
	_ = json.Unmarshal(resp.Data, &del)
	if !del.Deleted || del.WasLastMethod || del.NewDefaultMethodID == nil || *del.NewDefaultMethodID != m2.MethodID {
		t.Fatalf("delete default result unexpected: %+v, want new default %s", del, m2.MethodID)
	}
	ms = listMethodsOK(t, uid)
	if countDefaults(ms) != 1 {
		t.Fatalf("after delete defaults=%d, want 1", countDefaults(ms))
	}
	// 删 m2（默认）-> 递补 m3；删 m3（唯一）-> was_last_method，准入变 need_method
	code, resp = call(t, "DELETE", "/v1/withdraw-methods/"+m2.MethodID, uid, map[string]any{"tx_password": "123456"}, false)
	if code != 200 {
		t.Fatalf("delete m2 code=%d", code)
	}
	_ = json.Unmarshal(resp.Data, &del)
	if del.NewDefaultMethodID == nil || *del.NewDefaultMethodID != m3.MethodID {
		t.Fatalf("delete m2 new default unexpected: %+v, want %s", del, m3.MethodID)
	}
	code, resp = call(t, "DELETE", "/v1/withdraw-methods/"+m3.MethodID, uid, map[string]any{"tx_password": "123456"}, false)
	if code != 200 {
		t.Fatalf("delete last code=%d", code)
	}
	_ = json.Unmarshal(resp.Data, &del)
	if !del.WasLastMethod || del.NewDefaultMethodID != nil {
		t.Fatalf("delete last result unexpected: %+v", del)
	}
	if ms := listMethodsOK(t, uid); len(ms) != 0 {
		t.Fatalf("methods remain after deleting all: %+v", ms)
	}
	code, resp = call(t, "GET", "/v1/finance/status", uid, nil, false)
	if code != 200 {
		t.Fatalf("status after deleting all code=%d", code)
	}
	var st FinanceStatus
	_ = json.Unmarshal(resp.Data, &st)
	if st.FinanceAccountStatus != "need_method" || st.NextStep != "bind_method" {
		t.Fatalf("status after deleting all: %+v", st)
	}
	// 设默认幂等：新绑自动默认，再设一次仍 1 个默认
	m4 := addMethodOK(t, uid, 1, "张三", "zhangsan2@alipay.example", nil)
	code, _ = call(t, "POST", "/v1/withdraw-methods/"+m4.MethodID+"/set-default", uid, map[string]any{"tx_password": "123456"}, false)
	if code != 200 {
		t.Fatalf("set-default idempotent code=%d", code)
	}
	if ms := listMethodsOK(t, uid); countDefaults(ms) != 1 {
		t.Fatalf("idempotent set-default defaults=%d, want 1", countDefaults(ms))
	}
}

// 21 方式校验：同类型同账号去重、银行卡格式
func Test21MethodValidation(t *testing.T) {
	uid := uniq("u21")
	mustWallet(t, uid)
	addMethodOK(t, uid, 1, "张三", "dup@alipay.example", nil)
	code, _ := call(t, "POST", "/v1/withdraw-methods", uid, map[string]any{"type": 1, "real_name": "张三", "account_no": "dup@alipay.example"}, false)
	if code != 400 {
		t.Fatalf("duplicate method code=%d, want 400", code)
	}
	// 同账号不同类型允许
	addMethodOK(t, uid, 2, "张三", "dup@alipay.example", nil)
	// 银行卡：11 位、非数字拒绝；19 位通过
	code, _ = call(t, "POST", "/v1/withdraw-methods", uid, map[string]any{"type": 3, "real_name": "张三", "account_no": "62220212345", "bank_name": "工商银行"}, false)
	if code != 400 {
		t.Fatalf("short bank card code=%d, want 400", code)
	}
	code, _ = call(t, "POST", "/v1/withdraw-methods", uid, map[string]any{"type": 3, "real_name": "张三", "account_no": "622202123456789A", "bank_name": "工商银行"}, false)
	if code != 400 {
		t.Fatalf("non-digit bank card code=%d, want 400", code)
	}
	addMethodOK(t, uid, 3, "张三", "6222021234567890123", map[string]any{"bank_name": "工商银行"})
	// 修改时格式同样校验
	ms := listMethodsOK(t, uid)
	var bankID string
	for _, m := range ms {
		if m.Type == 3 {
			bankID = m.MethodID
		}
	}
	setTxPwd(t, uid, "123456")
	code, _ = call(t, "PUT", "/v1/withdraw-methods/"+bankID, uid, map[string]any{"account_no": "123", "tx_password": "123456"}, false)
	if code != 400 {
		t.Fatalf("update bad bank card code=%d, want 400", code)
	}
}

// 22 准入引导：四态 next_step/guidance + 业务守卫
func Test22AdmissionGuidance(t *testing.T) {
	uid := uniq("u22")
	mustWallet(t, uid)
	code, resp := call(t, "GET", "/v1/finance/status", uid, nil, false)
	if code != 200 {
		t.Fatalf("status new user code=%d", code)
	}
	var st FinanceStatus
	_ = json.Unmarshal(resp.Data, &st)
	if st.FinanceAccountStatus != "need_method" || st.NextStep != "bind_method" || st.MethodCount != 0 {
		t.Fatalf("new user status: %+v", st)
	}
	if err := testSrv.requireAdmission(context.Background(), uid); err == nil {
		t.Fatalf("requireAdmission passed for need_method user")
	}
	addMethodOK(t, uid, 1, "张三", "zhangsan@alipay.example", nil)
	code, resp = call(t, "GET", "/v1/finance/status", uid, nil, false)
	_ = json.Unmarshal(resp.Data, &st)
	if st.FinanceAccountStatus != "need_password" || st.NextStep != "set_password" || !st.HasDefaultMethod {
		t.Fatalf("bound-no-password status: %+v", st)
	}
	if err := testSrv.requireAdmission(context.Background(), uid); err == nil {
		t.Fatalf("requireAdmission passed for need_password user")
	}
	setTxPwd(t, uid, "123456")
	code, resp = call(t, "GET", "/v1/finance/status", uid, nil, false)
	_ = json.Unmarshal(resp.Data, &st)
	if st.FinanceAccountStatus != "ok" || st.NextStep != "none" || st.MethodCount != 1 {
		t.Fatalf("complete user status: %+v", st)
	}
	if err := testSrv.requireAdmission(context.Background(), uid); err != nil {
		t.Fatalf("requireAdmission failed for ok user: %v", err)
	}
	// 冻结账户 -> frozen + contact_support
	if _, err := testDB.Exec(`UPDATE wallet_account SET status=1 WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	code, resp = call(t, "GET", "/v1/finance/status", uid, nil, false)
	_ = json.Unmarshal(resp.Data, &st)
	if st.FinanceAccountStatus != "frozen" || st.NextStep != "contact_support" {
		t.Fatalf("frozen user status: %+v", st)
	}
}
