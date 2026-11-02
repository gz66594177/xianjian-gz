package io.openim.android.demo.fund;

import android.animation.AnimatorSet;
import android.animation.ObjectAnimator;
import android.animation.ValueAnimator;
import android.media.MediaPlayer;
import android.os.Bundle;
import android.view.View;
import android.widget.ArrayAdapter;

import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

import io.openim.android.demo.databinding.ActivityFundRedpacketDetailBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/**
 * 红包详情与领取页：状态/进度/剩余/领取列表/（拼手气）排名与手气最佳。
 * 金额、状态、领取记录全部以服务端详情接口为准；领取不重复验交易密码，但须已完成准入。
 */
public class RedPacketDetailActivity extends BaseActivity<BaseViewModel, ActivityFundRedpacketDetailBinding> {

    public static final String EXTRA_KIND = "kind";
    public static final String EXTRA_PACKET_ID = "packet_id";

    private String kind = RedPacket.KIND_FRIEND;
    private String packetId = "";
    private final List<String> lines = new ArrayList<>();
    private boolean envelopeOpened = false;
    private ArrayAdapter<String> adapter;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundRedpacketDetailBinding.inflate(getLayoutInflater()));
        sink();
        kind = getIntent().getStringExtra(EXTRA_KIND);
        if (kind == null) {
            kind = RedPacket.KIND_FRIEND;
        }
        packetId = getIntent().getStringExtra(EXTRA_PACKET_ID);
        if (packetId == null) {
            packetId = "";
        }
        view.tvTitle.setText(RedPacket.kindName(kind) + "详情");
        adapter = new ArrayAdapter<>(this, android.R.layout.simple_list_item_1, lines);
        view.list.setAdapter(adapter);
        view.btnClaim.setOnClickListener(v -> claim());
        FundUi.pressScale(view.btnClaim);
        load();
    }

    private void load() {
        showWaiting();
        RedPacket.getDetail(kind, packetId, new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                JSONObject packet = RedPacket.extractPacket(data);
                cancelWaiting();
                if (packet == null) {
                    toast("红包详情解析失败");
                    return;
                }
                render(packet);
                loadClaims();
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message != null && message.contains("does not belong to viewer")
                    ? "该红包不属于你，无法查看" : message);
            }
        });
    }

    private void render(JSONObject packet) {
        boolean isFriend = RedPacket.KIND_FRIEND.equals(kind);
        int status = packet.optInt("status", 0);
        StringBuilder sb = new StringBuilder();
        sb.append("状态：").append(FundModels.packetStatusName(status, isFriend));
        if (packet.optBoolean("expired", false)) {
            sb.append("（已过期）");
        }
        String greeting = packet.optString("greeting", "");
        if (greeting != null && !greeting.isEmpty() && !"null".equals(greeting)) {
            sb.append("\n祝福：").append(greeting);
        }
        view.tvStatus.setText(sb.toString());
        String greeting2 = packet.optString("greeting", "");
        view.tvGreeting.setText((greeting2 == null || greeting2.isEmpty() || "null".equals(greeting2))
            ? "恭喜发财，大吉大利" : greeting2);
        if (isFriend) {
            if (status == 1) {
                view.tvAmount.setText(Money.formatCents(packet.optLong("amount_cents", 0)));
                view.tvAmount.setVisibility(View.VISIBLE);
                view.tvProgress.setText("");
                envelopeOpened = true;
            } else {
                if (!envelopeOpened) view.tvAmount.setVisibility(View.GONE);
                view.tvProgress.setText("");
            }
        } else {
            view.tvProgress.setText("进度：" + packet.optInt("claimed_count", 0)
                + "/" + packet.optInt("total_count", 0)
                + " · 已领 " + Money.formatCents(packet.optLong("claimed_amount_cents", 0))
                + " · 剩余 " + Money.formatCents(packet.optLong("remaining_amount_cents", 0))
                + "（共 " + Money.formatCents(packet.optLong("total_amount_cents", 0)) + "）");
        }
        if (RedPacket.KIND_LUCKY.equals(kind)) {
            String bestId = packet.optString("best_claimer_id", "");
            if (bestId != null && !bestId.isEmpty() && !"null".equals(bestId)) {
                view.tvBest.setText("👑 手气最佳：" + bestId
                    + " · " + Money.formatCents(packet.optLong("best_amount_cents", 0)));
            } else {
                view.tvBest.setText("");
            }
        }
        view.btnClaim.setVisibility(status == 0 ? View.VISIBLE : View.GONE);
        if (envelopeOpened) {
            view.flapView.setVisibility(View.INVISIBLE);
            view.flapView.setRotationX(-180f);
            view.fuView.setScaleX(1.25f);
            view.fuView.setScaleY(1.25f);
        } else {
            view.flapView.setVisibility(View.VISIBLE);
            view.flapView.setRotationX(0f);
            view.fuView.setScaleX(1f);
            view.fuView.setScaleY(1f);
        }
        view.coin1.setVisibility(View.INVISIBLE);
        view.coin2.setVisibility(View.INVISIBLE);
        view.coin3.setVisibility(View.INVISIBLE);
    }

    private void loadClaims() {
        RedPacket.getClaims(kind, packetId, new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                lines.clear();
                List<JSONObject> claims = FundModels.toList(data);
                if (RedPacket.KIND_LUCKY.equals(kind)) {
                    loadRanking(claims);
                    return;
                }
                for (JSONObject c : claims) {
                    lines.add(c.optString("claimer_id", "")
                        + " · " + Money.formatCents(c.optLong("amount_cents", 0)));
                }
                adapter.notifyDataSetChanged();
            }

            @Override
            public void onErr(String code, String message) {
                adapter.notifyDataSetChanged();
            }
        });
    }

    /** 拼手气：领取列表按服务端排名接口展示（金额降序、领取时间升序）。 */
    private void loadRanking(final List<JSONObject> fallback) {
        FundApi.getLuckyRanking(packetId, new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                lines.clear();
                List<JSONObject> ranks = FundModels.toList(data);
                if (ranks.isEmpty()) {
                    ranks = fallback;
                }
                for (JSONObject r : ranks) {
                    String line = "";
                    if (r.has("rank")) {
                        line += "第 " + r.optInt("rank", 0) + " 名 · ";
                    }
                    line += r.optString("claimer_id", "")
                        + " · " + Money.formatCents(r.optLong("amount_cents", 0));
                    lines.add(line);
                }
                adapter.notifyDataSetChanged();
            }

            @Override
            public void onErr(String code, String message) {
                lines.clear();
                for (JSONObject c : fallback) {
                    lines.add(c.optString("claimer_id", "")
                        + " · " + Money.formatCents(c.optLong("amount_cents", 0)));
                }
                adapter.notifyDataSetChanged();
            }
        });
    }

    private void claim() {
        // 领取不重复验交易密码，但须已完成准入；未完成只引导、包状态不动。
        FundGate.check(this, () -> {
            showWaiting();
            RedPacket.claim(kind, packetId, new FundApi.Callback() {
                @Override
                public void onOk(Object data) {
                    cancelWaiting();
                    JSONObject packet = RedPacket.extractPacket(data);
                    JSONObject claim = FundModels.toObj(data) != null
                        ? FundModels.toObj(data).optJSONObject("claim") : null;
                    long claimCents = claim != null ? claim.optLong("amount_cents", 0) : 0;
                    envelopeOpened = true;
                    playOpenAnimation(claimCents);
                    if (packet != null) {
                        // 动画播完再按服务端状态重绘，金额与列表以服务端为准。
                        view.getRoot().postDelayed(() -> {
                            render(packet);
                            loadClaims();
                        }, 2000);
                    } else {
                        loadClaims();
                    }
                }

                @Override
                public void onErr(String code, String message) {
                    cancelWaiting();
                    toast(message);
                    load();
                }
            });
        });
    }

    /** 开包动画一体：音效 + 红包盖弹开 + 金币飞出 + 金额数字滚动弹出。 */
    private void playOpenAnimation(long amountCents) {
        try {
            MediaPlayer mp = MediaPlayer.create(this, io.openim.android.demo.R.raw.redpacket_open);
            if (mp != null) {
                mp.setOnCompletionListener(MediaPlayer::release);
                mp.start();
            }
        } catch (Exception ignored) {
        }
        view.flapView.setCameraDistance(8000f);
        view.flapView.setPivotY(0f);
        ObjectAnimator flap = ObjectAnimator.ofFloat(view.flapView, "rotationX", 0f, -180f);
        flap.setDuration(700);
        flap.addListener(new android.animation.AnimatorListenerAdapter() {
            @Override
            public void onAnimationEnd(android.animation.Animator animation) {
                view.flapView.setVisibility(View.INVISIBLE);
            }
        });
        ObjectAnimator fuScaleX = ObjectAnimator.ofFloat(view.fuView, "scaleX", 1f, 1.25f);
        ObjectAnimator fuScaleY = ObjectAnimator.ofFloat(view.fuView, "scaleY", 1f, 1.25f);
        fuScaleX.setDuration(700);
        fuScaleY.setDuration(700);
        AnimatorSet open = new AnimatorSet();
        open.playTogether(flap, fuScaleX, fuScaleY);
        open.start();
        view.getRoot().postDelayed(() -> {
            View[] coins = {view.coin1, view.coin2, view.coin3};
            for (int i = 0; i < coins.length; i++) {
                View c = coins[i];
                c.setVisibility(View.VISIBLE);
                c.setAlpha(1f);
                c.setTranslationY(0f);
                c.setRotation(0f);
                ObjectAnimator up = ObjectAnimator.ofFloat(c, "translationY", 0f, -220f);
                ObjectAnimator fade = ObjectAnimator.ofFloat(c, "alpha", 1f, 0f);
                ObjectAnimator rot = ObjectAnimator.ofFloat(c, "rotation", 0f, 360f);
                AnimatorSet s = new AnimatorSet();
                s.playTogether(up, fade, rot);
                s.setDuration(1100);
                s.setStartDelay(i * 120L);
                s.start();
            }
            view.tvAmount.setVisibility(View.VISIBLE);
            ValueAnimator count = ValueAnimator.ofInt(0, (int) amountCents);
            count.setDuration(1200);
            count.addUpdateListener(a -> view.tvAmount.setText(
                Money.formatCents((Integer) a.getAnimatedValue())));
            count.start();
        }, 650);
    }
}
