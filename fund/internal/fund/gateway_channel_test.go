package fund

// 0012 测试：网关充值通道配置（厂商开关）与建单门禁。
// 编号续接 Test152。开关语义：
//   关（行不存在或 enabled=false）→ 建单 400 GATEWAY_NOT_CONFIGURED；
//   开但无厂商适配器 → 仍 400 GATEWAY_NOT_CONFIGURED，绝不生成假收银台；
//   开且有适配器 → 正常建单。
// 未传 vendor 的老口径建单不受通道表约束（仅适配器门禁），故本文件测试均显式传 vendor；
// 旧测试（无 vendor）行为不变。

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func createGatewayChannelOK(t *testing.T, vendor, name string, enabled bool, params map[string]any) GatewayChannel {
	t.Helper()
	body := map[string]any{"vendor": vendor, "display_name": name, "enabled": enabled}
	if params != nil {
		body["params"] = params
	}
	code, resp := callAdminOp(t, "POST", "/v1/admin/gateway-channels", "admin1", body)
	if code != 200 || !resp.OK {
		t.Fatalf("create gateway channel: code=%d err=%+v", code, resp.Error)
	}
	var c GatewayChannel
	if err := json.Unmarshal(resp.Data, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func gwCreateWithVendor(t *testing.T, srv *Server, uid, vendor string, amount int64) (int, apiResp) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"amount_cents": amount, "vendor": vendor})
	req := httptest.NewRequest("POST", "/v1/recharge/gateway", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer imtok-"+uid)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var resp apiResp
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return rec.Code, resp
}

func noAdapterServer(t *testing.T) *Server {
	t.Helper()
	uploadDir, err := os.MkdirTemp("", "fund-uploads-nogw12")
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return NewServer(Config{Addr: "127.0.0.1:0", AdminSecret: testAdmin, AESKey: key, TokenVerifier: stubVerifier{}, UploadDir: uploadDir}, testDB)
}

func Test153GatewayChannelCRUD(t *testing.T) {
	vendor := uniq("gwch")
	c := createGatewayChannelOK(t, vendor, "测试网关", true, map[string]any{"merchant_id": "m123", "callback_url": "https://example.test/cb"})
	if c.Vendor != vendor || c.DisplayName != "测试网关" || !c.Enabled {
		t.Fatalf("channel fields wrong: %+v", c)
	}
	if c.Params["merchant_id"] != "m123" {
		t.Fatalf("channel params wrong: %+v", c.Params)
	}
	// 重复 vendor → 409。
	code, resp := callAdminOp(t, "POST", "/v1/admin/gateway-channels", "admin1",
		map[string]any{"vendor": vendor, "display_name": "重复"})
	if code != 409 || resp.Error.Code != "GATEWAY_CHANNEL_EXISTS" {
		t.Fatalf("duplicate vendor: code=%d err=%+v", code, resp.Error)
	}
	// 非法 vendor → 400。
	code, resp = callAdminOp(t, "POST", "/v1/admin/gateway-channels", "admin1",
		map[string]any{"vendor": "Bad Vendor!", "display_name": "x"})
	if code != 400 {
		t.Fatalf("bad vendor: code=%d err=%+v", code, resp.Error)
	}
	// 密钥类 params 键 → 400，且绝不落库。
	code, resp = callAdminOp(t, "POST", "/v1/admin/gateway-channels", "admin1",
		map[string]any{"vendor": uniq("gwch"), "display_name": "x", "params": map[string]any{"api_secret": "shh"}})
	if code != 400 || resp.Error.Code != "BAD_REQUEST" {
		t.Fatalf("secret param: code=%d err=%+v", code, resp.Error)
	}
	code, resp = callAdminOp(t, "POST", "/v1/admin/gateway-channels", "admin1",
		map[string]any{"vendor": uniq("gwch"), "display_name": "x", "params": map[string]any{"private_key": "shh"}})
	if code != 400 {
		t.Fatalf("private_key param: code=%d err=%+v", code, resp.Error)
	}
	// 单查与列表。
	code, resp = callAdminOp(t, "GET", "/v1/admin/gateway-channels/"+vendor, "admin1", nil)
	if code != 200 || !resp.OK {
		t.Fatalf("get channel: code=%d err=%+v", code, resp.Error)
	}
	code, resp = callAdminOp(t, "GET", "/v1/admin/gateway-channels", "admin1", nil)
	if code != 200 {
		t.Fatalf("list channels: code=%d", code)
	}
	var list []GatewayChannel
	_ = json.Unmarshal(resp.Data, &list)
	found := false
	for _, x := range list {
		if x.Vendor == vendor {
			found = true
		}
		// 响应里绝无密钥字段。
		raw, _ := json.Marshal(x)
		if strings.Contains(strings.ToLower(string(raw)), "secret") || strings.Contains(strings.ToLower(string(raw)), "private_key") {
			t.Fatalf("channel response leaks secret-like field: %s", raw)
		}
	}
	if !found {
		t.Fatalf("list missing channel %s", vendor)
	}
	// 更新：改名 + 停用 + params 整包替换。
	code, resp = callAdminOp(t, "PUT", "/v1/admin/gateway-channels/"+vendor, "admin1",
		map[string]any{"display_name": "改名网关", "enabled": false, "params": map[string]any{"merchant_id": "m999"}})
	if code != 200 || !resp.OK {
		t.Fatalf("update channel: code=%d err=%+v", code, resp.Error)
	}
	var updated GatewayChannel
	_ = json.Unmarshal(resp.Data, &updated)
	if updated.DisplayName != "改名网关" || updated.Enabled != false || updated.Params["merchant_id"] != "m999" {
		t.Fatalf("update not applied: %+v", updated)
	}
	if _, ok := updated.Params["callback_url"]; ok {
		t.Fatalf("params should be replaced wholesale, got: %+v", updated.Params)
	}
	// 查不存在的通道 → 404。
	code, resp = callAdminOp(t, "GET", "/v1/admin/gateway-channels/no-such-vendor-xyz", "admin1", nil)
	if code != 404 || resp.Error.Code != "GATEWAY_CHANNEL_NOT_FOUND" {
		t.Fatalf("missing channel: code=%d err=%+v", code, resp.Error)
	}
}

