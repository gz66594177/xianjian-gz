package fund

// 0013：手机号绑定（格式 + 跨账号唯一 + 一人一号幂等）与提现实名一致性
// （首绑姓名为基准，后续不同姓名拒绝；已绑姓名不得修改）。
import (
	"encoding/json"
	"testing"
)

func Test159ProfilePhoneAndRealName(t *testing.T) {
	uid := uniq("u159")
	mustWallet(t, uid)

	// 未绑时 has_phone=false。
	code, resp := call(t, "GET", "/v1/profile/phone", uid, nil, false)
	if code != 200 || !resp.OK {
		t.Fatalf("get phone code=%d resp=%+v", code, resp)
	}
	var pb PhoneBinding
	if err := json.Unmarshal(resp.Data, &pb); err != nil || pb.HasPhone {
		t.Fatalf("unbound phone unexpected: %+v err=%v", pb, err)
	}
	// 格式错误拒绝。
	code, _ = call(t, "POST", "/v1/profile/phone/bind", uid, map[string]any{"area_code": "+86", "phone": "abc"}, false)
	if code != 400 {
		t.Fatalf("bad phone code=%d, want 400", code)
	}
	// 正常绑定。
	code, resp = call(t, "POST", "/v1/profile/phone/bind", uid, map[string]any{"area_code": "+86", "phone": "13800138000"}, false)
	if code != 200 || !resp.OK {
		t.Fatalf("bind phone code=%d resp=%+v", code, resp)
	}
	if err := json.Unmarshal(resp.Data, &pb); err != nil || !pb.HasPhone || pb.Masked != "138****8000" {
		t.Fatalf("bound phone unexpected: %+v err=%v", pb, err)
	}
	// 同一用户绑同一号码幂等成功；绑不同号码拒绝。
	code, _ = call(t, "POST", "/v1/profile/phone/bind", uid, map[string]any{"area_code": "+86", "phone": "13800138000"}, false)
	if code != 200 {
		t.Fatalf("idempotent bind code=%d, want 200", code)
	}
	code, _ = call(t, "POST", "/v1/profile/phone/bind", uid, map[string]any{"area_code": "+86", "phone": "13900139000"}, false)
	if code != 400 {
		t.Fatalf("change phone code=%d, want 400", code)
	}
	// 同一号码绑到另一账号拒绝（单号码多账号）。
	uid2 := uniq("u159b")
	mustWallet(t, uid2)
	code, _ = call(t, "POST", "/v1/profile/phone/bind", uid2, map[string]any{"area_code": "+86", "phone": "13800138000"}, false)
	if code != 400 {
		t.Fatalf("phone reuse code=%d, want 400", code)
	}

	// 提现实名：首绑张三成功；第二种类型用李四拒绝；用张三成功。
	addMethodOK(t, uid, 2, "张三", "wx-159", nil)
	code, _ = call(t, "POST", "/v1/withdraw-methods", uid, map[string]any{"type": 1, "real_name": "李四", "account_no": "ali-159"}, false)
	if code != 400 {
		t.Fatalf("different name second method code=%d, want 400", code)
	}
	m2 := addMethodOK(t, uid, 1, "张三", "ali-159", nil)
	// 已绑姓名不得修改：改名拒绝；改同名/只设默认放行路径由 updateMethod 守卫。
	// 注：App 已撤修改入口，服务端仍须拦；此处用 PUT + 交易密码验证。
	setTxPwd(t, uid, "123456")
	code, _ = call(t, "PUT", "/v1/withdraw-methods/"+m2.MethodID, uid, map[string]any{"real_name": "王五", "tx_password": "123456"}, false)
	if code != 400 {
		t.Fatalf("rename method code=%d, want 400", code)
	}
	code, _ = call(t, "PUT", "/v1/withdraw-methods/"+m2.MethodID, uid, map[string]any{"real_name": "张三", "tx_password": "123456"}, false)
	if code != 200 {
		t.Fatalf("same name update code=%d, want 200", code)
	}
}
