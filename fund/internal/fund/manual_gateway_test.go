package fund

// 0010 测试：手动充值（收款方式 + 二维码 + 截图 + 双人复核入账）与网关支付（适配器 + 回调入账）。
// 编号续接 Phase 9（Test125 起）。全部用 stubVerifier（token "imtok-<uid>"），管理接口用 test-admin-secret；
// 网关用测试 stub 适配器（testGatewaySecret，仅测试库）；生产默认无适配器 = 通道关闭（Test138 另起无适配器服务验证）。

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testGatewaySecret = "test-gateway-secret-0010"

// stubGatewayAdapter：测试用网关适配器。收银台地址为固定测试域名（绝非真实网关）；
// 回调验签为 HMAC-SHA256(secret, order_id|gateway_order_no|amount|status)。
type stubGatewayAdapter struct{ secret string }

func (a stubGatewayAdapter) CreatePayment(ctx context.Context, orderID, userID string, amountCents int64) (string, string, error) {
	return "https://gateway.test/pay/" + orderID, "gwref_" + orderID, nil
}

func (a stubGatewayAdapter) VerifyCallback(r *http.Request) (*GatewayCallback, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var raw struct {
		OrderID        string `json:"order_id"`
		GatewayOrderNo string `json:"gateway_order_no"`
		AmountCents    int64  `json:"amount_cents"`
		Status         string `json:"status"`
		Signature      string `json:"signature"`
		FailureReason  string `json:"failure_reason"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(a.secret))
	fmt.Fprintf(mac, "%s|%s|%d|%s", raw.OrderID, raw.GatewayOrderNo, raw.AmountCents, raw.Status)
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(raw.Signature)) {
		return nil, errGatewayBadSignature
	}
	return &GatewayCallback{OrderID: raw.OrderID, GatewayOrderNo: raw.GatewayOrderNo, AmountCents: raw.AmountCents, Status: raw.Status, FailureReason: raw.FailureReason}, nil
}

func gwSign(orderID, gwNo string, amount int64, status string) string {
	mac := hmac.New(sha256.New, []byte(testGatewaySecret))
	fmt.Fprintf(mac, "%s|%s|%d|%s", orderID, gwNo, amount, status)
	return hex.EncodeToString(mac.Sum(nil))
}

// 最小合法图片字节：PNG 用真实 1x1 透明图；JPEG 仅需 JFIF 头即可通过服务端嗅探（服务端不解码像素）。
var tinyPNG = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1F, 0x15, 0xC4, 0x89,
	0x00, 0x00, 0x00, 0x0A, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9C, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
	0x0D, 0x0A, 0x2D, 0xB4, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82}
var tinyJPEG = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0xFF, 0xD9}

func callMultipartUser(t *testing.T, method, path, uid, field, filename string, data []byte) (int, apiResp) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	var resp apiResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func callMultipartAdmin(t *testing.T, method, path, adminID, field, filename string, data []byte) (int, apiResp) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
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

func createPayMethodOK(t *testing.T, typ, name string, sortOrder int) PaymentMethod {
	t.Helper()
	code, resp := callAdminOp(t, "POST", "/v1/admin/payment-methods", "admin1",
		map[string]any{"type": typ, "display_name": name, "sort_order": sortOrder})
	if code != 200 || !resp.OK {
		t.Fatalf("create pay method: code=%d err=%+v", code, resp.Error)
	}
	var m PaymentMethod
	if err := json.Unmarshal(resp.Data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func uploadQROK(t *testing.T, methodID string, data []byte) PaymentMethod {
	t.Helper()
	code, resp := callMultipartAdmin(t, "POST", "/v1/admin/payment-methods/"+methodID+"/qr", "admin1", "qr", "qr.png", data)
	if code != 200 || !resp.OK {
		t.Fatalf("upload qr: code=%d err=%+v", code, resp.Error)
	}
	var m PaymentMethod
	if err := json.Unmarshal(resp.Data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func createManualOK(t *testing.T, uid, methodID string, amount int64, idem string) ManualRechargeOrder {
	t.Helper()
	body := map[string]any{"method_id": methodID, "amount_cents": amount}
	if idem != "" {
		body["idempotency_key"] = idem
	}
	code, resp := call(t, "POST", "/v1/recharge/manual", uid, body, false)
	if code != 200 || !resp.OK {
		t.Fatalf("create manual: code=%d err=%+v", code, resp.Error)
	}
	var o ManualRechargeOrder
	if err := json.Unmarshal(resp.Data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func uploadProofOK(t *testing.T, uid, orderID string, data []byte) ManualRechargeOrder {
	t.Helper()
	code, resp := callMultipartUser(t, "POST", "/v1/recharge/"+orderID+"/proof", uid, "proof", "proof.png", data)
	if code != 200 || !resp.OK {
		t.Fatalf("upload proof: code=%d err=%+v", code, resp.Error)
	}
	var o ManualRechargeOrder
	if err := json.Unmarshal(resp.Data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func manualPendingOK(t *testing.T, uid string, amount int64) ManualRechargeOrder {
	t.Helper()
	m := createPayMethodOK(t, "wechat", "测试微信收款", 1)
	m = uploadQROK(t, m.MethodID, tinyPNG)
	o := createManualOK(t, uid, m.MethodID, amount, uniq("mr"))
	return uploadProofOK(t, uid, o.OrderID, tinyJPEG)
}

func approveManual(t *testing.T, orderID, adminID string) (int, apiResp) {
	t.Helper()
	return callAdminOp(t, "POST", "/v1/admin/recharge/"+orderID+"/approve", adminID, map[string]any{})
}

func Test125PayMethodCRUD(t *testing.T) {
	// 无管理密钥 401；非法类型 400；空展示名 400。
	code, _ := callAdminOp(t, "POST", "/v1/admin/payment-methods", "", map[string]any{"type": "wechat", "display_name": "x"})
	if code != 401 {
		t.Fatalf("no admin secret code=%d, want 401", code)
	}
	code, _ = callAdminOp(t, "POST", "/v1/admin/payment-methods", "admin1", map[string]any{"type": "cash", "display_name": "x"})
	if code != 400 {
		t.Fatalf("bad type code=%d, want 400", code)
	}
	m := createPayMethodOK(t, "alipay", "测试支付宝", 5)
	if m.Type != "alipay" || m.Enabled != true || m.QRFileID != nil {
		t.Fatalf("method fields wrong: %+v", m)
	}
	// 修改：改名 + 停用。
	code, resp := callAdminOp(t, "PUT", "/v1/admin/payment-methods/"+m.MethodID, "admin1",
		map[string]any{"display_name": "改名后", "enabled": false})
	if code != 200 || !resp.OK {
		t.Fatalf("update method: code=%d err=%+v", code, resp.Error)
	}
	var updated PaymentMethod
	_ = json.Unmarshal(resp.Data, &updated)
	if updated.DisplayName != "改名后" || updated.Enabled != false {
		t.Fatalf("update not applied: %+v", updated)
	}
	// 管理列表含停用项；用户列表不含停用项。
	code, resp = callAdminOp(t, "GET", "/v1/admin/payment-methods", "admin1", nil)
	if code != 200 {
		t.Fatalf("admin list: code=%d", code)
	}
	var adminList []PaymentMethod
	_ = json.Unmarshal(resp.Data, &adminList)
	found := false
	for _, x := range adminList {
		if x.MethodID == m.MethodID {
			found = true
		}
	}
	if !found {
		t.Fatalf("admin list missing disabled method")
	}
	code, resp = call(t, "GET", "/v1/recharge/methods", "u125", nil, false)
	if code != 200 {
		t.Fatalf("user list: code=%d", code)
	}
	var userList []PaymentMethod
	_ = json.Unmarshal(resp.Data, &userList)
	for _, x := range userList {
		if x.MethodID == m.MethodID {
			t.Fatalf("user list leaks disabled method")
		}
	}
}

func Test126QRUploadValidation(t *testing.T) {
	m := createPayMethodOK(t, "bank", "测试银行", 1)
	// 非图片 400；超大（PNG 头 + 填充至 5MB+1）400。
	code, _ := callMultipartAdmin(t, "POST", "/v1/admin/payment-methods/"+m.MethodID+"/qr", "admin1", "qr", "qr.txt", []byte("not an image at all"))
	if code != 400 {
		t.Fatalf("non-image qr code=%d, want 400", code)
	}
	big := append(append([]byte{}, tinyPNG...), make([]byte, (5<<20)+1-len(tinyPNG))...)
	code, _ = callMultipartAdmin(t, "POST", "/v1/admin/payment-methods/"+m.MethodID+"/qr", "admin1", "qr", "qr.png", big)
	if code != 400 {
		t.Fatalf("oversize qr code=%d, want 400", code)
	}
	m = uploadQROK(t, m.MethodID, tinyPNG)
	if m.QRFileID == nil || m.QRURL == nil || !strings.HasPrefix(*m.QRURL, "/v1/uploads/") {
		t.Fatalf("qr not attached: %+v", m)
	}
	// 用户取二维码：字节一致；未鉴权 401。
	req := httptest.NewRequest("GET", *m.QRURL, nil)
	req.Header.Set("Authorization", "Bearer imtok-u126")
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), tinyPNG) {
		t.Fatalf("qr fetch: code=%d len=%d, want 200 len=%d", rec.Code, rec.Body.Len(), len(tinyPNG))
	}
	req2 := httptest.NewRequest("GET", *m.QRURL, nil)
	rec2 := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != 401 {
		t.Fatalf("qr unauth code=%d, want 401", rec2.Code)
	}
}

func Test127ManualCreateValidation(t *testing.T) {
	uid := "u127"
	// 无二维码方式 400；停用方式 400；未知方式 404。
	noQR := createPayMethodOK(t, "wechat", "无码方式", 1)
	code, _ := call(t, "POST", "/v1/recharge/manual", uid, map[string]any{"method_id": noQR.MethodID, "amount_cents": 1000}, false)
	if code != 400 {
		t.Fatalf("no-qr method code=%d, want 400", code)
	}
	disabled := uploadQROK(t, createPayMethodOK(t, "alipay", "停用方式", 1).MethodID, tinyPNG)
	_, _ = callAdminOp(t, "PUT", "/v1/admin/payment-methods/"+disabled.MethodID, "admin1", map[string]any{"enabled": false})
	code, _ = call(t, "POST", "/v1/recharge/manual", uid, map[string]any{"method_id": disabled.MethodID, "amount_cents": 1000}, false)
	if code != 400 {
		t.Fatalf("disabled method code=%d, want 400", code)
	}
	code, _ = call(t, "POST", "/v1/recharge/manual", uid, map[string]any{"method_id": "pm_missing", "amount_cents": 1000}, false)
	if code != 404 {
		t.Fatalf("unknown method code=%d, want 404", code)
	}
	okMethod := uploadQROK(t, createPayMethodOK(t, "wechat", "正常方式", 1).MethodID, tinyPNG)
	// 金额 0 / 负数 / 非整数分 400。
	for _, amt := range []any{0, -100, 100.5} {
		code, _ = call(t, "POST", "/v1/recharge/manual", uid, map[string]any{"method_id": okMethod.MethodID, "amount_cents": amt}, false)
		if code != 400 {
			t.Fatalf("amount %v code=%d, want 400", amt, code)
		}
	}
	// 正常建单：状态 10 + 方式信息 + 二维码地址。
	o := createManualOK(t, uid, okMethod.MethodID, 12345, uniq("mr127"))
	if o.Status != ManualAwaitingProof || o.RechargeKind != "manual" || o.Channel != "manual" || o.QRURL == nil || o.MethodDisplayName != "正常方式" {
		t.Fatalf("manual order wrong: %+v", o)
	}
	// 建单不产生资金变动：新用户余额仍为 0。
	mustWallet(t, uid)
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("create moved balance: %d", bal)
	}
}

func Test128ManualCreateIdempotent(t *testing.T) {
	uid := "u128"
	m := uploadQROK(t, createPayMethodOK(t, "wechat", "幂等方式", 1).MethodID, tinyPNG)
	key := uniq("mr128")
	o1 := createManualOK(t, uid, m.MethodID, 5000, key)
	o2 := createManualOK(t, uid, m.MethodID, 5000, key)
	if o1.OrderID != o2.OrderID {
		t.Fatalf("idempotent replay gave different orders %s vs %s", o1.OrderID, o2.OrderID)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_order WHERE user_id=$1 AND client_idempotency_key=$2 AND recharge_kind='manual'`, uid, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("manual order rows=%d, want 1", n)
	}
}

