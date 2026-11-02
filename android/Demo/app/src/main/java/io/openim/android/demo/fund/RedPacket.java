package io.openim.android.demo.fund;

import org.json.JSONObject;

/**
 * 红包类型与详情/领取分发。
 * kind 取值：friend（好友红包）/ group（群普通红包）/ lucky（群拼手气红包）。
 * 消息载体约定见 RedPacketBridge：IM 自定义消息（contentType=110）data 内
 * customType=901，内层 data 只放 packetId 与 kind，不放金额。
 */
public final class RedPacket {

    private RedPacket() {
    }

    public static final String KIND_FRIEND = "friend";
    public static final String KIND_GROUP = "group";
    public static final String KIND_LUCKY = "lucky";

    public static String kindName(String kind) {
        if (KIND_GROUP.equals(kind)) {
            return "群普通红包";
        }
        if (KIND_LUCKY.equals(kind)) {
            return "群拼手气红包";
        }
        return "好友红包";
    }

    /** 详情接口有的直接返回 packet，有的包在 {packet, claim} 里；统一解包。 */
    public static JSONObject extractPacket(Object data) {
        JSONObject o = FundModels.toObj(data);
        if (o == null) {
            return null;
        }
        JSONObject inner = o.optJSONObject("packet");
        return inner != null ? inner : o;
    }

    public static void getDetail(String kind, String packetId, FundApi.Callback cb) {
        if (KIND_GROUP.equals(kind)) {
            FundApi.getGroupRedpacket(packetId, cb);
        } else if (KIND_LUCKY.equals(kind)) {
            FundApi.getLuckyRedpacket(packetId, cb);
        } else {
            FundApi.getFriendRedpacket(packetId, cb);
        }
    }

    public static void getClaims(String kind, String packetId, FundApi.Callback cb) {
        if (KIND_GROUP.equals(kind)) {
            FundApi.getGroupRedpacketClaims(packetId, cb);
        } else if (KIND_LUCKY.equals(kind)) {
            FundApi.getLuckyRedpacketClaims(packetId, cb);
        } else {
            FundApi.getFriendRedpacketClaims(packetId, cb);
        }
    }

    public static void claim(String kind, String packetId, FundApi.Callback cb) {
        if (KIND_GROUP.equals(kind)) {
            FundApi.claimGroupRedpacket(packetId, cb);
        } else if (KIND_LUCKY.equals(kind)) {
            FundApi.claimLuckyRedpacket(packetId, cb);
        } else {
            FundApi.claimFriendRedpacket(packetId, cb);
        }
    }

    public static void list(String kind, String role, int limit, int offset, FundApi.Callback cb) {
        if (KIND_GROUP.equals(kind)) {
            FundApi.listGroupRedpackets(role, limit, offset, cb);
        } else if (KIND_LUCKY.equals(kind)) {
            FundApi.listLuckyRedpackets(role, limit, offset, cb);
        } else {
            FundApi.listFriendRedpackets(role, limit, offset, cb);
        }
    }
}
