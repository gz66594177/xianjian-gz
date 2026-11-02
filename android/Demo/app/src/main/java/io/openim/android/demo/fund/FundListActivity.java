package io.openim.android.demo.fund;

import android.os.Bundle;
import android.view.View;
import android.widget.ArrayAdapter;

import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

import io.openim.android.demo.databinding.ActivityFundListBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/**
 * 通用分页列表：资金流水 / 充值记录 / 提现记录 / 转账记录。
 * 点击条目拉取服务端详情展示（不以列表缓存冒充详情状态）。
 */
public class FundListActivity extends BaseActivity<BaseViewModel, ActivityFundListBinding> {

    public static final String EXTRA_MODE = "mode";
    public static final String MODE_LEDGER = "ledger";
    public static final String MODE_RECHARGES = "recharges";
    public static final String MODE_WITHDRAWALS = "withdrawals";
    public static final String MODE_TRANSFERS_SENT = "transfers_sent";
    public static final String MODE_TRANSFERS_RECEIVED = "transfers_received";

    private String mode = MODE_LEDGER;
    private int offset = 0;
    private final List<String> lines = new ArrayList<>();
    private final List<JSONObject> items = new ArrayList<>();
    private ArrayAdapter<String> adapter;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundListBinding.inflate(getLayoutInflater()));
        sink();
        mode = getIntent().getStringExtra(EXTRA_MODE);
        if (mode == null) {
            mode = MODE_LEDGER;
        }
        view.tvTitle.setText(titleFor(mode));
        // 标题居中见布局；边框呼吸动画：主题色边框在不透明与半透明之间缓变，
        // 标题入场淡入。只作用于本页标题，不改列表与空态文案。
        view.tvTitle.setAlpha(0f);
        view.tvTitle.animate().alpha(1f).setDuration(300).start();
        try {
            android.graphics.drawable.GradientDrawable titleBg =
                (android.graphics.drawable.GradientDrawable) view.tvTitle.getBackground();
            if (titleBg != null) {
                int theme = androidx.core.content.ContextCompat.getColor(this,
                    io.openim.android.ouicore.R.color.theme);
                android.animation.ValueAnimator borderAnim =
                    android.animation.ValueAnimator.ofInt(255, 70, 255);
                borderAnim.setDuration(2000);
                borderAnim.setRepeatCount(android.animation.ValueAnimator.INFINITE);
                borderAnim.setRepeatMode(android.animation.ValueAnimator.REVERSE);
                borderAnim.addUpdateListener(a -> {
                    int alpha = (int) a.getAnimatedValue();
                    titleBg.setStroke(3, android.graphics.Color.argb(alpha,
                        android.graphics.Color.red(theme),
                        android.graphics.Color.green(theme),
                        android.graphics.Color.blue(theme)));
                });
                borderAnim.start();
            }
        } catch (Exception ignored) {
        }
        adapter = new ArrayAdapter<>(this, android.R.layout.simple_list_item_1, lines);
        view.list.setAdapter(adapter);
        view.list.setOnItemClickListener((parent, v, position, id) -> showDetail(items.get(position)));
        view.btnMore.setOnClickListener(v -> load(false));
        FundUi.pressScale(view.btnMore);
        load(true);
    }

    private String titleFor(String mode) {
        switch (mode) {
            case MODE_RECHARGES:
                return "充值记录";
            case MODE_WITHDRAWALS:
                return "提现记录";
            case MODE_TRANSFERS_SENT:
                return "转账记录（发出）";
            case MODE_TRANSFERS_RECEIVED:
                return "转账记录（收到）";
            default:
                return "资金流水";
        }
    }

    private void load(final boolean reset) {
        if (reset) {
            offset = 0;
            lines.clear();
            items.clear();
            adapter.notifyDataSetChanged();
        }
        showWaiting();
        FundApi.Callback cb = new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                cancelWaiting();
                List<JSONObject> page = FundModels.toList(data);
                if (page.isEmpty() && reset) {
                    view.tvEmpty.setVisibility(View.VISIBLE);
                } else {
                    view.tvEmpty.setVisibility(View.GONE);
                }
                for (JSONObject o : page) {
                    items.add(o);
                    lines.add(lineFor(mode, o));
                }
                offset += page.size();
                adapter.notifyDataSetChanged();
                view.btnMore.setVisibility(page.size() < 50 ? View.GONE : View.VISIBLE);
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message);
            }
        };
        switch (mode) {
            case MODE_RECHARGES:
                FundApi.listRecharges(null, 50, offset, cb);
                break;
            case MODE_WITHDRAWALS:
                FundApi.listWithdrawals(null, 50, offset, cb);
                break;
            case MODE_TRANSFERS_SENT:
                FundApi.listTransfers("sent", 50, offset, cb);
                break;
            case MODE_TRANSFERS_RECEIVED:
                FundApi.listTransfers("received", 50, offset, cb);
                break;
            default:
                FundApi.getLedger(50, offset, cb);
                break;
        }
    }

    private String lineFor(String mode, JSONObject o) {
        switch (mode) {
            case MODE_RECHARGES:
                return Money.formatCents(o.optLong("amount_cents", 0))
                    + " · " + FundModels.rechargeStatusName(o.optInt("status", 0))
                    + " · " + o.optString("order_id", "");
            case MODE_WITHDRAWALS:
                return Money.formatCents(o.optLong("amount_cents", 0))
                    + " · 手续费 " + Money.formatCents(o.optLong("fee_cents", 0))
                    + " · " + FundModels.withdrawStatusName(o.optInt("status", 0))
                    + " · " + o.optString("order_id", "");
            case MODE_TRANSFERS_SENT:
            case MODE_TRANSFERS_RECEIVED:
                return Money.formatCents(o.optLong("amount_cents", 0))
                    + " · 对方 " + (MODE_TRANSFERS_SENT.equals(mode)
                        ? o.optString("receiver_id", "") : o.optString("sender_id", ""))
                    + " · " + o.optString("order_id", "");
            default:
                return FundModels.ledgerLine(o);
        }
    }

    /** 点击条目：按订单号拉服务端详情展示。 */
    private void showDetail(JSONObject item) {
        if (MODE_LEDGER.equals(mode)) {
            FundUi.dialog(this)
                .setTitle("流水详情")
                .setMessage("txn：" + item.optString("txn_id", "")
                    + "\n" + FundModels.ledgerLine(item))
                .setPositiveButton("确定", null)
                .show();
            return;
        }
        String id = item.optString("order_id", "");
        if (id.isEmpty()) {
            return;
        }
        showWaiting();
        FundApi.Callback cb = new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                cancelWaiting();
                JSONObject o = FundModels.toObj(data);
                if (o == null) {
                    return;
                }
                FundUi.dialog(FundListActivity.this)
                    .setTitle("订单详情")
                    .setMessage(detailText(mode, o))
                    .setPositiveButton("确定", null)
                    .show();
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message);
            }
        };
        if (MODE_RECHARGES.equals(mode)) {
            FundApi.getRecharge(id, cb);
        } else if (MODE_WITHDRAWALS.equals(mode)) {
            FundApi.getWithdrawal(id, cb);
        } else {
            FundApi.getTransfer(id, cb);
        }
    }

    private String detailText(String mode, JSONObject o) {
        StringBuilder sb = new StringBuilder();
        sb.append("订单号：").append(o.optString("order_id", "")).append("\n");
        sb.append("金额：").append(Money.formatCents(o.optLong("amount_cents", 0))).append("\n");
        if (MODE_RECHARGES.equals(mode)) {
            sb.append("状态：").append(FundModels.rechargeStatusName(o.optInt("status", 0))).append("\n");
            sb.append("渠道：").append(o.optString("channel", "")).append("\n");
        } else if (MODE_WITHDRAWALS.equals(mode)) {
            sb.append("状态：").append(FundModels.withdrawStatusName(o.optInt("status", 0))).append("\n");
            sb.append("手续费：").append(Money.formatCents(o.optLong("fee_cents", 0))).append("\n");
            sb.append("实际到账：").append(Money.formatCents(o.optLong("actual_cents", 0))).append("\n");
            String proof = o.optString("payout_proof_no", "");
            if (proof != null && !proof.isEmpty() && !"null".equals(proof)) {
                sb.append("打款凭证：").append(proof).append("\n");
            }
            String reason = o.optString("failure_reason", "");
            if (reason != null && !reason.isEmpty() && !"null".equals(reason)) {
                sb.append("失败原因：").append(reason).append("\n");
            }
        } else {
            sb.append("转出方：").append(o.optString("sender_id", "")).append("\n");
            sb.append("收款方：").append(o.optString("receiver_id", "")).append("\n");
            sb.append("附言：").append(o.optString("remark", "")).append("\n");
            sb.append("状态：").append(o.optInt("status", 0) == 1 ? "成功" : "失败").append("\n");
        }
        return sb.toString();
    }
}