func Test129ProofUploadValidation(t *testing.T) {
	uid := "u129"
	m := uploadQROK(t, createPayMethodOK(t, "wechat", "凭证方式", 1).MethodID, tinyPNG)
	o := createManualOK(t, uid, m.MethodID, 8000, uniq("mr129"))
	// 他人订单 404；非图片 400；超大 400。
	code, _ := callMultipartUser(t, "POST", "/v1/recharge/"+o.OrderID+"/proof", "u129-other", "proof", "p.png", tinyPNG)
	if code != 404 {
		t.Fatalf("other user proof code=%d, want 404", code)
	}
	code, _ = callMultipartUser(t, "POST", "/v1/recharge/"+o.OrderID+"/proof", uid, "proof", "p.txt", []byte("hello"))
	if code != 400 {
		t.Fatalf("non-image proof code=%d, want 400", code)
	}
	big := append(append([]byte{}, tinyPNG...), make([]byte, (5<<20)+1-len(tinyPNG))...)
	code, _ = callMultipartUser(t, "POST", "/v1/recharge/"+o.OrderID+"/proof", uid, "proof", "p.png", big)
	if code != 400 {
		t.Fatalf("oversize proof code=%d, want 400", code)
	}
	// 正常上传：状态 11 + 凭证字段。
	o = uploadProofOK(t, uid, o.OrderID, tinyJPEG)
	if o.Status != ManualPendingReview || o.ProofFileID == nil || o.ProofURL == nil {
		t.Fatalf("proof not attached: %+v", o)
	}
}

