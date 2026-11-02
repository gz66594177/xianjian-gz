package io.openim.android.demo.fund;

import android.app.AlertDialog;
import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.text.Editable;
import android.text.TextWatcher;
import android.view.Gravity;
import android.view.View;
import android.widget.AdapterView;
import android.widget.ArrayAdapter;
import android.widget.Button;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import org.json.JSONObject;

import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.ArrayList;
import java.util.List;

import io.openim.android.demo.databinding.ActivityFundRechargeBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/**
 * 充值页 A/B 双通道（0010 后端接口，金额计算与旧接口签名不动）。
 *
 * <p>A 人工收款（一步一步弹窗引导，不提前全显示）：第 1 步页面只显示通道卡、
 * 收款方式、金额与“去付款”；第 2 步点“去付款”弹窗出收款二维码（下方
 * “已付款”/“取消付款”，带入场与二维码缩放动画）；点“已付款”才创建手动单
 * （POST /v1/recharge/manual，状态 10 待上传凭证），点“取消付款”不建单；
 * 第 3 步建单成功弹窗提示，再以滑入动画展开上传截图区；第 4 步提交截图
 * （POST /v1/recharge/&lt;id&gt;/proof）后成功弹窗，再展开订单结果与刷新/记录入口。
 * 截图区、订单结果、刷新、充值记录在对应步骤到达前一律隐藏。
 * 建单与传凭证不设准入/交易密码门禁（与 0010 服务端口径一致）。</p>
 *
 * <p>B 支付网关：输入金额 -> 创建网关单（POST /v1/recharge/gateway）-> 服务端
 * 返回 cashier_url 则外跳支付页；未返回则明示“链接待后台返回”，不编假地址。
 * 回到当前页时弹窗问“是否已经成功付款？”：点“是”提示“请等待支付结果返回
 * 即可到账！”并只做只读订单查询；点“付款遇到问题？”跳客服——App 内目前
 * 没有客服目的地（工作台 applet 亦明确不自编客服入口），此处以提示弹窗占位，
 * 并在交付里单列为待对接缺口，不编造一个客服页冒充已接通。
 * 生产未注册厂商时建单回 400 GATEWAY_NOT_CONFIGURED，自动切回人工收款。</p>
 *
 * <p>绝不伪造成功页：一切到账状态以服务端查询/回调为准。</p>
 *
 * <p>通道可见性（后台开关为准）：进页先调 GET /v1/recharge/channels——人工卡只在
 * manual_available 为真时显示（人工方式列表服务端已只返启用项，App 端再按
 * enabled 字段过滤一次兜底），网关卡只在 gateway_available 为真时显示（后台
 * 启用行 + 服务端已注册适配器，任一不满足即关闭，绝不出现假收银台）；两个
 * 通道都没开启时不显示任何通道入口与金额/建单区，只显示一行“目前暂无可使用
 * 充值通道，请联系客服获取！”（纯文案，不编客服联系方式与跳转）。新接口失败
 * 时回落旧 GET /v1/recharge/methods（人工仍按 enabled 过滤），网关 fail-closed
 * 隐藏；老生产服务端无新接口时即走此回落，网关在该次加载中隐藏、刷新后恢复。</p>
 */
public class RechargeActivity extends BaseActivity<BaseViewModel, ActivityFundRechargeBinding> {

    private static final int CHANNEL_MANUAL = 0;
    private static final int CHANNEL_GATEWAY = 1;
    private static final int REQ_PICK_PROOF = 1001;

