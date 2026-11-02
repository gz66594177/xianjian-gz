package io.openim.android.demo.fund;

import android.content.Intent;
import android.os.Bundle;

import io.openim.android.demo.databinding.ActivityFundSecurityBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/** 资金安全入口：银行卡 / 交易密码 / 提现账户（准入展示行已删，守卫逻辑不动）。 */
public class FundSecurityActivity extends BaseActivity<BaseViewModel, ActivityFundSecurityBinding> {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundSecurityBinding.inflate(getLayoutInflater()));
        sink();
        // 资金安全页只剩三类入口；准入展示行删除，但 FundGate 服务端守卫与
        // AdmissionActivity 引导流程是业务防线，保持不动（业务入口仍由守卫拦截）。
        view.rowTxPassword.setOnClickListener(v ->
            startActivity(new Intent(this, TxPasswordActivity.class)));
        view.rowBankCard.setOnClickListener(v ->
            startActivity(new Intent(this, WithdrawMethodsActivity.class)));
        view.rowWithdrawAccount.setOnClickListener(v ->
            startActivity(new Intent(this, WithdrawMethodsActivity.class)));
    }
}