func Test154GatewaySwitchOffCloses(t *testing.T) {
	vendor := uniq("gwch")
	createGatewayChannelOK(t, vendor, "开关关", false, nil)
	uid := "u154"
	mustWallet(t, uid)
	// 有适配器（testSrv stub）但开关关 → 仍关。
	code, resp := gwCreateWithVendor(t, testSrv, uid, vendor, 10000)
	if code != 400 || resp.Error.Code != "GATEWAY_NOT_CONFIGURED" {
		t.Fatalf("switch off: code=%d err=%+v", code, resp.Error)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("switch-off create moved balance: %d", bal)
	}
	// 打开开关 → 同一适配器可建单（证明开关是唯一阻挡）。
	code, resp = callAdminOp(t, "PUT", "/v1/admin/gateway-channels/"+vendor, "admin1", map[string]any{"enabled": true})
	if code != 200 || !resp.OK {
		t.Fatalf("enable channel: code=%d err=%+v", code, resp.Error)
	}
	code, resp = gwCreateWithVendor(t, testSrv, uid, vendor, 10000)
	if code != 200 || !resp.OK {
		t.Fatalf("switch on: code=%d err=%+v", code, resp.Error)
	}
	var o GatewayRechargeOrder
	_ = json.Unmarshal(resp.Data, &o)
	if o.CashierURL == nil || !strings.Contains(*o.CashierURL, "gateway.test") {
		t.Fatalf("unexpected cashier url: %+v", o.CashierURL)
	}
}

func Test155GatewayOnButNoAdapterStillClosed(t *testing.T) {
	vendor := uniq("gwch")
	createGatewayChannelOK(t, vendor, "开关开", true, nil)
	uid := "u155"
	mustWallet(t, uid)
	srv := noAdapterServer(t)
	code, resp := gwCreateWithVendor(t, srv, uid, vendor, 10000)
	if code != 400 || resp.Error.Code != "GATEWAY_NOT_CONFIGURED" {
		t.Fatalf("on-but-no-adapter: code=%d err=%+v", code, resp.Error)
	}
	bal, _ := balance(t, uid)
	if bal != 0 {
		t.Fatalf("no-adapter create moved balance: %d", bal)
	}
}

func Test156GatewayUnknownVendorClosed(t *testing.T) {
	uid := "u156"
	mustWallet(t, uid)
	// 行不存在 = 关（fail-closed），即使 testSrv 有 stub 适配器。
	code, resp := gwCreateWithVendor(t, testSrv, uid, "no-such-vendor-xyz", 10000)
	if code != 400 || resp.Error.Code != "GATEWAY_NOT_CONFIGURED" {
		t.Fatalf("unknown vendor: code=%d err=%+v", code, resp.Error)
	}
}

func Test157ManualDisabledFiltered(t *testing.T) {
	// 0012 轮显式覆盖：人工方式停用后，用户方法列表不再出现，管理列表仍可见。
	m := createPayMethodOK(t, "wechat", "测试微信", 7)
	code, resp := callAdminOp(t, "PUT", "/v1/admin/payment-methods/"+m.MethodID, "admin1",
		map[string]any{"enabled": false})
	if code != 200 || !resp.OK {
		t.Fatalf("disable method: code=%d err=%+v", code, resp.Error)
	}
	code, resp = call(t, "GET", "/v1/recharge/methods", "u157", nil, false)
	if code != 200 {
		t.Fatalf("user methods: code=%d", code)
	}
	var userList []PaymentMethod
	_ = json.Unmarshal(resp.Data, &userList)
	for _, x := range userList {
		if x.MethodID == m.MethodID {
			t.Fatalf("user list leaks disabled method")
		}
	}
	code, resp = callAdminOp(t, "GET", "/v1/admin/payment-methods", "admin1", nil)
	if code != 200 {
		t.Fatalf("admin methods: code=%d", code)
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
}
