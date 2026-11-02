package io.openim.android.demo.fund;

import android.app.AlertDialog;
import android.content.Intent;
import android.os.Bundle;
import android.text.Editable;
import android.text.TextWatcher;
import android.widget.ArrayAdapter;

import org.json.JSONObject;

import java.util.ArrayList;
import java.util.List;

import io.openim.android.demo.databinding.ActivityFundWithdrawBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/** 提现页：方式选择 + 提交（交易密码，6 格弹窗）+ 记录。试算按钮已按用户要求去掉，手续费与实际到账以提交后服务端返回为准。 */
public class WithdrawActivity extends BaseActivity<BaseViewModel, ActivityFundWithdrawBinding> {

    private final List<JSONObject> methods = new ArrayList<>();
    private final List<String> methodLines = new ArrayList<>();
    private ArrayAdapter<String> adapter;

    // 可用余额：GET /v1/wallet 的 balance_cents（不含 frozen_cents）。
    private long availableCents = 0;
    private boolean balanceLoaded = false;
    private boolean balanceFailed = false;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundWithdrawBinding.inflate(getLayoutInflater()));
        sink();
        adapter = new ArrayAdapter<>(this, android.R.layout.simple_spinner_item, methodLines);
        adapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);
        view.spMethod.setAdapter(adapter);
        view.btnSubmit.setOnClickListener(v -> submit());
        FundUi.pressScale(view.btnSubmit, view.btnRecords);
        view.btnRecords.setOnClickListener(v -> {
            Intent intent = new Intent(this, FundListActivity.class);
            intent.putExtra(FundListActivity.EXTRA_MODE, FundListActivity.MODE_WITHDRAWALS);
            startActivity(intent);
        });
        loadMethods();
        loadBalance();
        view.etAmount.addTextChangedListener(new TextWatcher() {
            @Override public void beforeTextChanged(CharSequence s, int start, int count, int after) {}
            @Override public void onTextChanged(CharSequence s, int start, int before, int count) {}
            @Override public void afterTextChanged(Editable s) { updateBalanceState(); }
        });
    }

    private void loadMethods() {
        showWaiting();
        FundApi.getWithdrawMethods(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                cancelWaiting();
                methods.clear();
                methodLines.clear();
                for (JSONObject m : FundModels.toList(data)) {
                    methods.add(m);
                    methodLines.add(FundModels.methodLine(m));
                }
                adapter.notifyDataSetChanged();
                if (methods.isEmpty()) {
                    toast("请先绑定提现方式");
                }
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message);
            }
        });
    }

    /** 加载可用余额（静默加载，不与 loadMethods 的等待框抢占）。 */
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

    /** 余额文案 + 超额时提交按钮置灰（实时提示，提交时的 submit() 校验仍是最终关口）。 */
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
            base += "\n提现金额不能超过目前余额";
        }
        view.tvBalance.setText(base);
        view.btnSubmit.setEnabled(!over);
    }

    private void submit() {
        if (methods.isEmpty() || view.spMethod.getSelectedItemPosition() < 0) {
            toast("请先绑定提现方式");
            return;
        }
        final long cents = Money.parseYuanToCents(view.etAmount.getText().toString());
        if (cents <= 0) {
            toast("请输入有效金额（元，最多 2 位小数）");
            return;
        }
        // 超额拦截：必须在交易密码弹窗之前，不发提现请求。
        // 注意：服务端最终按“金额+手续费”对可用余额兜底，见交付说明。
        if (!balanceLoaded) {
            toast("余额未加载，请稍后重试");
            return;
        }
        if (cents > availableCents) {
            toast("提现金额不能超过目前余额");
            return;
        }
        final String methodId = methods.get(view.spMethod.getSelectedItemPosition())
            .optString("method_id", "");
        if (methodId.isEmpty()) {
            toast("提现方式无效，请重新选择");
            return;
        }
        // 提交前先查准入：未完成只引导、不建单、不扣款。
        // 交易密码弹窗与发红包同款：6 格、输满自动提交、标题“输入交易密码”；
        // 密码错时弹窗不关、红字显示服务端文案、清空重输（核验中状态由弹窗自身展示，不另弹等待框）。
        FundGate.check(this, () -> TxPasswordDialog.showForVerification(this, "输入交易密码",
            (password, handle) -> {
            if (password == null || password.length() != 6) {
                handle.fail("请输入 6 位交易密码");
                return;
            }
            try {
                FundApi.createWithdrawal(methodId, cents, password, new FundApi.Callback() {
                    @Override
                    public void onOk(Object data) {
                        JSONObject o = FundModels.toObj(data);
                        if (o == null) {
                            handle.complete();
                            return;
                        }
                        handle.complete();
                        view.tvResult.setText("提现已提交\n订单号：" + o.optString("order_id", "")
                            + "\n金额：" + Money.formatCents(o.optLong("amount_cents", 0))
                            + "\n手续费：" + Money.formatCents(o.optLong("fee_cents", 0))
                            + "\n实际到账：" + Money.formatCents(o.optLong("actual_cents", 0))
                            + "\n状态：" + FundModels.withdrawStatusName(o.optInt("status", 0)));
                        loadBalance();
                        FundUi.showSuccess(WithdrawActivity.this, "提现已提交，等待审核", null);
                    }

                    @Override
                    public void onErr(String code, String message) {
                        handle.fail(message);
                    }
                });
            } catch (Exception e) {
                handle.fail("提交失败，请重试");
            }
        }));
    }
}