func Test130ProofReuploadReplaces(t *testing.T) {
	uid := "u130"
	m := uploadQROK(t, createPayMethodOK(t, "wechat", "重传方式", 1).MethodID, tinyPNG)
	o := createManualOK(t, uid, m.MethodID, 9000, uniq("mr130"))
	o = uploadProofOK(t, uid, o.OrderID, tinyPNG)
	oldFile := *o.ProofFileID
	o = uploadProofOK(t, uid, o.OrderID, tinyJPEG)
	if o.ProofFileID == nil || *o.ProofFileID == oldFile {
		t.Fatalf("reupload did not replace file: %v vs %v", o.ProofFileID, oldFile)
	}
	var n int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM fund_upload_file WHERE file_id=$1`, oldFile).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("old proof file row still exists")
	}
	// 旧文件取用 404（行已删）。
	req := httptest.NewRequest("GET", "/v1/uploads/"+oldFile, nil)
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("old proof fetch code=%d, want 404", rec.Code)
	}
}

func Test131ProofOnPaidOrderRejected(t *testing.T) {
	uid := "u131"
	o := manualPendingOK(t, uid, 7000)
	code, resp := approveManual(t, o.OrderID, "admin1")
	if code != 200 || resp.Data == nil {
		t.Fatalf("approve1: code=%d err=%+v", code, resp.Error)
	}
	code, _ = approveManual(t, o.OrderID, "admin2")
	if code != 200 {
		t.Fatalf("approve2: code=%d", code)
	}
	code, _ = callMultipartUser(t, "POST", "/v1/recharge/"+o.OrderID+"/proof", uid, "proof", "p.png", tinyPNG)
	if code != 409 {
		t.Fatalf("proof on paid order code=%d, want 409", code)
	}
}

func Test132AdminPendingList(t *testing.T) {
	uid := "u132"
	o := manualPendingOK(t, uid, 6000)
	// 待上传单（10）不在 pending 列；待审核单（11）在列；user 过滤生效。
	_ = createManualOK(t, uid, uploadQROK(t, createPayMethodOK(t, "wechat", "pending方式", 1).MethodID, tinyPNG).MethodID, 1000, uniq("mr132"))
	code, resp := callAdminOp(t, "GET", "/v1/admin/recharge/pending", "admin1", nil)
	if code != 200 {
		t.Fatalf("pending list: code=%d", code)
	}
	var list []ManualRechargeOrder
	_ = json.Unmarshal(resp.Data, &list)
	found, foundOther := false, false
	for _, x := range list {
		if x.OrderID == o.OrderID {
			found = true
		}
		if x.Status != ManualPendingReview {
			t.Fatalf("pending list contains non-11 order %s status %d", x.OrderID, x.Status)
		}
		if x.UserID != uid && x.OrderID != o.OrderID {
			foundOther = true
		}
	}
	if !found {
		t.Fatalf("pending list missing order %s", o.OrderID)
	}
	_ = foundOther
	code, resp = callAdminOp(t, "GET", "/v1/admin/recharge/pending?user_id="+uid, "admin1", nil)
	if code != 200 {
		t.Fatalf("pending filtered: code=%d", code)
	}
	var filtered []ManualRechargeOrder
	_ = json.Unmarshal(resp.Data, &filtered)
	for _, x := range filtered {
		if x.UserID != uid {
			t.Fatalf("pending user filter leaks %s", x.UserID)
		}
	}
}

func Test133ManualApproveDualReview(t *testing.T) {
	uid := "u133"
	mustWallet(t, uid)
	o := manualPendingOK(t, uid, 12345)
	// 第一复核：余额不动。
	code, resp := approveManual(t, o.OrderID, "admin1")
	if code != 200 {
		t.Fatalf("approve1: code=%d err=%+v", code, resp.Error)
	}
	var stage map[string]string
	_ = json.Unmarshal(resp.Data, &stage)
	if stage["stage"] != "awaiting_second_approval" {
		t.Fatalf("approve1 stage=%v", stage)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("first approval moved balance: %d", bal)
	}
	// 同一人第二次 400。
	code, _ = approveManual(t, o.OrderID, "admin1")
	if code != 400 {
		t.Fatalf("same admin twice code=%d, want 400", code)
	}
	// 第二名不同管理员：分毫不差入账。
	code, resp = approveManual(t, o.OrderID, "admin2")
	if code != 200 {
		t.Fatalf("approve2: code=%d err=%+v", code, resp.Error)
	}
	bal, _ = balance(t, uid)
	if bal != 12345 {
		t.Fatalf("balance after approve=%d, want 12345", bal)
	}
	code, resp = call(t, "GET", "/v1/recharge/orders/"+o.OrderID, uid, nil, false)
	if code != 200 {
		t.Fatalf("detail: code=%d", code)
	}
	var done ManualRechargeOrder
	_ = json.Unmarshal(resp.Data, &done)
	if done.Status != RechargePaid || done.PaidTxnID == nil || done.ReviewSecondAdminID == nil || *done.ReviewSecondAdminID != "admin2" {
		t.Fatalf("approved order wrong: %+v", done)
	}
	ledgerSumZero(t, *done.PaidTxnID)
	var txnType int
	var refType string
	if err := testDB.QueryRow(`SELECT type, ref_type FROM wallet_txn WHERE txn_id=$1`, *done.PaidTxnID).Scan(&txnType, &refType); err != nil {
		t.Fatal(err)
	}
	if txnType != TxnRecharge || refType != "recharge" {
		t.Fatalf("paid txn type=%d ref=%s, want 20/recharge", txnType, refType)
	}
	var entries int
	if err := testDB.QueryRow(`SELECT COUNT(*) FROM wallet_ledger WHERE txn_id=$1`, *done.PaidTxnID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 2 {
		t.Fatalf("ledger entries=%d, want 2", entries)
	}
}

func Test134ManualApproveRepeatRejected(t *testing.T) {
	uid := "u134"
	o := manualPendingOK(t, uid, 5000)
	_, _ = approveManual(t, o.OrderID, "admin1")
	_, _ = approveManual(t, o.OrderID, "admin2")
	// 已终态订单再审批 = 非法状态迁移（本服务统一 409），与同人复核的 400 区分。
	code, _ := approveManual(t, o.OrderID, "admin1")
	if code != 409 {
		t.Fatalf("approve paid order code=%d, want 409", code)
	}
	code, _ = approveManual(t, o.OrderID, "admin2")
	if code != 409 {
		t.Fatalf("approve paid order (2nd admin) code=%d, want 409", code)
	}
}

func Test135ManualRejectNoCredit(t *testing.T) {
	uid := "u135"
	mustWallet(t, uid)
	o := manualPendingOK(t, uid, 8000)
	var txnBefore int
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE user_id=$1`, uid).Scan(&txnBefore)
	code, resp := callAdminOp(t, "POST", "/v1/admin/recharge/"+o.OrderID+"/reject", "admin1", map[string]any{"review_note": "截图模糊"})
	if code != 200 || !resp.OK {
		t.Fatalf("reject: code=%d err=%+v", code, resp.Error)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("reject moved balance: %d", bal)
	}
	var txnAfter int
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE user_id=$1`, uid).Scan(&txnAfter)
	if txnAfter != txnBefore {
		t.Fatalf("reject created txn: %d -> %d", txnBefore, txnAfter)
	}
	code, resp = call(t, "GET", "/v1/recharge/orders/"+o.OrderID, uid, nil, false)
	if code != 200 {
		t.Fatalf("detail: code=%d", code)
	}
	var done ManualRechargeOrder
	_ = json.Unmarshal(resp.Data, &done)
	if done.Status != ManualRejected || done.ReviewNote == nil || *done.ReviewNote != "截图模糊" {
		t.Fatalf("rejected order wrong: %+v", done)
	}
	// 非待审核单再拒绝 = 非法状态迁移（本服务统一 409）。
	code, _ = callAdminOp(t, "POST", "/v1/admin/recharge/"+o.OrderID+"/reject", "admin1", map[string]any{"review_note": "x"})
	if code != 409 {
		t.Fatalf("reject rejected order code=%d, want 409", code)
	}
}

func Test136ManualOrderVisibility(t *testing.T) {
	uid := "u136"
	o := manualPendingOK(t, uid, 4000)
	// 他人详情 404。
	code, _ := call(t, "GET", "/v1/recharge/orders/"+o.OrderID, "u136-other", nil, false)
	if code != 404 {
		t.Fatalf("other user detail code=%d, want 404", code)
	}
	// 回调列表（Phase 8）不含手动单。
	code, resp := call(t, "GET", "/v1/recharges", uid, nil, false)
	if code != 200 {
		t.Fatalf("callback list: code=%d", code)
	}
	var cbList []RechargeOrder
	_ = json.Unmarshal(resp.Data, &cbList)
	for _, x := range cbList {
		if x.OrderID == o.OrderID {
			t.Fatalf("callback list leaks manual order")
		}
	}
	// 通用列表 kind 过滤：manual 含、callback 不含。
	code, resp = call(t, "GET", "/v1/recharge/orders?kind=manual", uid, nil, false)
	if code != 200 {
		t.Fatalf("generic manual list: code=%d", code)
	}
	var mList []RechargeOrderView
	_ = json.Unmarshal(resp.Data, &mList)
	found := false
	for _, x := range mList {
		if x.OrderID == o.OrderID {
			found = true
		}
		if x.RechargeKind != "manual" {
			t.Fatalf("kind filter leaks %s", x.RechargeKind)
		}
	}
	if !found {
		t.Fatalf("generic manual list missing order")
	}
	code, resp = call(t, "GET", "/v1/recharge/orders?kind=callback", uid, nil, false)
	if code != 200 {
		t.Fatalf("generic callback list: code=%d", code)
	}
	var cList []RechargeOrderView
	_ = json.Unmarshal(resp.Data, &cList)
	for _, x := range cList {
		if x.OrderID == o.OrderID {
			t.Fatalf("callback kind filter leaks manual order")
		}
	}
}

func Test137ManualAuditLogs(t *testing.T) {
	uid := "u137"
	beforeCreate := auditCount(t, uid, "manual_recharge_create")
	beforeProof := auditCount(t, uid, "manual_recharge_proof_upload")
	beforeExec := auditCount(t, uid, "admin_manual_recharge_execute")
	o := manualPendingOK(t, uid, 3000)
	_, _ = approveManual(t, o.OrderID, "admin1")
	_, _ = approveManual(t, o.OrderID, "admin2")
	if auditCount(t, uid, "manual_recharge_create") <= beforeCreate {
		t.Fatalf("missing create audit")
	}
	if auditCount(t, uid, "manual_recharge_proof_upload") <= beforeProof {
		t.Fatalf("missing proof audit")
	}
	if auditCount(t, uid, "admin_manual_recharge_execute") <= beforeExec {
		t.Fatalf("missing execute audit")
	}
}

func Test138GatewayUnconfigured(t *testing.T) {
	// 另起无适配器服务（同测试库）：建单 400 GATEWAY_NOT_CONFIGURED，回调 501，均不入账。
	uploadDir, err := os.MkdirTemp("", "fund-uploads-nogw")
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	srv := NewServer(Config{Addr: "127.0.0.1:0", AdminSecret: testAdmin, AESKey: key, TokenVerifier: stubVerifier{}, UploadDir: uploadDir}, testDB)
	uid := "u138"
	mustWallet(t, uid)
	body, _ := json.Marshal(map[string]any{"amount_cents": 10000})
	req := httptest.NewRequest("POST", "/v1/recharge/gateway", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("unconfigured create code=%d, want 400", rec.Code)
	}
	var resp apiResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Error.Code != "GATEWAY_NOT_CONFIGURED" {
		t.Fatalf("unconfigured code=%s, want GATEWAY_NOT_CONFIGURED", resp.Error.Code)
	}
	req2 := httptest.NewRequest("POST", "/v1/payments/callback/gateway", bytes.NewReader([]byte("{}")))
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != 501 {
		t.Fatalf("unconfigured callback code=%d, want 501", rec2.Code)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("unconfigured gateway moved balance: %d", bal)
	}
}

func gwCreateOK(t *testing.T, uid string, amount int64, idem string) GatewayRechargeOrder {
	t.Helper()
	body := map[string]any{"amount_cents": amount}
	if idem != "" {
		body["idempotency_key"] = idem
	}
	code, resp := call(t, "POST", "/v1/recharge/gateway", uid, body, false)
	if code != 200 || !resp.OK {
		t.Fatalf("gateway create: code=%d err=%+v", code, resp.Error)
	}
	var o GatewayRechargeOrder
	if err := json.Unmarshal(resp.Data, &o); err != nil {
		t.Fatal(err)
	}
	return o
}

func gwCallback(t *testing.T, orderID, gwNo string, amount int64, status, sig string) (int, apiResp) {
	t.Helper()
	body := map[string]any{"order_id": orderID, "gateway_order_no": gwNo, "amount_cents": amount, "status": status, "signature": sig}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest("POST", "/v1/payments/callback/gateway", &buf)
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	var resp apiResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func Test139GatewayPollZeroCredit(t *testing.T) {
	// “是，已付款”零入账：App 只轮询状态（只读 GET），余额与订单状态均不动。
	uid := "u139"
	mustWallet(t, uid)
	o := gwCreateOK(t, uid, 20000, uniq("gw139"))
	if o.Status != RechargePending || o.CashierURL == nil || !strings.HasPrefix(*o.CashierURL, "https://gateway.test/pay/") {
		t.Fatalf("gateway order wrong: %+v", o)
	}
	if o.ExpiresAt == nil || time.Until(*o.ExpiresAt) < 29*time.Minute {
		t.Fatalf("gateway expiry wrong: %v", o.ExpiresAt)
	}
	for i := 0; i < 5; i++ {
		code, resp := call(t, "GET", "/v1/recharge/orders/"+o.OrderID, uid, nil, false)
		if code != 200 || !resp.OK {
			t.Fatalf("poll %d: code=%d", i, code)
		}
		var polled GatewayRechargeOrder
		_ = json.Unmarshal(resp.Data, &polled)
		if polled.Status != RechargePending {
			t.Fatalf("poll changed status to %d", polled.Status)
		}
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("polling moved balance: %d", bal)
	}
}

func Test140GatewayCallbackPaid(t *testing.T) {
	uid := "u140"
	mustWallet(t, uid)
	o := gwCreateOK(t, uid, 15000, uniq("gw140"))
	gwNo := "gwno140"
	code, resp := gwCallback(t, o.OrderID, gwNo, 15000, "paid", gwSign(o.OrderID, gwNo, 15000, "paid"))
	if code != 200 || !resp.OK {
		t.Fatalf("gateway callback: code=%d err=%+v", code, resp.Error)
	}
	var done GatewayRechargeOrder
	_ = json.Unmarshal(resp.Data, &done)
	if done.Status != RechargePaid || done.PaidTxnID == nil {
		t.Fatalf("paid order wrong: %+v", done)
	}
	bal, _ := balance(t, uid)
	if bal != 15000 {
		t.Fatalf("balance=%d, want 15000", bal)
	}
	ledgerSumZero(t, *done.PaidTxnID)
	var txnType int
	var refType string
	if err := testDB.QueryRow(`SELECT type, ref_type FROM wallet_txn WHERE txn_id=$1`, *done.PaidTxnID).Scan(&txnType, &refType); err != nil {
		t.Fatal(err)
	}
	if txnType != TxnRecharge || refType != "recharge" {
		t.Fatalf("txn type=%d ref=%s, want 20/recharge", txnType, refType)
	}
}

func Test141GatewayCallbackIdempotent(t *testing.T) {
	uid := "u141"
	mustWallet(t, uid)
	o := gwCreateOK(t, uid, 9000, uniq("gw141"))
	gwNo := "gwno141"
	code, _ := gwCallback(t, o.OrderID, gwNo, 9000, "paid", gwSign(o.OrderID, gwNo, 9000, "paid"))
	if code != 200 {
		t.Fatalf("first callback: code=%d", code)
	}
	code, _ = gwCallback(t, o.OrderID, gwNo, 9000, "paid", gwSign(o.OrderID, gwNo, 9000, "paid"))
	if code != 200 {
		t.Fatalf("replay callback: code=%d", code)
	}
	bal, _ := balance(t, uid)
	if bal != 9000 {
		t.Fatalf("replay double-credited: %d", bal)
	}
	var n int
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM wallet_txn WHERE ref_id=$1 AND type=20`, o.OrderID).Scan(&n)
	if n != 1 {
		t.Fatalf("credit txn rows=%d, want 1", n)
	}
}