    private int channel = CHANNEL_MANUAL;
    private boolean channelsLoaded;
    private boolean manualAvailable;
    private boolean gatewayAvailable;
    private final List<RechargePayee> payees = new ArrayList<>();
    private final List<String> payeeLines = new ArrayList<>();
    private ArrayAdapter<String> payeeAdapter;
    private String currentQrUrl;
    private String manualOrderId;
    private String gatewayOrderId;
    private boolean pendingGatewayReturn;
    private File proofFile;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundRechargeBinding.inflate(getLayoutInflater()));
        sink();
        Button[] ds = new Button[]{view.d1, view.d2, view.d3, view.d4,
            view.d5, view.d6, view.d7, view.d8};
        for (int i = 0; i < ds.length; i++) {
            final long cents = FundConfig.RECHARGE_DENOMINATIONS_CENTS[i];
            final Button chip = ds[i];
            chip.setOnClickListener(v -> {
                for (Button b : ds) {
                    b.setSelected(b == chip);
                }
                FundUi.pop(chip);
                view.etAmount.setText(Money.formatCents(cents).substring(1));
            });
        }
        view.etAmount.addTextChangedListener(new TextWatcher() {
            @Override
            public void beforeTextChanged(CharSequence s, int start, int count, int after) {
            }

            @Override
            public void onTextChanged(CharSequence s, int start, int before, int count) {
            }

            @Override
            public void afterTextChanged(Editable s) {
                updateQrVisibility();
            }
        });
        payeeAdapter = new ArrayAdapter<>(this,
            android.R.layout.simple_spinner_item, payeeLines);
        payeeAdapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);
        view.spPayee.setAdapter(payeeAdapter);
        view.spPayee.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener() {
            @Override
            public void onItemSelected(AdapterView<?> parent, View v, int position, long id) {
                updateQrVisibility();
            }

            @Override
            public void onNothingSelected(AdapterView<?> parent) {
            }
        });
        view.cardManual.setOnClickListener(v -> selectChannel(CHANNEL_MANUAL));
        view.cardGateway.setOnClickListener(v -> selectChannel(CHANNEL_GATEWAY));
        view.btnCreate.setOnClickListener(v -> create());
        view.btnPickProof.setOnClickListener(v -> pickProof());
        view.btnSubmitProof.setOnClickListener(v -> submitProof());
        view.btnRefreshOrder.setOnClickListener(v -> refreshOrder());
        view.btnRecords.setOnClickListener(v -> {
            Intent intent = new Intent(this, FundListActivity.class);
            intent.putExtra(FundListActivity.EXTRA_MODE, FundListActivity.MODE_RECHARGES);
            startActivity(intent);
        });
        FundUi.pressScale(view.btnCreate, view.btnPickProof, view.btnSubmitProof,
            view.btnRefreshOrder, view.btnRecords);
        view.btnRefreshOrder.setVisibility(View.GONE);
        view.proofGroup.setVisibility(View.GONE);
        view.tvResult.setVisibility(View.GONE);
        view.btnRecords.setVisibility(View.GONE);
        selectChannel(CHANNEL_MANUAL);
        loadChannels();
    }

    @Override
    protected void onResume() {
        super.onResume();
        // 网关外跳支付页回来：只弹窗询问，不擅自判成功、不擅自扣款。
        if (pendingGatewayReturn && gatewayOrderId != null && !gatewayOrderId.isEmpty()) {
            pendingGatewayReturn = false;
            FundUi.showPaidConfirm(this, "订单号：" + gatewayOrderId,
                () -> {
                    toast("请等待支付结果返回即可到账！");
                    refreshOrder();
                },
                () -> FundUi.showServiceNote(this, "付款遇到问题？",
                    "客服入口尚未在 App 内配置（需后台提供客服页地址后接入）。\n订单号："
                        + gatewayOrderId + "\n可凭订单号联系管理端查询。"));
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode != REQ_PICK_PROOF || resultCode != RESULT_OK
            || data == null || data.getData() == null) {
            return;
        }
        try {
            Uri uri = data.getData();
            File out = new File(getCacheDir(),
                "proof_" + (manualOrderId == null ? "tmp" : manualOrderId) + ".jpg");
            try (InputStream in = getContentResolver().openInputStream(uri);
                 OutputStream os = new FileOutputStream(out)) {
                if (in == null) {
                    toast("读取截图失败，请重试");
                    return;
                }
                byte[] buf = new byte[8192];
                int n;
                while ((n = in.read(buf)) != -1) {
                    os.write(buf, 0, n);
                }
            }
            proofFile = out;
            view.tvProofName.setText("已选择截图（约 " + (out.length() / 1024 + 1) + " KB）");
        } catch (Exception e) {
            toast("读取截图失败，请重试");
        }
    }

    // ---------------- 通道与收款方式 ----------------

    private void selectChannel(int next) {
        // 已加载且该通道未开启时忽略切换（隐藏的卡不可点，此处防程序内误切）。
        if (channelsLoaded) {
            if (next == CHANNEL_MANUAL && !manualAvailable) {
                return;
            }
            if (next == CHANNEL_GATEWAY && !gatewayAvailable) {
                return;
            }
        }
        channel = next;
        boolean manual = next == CHANNEL_MANUAL;
        view.cardManual.setSelected(manual);
        view.cardGateway.setSelected(!manual);
        view.manualGroup.setVisibility(manual ? View.VISIBLE : View.GONE);
        view.btnCreate.setText(manual ? "去付款" : "去网关支付");
        if (!manual) {
            view.qrCard.setVisibility(View.GONE);
        }
        updateQrVisibility();
    }

    /** 通道总览优先；新接口失败（老生产 404 / 网络异常）时回落旧列表，网关关闭。 */
    private void loadChannels() {
        FundApi.getRechargeChannels(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                JSONObject o = FundModels.toObj(data);
                if (o == null) {
                    loadMethodsFallback();
                    return;
                }
                payees.clear();
                payeeLines.clear();
                for (JSONObject p : FundModels.toList(o.optJSONArray("manual_methods"))) {
                    RechargePayee payee = RechargePayee.parse(p);
                    if (payee != null && payee.enabled) {
                        payees.add(payee);
                        payeeLines.add(payee.label());
                    }
                }
                payeeAdapter.notifyDataSetChanged();
                // 服务端过滤优先：manual_available 为准，空列表即不可用。
                manualAvailable = o.optBoolean("manual_available", !payees.isEmpty())
                    && !payees.isEmpty();
                gatewayAvailable = o.optBoolean("gateway_available", false);
                channelsLoaded = true;
                applyChannelVisibility();
            }

            @Override
            public void onErr(String code, String message) {
                loadMethodsFallback();
            }
        });
    }

    /** 回落：旧人工列表（仍按 enabled 过滤），网关 fail-closed 隐藏。 */
    private void loadMethodsFallback() {
        FundApi.listRechargeMethods(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                payees.clear();
                payeeLines.clear();
                for (JSONObject o : FundModels.toList(data)) {
                    RechargePayee p = RechargePayee.parse(o);
                    if (p != null && p.enabled) {
                        payees.add(p);
                        payeeLines.add(p.label());
                    }
                }
                payeeAdapter.notifyDataSetChanged();
                manualAvailable = !payees.isEmpty();
                gatewayAvailable = false;
                channelsLoaded = true;
                applyChannelVisibility();
            }

            @Override
            public void onErr(String code, String message) {
                toast(message);
            }
        });
    }

    /** 按后台开关渲染：只显示已开启的通道卡；全关时只留一行提示，不编客服跳转。 */
    private void applyChannelVisibility() {
        boolean any = manualAvailable || gatewayAvailable;
        view.cardManual.setVisibility(manualAvailable ? View.VISIBLE : View.GONE);
        view.cardGateway.setVisibility(gatewayAvailable ? View.VISIBLE : View.GONE);
        view.tvNoChannel.setVisibility(any ? View.GONE : View.VISIBLE);
        view.amountGroup.setVisibility(any ? View.VISIBLE : View.GONE);
        view.btnCreate.setVisibility(any ? View.VISIBLE : View.GONE);
        if (!any) {
            view.manualGroup.setVisibility(View.GONE);
            view.qrCard.setVisibility(View.GONE);
            currentQrUrl = null;
            return;
        }
        // 当前通道若已关闭，自动切到另一已开启通道。
        if (channel == CHANNEL_MANUAL && !manualAvailable) {
            selectChannel(CHANNEL_GATEWAY);
        } else if (channel == CHANNEL_GATEWAY && !gatewayAvailable) {
            selectChannel(CHANNEL_MANUAL);
        } else {
            selectChannel(channel);
        }
        updateQrVisibility();
    }

    private RechargePayee selectedPayee() {
        int pos = view.spPayee.getSelectedItemPosition();
        if (pos < 0 || pos >= payees.size()) {
            return null;
        }
        return payees.get(pos);
    }

    /** 内联二维码已改为弹窗：页面上不再常驻二维码卡，金额/方式变化只清旧状态。
     * 二维码只在点“去付款”后以弹窗出现，下方“已付款”/“取消付款”。 */
    private void updateQrVisibility() {
        view.qrCard.setVisibility(View.GONE);
        currentQrUrl = null;
    }

    // ---------------- 建单 ----------------

    private void create() {
        if (channelsLoaded && !manualAvailable && !gatewayAvailable) {
            toast("目前暂无可使用充值通道，请联系客服获取！");
            return;
        }
        long cents = Money.parseYuanToCents(view.etAmount.getText().toString());
        if (cents <= 0) {
            toast("请输入有效金额（元，最多 2 位小数）");
            return;
        }
        if (channel == CHANNEL_MANUAL) {
            showQrPaymentDialog(cents);
        } else {
            createGateway(cents);
        }
    }

    /** 下一步展开动画：从下方 24dp 滑入 + 淡入 300ms，用于截图区/结果/刷新/记录。 */
    private void animateReveal(View v) {
        if (v == null) {
            return;
        }
        v.setVisibility(View.VISIBLE);
        v.setAlpha(0f);
        v.setTranslationY(24 * getResources().getDisplayMetrics().density);
        v.animate().alpha(1f).translationY(0f).setDuration(300).start();
    }

    /** 付款二维码弹窗：下方“已付款”才正式建单，“取消付款”只关弹窗、不建单。
     * 二维码加载成功前“已付款”置灰（没看到码就点已付款无意义）；加载失败则保持置灰。 */
    /** 付款二维码弹窗：按钮做到弹窗内容里（已付款主按钮 + 取消付款幽灵按钮），
     * 不再依赖 AlertDialog 系统按钮栏——此前系统按钮栏在部分主题下不可见，
     * 用户只能看到二维码下方大片空白、找不到已付款/取消付款。
     * 二维码加载成功前“已付款”置灰（没看到码就点已付款无意义）；加载失败则保持置灰。 */
    private void showQrPaymentDialog(final long cents) {
        final RechargePayee p = selectedPayee();
        if (p == null) {
            toast("请先选择后台收款方式");
            return;
        }
        if (!p.hasQr()) {
            toast("该收款方式暂未上传收款二维码，无法付款");
            return;
        }
        float density = getResources().getDisplayMetrics().density;
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setGravity(Gravity.CENTER_HORIZONTAL);
        int pad = (int) (24 * density);
        root.setPadding(pad, pad, pad, pad);
        TextView tvPayee = new TextView(this);
        tvPayee.setText(p.label());
        tvPayee.setGravity(Gravity.CENTER);
        TextView tvAmount = new TextView(this);
        tvAmount.setText("充值金额：" + Money.formatCents(cents));
        tvAmount.setGravity(Gravity.CENTER);
        final TextView tvHint = new TextView(this);
        tvHint.setText("二维码加载中…");
        tvHint.setGravity(Gravity.CENTER);
        final ImageView iv = new ImageView(this);
        int size = (int) (240 * density);
        iv.setLayoutParams(new LinearLayout.LayoutParams(size, size));
        root.addView(tvPayee);
        root.addView(tvAmount);
        root.addView(iv);
        root.addView(tvHint);
        int btnH = (int) (52 * density);
        final android.widget.Button btnPaid = new android.widget.Button(this);
        btnPaid.setText("已付款");
        btnPaid.setEnabled(false);
        btnPaid.setAlpha(0.5f);
        btnPaid.setTextColor(android.graphics.Color.WHITE);
        btnPaid.setTextSize(16);
        int paidBg = getResources().getIdentifier("shape_btn_primary", "drawable", getPackageName());
        if (paidBg != 0) btnPaid.setBackgroundResource(paidBg);
        LinearLayout.LayoutParams lpPaid = new LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, btnH);
        lpPaid.topMargin = (int) (20 * density);
        root.addView(btnPaid, lpPaid);
        final android.widget.Button btnCancel = new android.widget.Button(this);
        btnCancel.setText("取消付款");
        int cancelBg = getResources().getIdentifier("shape_btn_secondary", "drawable", getPackageName());
        if (cancelBg != 0) btnCancel.setBackgroundResource(cancelBg);
        LinearLayout.LayoutParams lpCancel = new LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, btnH);
        lpCancel.topMargin = (int) (12 * density);
        root.addView(btnCancel, lpCancel);
        final AlertDialog dialog = FundUi.dialog(this)
            .setTitle("请按此码线下付款")
            .setView(root)
            .setCancelable(true)
            .create();
        btnPaid.setOnClickListener(v -> {
            dialog.dismiss();
            createManual(cents);
        });
        btnCancel.setOnClickListener(v -> dialog.dismiss());
        dialog.show();
        root.setAlpha(0f);
        root.setScaleX(0.92f);
        root.setScaleY(0.92f);
        root.animate().alpha(1f).scaleX(1f).scaleY(1f).setDuration(250).start();
        FundApi.loadAuthedImage(p.qrUrl, new FundApi.ImageCallback() {
            @Override
            public void onBitmap(android.graphics.Bitmap bitmap) {
                iv.setImageBitmap(bitmap);
                iv.setScaleX(0.8f);
                iv.setScaleY(0.8f);
                iv.setAlpha(0f);
                iv.animate().scaleX(1f).scaleY(1f).alpha(1f).setDuration(300).start();
                tvHint.setText(p.label() + " · 请按此码线下付款，付完点已付款");
                btnPaid.setEnabled(true);
                btnPaid.setAlpha(1f);
            }

            @Override
            public void onErr() {
                tvHint.setText("收款二维码加载失败，请检查网络后重新打开");
            }
        });
    }

    private void createManual(final long cents) {
        final RechargePayee p = selectedPayee();
        if (p == null) {
            toast("请先选择后台收款方式");
            return;
        }
        if (!p.hasQr()) {
            toast("该收款方式暂未上传收款二维码，无法建单");
            return;
        }
        showWaiting();
        try {
            FundApi.createManualRecharge(p.methodId, cents, new FundApi.Callback() {
                @Override
                public void onOk(Object data) {
                    cancelWaiting();
                    JSONObject o = FundModels.toObj(data);
                    if (o == null) {
                        return;
                    }
                    manualOrderId = o.optString("order_id", "");
                    proofFile = null;
                    view.tvProofName.setText("");
                    view.tvResult.setText("订单已创建（待上传凭证）\n订单号：" + manualOrderId
                        + "\n金额：" + Money.formatCents(o.optLong("amount_cents", 0))
                        + "\n收款方式：" + p.label()
                        + "\n请上传成功支付截图，等待审核。");
                    // 第 3 步先弹窗提示，再展开截图区与订单结果（刷新/记录留到提交后）。
                    FundUi.dialog(RechargeActivity.this)
                        .setTitle("订单已创建")
                        .setMessage("订单号：" + manualOrderId + "\n金额："
                            + Money.formatCents(o.optLong("amount_cents", 0))
                            + "\n下一步：请上传成功支付截图，提交后等待审核。")
                        .setPositiveButton("上传支付截图", (d, w) -> {
                            animateReveal(view.proofGroup);
                            animateReveal(view.tvResult);
                        })
                        .setCancelable(false)
                        .show();
                }

                @Override
                public void onErr(String code, String message) {
                    cancelWaiting();
                    toast(message);
                }
            });
        } catch (Exception e) {
            cancelWaiting();
            toast("创建失败，请重试");
        }
    }

    private void createGateway(final long cents) {
        showWaiting();
        try {
            FundApi.createGatewayRecharge(cents, new FundApi.Callback() {
                @Override
                public void onOk(Object data) {
                    cancelWaiting();
                    JSONObject o = FundModels.toObj(data);
                    if (o == null) {
                        return;
                    }
                    gatewayOrderId = o.optString("order_id", "");
                    String cashier = o.isNull("cashier_url")
                        ? "" : o.optString("cashier_url", "");
                    animateReveal(view.tvResult);
                    animateReveal(view.btnRefreshOrder);
                    animateReveal(view.btnRecords);
                    if (cashier.isEmpty()) {
                        view.tvResult.setText("网关订单已创建\n订单号：" + gatewayOrderId
                            + "\n金额：" + Money.formatCents(o.optLong("amount_cents", 0))
                            + "\n后台未返回支付链接，请稍后刷新订单状态；不要重复建单。");
                        return;
                    }
                    view.tvResult.setText("网关订单已创建，正在跳转支付页…\n订单号：" + gatewayOrderId
                        + "\n金额：" + Money.formatCents(o.optLong("amount_cents", 0)));
                    pendingGatewayReturn = true;
                    try {
                        startActivity(new Intent(Intent.ACTION_VIEW, Uri.parse(cashier)));
                    } catch (ActivityNotFoundException e) {
                        pendingGatewayReturn = false;
                        toast("无法打开支付页，请检查浏览器后刷新订单状态");
                    }
                }

                @Override
                public void onErr(String code, String message) {
                    cancelWaiting();
                    if ("GATEWAY_NOT_CONFIGURED".equals(code)) {
                        // 建单时才发现网关未开通：本页网关卡即刻关闭并按开关重渲染；
                        // 人工若也未开启，则落入全关提示，不再强行切回人工卡。
                        gatewayAvailable = false;
                        applyChannelVisibility();
                        if (manualAvailable) {
                            toast("网关支付暂未开通，已切回人工收款");
                        } else {
                            toast("目前暂无可使用充值通道，请联系客服获取！");
                        }
                        return;
                    }
                    toast(message);
                }
            });
        } catch (Exception e) {
            cancelWaiting();
            toast("创建失败，请重试");
        }
    }

    // ---------------- 截图与只读查询 ----------------

    private void pickProof() {
        if (manualOrderId == null || manualOrderId.isEmpty()) {
            toast("请先创建手动充值订单");
            return;
        }
        Intent intent = new Intent(Intent.ACTION_GET_CONTENT);
        intent.setType("image/*");
        intent.addCategory(Intent.CATEGORY_OPENABLE);
        try {
            startActivityForResult(Intent.createChooser(intent, "选择支付截图"), REQ_PICK_PROOF);
        } catch (ActivityNotFoundException e) {
            toast("无法打开图片选择器");
        }
    }

    private void submitProof() {
        if (manualOrderId == null || manualOrderId.isEmpty()) {
            toast("请先创建手动充值订单");
            return;
        }
        if (proofFile == null || !proofFile.exists()) {
            toast("请先选择支付截图");
            return;
        }
        showWaiting();
        FundApi.uploadRechargeProof(manualOrderId, proofFile, new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                cancelWaiting();
                JSONObject o = FundModels.toObj(data);
                if (o == null) {
                    return;
                }
                view.tvResult.setText("截图已提交，等待审核\n订单号：" + manualOrderId
                    + "\n金额：" + Money.formatCents(o.optLong("amount_cents", 0))
                    + "\n状态：" + unifiedStatusName(o.optString("recharge_kind", "manual"),
                        o.optInt("status", 11))
                    + "\n管理员双人复核通过后才入账，上传截图不等于已到账。");
                FundUi.showSuccess(RechargeActivity.this, "已提交，等待审核", () -> {
                    animateReveal(view.btnRefreshOrder);
                    animateReveal(view.btnRecords);
                });
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message);
            }
        });
    }

    private void refreshOrder() {
        final String orderId = manualOrderId != null && !manualOrderId.isEmpty()
            ? manualOrderId : gatewayOrderId;
        if (orderId == null || orderId.isEmpty()) {
            return;
        }
        showWaiting();
        FundApi.getRechargeOrder(orderId, new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                cancelWaiting();
                JSONObject o = FundModels.toObj(data);
                if (o == null) {
                    return;
                }
                String kind = o.optString("recharge_kind", "");
                String status = unifiedStatusName(kind, o.optInt("status", 0));
                if (o.optBoolean("expired", false)) {
                    status += "（已过期）";
                }
                view.tvResult.setText("订单号：" + orderId
                    + "\n金额：" + Money.formatCents(o.optLong("amount_cents", 0))
                    + "\n渠道：" + o.optString("channel", "")
                    + "\n状态：" + status
                    + "\n状态以服务端查询为准；人工单须双人复核通过才入账，网关单以回调为准。");
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message);
            }
        });
    }

    /** 统一订单状态文案（0010）：手动 10/11/12/1，网关 0/1，其余回落回调单文案。 */
    private String unifiedStatusName(String kind, int status) {
        if ("manual".equals(kind)) {
            switch (status) {
                case 10:
                    return "待上传凭证";
                case 11:
                    return "待审核";
                case 12:
                    return "已拒绝";
                case 1:
                    return "已入账";
                default:
                    return "未知状态";
            }
        }
        if ("gateway".equals(kind)) {
            switch (status) {
                case 0:
                    return "待支付";
                case 1:
                    return "已支付";
                default:
                    return FundModels.rechargeStatusName(status);
            }
        }
        return FundModels.rechargeStatusName(status);
    }
}
