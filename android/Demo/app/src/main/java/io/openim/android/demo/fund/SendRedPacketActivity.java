package io.openim.android.demo.fund;

import android.os.Bundle;
import android.text.Editable;
import android.text.TextWatcher;
import android.view.View;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.List;

import io.openim.android.demo.databinding.ActivityFundSendRedpacketBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;
import io.openim.android.sdk.OpenIMClient;
import io.openim.android.sdk.enums.GroupMemberFilter;
import io.openim.android.sdk.listener.OnBase;
import io.openim.android.sdk.listener.OnMsgSendCallback;
import io.openim.android.sdk.models.GroupMembersInfo;
import io.openim.android.sdk.models.Message;
import io.openim.android.sdk.models.OfflinePushInfo;

/**
 * 红包发送页：好友 / 群普通 / 群拼手气。
 * 发包成功后以 IM 自定义消息发送 packetId（消息体只放 packetId 与 kind，不放金额）；
 * IM 消息发送失败时红包本身已建包，不退款、不重建，只提供重发消息入口。
 */
public class SendRedPacketActivity extends BaseActivity<BaseViewModel, ActivityFundSendRedpacketBinding> {

    public static final String EXTRA_TARGET_USER_ID = "target_user_id";
    public static final String EXTRA_TARGET_GROUP_ID = "target_group_id";
    public static final String EXTRA_KIND = "kind";

    private String kind = RedPacket.KIND_FRIEND;
    private String targetUserId = "";
    private String targetGroupId = "";
    private String lastPacketId = "";

