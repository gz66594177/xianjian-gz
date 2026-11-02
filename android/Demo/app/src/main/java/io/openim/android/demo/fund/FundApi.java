package io.openim.android.demo.fund;

import android.os.Handler;
import android.os.Looper;
import android.text.TextUtils;

import org.json.JSONArray;
import org.json.JSONObject;

import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import java.io.BufferedReader;
import java.io.File;
import java.io.FileInputStream;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.util.UUID;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import io.openim.android.ouicore.base.BaseApp;

/**
 * 仙聊资金服务 HTTP 客户端（Phase 10，Demo/App 层新模块）。
 *
 * <p>鉴权：IM token 原样透传（Authorization: Bearer &lt;chatToken&gt;），绝不用共享密钥冒充用户。
 * 金额：全部 long 分。交易密码：只放进当次请求体，绝不落盘、绝不进日志——
 * 本类不打印任何请求体、响应体与请求头。</p>
 *
 * <p>服务端成功包 {"ok":true,"data":...}，失败包 {"ok":false,"error":{"code":...,"message":...}}。
 * 回调 data 可能是 JSONObject / JSONArray / null，调用方按接口约定转型。</p>
 */
public final class FundApi {

    private FundApi() {
    }

    public interface Callback {
        /** data 为 JSONObject / JSONArray / JSONObject.NULL 之一。 */
        void onOk(Object data);

        /** code 为服务端 error.code 或 HTTP_<status> / NETWORK。 */
        void onErr(String code, String message);
    }

    private static final ExecutorService EXECUTOR = Executors.newCachedThreadPool();
    private static final Handler MAIN = new Handler(Looper.getMainLooper());

    /**
     * 当前登录用户的 IM token（取自登录凭证，不另存）。
     * 注意：登录凭证含 chatToken（chat 业务）与 imToken（IM 服务）两个 token；
     * 资金服务调 OpenIM server parse_token 校验，必须用 imToken，绝不能用 chatToken。
     */
    public static String imToken() {
        try {
            if (BaseApp.inst() != null && BaseApp.inst().loginCertificate != null) {
                return BaseApp.inst().loginCertificate.imToken;
            }
        } catch (Exception ignored) {
        }
        return null;
    }

    /** 当前登录用户 ID（取自登录凭证）。 */
    public static String myUserId() {
        try {
            if (BaseApp.inst() != null && BaseApp.inst().loginCertificate != null) {
                return BaseApp.inst().loginCertificate.userID;
            }
        } catch (Exception ignored) {
        }
        return null;
    }

    /** 客户端幂等键：每次新建业务单据时生成，防重复提交。 */
    public static String newIdempotencyKey() {
        return UUID.randomUUID().toString();
    }

    // ------------------------------------------------------------------
    // 通用请求
    // ------------------------------------------------------------------

    public static void get(String path, Callback cb) {
        request("GET", path, null, cb);
    }

    public static void post(String path, JSONObject body, Callback cb) {
        request("POST", path, body, cb);
    }

    public static void put(String path, JSONObject body, Callback cb) {
        request("PUT", path, body, cb);
    }

    public static void delete(String path, JSONObject body, Callback cb) {
        request("DELETE", path, body, cb);
    }

    public static void request(final String method, final String path,
                               final JSONObject body, final Callback cb) {
        EXECUTOR.execute(() -> {
            HttpURLConnection conn = null;
            try {
                String token = imToken();
                if (TextUtils.isEmpty(token)) {
                    postErr(cb, "UNAUTHORIZED", "未登录或 IM token 缺失，请重新登录");
                    return;
                }
                URL url = new URL(FundConfig.getBaseUrl() + path);
                conn = (HttpURLConnection) url.openConnection();
                conn.setRequestMethod(method);
                conn.setConnectTimeout(15000);
                conn.setReadTimeout(30000);
                conn.setRequestProperty("Authorization", "Bearer " + token);
                conn.setRequestProperty("operationID",
                    String.valueOf(System.currentTimeMillis()));
                conn.setRequestProperty("Accept", "application/json");
                if (body != null) {
                    conn.setDoOutput(true);
                    conn.setRequestProperty("Content-Type", "application/json");
                    byte[] bytes = body.toString().getBytes(StandardCharsets.UTF_8);
                    conn.setFixedLengthStreamingMode(bytes.length);
                    try (OutputStream os = conn.getOutputStream()) {
                        os.write(bytes);
                    }
                }
                int status = conn.getResponseCode();
                InputStream in = status >= 400 ? conn.getErrorStream() : conn.getInputStream();
                String text = readAll(in);
                JSONObject resp = new JSONObject(text);
                if (resp.optBoolean("ok", false)) {
                    Object data = resp.isNull("data") ? JSONObject.NULL : resp.get("data");
                    postOk(cb, data);
                } else {
                    JSONObject err = resp.optJSONObject("error");
                    String code = err != null ? err.optString("code", "HTTP_" + status) : "HTTP_" + status;
                    String msg = err != null ? err.optString("message", "") : "";
                    if (TextUtils.isEmpty(msg)) {
                        msg = "资金服务返回异常（" + status + "）";
                    }
                    postErr(cb, code, msg);
                }
            } catch (Exception e) {
                postErr(cb, "NETWORK", "网络异常，请检查连接后重试");
            } finally {
                if (conn != null) {
                    conn.disconnect();
                }
            }
        });
    }

