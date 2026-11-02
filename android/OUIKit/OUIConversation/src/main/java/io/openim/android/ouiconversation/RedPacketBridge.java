package io.openim.android.ouiconversation;

import android.content.Context;
import android.text.TextUtils;

import org.json.JSONObject;

import io.openim.android.sdk.enums.MessageType;
import io.openim.android.sdk.models.Message;

/**
 * 仙聊红包 OUI 桥（Phase 10 触点支撑文件，不改动其他 OUI 代码）。
 *
 * <p>载体约定（与资金服务 API 文档 §6–§8 一致，Phase 10 定稿）：
 * IM 自定义消息的 contentType 由 SDK 定为 CUSTOM(110)；data 内 customType=901 为仙聊红包
 * （与通话信令 200–204 不冲突；901 不作为 contentType 使用，SDK 无此 contentType）。
 * 内层 data 只放 packetId 与 kind（friend/group/lucky），绝不放金额；
 * 金额、状态、领取记录全部以资金服务详情接口为准。</p>
 *
 * <p>OUI 层不直接依赖 Demo 层：DemoApplication 在启动时注册 Handler，
 * 负责打开 Demo 资金页与调资金服务查包状态；未注册时气泡只显示占位文案、不崩溃。</p>
 */
public final class RedPacketBridge {

    private RedPacketBridge() {
    }

    /** data 内 customType：仙聊红包。 */
    public static final int CUSTOM_TYPE_RED_PACKET = 901;

    public static class PacketRef {
        public String packetId;
        public String kind;
    }

    public interface StatusCallback {
        void onResult(JSONObject packet);

        void onError();
    }

    public interface Handler {
        void openDetail(Context context, String kind, String packetId);

        void openSend(Context context, String userId, String groupId);

        void fetchStatus(String kind, String packetId, StatusCallback cb);
    }

    public static Handler handler;

    /** 解析红包消息；非红包消息返回 null。 */
    public static PacketRef parse(Message msg) {
        if (msg == null || msg.getContentType() != MessageType.CUSTOM) {
            return null;
        }
        try {
            if (msg.getCustomElem() == null || TextUtils.isEmpty(msg.getCustomElem().getData())) {
                return null;
            }
            JSONObject outer = new JSONObject(msg.getCustomElem().getData());
            if (outer.optInt("customType", -1) != CUSTOM_TYPE_RED_PACKET) {
                return null;
            }
            String innerStr = outer.optString("data", "");
            if (TextUtils.isEmpty(innerStr)) {
                return null;
            }
            JSONObject inner = new JSONObject(innerStr);
            String packetId = inner.optString("packetId", "");
            if (TextUtils.isEmpty(packetId)) {
                return null;
            }
            PacketRef ref = new PacketRef();
            ref.packetId = packetId;
            String kind = inner.optString("kind", "friend");
            if (!"group".equals(kind) && !"lucky".equals(kind)) {
                kind = "friend";
            }
            ref.kind = kind;
            return ref;
        } catch (Exception e) {
            return null;
        }
    }
}
