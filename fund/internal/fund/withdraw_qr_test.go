package fund

// 0011：提现方式收款二维码（微信/支付宝二选一代替账号）+ 每种方式类型每账号最多绑一次。
// 注意：二维码不代表商户 API 自动打款，只供人工打款时查看；提现建单仍按 method_id 走。

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func uploadMethodQR(t *testing.T, uid string, data []byte) string {
	t.Helper()
	code, resp := callMultipartUser(t, "POST", "/v1/withdraw-methods/upload-qr", uid, "qr", "qr.png", data)
	if code != 200 || !resp.OK {
		t.Fatalf("upload method qr code=%d resp=%+v", code, resp)
	}
	var m map[string]string
	if err := json.Unmarshal(resp.Data, &m); err != nil {
		t.Fatal(err)
	}
	if m["qr_file_id"] == "" || m["qr_url"] == "" {
		t.Fatalf("upload method qr missing fields: %+v", m)
	}
	return m["qr_file_id"]
}

func Test22WithdrawMethodQR(t *testing.T) {
	uid := uniq("u22qr")
	mustWallet(t, uid)

	// 二维码方式：不填账号，只传 qr_file_id，即可绑定微信。
	qr1 := uploadMethodQR(t, uid, tinyPNG)
	code, resp := call(t, "POST", "/v1/withdraw-methods", uid,
		map[string]any{"type": 2, "real_name": "张三", "qr_file_id": qr1}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("add qr method code=%d resp=%+v", code, resp)
	}
	var m WithdrawMethod
	if err := json.Unmarshal(resp.Data, &m); err != nil {
		t.Fatal(err)
	}
	if m.QRFileID == nil || *m.QRFileID != qr1 || m.QRURL == nil || *m.QRURL != "/v1/uploads/"+qr1 {
		t.Fatalf("qr method fields unexpected: %+v", m)
	}
	if m.AccountNo != "" {
		t.Fatalf("qr method account_no should be empty, got %q", m.AccountNo)
	}
	// 本人可凭鉴权取用该二维码；他人取用应 404（不透露存在性）。
	req := httptest.NewRequest("GET", "/v1/uploads/"+qr1, nil)
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("owner get qr code=%d, want 200", rec.Code)
	}
	req2 := httptest.NewRequest("GET", "/v1/uploads/"+qr1, nil)
	req2.Header.Set("Authorization", "Bearer imtok-"+uniq("u22other"))
	rec2 := httptest.NewRecorder()
	testSrv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != 404 {
		t.Fatalf("other user get qr code=%d, want 404", rec2.Code)
	}

	// 一型一次：同一账号再绑微信（换个二维码、换个账号）一律 400，且文案为客服提示。
	qr2 := uploadMethodQR(t, uid, tinyPNG)
	code, _ = call(t, "POST", "/v1/withdraw-methods", uid,
		map[string]any{"type": 2, "real_name": "张三", "qr_file_id": qr2}, false)
	if code != 400 {
		t.Fatalf("second wechat method code=%d, want 400", code)
	}
	// 账号与二维码二选一：二者都传、都不传都 400；银行卡传二维码也 400。
	uid2 := uniq("u22qr2")
	mustWallet(t, uid2)
	qr3 := uploadMethodQR(t, uid2, tinyPNG)
	code, _ = call(t, "POST", "/v1/withdraw-methods", uid2,
		map[string]any{"type": 1, "real_name": "张三", "account_no": "a@alipay.example", "qr_file_id": qr3}, false)
	if code != 400 {
		t.Fatalf("both account and qr code=%d, want 400", code)
	}
	code, _ = call(t, "POST", "/v1/withdraw-methods", uid2,
		map[string]any{"type": 1, "real_name": "张三"}, false)
	if code != 400 {
		t.Fatalf("neither account nor qr code=%d, want 400", code)
	}
	code, _ = call(t, "POST", "/v1/withdraw-methods", uid2,
		map[string]any{"type": 3, "real_name": "张三", "account_no": "6222021234567890", "bank_name": "工商银行", "qr_file_id": qr3}, false)
	if code != 400 {
		t.Fatalf("bank with qr code=%d, want 400", code)
	}
	// 同一二维码不能绑给第二个账号。
	uid3 := uniq("u22qr3")
	mustWallet(t, uid3)
	code, _ = call(t, "POST", "/v1/withdraw-methods", uid3,
		map[string]any{"type": 2, "real_name": "李四", "qr_file_id": qr1}, false)
	if code != 400 {
		t.Fatalf("reused qr code=%d, want 400", code)
	}
}