func Test142GatewayCallbackBadSignature(t *testing.T) {
	uid := "u142"
	mustWallet(t, uid)
	o := gwCreateOK(t, uid, 11000, uniq("gw142"))
	code, _ := gwCallback(t, o.OrderID, "gwno142", 11000, "paid", "bad-signature")
	if code != 401 {
		t.Fatalf("bad signature code=%d, want 401", code)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("bad signature credited: %d", bal)
	}
	code, resp := call(t, "GET", "/v1/recharge/orders/"+o.OrderID, uid, nil, false)
	if code != 200 {
		t.Fatalf("detail: code=%d", code)
	}
	var polled GatewayRechargeOrder
	_ = json.Unmarshal(resp.Data, &polled)
	if polled.Status != RechargePending {
		t.Fatalf("bad signature changed status to %d", polled.Status)
	}
}

func Test143GatewayCallbackAmountMismatch(t *testing.T) {
	uid := "u143"
	mustWallet(t, uid)
	o := gwCreateOK(t, uid, 12000, uniq("gw143"))
	before := auditCount(t, uid, "gateway_callback_amount_mismatch")
	code, resp := gwCallback(t, o.OrderID, "gwno143", 12001, "paid", gwSign(o.OrderID, "gwno143", 12001, "paid"))
	if code != 400 || resp.Error.Code != "CALLBACK_AMOUNT_MISMATCH" {
		t.Fatalf("mismatch: code=%d err=%+v, want 400/CALLBACK_AMOUNT_MISMATCH", code, resp.Error)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("mismatch credited: %d", bal)
	}
	if auditCount(t, uid, "gateway_callback_amount_mismatch") <= before {
		t.Fatalf("missing mismatch audit")
	}
}