    // 可用余额：GET /v1/wallet 的 balance_cents（不含 frozen_cents），与提现页同款。
    private long availableCents = 0;
    private boolean balanceLoaded = false;
    private boolean balanceFailed = false;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundSendRedpacketBinding.inflate(getLayoutInflater()));
        sink();
        kind = getIntent().getStringExtra(EXTRA_KIND);
        if (kind == null) {
            kind = RedPacket.KIND_FRIEND;
        }
        targetUserId = getIntent().getStringExtra(EXTRA_TARGET_USER_ID);
        targetGroupId = getIntent().getStringExtra(EXTRA_TARGET_GROUP_ID);
        if (targetUserId == null) {
            targetUserId = "";
        }
        if (targetGroupId == null) {
            targetGroupId = "";
        }
        boolean isFriend = RedPacket.KIND_FRIEND.equals(kind);
        view.tvTarget.setText(RedPacket.kindName(kind) + " · "
            + (isFriend ? "发送给 " + targetUserId : "群 " + targetGroupId));
        view.tvCountLabel.setVisibility(isFriend ? View.GONE : View.VISIBLE);
        view.etCount.setVisibility(isFriend ? View.GONE : View.VISIBLE);
        view.btnResendMsg.setVisibility(View.GONE);
        view.btnSend.setOnClickListener(v -> send());
        FundUi.pressScale(view.btnSend, view.btnResendMsg);
        loadBalance();
        view.etAmount.addTextChangedListener(new TextWatcher() {
            @Override public void beforeTextChanged(CharSequence s, int start, int count, int after) {}
            @Override public void onTextChanged(CharSequence s, int start, int before, int count) {}
            @Override public void afterTextChanged(Editable s) { updateBalanceState(); }
        });
        view.btnResendMsg.setOnClickListener(v -> {
            if (!lastPacketId.isEmpty()) {
                sendImMessage(lastPacketId);
            }
        });
    }

    private void send() {
        final long cents = Money.parseYuanToCents(view.etAmount.getText().toString());
        if (cents <= 0) {
            toast("请输入有效金额（元，最多 2 位小数）");
            return;
        }
        final String greeting = view.etGreeting.getText().toString().trim();
        if (greeting.length() > 200) {
            toast("祝福语最多 200 字");
            return;
        }
        final int count;
        if (RedPacket.KIND_FRIEND.equals(kind)) {
            count = 1;
        } else {
            try {
                count = Integer.parseInt(view.etCount.getText().toString().trim());
            } catch (Exception e) {
                toast("请输入有效红包个数");
                return;
            }
            if (count < 1 || count > 1000) {
                toast("红包个数须在 1–1000 之间");
                return;
            }
            if (cents < count) {
                toast("总金额（分）须不少于红包个数，每份至少 1 分");
                return;
            }
        }
        // 超额拦截：必须在准入引导与交易密码弹窗之前，不发红包请求。
        // 注意：输入框标的是“总金额”，三类接口传的都是总额（好友 amount_cents、
        // 群普通/拼手气 total_amount_cents），故直接拿总金额与余额比，不要乘个数。
        if (!balanceLoaded) {
            toast("余额未加载，请稍后重试");
            return;
        }
        if (cents > availableCents) {
            toast("红包总金额不能超过目前余额");
            return;
        }
        // 发包前先查准入：未完成只引导、不扣款。
        FundGate.check(this, () -> {
            if (RedPacket.KIND_FRIEND.equals(kind)) {
                askTxAndCreate(cents, greeting, count, null);
            } else {
                fetchGroupMembersAndCreate(cents, greeting, count);
            }
        });
    }

    /** 加载可用余额（静默加载，不弹等待框，不与发包流程的等待框抢占）。 */
    private void loadBalance() {
        balanceLoaded = false;
        balanceFailed = false;
        FundApi.getWallet(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                JSONObject w = FundModels.toObj(data);
                if (w == null) {
                    balanceFailed = true;
                    updateBalanceState();
                    return;
                }
                availableCents = w.optLong("balance_cents", 0);
                balanceLoaded = true;
                updateBalanceState();
            }

            @Override
            public void onErr(String code, String message) {
                if ("WALLET_NOT_FOUND".equals(code) || "NOT_FOUND".equals(code)) {
                    FundApi.createWallet(new FundApi.Callback() {
                        @Override
                        public void onOk(Object data) { loadBalance(); }
                        @Override
                        public void onErr(String c, String m) {
                            balanceFailed = true;
                            updateBalanceState();
                        }
                    });
                } else {
                    balanceFailed = true;
                    updateBalanceState();
                }
            }
        });
    }

    /** 余额文案 + 超额时发送按钮置灰（实时提示，提交时的 send() 校验仍是最终关口）。 */
    private void updateBalanceState() {
        String base;
        if (balanceLoaded) {
            base = "目前余额：" + Money.formatCents(availableCents);
        } else if (balanceFailed) {
            base = "目前余额：加载失败，请返回重试";
        } else {
            base = "目前余额：加载中…";
        }
        final long cents = Money.parseYuanToCents(view.etAmount.getText().toString());
        boolean over = balanceLoaded && cents > 0 && cents > availableCents;
        if (over) {
            base += "\n红包总金额不能超过目前余额";
        }
        view.tvBalance.setText(base);
        view.btnSend.setEnabled(!over);
    }

    /**
     * 群红包：发包前调 OpenIM 群接口拉取成员列表作为领取集合（资金服务不直读 IM 库表）。
     * 拉取失败时不静默改开放包，直接报错。
     */
    private void fetchGroupMembersAndCreate(final long cents, final String greeting, final int count) {
        showWaiting();
        OpenIMClient.getInstance().groupManager.getGroupMemberList(
            new OnBase<List<GroupMembersInfo>>() {
                @Override
                public void onError(int code, String error) {
                    cancelWaiting();
                    toast("拉取群成员失败，无法发包：" + error);
                }

                @Override
                public void onSuccess(List<GroupMembersInfo> data) {
                    cancelWaiting();
                    JSONArray ids = new JSONArray();
                    if (data != null) {
                        for (GroupMembersInfo m : data) {
                            if (m != null && m.getUserID() != null) {
                                ids.put(m.getUserID());
                            }
                        }
                    }
                    askTxAndCreate(cents, greeting, count, ids);
                }
            }, targetGroupId, GroupMemberFilter.All.value, 0, 1000);
    }

    private void askTxAndCreate(final long cents, final String greeting, final int count,
                                final JSONArray memberIds) {
        // 6 格交易密码弹窗：输满自动核验，无确认按钮；右上角 ❌ = 取消付款。
        // 核验中弹窗不关；密码错时 handle.fail() 清空 6 格、红字提示、允许重输，不误发包。
        TxPasswordDialog.showForVerification(this, "输入交易密码", (password, handle) -> {
            if (password == null || password.length() != 6) {
                handle.fail("请输入 6 位交易密码");
                return;
            }
            try {
                FundApi.Callback cb = new FundApi.Callback() {
                    @Override
                    public void onOk(Object data) {
                        JSONObject packet = RedPacket.extractPacket(data);
                        if (packet == null) {
                            handle.complete();
                            toast("发包结果解析失败");
                            return;
                        }
                        handle.complete();
                        lastPacketId = packet.optString("packet_id", "");
                        view.tvResult.setText("红包已创建，正在发送消息…\n红包号：" + lastPacketId);
                        sendImMessage(lastPacketId);
                    }

                    @Override
                    public void onErr(String code, String message) {
                        handle.fail(message);
                    }
                };
                if (RedPacket.KIND_FRIEND.equals(kind)) {
                    FundApi.createFriendRedpacket(targetUserId, cents, greeting, password, cb);
                } else if (RedPacket.KIND_GROUP.equals(kind)) {
                    FundApi.createGroupRedpacket(targetGroupId, cents, count, greeting, memberIds, password, cb);
                } else {
                    FundApi.createLuckyRedpacket(targetGroupId, cents, count, greeting, memberIds, password, cb);
                }
            } catch (Exception e) {
                handle.fail("发包失败，请重试");
            }
        });
    }

    /**
     * IM 自定义消息载体：contentType 由 SDK 定为 CUSTOM(110)；
     * data 内 customType=901（仙聊红包，与通话 200–204 不冲突），
     * 内层 data 只放 packetId 与 kind，不放金额。
     */
    private void sendImMessage(String packetId) {
        try {
            JSONObject inner = new JSONObject();
            inner.put("packetId", packetId);
            inner.put("kind", kind);
            JSONObject outer = new JSONObject();
            outer.put("customType", 901);
            outer.put("data", inner.toString());
            Message msg = OpenIMClient.getInstance().messageManager.createCustomMessage(
                outer.toString(), "", "");
            String recvUid = RedPacket.KIND_FRIEND.equals(kind) ? targetUserId : "";
            String recvGid = RedPacket.KIND_FRIEND.equals(kind) ? "" : targetGroupId;
            OpenIMClient.getInstance().messageManager.sendMessage(new OnMsgSendCallback() {
                @Override
                public void onError(int code, String error) {
                    view.tvResult.setText("红包已创建但消息发送失败，红包号：" + lastPacketId
                        + "，请点“重新发送红包消息”。");
                    view.btnResendMsg.setVisibility(View.VISIBLE);
                }

                @Override
                public void onProgress(long progress) {
                }

                @Override
                public void onSuccess(Message message) {
                    view.tvResult.setText("红包已发送！红包号：" + lastPacketId);
                    view.btnResendMsg.setVisibility(View.GONE);
                    toast("红包已发送");
                    finish();
                }
            }, msg, recvUid, recvGid, new OfflinePushInfo());
        } catch (Exception e) {
            view.tvResult.setText("红包已创建但消息构造失败，红包号：" + lastPacketId);
            view.btnResendMsg.setVisibility(View.VISIBLE);
        }
    }
}
