package io.openim.android.demo.fund;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

/**
 * 资金服务返回体解析（防御式 opt 取值；金额一律 long 分）。
 * 服务端返回为准：客户端不做余额加减、不缓存金额冒充服务端状态。
 */
public final class FundModels {

    private FundModels() {
    }

    public static List<JSONObject> toList(Object data) {
        List<JSONObject> out = new ArrayList<>();
        if (data instanceof JSONArray) {
            JSONArray arr = (JSONArray) data;
            for (int i = 0; i < arr.length(); i++) {
                JSONObject o = arr.optJSONObject(i);
                if (o != null) {
                    out.add(o);
                }
            }
        } else if (data instanceof JSONObject) {
            out.add((JSONObject) data);
        }
        return out;
    }

    public static JSONObject toObj(Object data) {
        if (data instanceof JSONObject) {
            return (JSONObject) data;
        }
        return null;
    }

    // ---------------- 准入 ----------------

    public static class FinanceStatus {
        public boolean hasMethod;
        public boolean hasPassword;
        public String accountStatus = "";
        public int methodCount;
        public boolean hasDefaultMethod;
        public String nextStep = "";
        public String guidance = "";

        public static FinanceStatus parse(JSONObject o) {
            FinanceStatus s = new FinanceStatus();
            if (o == null) {
                return s;
            }
            s.hasMethod = o.optBoolean("has_withdrawal_method", false);
            s.hasPassword = o.optBoolean("has_transaction_password", false);
            s.accountStatus = o.optString("finance_account_status", "");
            s.methodCount = o.optInt("method_count", 0);
            s.hasDefaultMethod = o.optBoolean("has_default_method", false);
            s.nextStep = o.optString("next_step", "");
            s.guidance = o.optString("guidance", "");
            return s;
        }

        public boolean isOk() {
            return "ok".equals(accountStatus);
        }
    }

    // ---------------- 提现方式 ----------------

    /** type: 1 支付宝 2 微信 3 银行卡。 */
    public static String methodTypeName(int type) {
        switch (type) {
            case 1:
                return "支付宝";
            case 2:
                return "微信支付";
            case 3:
                return "银行卡";
            default:
                return "未知方式";
        }
    }

    public static String methodLine(JSONObject m) {
        StringBuilder sb = new StringBuilder();
        sb.append(methodTypeName(m.optInt("type", 0)));
        sb.append(" · ").append(m.optString("real_name", ""));
        String acct = m.optString("account_no", "");
        if (acct.isEmpty() && !m.optString("qr_file_id", "").isEmpty()) {
            sb.append(" · 收款二维码");
        } else {
            sb.append(" · ").append(acct);
            if (!m.optString("qr_file_id", "").isEmpty()) {
                sb.append(" · 收款二维码");
            }
        }
        if (m.optBoolean("is_default", false)) {
            sb.append("（默认）");
        }
        return sb.toString();
    }

    // ---------------- 提现 7 态 ----------------

    /** 0 待审核 1 已通过 2 已拒绝 3 处理中 4 成功 5 失败 6 已退款。 */
    public static String withdrawStatusName(int status) {
        switch (status) {
            case 0:
                return "待审核";
            case 1:
                return "已通过（未打款）";
            case 2:
                return "已拒绝";
            case 3:
                return "处理中（未打款）";
            case 4:
                return "成功";
            case 5:
                return "失败";
            case 6:
                return "已退款";
            default:
                return "未知状态";
        }
    }

    // ---------------- 充值 4 态 ----------------

    /** 0 待支付 1 已支付 2 已失败 3 已退款（order_type=1 命名空间）。 */
    public static String rechargeStatusName(int status) {
        switch (status) {
            case 0:
                return "待支付";
            case 1:
                return "已支付";
            case 2:
                return "已失败";
            case 3:
                return "已退款";
            default:
                return "未知状态";
        }
    }

    // ---------------- 红包 ----------------

    /** 好友/群包 status: 0 待领取 1 已领完（好友包为已领取） 2 已过期退款。 */
    public static String packetStatusName(int status, boolean isFriend) {
        switch (status) {
            case 0:
                return "待领取";
            case 1:
                return isFriend ? "已领取" : "已领完";
            case 2:
                return "已过期退款";
            default:
                return "未知状态";
        }
    }

    /** 红包气泡文案（按服务端状态渲染，不用本地缓存金额）。 */
    public static String bubbleText(String kind, JSONObject packet) {
        if (packet == null) {
            return "🧧 仙聊红包，点击查看";
        }
        int status = packet.optInt("status", 0);
        boolean expired = packet.optBoolean("expired", false);
        if ("friend".equals(kind)) {
            if (status == 1) {
                return "🧧 仙聊红包 · 已领取";
            }
            if (status == 2 || expired) {
                return "🧧 仙聊红包 · 已过期退款";
            }
            return "🧧 仙聊红包 · 待领取，点击领取";
        }
        int claimed = packet.optInt("claimed_count", 0);
        int total = packet.optInt("total_count", 0);
        if (status == 1) {
            return "🧧 群红包 · 已领完（" + claimed + "/" + total + "）";
        }
        if (status == 2 || expired) {
            return "🧧 群红包 · 已过期（" + claimed + "/" + total + "）";
        }
        return "🧧 群红包 · " + claimed + "/" + total + " 已领，点击领取";
    }

    // ---------------- 流水 ----------------

    /** 流水行文案：方向与账户性质只做展示，不推导余额。 */
    public static String ledgerLine(JSONObject e) {
        StringBuilder sb = new StringBuilder();
        int accountType = e.optInt("account_type", 1);
        int direction = e.optInt("direction", 0);
        sb.append(accountType == 2 ? "冻结" : "可用");
        sb.append(direction == 2 ? " ＋" : " －");
        sb.append(Money.formatCents(e.optLong("amount_cents", 0)));
        String opp = e.optString("opposite_user_id", "");
        if (opp != null && !opp.isEmpty() && !"null".equals(opp)) {
            sb.append(" · 对方 ").append(opp);
        }
        sb.append(" · 余额 ").append(Money.formatCents(e.optLong("balance_after_cents", 0)));
        return sb.toString();
    }
}