func Test144GatewayCallbackFailed(t *testing.T) {
	uid := "u144"
	mustWallet(t, uid)
	o := gwCreateOK(t, uid, 7000, uniq("gw144"))
	code, resp := gwCallback(t, o.OrderID, "gwno144", 7000, "failed", gwSign(o.OrderID, "gwno144", 7000, "failed"))
	if code != 200 || !resp.OK {
		t.Fatalf("failed callback: code=%d err=%+v", code, resp.Error)
	}
	var done GatewayRechargeOrder
	_ = json.Unmarshal(resp.Data, &done)
	if done.Status != RechargeFailed {
		t.Fatalf("failed order status=%d, want 2", done.Status)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("failed callback credited: %d", bal)
	}
}

func Test145GatewayExpiredCallbackRefused(t *testing.T) {
	uid := "u145"
	mustWallet(t, uid)
	o := gwCreateOK(t, uid, 13000, uniq("gw145"))
	// 把过期时间拨到过去（模拟超时订单）。
	if _, err := testDB.Exec(`UPDATE wallet_order SET expires_at = now() - interval '1 hour' WHERE order_id=$1`, o.OrderID); err != nil {
		t.Fatal(err)
	}
	// 轮询详情：expired 计算字段为真，但状态与余额不动。
	code, resp := call(t, "GET", "/v1/recharge/orders/"+o.OrderID, uid, nil, false)
	if code != 200 {
		t.Fatalf("detail: code=%d", code)
	}
	var polled GatewayRechargeOrder
	_ = json.Unmarshal(resp.Data, &polled)
	if !polled.Expired || polled.Status != RechargePending {
		t.Fatalf("expired flag wrong: expired=%v status=%d", polled.Expired, polled.Status)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("expired poll moved balance: %d", bal)
	}
	// 过期回调：拒绝入账并关单。
	code, resp = gwCallback(t, o.OrderID, "gwno145", 13000, "paid", gwSign(o.OrderID, "gwno145", 13000, "paid"))
	if code != 409 {
		t.Fatalf("expired callback code=%d, want 409", code)
	}
	bal, _ = balance(t, uid)
	if bal != 0 {
		t.Fatalf("expired callback credited: %d", bal)
	}
	code, resp = call(t, "GET", "/v1/recharge/orders/"+o.OrderID, uid, nil, false)
	if code != 200 {
		t.Fatalf("detail after: code=%d", code)
	}
	_ = json.Unmarshal(resp.Data, &polled)
	if polled.Status != GatewayClosed {
		t.Fatalf("expired order status=%d, want 4", polled.Status)
	}
}

