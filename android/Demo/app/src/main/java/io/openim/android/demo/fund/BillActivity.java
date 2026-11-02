package io.openim.android.demo.fund;

import android.content.res.ColorStateList;
import android.os.Bundle;
import android.view.View;
import android.widget.ArrayAdapter;
import android.widget.TextView;

import androidx.core.content.ContextCompat;

import org.json.JSONObject;

import java.text.ParseException;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Calendar;
import java.util.Collections;
import java.util.List;
import java.util.Locale;
import java.util.TimeZone;

import io.openim.android.demo.databinding.ActivityFundBillBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.base.BaseViewModel;

/**
 * 账单页（独立页）：合并各资金记录来源的真实数据，按月份 + 类型 + 时间筛选。
 * 流水接口（GET /v1/wallet/ledger）只支持 limit/offset，不支持类型/时间参数，
 * 且流水条目不带业务类型字段，无法从流水反推充值/提现/转账/红包——因此账单不走
 * 流水反推，而是直接合并各业务列表（充值/提现/转账发出/收到/好友红包发出/收到/
 * 群红包发出/收到/拼手气发出/收到），类型全部来自真实来源；群/拼手气“收到”金额
 * 以该包领取记录中本人一条为准（需逐包拉 claims，失败则该包不计入汇总）。
 * 各来源只取首批 50 条，见页面脚注。
 */
public class BillActivity extends BaseActivity<BaseViewModel, ActivityFundBillBinding> {

    private static final int TYPE_ALL = 0;
    private static final int TYPE_RECHARGE = 1;
    private static final int TYPE_WITHDRAW = 2;
    private static final int TYPE_TRANSFER = 3;
    private static final int TYPE_REDPACKET = 4;
    private static final int TYPE_INCOME = 5;

    private static final int TIME_ALL = 0;
    private static final int TIME_TODAY = 1;
    private static final int TIME_WEEK = 2;
    private static final int TIME_MONTH = 3;
    private static final int TIME_QUARTER = 4;

    private static class Item {
        int type;
        boolean income; // true 入账，false 出账；中性（未支付/已拒绝/未领取）不计汇总
        boolean counted;
        long amountCents;
        long timeMillis;
        String label;
        String detail;
    }

    private final List<Item> all = new ArrayList<>();
    private final List<String> lines = new ArrayList<>();
    private final List<Item> shown = new ArrayList<>();
    private ArrayAdapter<String> adapter;

