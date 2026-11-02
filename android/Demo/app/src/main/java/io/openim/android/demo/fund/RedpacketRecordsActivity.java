package io.openim.android.demo.fund;

import android.content.Intent;
import android.os.Bundle;
import android.view.View;
import android.widget.ArrayAdapter;

import io.openim.android.ouicore.widget.BottomPopDialog;

import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

import io.openim.android.demo.databinding.ActivityFundRecordsBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/** 红包记录入口：好友 / 群普通 / 拼手气 × 发出 / 收到。点击进详情。 */
public class RedpacketRecordsActivity extends BaseActivity<BaseViewModel, ActivityFundRecordsBinding> {

    private final List<String> lines = new ArrayList<>();
    private final List<JSONObject> items = new ArrayList<>();
    private ArrayAdapter<String> adapter;
    private int offset = 0;
    private int kindPos = 0;
    private int rolePos = 0;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundRecordsBinding.inflate(getLayoutInflater()));
        sink();
        view.rowKind.setOnClickListener(v -> showOptions(
            new String[]{"好友红包", "群普通红包", "群拼手气红包"}, kindPos, pos -> {
                kindPos = pos;
                view.tvKind.setText(new String[]{"好友红包", "群普通红包", "群拼手气红包"}[pos]);
                load(true);
            }));
        view.rowRole.setOnClickListener(v -> showOptions(
            new String[]{"我发出的", "我收到的"}, rolePos, pos -> {
                rolePos = pos;
                view.tvRole.setText(new String[]{"我发出的", "我收到的"}[pos]);
                load(true);
            }));
        FundUi.pressScale(view.rowKind, view.rowRole);
        adapter = new ArrayAdapter<>(this, android.R.layout.simple_list_item_1, lines);
        view.list.setAdapter(adapter);
        load(true);
        view.list.setOnItemClickListener((parent, v, position, id) -> {
            JSONObject o = items.get(position);
            // 自己发出的包：领取详情接口只认领取人，硬进会报英文错；直接用列表数据出摘要。
            if ("sent".equals(currentRole())) {
                boolean isFriend = RedPacket.KIND_FRIEND.equals(currentKind());
                StringBuilder sb = new StringBuilder();
                sb.append("金额：").append(isFriend
                    ? Money.formatCents(o.optLong("amount_cents", 0))
                    : Money.formatCents(o.optLong("total_amount_cents", 0)));
                sb.append("\n状态：").append(FundModels.packetStatusName(o.optInt("status", 0), isFriend));
                if (!isFriend) {
                    sb.append("\n已领：").append(o.optInt("claimed_count", 0))
                        .append("/").append(o.optInt("total_count", 0));
                }
                String g = o.optString("greeting", "");
                if (g != null && !g.isEmpty() && !"null".equals(g)) sb.append("\n祝福：").append(g);
                FundUi.dialog(this).setTitle("红包详情").setMessage(sb.toString())
                    .setPositiveButton("知道了", null).show();
                return;
            }
            Intent intent = new Intent(this, RedPacketDetailActivity.class);
            intent.putExtra(RedPacketDetailActivity.EXTRA_KIND, currentKind());
            intent.putExtra(RedPacketDetailActivity.EXTRA_PACKET_ID, o.optString("packet_id", ""));
            startActivity(intent);
        });
        view.btnMore.setOnClickListener(v -> load(false));
        FundUi.pressScale(view.btnMore);
    }

    private String currentKind() {
        int pos = kindPos;
        if (pos == 1) {
            return RedPacket.KIND_GROUP;
        }
        if (pos == 2) {
            return RedPacket.KIND_LUCKY;
        }
        return RedPacket.KIND_FRIEND;
    }

    private String currentRole() {
        return rolePos == 1 ? "received" : "sent";
    }

    /** 底部动画弹窗选一项：复用注册弹窗 BottomPopDialog（上滑+淡入、减速、统一时长）。
     * 选中项用主色 + 对勾标出，未选中用正文色；只改呈现与动效，选项内容与筛选逻辑原样。 */
    private void showOptions(String[] options, int current, java.util.function.IntConsumer onSelect) {
        BottomPopDialog dialog = new BottomPopDialog(this);
        android.view.View m1 = dialog.getMainView().menu1;
        android.view.View m2 = dialog.getMainView().menu2;
        android.view.View m4 = dialog.getMainView().menu4;
        android.view.View m3 = dialog.getMainView().menu3;
        android.widget.TextView[] rows = new android.widget.TextView[]{
            (android.widget.TextView) m1, (android.widget.TextView) m2, (android.widget.TextView) m4};
        for (int i = 0; i < options.length; i++) {
            final int pos = i;
            rows[i].setVisibility(android.view.View.VISIBLE);
            rows[i].setText(options[i] + (i == current ? "  \u2713" : ""));
            rows[i].setTextColor(getResources().getColor(
                i == current ? io.openim.android.ouicore.R.color.theme
                             : io.openim.android.ouicore.R.color.txt_black, getTheme()));
            rows[i].setOnClickListener(v -> {
                dialog.dismiss();
                if (pos != current) {
                    onSelect.accept(pos);
                }
            });
        }
        FundUi.pressScale(m4);
        m3.setOnClickListener(v -> dialog.dismiss());
        dialog.show();
    }

    private void load(final boolean reset) {
        if (reset) {
            offset = 0;
            lines.clear();
            items.clear();
            adapter.notifyDataSetChanged();
        }
        final String kind = currentKind();
        final boolean isFriend = RedPacket.KIND_FRIEND.equals(kind);
        showWaiting();
        RedPacket.list(kind, currentRole(), 50, offset, new FundApi.Callback() {
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
                    String line;
                    String greeting = o.optString("greeting", "");
                    String tail = (greeting != null && !greeting.isEmpty() && !"null".equals(greeting))
                        ? greeting
                        : ("对方 " + ("sent".equals(currentRole())
                            ? o.optString("receiver_id", "") : o.optString("sender_id", "")));
                    if (isFriend) {
                        line = Money.formatCents(o.optLong("amount_cents", 0))
                            + " · " + FundModels.packetStatusName(o.optInt("status", 0), true)
                            + " · " + tail;
                    } else {
                        line = Money.formatCents(o.optLong("total_amount_cents", 0))
                            + " · " + o.optInt("claimed_count", 0) + "/"
                            + o.optInt("total_count", 0)
                            + " · " + FundModels.packetStatusName(o.optInt("status", 0), false)
                            + " · " + tail;
                    }
                    lines.add(line);
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
        });
    }
}
