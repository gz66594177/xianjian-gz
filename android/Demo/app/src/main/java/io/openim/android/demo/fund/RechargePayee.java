package io.openim.android.demo.fund;

import org.json.JSONObject;

/**
 * 后台收款方式（0010 手动充值）：GET /v1/recharge/methods 返回条目。
 * 方式列表与二维码全部取后台配置，App 内不写死任何收款方式。
 * qrUrl 为相对路径（/v1/uploads/...）或 null（未上传收款码时建单会被服务端拒绝）。
 */
public final class RechargePayee {

    public final String methodId;
    public final String type;
    public final String displayName;
    public final String qrUrl;
    public final boolean enabled;

    private RechargePayee(String methodId, String type, String displayName,
                          String qrUrl, boolean enabled) {
        this.methodId = methodId;
        this.type = type;
        this.displayName = displayName;
        this.qrUrl = qrUrl;
        this.enabled = enabled;
    }

    public static RechargePayee parse(JSONObject o) {
        if (o == null) {
            return null;
        }
        String id = o.optString("method_id", "");
        if (id.isEmpty()) {
            return null;
        }
        return new RechargePayee(
            id,
            o.optString("type", ""),
            o.optString("display_name", ""),
            o.isNull("qr_url") ? null : o.optString("qr_url", null),
            o.optBoolean("enabled", true));
    }

    /** wechat/alipay/bank -> 微信支付 / 支付宝 / 银行卡；未知类型回落展示名。 */
    public String typeName() {
        switch (type) {
            case "wechat":
                return "微信支付";
            case "alipay":
                return "支付宝";
            case "bank":
                return "银行卡";
            default:
                return type.isEmpty() ? displayName : type;
        }
    }

    public String label() {
        if (displayName == null || displayName.isEmpty()) {
            return typeName();
        }
        return displayName + " · " + typeName();
    }

    public boolean hasQr() {
        return qrUrl != null && !qrUrl.isEmpty();
    }
}