    private static String readAll(InputStream in) throws Exception {
        if (in == null) {
            return "{}";
        }
        BufferedReader reader = new BufferedReader(new InputStreamReader(in, StandardCharsets.UTF_8));
        StringBuilder sb = new StringBuilder();
        String line;
        while ((line = reader.readLine()) != null) {
            sb.append(line);
        }
        return sb.toString();
    }

    private static void postOk(final Callback cb, final Object data) {
        MAIN.post(() -> cb.onOk(data));
    }

    private static void postErr(final Callback cb, final String code, final String message) {
        MAIN.post(() -> cb.onErr(code, message));
    }

    static String enc(String s) {
        try {
            return URLEncoder.encode(s == null ? "" : s, "UTF-8");
        } catch (Exception e) {
            return "";
        }
    }

    // ------------------------------------------------------------------
    // 准入与钱包（API §5、§2）
    // ------------------------------------------------------------------

    public static void getFinanceStatus(Callback cb) {
        get("/v1/finance/status", cb);
    }

    public static void getWallet(Callback cb) {
        get("/v1/wallet", cb);
    }

    public static void createWallet(Callback cb) {
        post("/v1/wallet/create", new JSONObject(), cb);
    }

    public static void getLedger(int limit, int offset, Callback cb) {
        get("/v1/wallet/ledger?limit=" + limit + "&offset=" + offset, cb);
    }

    // ------------------------------------------------------------------
    // 交易密码（API §3）
    // ------------------------------------------------------------------

    public static void getTxPasswordStatus(Callback cb) {
        get("/v1/tx-password/status", cb);
    }

    public static void setTxPassword(String password, String confirm, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("password", password);
        body.put("confirm_password", confirm);
        post("/v1/tx-password/set", body, cb);
    }

    public static void changeTxPassword(String oldPw, String newPw, String confirm, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("old_password", oldPw);
        body.put("new_password", newPw);
        body.put("confirm_password", confirm);
        post("/v1/tx-password/change", body, cb);
    }

    // ------------------------------------------------------------------
    // 提现方式（API §4）
    // ------------------------------------------------------------------

    public static void getWithdrawMethods(Callback cb) {
        get("/v1/withdraw-methods", cb);
    }

    public static void addWithdrawMethod(int type, String realName, String accountNo,
                                         String bankName, String bankBranch,
                                         String qrFileId,
                                         boolean isDefault, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("type", type);
        body.put("real_name", realName);
        if (!TextUtils.isEmpty(accountNo)) {
            body.put("account_no", accountNo);
        }
        if (!TextUtils.isEmpty(qrFileId)) {
            body.put("qr_file_id", qrFileId);
        }
        if (!TextUtils.isEmpty(bankName)) {
            body.put("bank_name", bankName);
        }
        if (!TextUtils.isEmpty(bankBranch)) {
            body.put("bank_branch", bankBranch);
        }
        body.put("is_default", isDefault);
        post("/v1/withdraw-methods", body, cb);
    }

    public static void updateWithdrawMethod(String id, String realName, String accountNo,
                                            String bankName, String bankBranch,
                                            Boolean isDefault, String txPassword,
                                            Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        if (realName != null) {
            body.put("real_name", realName);
        }
        if (accountNo != null) {
            body.put("account_no", accountNo);
        }
        if (bankName != null) {
            body.put("bank_name", bankName);
        }
        if (bankBranch != null) {
            body.put("bank_branch", bankBranch);
        }
        if (isDefault != null) {
            body.put("is_default", isDefault);
        }
        body.put("tx_password", txPassword);
        put("/v1/withdraw-methods/" + enc(id), body, cb);
    }