    private int typeFilter = TYPE_ALL;
    private int timeFilter = TIME_ALL;
    private int selYear;
    private int selMonth; // 0-11
    private int pending = 0;
    private String myId = "";

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundBillBinding.inflate(getLayoutInflater()));
        sink();
        if (BaseApp.inst().loginCertificate != null) {
            myId = BaseApp.inst().loginCertificate.userID;
        }
        Calendar now = Calendar.getInstance();
        selYear = now.get(Calendar.YEAR);
        selMonth = now.get(Calendar.MONTH);
        adapter = new ArrayAdapter<>(this, android.R.layout.simple_list_item_1, lines);
        view.list.setAdapter(adapter);
        view.list.setOnItemClickListener((parent, v, position, id) -> {
            Item it = shown.get(position);
            FundUi.dialog(this)
                .setTitle(it.label)
                .setMessage(it.detail)
                .setPositiveButton("确定", null)
                .show();
        });
        view.btnPrevMonth.setOnClickListener(v -> shiftMonth(-1));
        view.btnNextMonth.setOnClickListener(v -> shiftMonth(1));
        FundUi.pressScale(view.btnPrevMonth, view.btnNextMonth);
        bindTypeChips();
        bindTimeChips();
        refreshChips();
        loadAll();
    }

    private void shiftMonth(int delta) {
        Calendar c = Calendar.getInstance();
        c.set(selYear, selMonth, 1);
        c.add(Calendar.MONTH, delta);
        selYear = c.get(Calendar.YEAR);
        selMonth = c.get(Calendar.MONTH);
        applyFilters();
    }

    private void bindTypeChips() {
        view.chipAll.setOnClickListener(v -> { typeFilter = TYPE_ALL; refreshChips(); applyFilters(); });
        view.chipRecharge.setOnClickListener(v -> { typeFilter = TYPE_RECHARGE; refreshChips(); applyFilters(); });
        view.chipWithdraw.setOnClickListener(v -> { typeFilter = TYPE_WITHDRAW; refreshChips(); applyFilters(); });
        view.chipTransfer.setOnClickListener(v -> { typeFilter = TYPE_TRANSFER; refreshChips(); applyFilters(); });
        view.chipRedpacket.setOnClickListener(v -> { typeFilter = TYPE_REDPACKET; refreshChips(); applyFilters(); });
        view.chipIncome.setOnClickListener(v -> { typeFilter = TYPE_INCOME; refreshChips(); applyFilters(); });
    }

    private void bindTimeChips() {
        view.timeAll.setOnClickListener(v -> { timeFilter = TIME_ALL; refreshChips(); applyFilters(); });
        view.timeToday.setOnClickListener(v -> { timeFilter = TIME_TODAY; refreshChips(); applyFilters(); });
        view.timeWeek.setOnClickListener(v -> { timeFilter = TIME_WEEK; refreshChips(); applyFilters(); });
        view.timeMonth.setOnClickListener(v -> { timeFilter = TIME_MONTH; refreshChips(); applyFilters(); });
        view.timeQuarter.setOnClickListener(v -> { timeFilter = TIME_QUARTER; refreshChips(); applyFilters(); });
    }

    private void paintChip(TextView tv, boolean selected) {
        int bg = ContextCompat.getColor(this, selected ? io.openim.android.ouicore.R.color.chip_selected_bg : io.openim.android.ouicore.R.color.chip_bg);
        int fg = ContextCompat.getColor(this, selected ? io.openim.android.ouicore.R.color.chip_selected_text : io.openim.android.ouicore.R.color.chip_text);
        tv.setBackgroundTintList(ColorStateList.valueOf(bg));
        tv.setTextColor(fg);
    }

    private void refreshChips() {
        paintChip(view.chipAll, typeFilter == TYPE_ALL);
        paintChip(view.chipRecharge, typeFilter == TYPE_RECHARGE);
        paintChip(view.chipWithdraw, typeFilter == TYPE_WITHDRAW);
        paintChip(view.chipTransfer, typeFilter == TYPE_TRANSFER);
        paintChip(view.chipRedpacket, typeFilter == TYPE_REDPACKET);
        paintChip(view.chipIncome, typeFilter == TYPE_INCOME);
        paintChip(view.timeAll, timeFilter == TIME_ALL);
        paintChip(view.timeToday, timeFilter == TIME_TODAY);
        paintChip(view.timeWeek, timeFilter == TIME_WEEK);
        paintChip(view.timeMonth, timeFilter == TIME_MONTH);
        paintChip(view.timeQuarter, timeFilter == TIME_QUARTER);
    }

    // ---------------- 数据加载 ----------------

    private synchronized void taskStart() {
        pending++;
    }

    private synchronized void taskDone() {
        pending--;
        if (pending <= 0) {
            runOnUiThread(() -> {
                cancelWaiting();
                Collections.sort(all, (a, b) -> Long.compare(b.timeMillis, a.timeMillis));
                applyFilters();
            });
        }
    }

    private void loadAll() {
        showWaiting();
        taskStart(); // 总任务（防止 10 路回调竞态下提前收尾）
        loadList(cb -> FundApi.listRecharges(null, 50, 0, cb), (o, items) -> {
            Item it = base(TYPE_RECHARGE);
            it.amountCents = o.optLong("amount_cents", 0);
            it.timeMillis = parseTime(o.optString("created_at", ""));
            it.income = o.optInt("status", 0) == 1;
            it.counted = it.income;
            it.label = "充值" + (it.income ? "" : "（" + FundModels.rechargeStatusName(o.optInt("status", 0)) + "）");
            it.detail = "订单号：" + o.optString("order_id", "") + "\n金额：" + Money.formatCents(it.amountCents)
                + "\n状态：" + FundModels.rechargeStatusName(o.optInt("status", 0));
            items.add(it);
        });
        loadList(cb -> FundApi.listWithdrawals(null, 50, 0, cb), (o, items) -> {
            Item it = base(TYPE_WITHDRAW);
            it.amountCents = o.optLong("amount_cents", 0);
            it.timeMillis = parseTime(o.optString("created_at", ""));
            int st = o.optInt("status", 0);
            it.income = false;
            it.counted = st == 0 || st == 1 || st == 3 || st == 4; // 待审核/通过/处理中/成功计支出
            it.label = "提现（" + FundModels.withdrawStatusName(st) + "）";
            it.detail = "订单号：" + o.optString("order_id", "") + "\n金额：" + Money.formatCents(it.amountCents)
                + "\n手续费：" + Money.formatCents(o.optLong("fee_cents", 0))
                + "\n状态：" + FundModels.withdrawStatusName(st);
            items.add(it);
        });
        loadList(cb -> FundApi.listTransfers("sent", 50, 0, cb), (o, items) -> {
            Item it = base(TYPE_TRANSFER);
            it.amountCents = o.optLong("amount_cents", 0);
            it.timeMillis = parseTime(o.optString("created_at", ""));
            it.income = false;
            it.counted = true;
            it.label = "转账发出 · 对方 " + o.optString("receiver_id", "");
            it.detail = "订单号：" + o.optString("order_id", "") + "\n金额：" + Money.formatCents(it.amountCents)
                + "\n收款方：" + o.optString("receiver_id", "") + "\n附言：" + o.optString("remark", "");
            items.add(it);
        });
        loadList(cb -> FundApi.listTransfers("received", 50, 0, cb), (o, items) -> {
            Item it = base(TYPE_TRANSFER);
            it.amountCents = o.optLong("amount_cents", 0);
            it.timeMillis = parseTime(o.optString("created_at", ""));
            it.income = true;
            it.counted = true;
            it.label = "转账收到 · 对方 " + o.optString("sender_id", "");
            it.detail = "订单号：" + o.optString("order_id", "") + "\n金额：" + Money.formatCents(it.amountCents)
                + "\n转出方：" + o.optString("sender_id", "") + "\n附言：" + o.optString("remark", "");
            items.add(it);
        });
        loadList(cb -> FundApi.listFriendRedpackets("sent", 50, 0, cb), (o, items) -> {
            Item it = base(TYPE_REDPACKET);
            it.amountCents = o.optLong("amount_cents", 0);
            it.timeMillis = parseTime(o.optString("created_at", ""));
            int st = o.optInt("status", 0);
            it.income = false;
            it.counted = st != 2; // 过期退款不计支出
            it.label = "好友红包发出 · 对方 " + o.optString("receiver_id", "");
            it.detail = "红包号：" + o.optString("packet_id", "") + "\n金额：" + Money.formatCents(it.amountCents);
            items.add(it);
        });
        loadList(cb -> FundApi.listFriendRedpackets("received", 50, 0, cb), (o, items) -> {
            Item it = base(TYPE_REDPACKET);
            it.amountCents = o.optLong("amount_cents", 0);
            it.timeMillis = parseTime(o.optString("created_at", ""));
            int st = o.optInt("status", 0);
            it.income = true;
            it.counted = st == 1; // 已领取才计收入
            it.label = "好友红包收到 · 对方 " + o.optString("sender_id", "");
            it.detail = "红包号：" + o.optString("packet_id", "") + "\n金额：" + Money.formatCents(it.amountCents);
            items.add(it);
        });
        loadList(cb -> FundApi.listGroupRedpackets("sent", 50, 0, cb), (o, items) -> {
            Item it = base(TYPE_REDPACKET);
            it.amountCents = o.optLong("total_amount_cents", 0);
            it.timeMillis = parseTime(o.optString("created_at", ""));
            int st = o.optInt("status", 0);
            it.income = false;
            it.counted = st != 2;
            it.label = "群红包发出 · " + o.optInt("total_count", 0) + " 份";
            it.detail = "红包号：" + o.optString("packet_id", "") + "\n总金额：" + Money.formatCents(it.amountCents);
            items.add(it);
        });
        loadList(cb -> FundApi.listLuckyRedpackets("sent", 50, 0, cb), (o, items) -> {
            Item it = base(TYPE_REDPACKET);
            it.amountCents = o.optLong("total_amount_cents", 0);
            it.timeMillis = parseTime(o.optString("created_at", ""));
            int st = o.optInt("status", 0);
            it.income = false;
            it.counted = st != 2;
            it.label = "拼手气红包发出 · " + o.optInt("total_count", 0) + " 份";
            it.detail = "红包号：" + o.optString("packet_id", "") + "\n总金额：" + Money.formatCents(it.amountCents);
            items.add(it);
        });
        // 群/拼手气“收到”：列表只给包总量，本人领取额需逐包拉 claims。
        loadGroupReceived(false);
        loadGroupReceived(true);
        taskDone(); // 总任务收尾（各路回调自行 taskDone）
    }

    private interface ListCall {
        void call(FundApi.Callback cb);
    }

    private interface RowMap {
        void map(JSONObject o, List<Item> out);
    }

    private void loadList(ListCall call, RowMap map) {
        taskStart();
        call.call(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                List<Item> items = new ArrayList<>();
                for (JSONObject o : FundModels.toList(data)) {
                    try {
                        map.map(o, items);
                    } catch (Exception ignored) {
                    }
                }
                synchronized (BillActivity.this) {
                    all.addAll(items);
                }
                taskDone();
            }

            @Override
            public void onErr(String code, String message) {
                taskDone();
            }
        });
    }

    private void loadGroupReceived(final boolean lucky) {
        taskStart();
        FundApi.Callback listCb = new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                List<JSONObject> packets = FundModels.toList(data);
                if (packets.isEmpty()) {
                    taskDone();
                    return;
                }
                for (JSONObject pkt : packets) {
                    final String packetId = pkt.optString("packet_id", "");
                    if (packetId.isEmpty()) continue;
                    taskStart();
                    FundApi.Callback claimsCb = new FundApi.Callback() {
                        @Override
                        public void onOk(Object d2) {
                            for (JSONObject c : FundModels.toList(d2)) {
                                if (myId.equals(c.optString("claimer_id", ""))) {
                                    Item it = base(TYPE_REDPACKET);
                                    it.amountCents = c.optLong("amount_cents", 0);
                                    it.timeMillis = parseTime(c.optString("claimed_at", ""));
                                    if (it.timeMillis == 0) {
                                        it.timeMillis = parseTime(pkt.optString("created_at", ""));
                                    }
                                    it.income = true;
                                    it.counted = true;
                                    it.label = (lucky ? "拼手气红包收到" : "群红包收到");
                                    it.detail = "红包号：" + packetId + "\n领取金额：" + Money.formatCents(it.amountCents);
                                    synchronized (BillActivity.this) {
                                        all.add(it);
                                    }
                                    break;
                                }
                            }
                            taskDone();
                        }

                        @Override
                        public void onErr(String code, String message) {
                            taskDone(); // 领额拉不到：该包不计入汇总，不编造
                        }
                    };
                    if (lucky) {
                        FundApi.getLuckyRedpacketClaims(packetId, claimsCb);
                    } else {
                        FundApi.getGroupRedpacketClaims(packetId, claimsCb);
                    }
                }
                taskDone();
            }

            @Override
            public void onErr(String code, String message) {
                taskDone();
            }
        };
        if (lucky) {
            FundApi.listLuckyRedpackets("received", 50, 0, listCb);
        } else {
            FundApi.listGroupRedpackets("received", 50, 0, listCb);
        }
    }

    private Item base(int type) {
        Item it = new Item();
        it.type = type;
        return it;
    }

    // ---------------- 筛选与汇总 ----------------

    private void applyFilters() {
        view.tvMonth.setText(selYear + "年" + (selMonth + 1) + "月");
        long income = 0;
        long expense = 0;
        for (Item it : all) {
            if (!inMonth(it.timeMillis)) continue;
            if (it.counted) {
                if (it.income) income += it.amountCents;
                else expense += it.amountCents;
            }
        }
        view.tvSummary.setText("本月收入 " + Money.formatCents(income)
            + " · 本月支出 " + Money.formatCents(expense));
        shown.clear();
        lines.clear();
        long winStart = windowStart();
        for (Item it : all) {
            if (!inMonth(it.timeMillis)) continue;
            if (winStart > 0 && it.timeMillis < winStart) continue;
            if (!typeMatch(it)) continue;
            shown.add(it);
        }
        for (Item it : shown) {
            lines.add((it.income ? "＋" : "－") + Money.formatCents(it.amountCents)
                + " · " + it.label + " · " + formatDay(it.timeMillis));
        }
        view.tvCount.setText("共 " + shown.size() + " 笔");
        view.tvEmpty.setVisibility(shown.isEmpty() ? View.VISIBLE : View.GONE);
        adapter.notifyDataSetChanged();
    }

    private boolean inMonth(long t) {
        if (t <= 0) return false;
        Calendar c = Calendar.getInstance();
        c.setTimeInMillis(t);
        return c.get(Calendar.YEAR) == selYear && c.get(Calendar.MONTH) == selMonth;
    }

    private boolean typeMatch(Item it) {
        switch (typeFilter) {
            case TYPE_RECHARGE:
                return it.type == TYPE_RECHARGE;
            case TYPE_WITHDRAW:
                return it.type == TYPE_WITHDRAW;
            case TYPE_TRANSFER:
                return it.type == TYPE_TRANSFER;
            case TYPE_REDPACKET:
                return it.type == TYPE_REDPACKET;
            case TYPE_INCOME:
                return it.income;
            default:
                return true;
        }
    }

    /** 时间窗起点（毫秒）；全部返回 0。今天=当日0点，本周=周一0点，本月=当月1日，近3个月=90天前。 */
    private long windowStart() {
        Calendar c = Calendar.getInstance();
        c.set(Calendar.HOUR_OF_DAY, 0);
        c.set(Calendar.MINUTE, 0);
        c.set(Calendar.SECOND, 0);
        c.set(Calendar.MILLISECOND, 0);
        switch (timeFilter) {
            case TIME_TODAY:
                return c.getTimeInMillis();
            case TIME_WEEK: {
                int dow = c.get(Calendar.DAY_OF_WEEK);
                int shift = (dow == Calendar.SUNDAY) ? 6 : dow - Calendar.MONDAY;
                c.add(Calendar.DAY_OF_MONTH, -shift);
                return c.getTimeInMillis();
            }
            case TIME_MONTH:
                c.set(Calendar.DAY_OF_MONTH, 1);
                return c.getTimeInMillis();
            case TIME_QUARTER:
                return System.currentTimeMillis() - 90L * 24 * 60 * 60 * 1000;
            default:
                return 0;
        }
    }

    private String formatDay(long t) {
        if (t <= 0) return "";
        return new SimpleDateFormat("MM-dd HH:mm", Locale.getDefault()).format(new java.util.Date(t));
    }

    /** 服务端 Go time.Time 序列化为 RFC3339（可能带纳秒）；先去小数秒再解析。 */
    private long parseTime(String s) {
        if (s == null || s.isEmpty() || "null".equals(s)) return 0;
        String v = s.replaceAll("\\.\\d+", "");
        String[] patterns = {"yyyy-MM-dd'T'HH:mm:ssX", "yyyy-MM-dd HH:mm:ss", "yyyy-MM-dd'T'HH:mm:ss"};
        for (String pat : patterns) {
            try {
                SimpleDateFormat f = new SimpleDateFormat(pat, Locale.US);
                f.setTimeZone(TimeZone.getTimeZone("UTC"));
                return f.parse(v).getTime();
            } catch (ParseException ignored) {
            }
        }
        return 0;
    }
}
