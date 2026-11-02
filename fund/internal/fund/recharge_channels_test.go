package fund

// Test158：充值通道总览 GET /v1/recharge/channels。
//   - 启用中的网关通道 + 有适配器（testSrv 带 stub 适配器）→ gateway_available=true，
//     且列表里只有 vendor/display_name，绝不含 params/商户号等非密钥参数；
//   - 停用的网关通道 → 不出现在 gateway_channels 里；
//   - 无适配器服务器 → 即使库里有启用行，gateway_available 也为 false、列表为空；
//   - 人工方式停用后 → manual_methods 里不再出现（服务端过滤 + 本接口复用该列表）。

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func Test158RechargeChannels(t *testing.T) {
	// 启用行：带一个非密钥参数，响应里必须看不到它。
	enabledVendor := uniq("gwch158")
	createGatewayChannelOK(t, enabledVendor, "测试网关158", true,
		map[string]any{"merchant_id": "m158-secret-must-not-leak"})
	// 停用行：同页返回里必须看不到它。
	disabledVendor := uniq("gwch158off")
	createGatewayChannelOK(t, disabledVendor, "停用网关158", false, nil)
	// 停用的人工方式：manual_methods 里必须看不到它。
	m := createPayMethodOK(t, "wechat", "测试微信158", 7)
	code, resp := callAdminOp(t, "PUT", "/v1/admin/payment-methods/"+m.MethodID, "admin1",
		map[string]any{"enabled": false})
	if code != 200 || !resp.OK {
		t.Fatalf("disable method: code=%d err=%+v", code, resp.Error)
	}

	code, resp = call(t, "GET", "/v1/recharge/channels", "u158", nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("channels: code=%d err=%+v", code, resp.Error)
	}
	raw := string(resp.Data)
	if strings.Contains(raw, "m158-secret-must-not-leak") || strings.Contains(raw, "merchant_id") || strings.Contains(raw, "params") {
		t.Fatalf("channels leaks gateway params: %s", raw)
	}
	var ch RechargeChannels
	if err := json.Unmarshal(resp.Data, &ch); err != nil {
		t.Fatal(err)
	}
	if !ch.GatewayAvailable {
		t.Fatalf("gateway should be available with an enabled channel + stub adapter: %+v", ch)
	}
	foundEnabled, foundDisabled := false, false
	for _, c := range ch.GatewayChannels {
		if c.Vendor == enabledVendor {
			foundEnabled = true
			if c.DisplayName != "测试网关158" {
				t.Fatalf("gateway display name wrong: %+v", c)
			}
		}
		if c.Vendor == disabledVendor {
			foundDisabled = true
		}
	}
	if !foundEnabled {
		t.Fatalf("enabled gateway channel missing: %+v", ch.GatewayChannels)
	}
	if foundDisabled {
		t.Fatalf("disabled gateway channel leaked: %+v", ch.GatewayChannels)
	}
	for _, mm := range ch.ManualMethods {
		if mm.MethodID == m.MethodID {
			t.Fatalf("disabled pay method leaked in channels")
		}
	}

	// 无适配器：即使库里有启用行，网关也必须关。
	srv := noAdapterServer(t)
	req := httptest.NewRequest("GET", "/v1/recharge/channels", nil)
	req.Header.Set("Authorization", "Bearer imtok-u158")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("no-adapter channels: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp2 apiResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if !resp2.OK {
		t.Fatalf("no-adapter channels not ok: %+v", resp2.Error)
	}
	var ch2 RechargeChannels
	if err := json.Unmarshal(resp2.Data, &ch2); err != nil {
		t.Fatal(err)
	}
	if ch2.GatewayAvailable || len(ch2.GatewayChannels) != 0 {
		t.Fatalf("no-adapter gateway must be closed: %+v", ch2)
	}
}