    public static void deleteWithdrawMethod(String id, String txPassword, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("tx_password", txPassword);
        delete("/v1/withdraw-methods/" + enc(id), body, cb);
    }

    // ------------------------------------------------------------------
    // 手机号绑定（0013）：邮箱注册用户首次进页强制绑定；手机注册用户不强制。
    // 只返回 has_phone / 脱敏号码，完整号码绝不经本接口回显。
    // ------------------------------------------------------------------

    public static void getProfilePhone(Callback cb) {
        get("/v1/profile/phone", cb);
    }

    public static void bindProfilePhone(String areaCode, String phone, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("area_code", areaCode);
        body.put("phone", phone);
        post("/v1/profile/phone/bind", body, cb);
    }

    public static void setDefaultWithdrawMethod(String id, String txPassword, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("tx_password", txPassword);
        post("/v1/withdraw-methods/" + enc(id) + "/set-default", body, cb);
    }

    // ------------------------------------------------------------------
    // 充值（API §13）
    // ------------------------------------------------------------------

    public static void createRecharge(long amountCents, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("amount_cents", amountCents);
        body.put("idempotency_key", newIdempotencyKey());
        post("/v1/recharges", body, cb);
    }

    public static void getRecharge(String orderId, Callback cb) {
        get("/v1/recharges/" + enc(orderId), cb);
    }

    public static void listRecharges(String status, int limit, int offset, Callback cb) {
        String path = "/v1/recharges?limit=" + limit + "&offset=" + offset;
        if (!TextUtils.isEmpty(status)) {
            path += "&status=" + enc(status);
        }
        get(path, cb);
    }

    // ------------------------------------------------------------------
    // 充值 A/B（0010 手动 + 网关）：方式列表与二维码全部取后台配置。
    //   手动：GET /v1/recharge/methods -> POST /v1/recharge/manual
    //        -> POST /v1/recharge/<id>/proof（multipart，字段名 proof）
    //        -> GET /v1/recharge/orders/<id>（只读查询状态）
    //   网关：POST /v1/recharge/gateway -> cashier_url 外跳支付页；
    //        生产未注册厂商时服务端回 400 GATEWAY_NOT_CONFIGURED。
    //   统一订单状态：手动 10 待上传凭证 / 11 待审核 / 12 已拒绝 / 1 已入账；
    //        网关 0 待支付 / 1 已支付（回调入账以服务端为准）。
    // ------------------------------------------------------------------

    public static void listRechargeMethods(Callback cb) {
        get("/v1/recharge/methods", cb);
    }

    /**
     * 充值通道总览（GET /v1/recharge/channels，用户侧只读）：
     * 返回人工启用方式列表 + manual_available + 仅启用厂商的网关列表 +
     * gateway_available（启用行 + 已注册适配器才为真）。响应绝不含网关 params
     * 与密钥。老生产服务端无此路由时走 onErr，调用方回落旧列表且网关关闭。
     */
    public static void getRechargeChannels(Callback cb) {
        get("/v1/recharge/channels", cb);
    }

    public static void createManualRecharge(String methodId, long amountCents,
                                            Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("method_id", methodId);
        body.put("amount_cents", amountCents);
        body.put("idempotency_key", newIdempotencyKey());
        post("/v1/recharge/manual", body, cb);
    }

    public static void createGatewayRecharge(long amountCents, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("amount_cents", amountCents);
        body.put("idempotency_key", newIdempotencyKey());
        post("/v1/recharge/gateway", body, cb);
    }

    public static void getRechargeOrder(String orderId, Callback cb) {
        get("/v1/recharge/orders/" + enc(orderId), cb);
    }

    /** 带 IM 鉴权的图片加载（收款二维码经 /v1/uploads 鉴权取用）。 */
    public interface ImageCallback {
        void onBitmap(Bitmap bitmap);

        void onErr();
    }