func Test146GatewayExpireScan(t *testing.T) {
	uid := "u146"
	mustWallet(t, uid)
	expired := gwCreateOK(t, uid, 5000, uniq("gw146e"))
	fresh := gwCreateOK(t, uid, 6000, uniq("gw146f"))
	if _, err := testDB.Exec(`UPDATE wallet_order SET expires_at = now() - interval '1 hour' WHERE order_id=$1`, expired.OrderID); err != nil {
		t.Fatal(err)
	}
	code, resp := callAdminOp(t, "POST", "/v1/admin/recharge/gateway/expire", "admin1", map[string]any{})
	if code != 200 || !resp.OK {
		t.Fatalf("expire scan: code=%d err=%+v", code, resp.Error)
	}
	var out struct {
		ClosedIDs []string `json:"closed_ids"`
	}
	_ = json.Unmarshal(resp.Data, &out)
	closed := false
	for _, id := range out.ClosedIDs {
		if id == expired.OrderID {
			closed = true
		}
		if id == fresh.OrderID {
			t.Fatalf("expire scan closed fresh order")
		}
	}
	if !closed {
		t.Fatalf("expire scan missed expired order")
	}
	code, resp = call(t, "GET", "/v1/recharge/orders/"+fresh.OrderID, uid, nil, false)
	if code != 200 {
		t.Fatalf("fresh detail: code=%d", code)
	}
	var polled GatewayRechargeOrder
	_ = json.Unmarshal(resp.Data, &polled)
	if polled.Status != RechargePending {
		t.Fatalf("fresh order status changed to %d", polled.Status)
	}
}

