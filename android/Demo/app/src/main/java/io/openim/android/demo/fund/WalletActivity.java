package io.openim.android.demo.fund;

import android.content.Intent;
import android.os.Bundle;

import org.json.JSONObject;

import io.openim.android.demo.databinding.ActivityFundWalletBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/** 钱包主页：余额与流水入口。金额以服务端返回为准，不做余额推导。 */
public class WalletActivity extends BaseActivity<BaseViewModel, ActivityFundWalletBinding> {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundWalletBinding.inflate(getLayoutInflater()));
        sink();
        view.btnRecharge.setOnClickListener(v ->
            startActivity(new Intent(this, RechargeActivity.class)));
        view.btnWithdraw.setOnClickListener(v ->
            startActivity(new Intent(this, WithdrawActivity.class)));
        // 钱包页只留余额 + 充值/提现 + 账单入口行；账单为独立页，不再摊开在钱包页。
        view.billRow.setOnClickListener(v ->
            startActivity(new Intent(this, BillActivity.class)));
        FundUi.pressScale(view.btnRecharge, view.btnWithdraw, view.billRow);
        load();
    }

    private void load() {
        showWaiting();
        FundApi.getWallet(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                JSONObject w = FundModels.toObj(data);
                if (w == null) {
                    cancelWaiting();
                    view.tvAvailable.setText("—");
                    view.tvBalances.setText("钱包信息解析失败");
                    return;
                }
                // 钱包卡：大额只放可用余额，不再与标题重复；冻结与状态放第二行。
                // 状态取服务端原值原样显示（FundModels 未提供钱包状态中文映射，不编造）。
                view.tvAvailable.setText(Money.formatCents(w.optLong("balance_cents", 0)));
                view.tvBalances.setText("冻结余额：" + Money.formatCents(w.optLong("frozen_cents", 0))
                    + "\n钱包状态：" + w.optString("status", ""));
                cancelWaiting();
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                if ("WALLET_NOT_FOUND".equals(code) || "NOT_FOUND".equals(code)) {
                    createWallet();
                } else {
                    toast(message);
                    view.tvBalances.setText("加载失败：" + message);
                }
            }
        });
    }

    /** 钱包不存在时自动创建（幂等），再重新加载。 */
    private void createWallet() {
        FundApi.createWallet(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                load();
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message);
            }
        });
    }

}