    public static void loadAuthedImage(final String urlOrPath, final ImageCallback cb) {
        EXECUTOR.execute(() -> {
            HttpURLConnection conn = null;
            try {
                String token = imToken();
                if (TextUtils.isEmpty(token)) {
                    MAIN.post(cb::onErr);
                    return;
                }
                String full = urlOrPath.startsWith("http")
                    ? urlOrPath : FundConfig.getBaseUrl() + urlOrPath;
                conn = (HttpURLConnection) new URL(full).openConnection();
                conn.setRequestMethod("GET");
                conn.setConnectTimeout(15000);
                conn.setReadTimeout(30000);
                conn.setRequestProperty("Authorization", "Bearer " + token);
                int status = conn.getResponseCode();
                if (status >= 400) {
                    MAIN.post(cb::onErr);
                    return;
                }
                final Bitmap bitmap = BitmapFactory.decodeStream(conn.getInputStream());
                if (bitmap == null) {
                    MAIN.post(cb::onErr);
                } else {
                    MAIN.post(() -> cb.onBitmap(bitmap));
                }
            } catch (Exception e) {
                MAIN.post(cb::onErr);
            } finally {
                if (conn != null) {
                    conn.disconnect();
                }
            }
        });
    }

    /** 手动充值凭证上传：multipart/form-data，字段名 proof；绝不打印文件内容。 */
    public static void uploadRechargeProof(final String orderId, final File file,
                                           final Callback cb) {
        EXECUTOR.execute(() -> {
            HttpURLConnection conn = null;
            try {
                String token = imToken();
                if (TextUtils.isEmpty(token)) {
                    postErr(cb, "UNAUTHORIZED", "未登录或 IM token 缺失，请重新登录");
                    return;
                }
                String boundary = "----XianliaoProof" + System.currentTimeMillis();
                URL url = new URL(FundConfig.getBaseUrl()
                    + "/v1/recharge/" + enc(orderId) + "/proof");
                conn = (HttpURLConnection) url.openConnection();
                conn.setRequestMethod("POST");
                conn.setConnectTimeout(15000);
                conn.setReadTimeout(30000);
                conn.setDoOutput(true);
                conn.setRequestProperty("Authorization", "Bearer " + token);
                conn.setRequestProperty("operationID",
                    String.valueOf(System.currentTimeMillis()));
                conn.setRequestProperty("Accept", "application/json");
                conn.setRequestProperty("Content-Type",
                    "multipart/form-data; boundary=" + boundary);
                try (java.io.OutputStream os = conn.getOutputStream()) {
                    String head = "--" + boundary + "\r\n"
                        + "Content-Disposition: form-data; name=\"proof\"; filename=\""
                        + file.getName() + "\"\r\n"
                        + "Content-Type: image/jpeg\r\n\r\n";
                    os.write(head.getBytes(StandardCharsets.UTF_8));
                    try (FileInputStream fis = new FileInputStream(file)) {
                        byte[] buf = new byte[8192];
                        int n;
                        while ((n = fis.read(buf)) != -1) {
                            os.write(buf, 0, n);
                        }
                    }
                    String tail = "\r\n--" + boundary + "--\r\n";
                    os.write(tail.getBytes(StandardCharsets.UTF_8));
                    os.flush();
                }
                int status = conn.getResponseCode();
                InputStream in = status >= 400 ? conn.getErrorStream() : conn.getInputStream();
                String text = readAll(in);
                JSONObject resp = new JSONObject(text);
                if (resp.optBoolean("ok", false)) {
                    Object data = resp.isNull("data") ? JSONObject.NULL : resp.get("data");
                    postOk(cb, data);
                } else {
                    JSONObject err = resp.optJSONObject("error");
                    String code = err != null ? err.optString("code", "HTTP_" + status) : "HTTP_" + status;
                    String msg = err != null ? err.optString("message", "") : "";
                    if (TextUtils.isEmpty(msg)) {
                        msg = "资金服务返回异常（" + status + "）";
                    }
                    postErr(cb, code, msg);
                }
            } catch (Exception e) {
                postErr(cb, "NETWORK", "网络异常，请检查连接后重试");
            } finally {
                if (conn != null) {
                    conn.disconnect();
                }
            }
        });
    }