func Test147GatewayCallbackUnknownOrder(t *testing.T) {
	code, resp := gwCallback(t, "gw_missing", "gwno147", 1000, "paid", gwSign("gw_missing", "gwno147", 1000, "paid"))
	if code != 404 {
		t.Fatalf("unknown order callback code=%d err=%+v, want 404", code, resp.Error)
	}
}

func Test148UploadAuthIsolation(t *testing.T) {
	uid := "u148"
	m := uploadQROK(t, createPayMethodOK(t, "wechat", "隔离方式", 1).MethodID, tinyPNG)
	o := manualPendingOK(t, uid, 4000)
	// 凭证：他人 404、本人 200、管理 200。
	proofURL := *o.ProofURL
	req := httptest.NewRequest("GET", proofURL, nil)
	req.Header.Set("Authorization", "Bearer imtok-u148-other")
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("other user proof fetch code=%d, want 404", rec.Code)
	}
	req = httptest.NewRequest("GET", proofURL, nil)
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec = httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), tinyJPEG) {
		t.Fatalf("owner proof fetch code=%d len=%d", rec.Code, rec.Body.Len())
	}
	req = httptest.NewRequest("GET", proofURL, nil)
	req.Header.Set("X-Admin-Secret", testAdmin)
	rec = httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("admin proof fetch code=%d, want 200", rec.Code)
	}
	// 停用方式的二维码：用户取用 404。
	_, _ = callAdminOp(t, "PUT", "/v1/admin/payment-methods/"+m.MethodID, "admin1", map[string]any{"enabled": false})
	req = httptest.NewRequest("GET", *m.QRURL, nil)
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec = httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatalf("disabled method qr fetch code=%d, want 404", rec.Code)
	}
}