    /** 提现方式收款二维码上传：multipart/form-data，字段名 qr；返回 data.qr_file_id / qr_url。 */
    public static void uploadWithdrawMethodQR(final File file,
                                              final Callback cb) {
        EXECUTOR.execute(() -> {
            HttpURLConnection conn = null;
            try {
                String token = imToken();
                if (TextUtils.isEmpty(token)) {
                    postErr(cb, "UNAUTHORIZED", "未登录或 IM token 缺失，请重新登录");
                    return;
                }
                String boundary = "----XianliaoMethodQr" + System.currentTimeMillis();
                URL url = new URL(FundConfig.getBaseUrl()
                    + "/v1/withdraw-methods/upload-qr");
                conn = (HttpURLConnection) url.openConnection();
                conn.setRequestMethod("POST");
                conn.setConnectTimeout(15000);
                conn.setReadTimeout(30000);
                conn.setDoOutput(true);
                conn.setRequestProperty("Authorization", "Bearer " + token);
                conn.setRequestProperty("operationID",
                    String.valueOf(System.currentTimeMillis()));
                conn.setRequestProperty("Accept", "application/json");
                conn.setRequestProperty("Content-Type",
                    "multipart/form-data; boundary=" + boundary);
                try (java.io.OutputStream os = conn.getOutputStream()) {
                    String head = "--" + boundary + "\r\n"
                        + "Content-Disposition: form-data; name=\"qr\"; filename=\""
                        + file.getName() + "\"\r\n"
                        + "Content-Type: image/jpeg\r\n\r\n";
                    os.write(head.getBytes(StandardCharsets.UTF_8));
                    try (FileInputStream fis = new FileInputStream(file)) {
                        byte[] buf = new byte[8192];
                        int n;
                        while ((n = fis.read(buf)) != -1) {
                            os.write(buf, 0, n);
                        }
                    }
                    String tail = "\r\n--" + boundary + "--\r\n";
                    os.write(tail.getBytes(StandardCharsets.UTF_8));
                    os.flush();
                }
                int status = conn.getResponseCode();
                InputStream in = status >= 400 ? conn.getErrorStream() : conn.getInputStream();
                String text = readAll(in);
                JSONObject resp = new JSONObject(text);
                if (resp.optBoolean("ok", false)) {
                    Object data = resp.isNull("data") ? JSONObject.NULL : resp.get("data");
                    postOk(cb, data);
                } else {
                    JSONObject err = resp.optJSONObject("error");
                    String code = err != null ? err.optString("code", "HTTP_" + status) : "HTTP_" + status;
                    String msg = err != null ? err.optString("message", "") : "";
                    if (TextUtils.isEmpty(msg)) {
                        msg = "资金服务返回异常（" + status + "）";
                    }
                    postErr(cb, code, msg);
                }
            } catch (Exception e) {
                postErr(cb, "NETWORK", "网络异常，请检查连接后重试");
            } finally {
                if (conn != null) {
                    conn.disconnect();
                }
            }
        });
    }

    // ------------------------------------------------------------------
    // 提现（API §14）
    // ------------------------------------------------------------------

    public static void getWithdrawQuote(long amountCents, Callback cb) {
        get("/v1/withdrawals/quote?amount_cents=" + amountCents, cb);
    }

    public static void createWithdrawal(String methodId, long amountCents, String txPassword,
                                        Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("withdraw_method_id", methodId);
        body.put("amount_cents", amountCents);
        body.put("tx_password", txPassword);
        body.put("idempotency_key", newIdempotencyKey());
        post("/v1/withdrawals", body, cb);
    }

    public static void getWithdrawal(String orderId, Callback cb) {
        get("/v1/withdrawals/" + enc(orderId), cb);
    }

    public static void listWithdrawals(String status, int limit, int offset, Callback cb) {
        String path = "/v1/withdrawals?limit=" + limit + "&offset=" + offset;
        if (!TextUtils.isEmpty(status)) {
            path += "&status=" + enc(status);
        }
        get(path, cb);
    }

    // ------------------------------------------------------------------
    // 好友红包（API §6）
    // ------------------------------------------------------------------

    public static void createFriendRedpacket(String receiverId, long amountCents, String greeting,
                                             String txPassword, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("receiver_id", receiverId);
        body.put("amount_cents", amountCents);
        if (!TextUtils.isEmpty(greeting)) {
            body.put("greeting", greeting);
        }
        body.put("tx_password", txPassword);
        body.put("idempotency_key", newIdempotencyKey());
        post("/v1/redpackets", body, cb);
    }

    public static void claimFriendRedpacket(String packetId, Callback cb) {
        post("/v1/redpackets/" + enc(packetId) + "/claim", new JSONObject(), cb);
    }

    public static void getFriendRedpacket(String packetId, Callback cb) {
        get("/v1/redpackets/" + enc(packetId), cb);
    }