func Test149ThreeKindsCoexist(t *testing.T) {
	uid := "u149"
	mustWallet(t, uid)
	cb := paidRecharge(t, uid, 1000) // Phase 8 回调单（已有 helper）
	mn := manualPendingOK(t, uid, 2000)
	gw := gwCreateOK(t, uid, 3000, uniq("gw149"))
	code, resp := call(t, "GET", "/v1/recharge/orders", uid, nil, false)
	if code != 200 {
		t.Fatalf("generic list: code=%d", code)
	}
	var list []RechargeOrderView
	_ = json.Unmarshal(resp.Data, &list)
	kinds := map[string]bool{}
	for _, x := range list {
		kinds[x.RechargeKind] = true
	}
	for _, want := range []string{"callback", "manual", "gateway"} {
		if !kinds[want] {
			t.Fatalf("generic list missing kind %s (have %v)", want, kinds)
		}
	}
	_ = cb
	_ = mn
	_ = gw
	// 回调列表只含回调单。
	code, resp = call(t, "GET", "/v1/recharges", uid, nil, false)
	if code != 200 {
		t.Fatalf("callback list: code=%d", code)
	}
	var cbList []RechargeOrder
	_ = json.Unmarshal(resp.Data, &cbList)
	for _, x := range cbList {
		if x.OrderID == mn.OrderID || x.OrderID == gw.OrderID {
			t.Fatalf("callback list leaks kind order %s", x.OrderID)
		}
	}
}

func Test150LedgerConservationManualGateway(t *testing.T) {
	// 新用户两笔入账（手动审核 + 网关回调）：用户余额增量 = 平台户余额增量 = 两单之和。
	uid := "u150"
	mustWallet(t, uid)
	var platBefore int64
	_ = testDB.QueryRow(`SELECT balance_cents FROM wallet_account WHERE user_id='platform:settlement'`).Scan(&platBefore)
	mn := manualPendingOK(t, uid, 11111)
	_, _ = approveManual(t, mn.OrderID, "admin1")
	_, _ = approveManual(t, mn.OrderID, "admin2")
	gw := gwCreateOK(t, uid, 22222, uniq("gw150"))
	code, _ := gwCallback(t, gw.OrderID, "gwno150", 22222, "paid", gwSign(gw.OrderID, "gwno150", 22222, "paid"))
	if code != 200 {
		t.Fatalf("gateway callback: code=%d", code)
	}
	bal, _ := balance(t, uid)
	if bal != 33333 {
		t.Fatalf("user balance=%d, want 33333", bal)
	}
	var platAfter int64
	_ = testDB.QueryRow(`SELECT balance_cents FROM wallet_account WHERE user_id='platform:settlement'`).Scan(&platAfter)
	if platAfter-platBefore != 33333 {
		t.Fatalf("platform delta=%d, want 33333", platAfter-platBefore)
	}
}

func Test151CrossKindIsolation(t *testing.T) {
	uid := "u151"
	mn := manualPendingOK(t, uid, 5000)
	// 网关回调指向手动单：按网关 kind 查不到 → 404，不入账。
	code, _ := gwCallback(t, mn.OrderID, "gwno151", 5000, "paid", gwSign(mn.OrderID, "gwno151", 5000, "paid"))
	if code != 404 {
		t.Fatalf("gateway callback on manual order code=%d, want 404", code)
	}
	// 手动审核指向网关单：按手动 kind 查不到 → 404。
	gw := gwCreateOK(t, uid, 6000, uniq("gw151"))
	code, _ = approveManual(t, gw.OrderID, "admin1")
	if code != 404 {
		t.Fatalf("manual approve on gateway order code=%d, want 404", code)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("cross-kind moved balance: %d", bal)
	}
}

func Test152ManualCreateNeedsNoAdmission(t *testing.T) {
	// 建单不产生资金变动，故不设准入/交易密码门禁：全新用户（无提现方式、无交易密码）仍可建手动单。
	uid := "u152fresh"
	m := uploadQROK(t, createPayMethodOK(t, "alipay", "无门禁方式", 1).MethodID, tinyPNG)
	o := createManualOK(t, uid, m.MethodID, 2500, uniq("mr152"))
	if o.Status != ManualAwaitingProof {
		t.Fatalf("fresh user manual order status=%d, want 10", o.Status)
	}
}