    public static void getFriendRedpacketClaims(String packetId, Callback cb) {
        get("/v1/redpackets/" + enc(packetId) + "/claims", cb);
    }

    public static void listFriendRedpackets(String role, int limit, int offset, Callback cb) {
        get("/v1/redpackets?role=" + enc(role) + "&limit=" + limit + "&offset=" + offset, cb);
    }

    // ------------------------------------------------------------------
    // 群普通红包（API §7）
    // ------------------------------------------------------------------

    public static void createGroupRedpacket(String groupId, long totalCents, int totalCount,
                                            String greeting, JSONArray memberIds,
                                            String txPassword, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("group_id", groupId);
        body.put("total_amount_cents", totalCents);
        body.put("total_count", totalCount);
        if (!TextUtils.isEmpty(greeting)) {
            body.put("greeting", greeting);
        }
        if (memberIds != null) {
            body.put("member_ids", memberIds);
        }
        body.put("tx_password", txPassword);
        body.put("idempotency_key", newIdempotencyKey());
        post("/v1/group-redpackets", body, cb);
    }

    public static void claimGroupRedpacket(String packetId, Callback cb) {
        post("/v1/group-redpackets/" + enc(packetId) + "/claim", new JSONObject(), cb);
    }

    public static void getGroupRedpacket(String packetId, Callback cb) {
        get("/v1/group-redpackets/" + enc(packetId), cb);
    }

    public static void getGroupRedpacketClaims(String packetId, Callback cb) {
        get("/v1/group-redpackets/" + enc(packetId) + "/claims", cb);
    }

    public static void listGroupRedpackets(String role, int limit, int offset, Callback cb) {
        get("/v1/group-redpackets?role=" + enc(role) + "&limit=" + limit + "&offset=" + offset, cb);
    }

    // ------------------------------------------------------------------
    // 群拼手气红包（API §8）
    // ------------------------------------------------------------------

    public static void createLuckyRedpacket(String groupId, long totalCents, int totalCount,
                                            String greeting, JSONArray memberIds,
                                            String txPassword, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("group_id", groupId);
        body.put("total_amount_cents", totalCents);
        body.put("total_count", totalCount);
        if (!TextUtils.isEmpty(greeting)) {
            body.put("greeting", greeting);
        }
        if (memberIds != null) {
            body.put("member_ids", memberIds);
        }
        body.put("tx_password", txPassword);
        body.put("idempotency_key", newIdempotencyKey());
        post("/v1/group-lucky-redpackets", body, cb);
    }

    public static void claimLuckyRedpacket(String packetId, Callback cb) {
        post("/v1/group-lucky-redpackets/" + enc(packetId) + "/claim", new JSONObject(), cb);
    }

    public static void getLuckyRedpacket(String packetId, Callback cb) {
        get("/v1/group-lucky-redpackets/" + enc(packetId), cb);
    }

    public static void getLuckyRedpacketClaims(String packetId, Callback cb) {
        get("/v1/group-lucky-redpackets/" + enc(packetId) + "/claims", cb);
    }

    public static void getLuckyRanking(String packetId, Callback cb) {
        get("/v1/group-lucky-redpackets/" + enc(packetId) + "/ranking", cb);
    }

    public static void listLuckyRedpackets(String role, int limit, int offset, Callback cb) {
        get("/v1/group-lucky-redpackets?role=" + enc(role) + "&limit=" + limit + "&offset=" + offset, cb);
    }

    // ------------------------------------------------------------------
    // 转账（API §9，即时到账）
    // ------------------------------------------------------------------

    public static void createTransfer(String receiverId, long amountCents, String remark,
                                      String txPassword, Callback cb) throws Exception {
        JSONObject body = new JSONObject();
        body.put("receiver_id", receiverId);
        body.put("amount_cents", amountCents);
        if (!TextUtils.isEmpty(remark)) {
            body.put("remark", remark);
        }
        body.put("tx_password", txPassword);
        body.put("idempotency_key", newIdempotencyKey());
        post("/v1/transfers", body, cb);
    }

    public static void getTransfer(String orderId, Callback cb) {
        get("/v1/transfers/" + enc(orderId), cb);
    }

    public static void listTransfers(String role, int limit, int offset, Callback cb) {
        get("/v1/transfers?role=" + enc(role) + "&limit=" + limit + "&offset=" + offset, cb);
    }
}
