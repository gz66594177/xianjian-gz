package io.openim.android.demo.fund;

import android.content.Intent;
import android.os.Bundle;

import io.openim.android.demo.databinding.ActivityFundAdmissionBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/**
 * 资金准入引导页：先绑方式，后设密码；未完成时资金功能入口只引导、不进业务。
 * 准入状态以服务端 GET /v1/finance/status 为准。
 */
public class AdmissionActivity extends BaseActivity<BaseViewModel, ActivityFundAdmissionBinding> {

    public static final String EXTRA_NEXT_STEP = "next_step";
    public static final String EXTRA_GUIDANCE = "guidance";
    public static final String EXTRA_HAS_METHOD = "has_method";
    public static final String EXTRA_HAS_PASSWORD = "has_password";

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundAdmissionBinding.inflate(getLayoutInflater()));
        sink();
        view.btnBindMethod.setOnClickListener(v ->
            startActivity(new Intent(this, WithdrawMethodsActivity.class)));
        view.btnSetPassword.setOnClickListener(v ->
            startActivity(new Intent(this, TxPasswordActivity.class)));
        view.btnRefresh.setOnClickListener(v -> recheck());
        FundUi.pressScale(view.btnBindMethod, view.btnSetPassword, view.btnRefresh);
        renderFromIntent();
    }

    @Override
    protected void onResume() {
        super.onResume();
        recheckSilently();
    }

    private void renderFromIntent() {
        boolean hasMethod = getIntent().getBooleanExtra(EXTRA_HAS_METHOD, false);
        boolean hasPassword = getIntent().getBooleanExtra(EXTRA_HAS_PASSWORD, false);
        String guidance = getIntent().getStringExtra(EXTRA_GUIDANCE);
        render(hasMethod, hasPassword, guidance);
    }

    private void render(boolean hasMethod, boolean hasPassword, String guidance) {
        StringBuilder sb = new StringBuilder();
        sb.append("1. 绑定提现方式：").append(hasMethod ? "已完成" : "未完成").append("\n");
        sb.append("2. 设置交易密码：").append(hasPassword ? "已完成" : "未完成").append("\n");
        if (guidance != null && !guidance.isEmpty()) {
            sb.append("\n").append(guidance);
        }
        view.tvGuidance.setText(sb.toString());
        view.btnBindMethod.setEnabled(!hasMethod);
        view.btnSetPassword.setEnabled(hasMethod && !hasPassword);
    }

    private void recheck() {
        showWaiting();
        FundApi.getFinanceStatus(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                cancelWaiting();
                FundModels.FinanceStatus s =
                    FundModels.FinanceStatus.parse(FundModels.toObj(data));
                if (s.isOk()) {
                    toast("准入已完成，可以使用资金功能");
                    finish();
                } else {
                    render(s.hasMethod, s.hasPassword, s.guidance);
                }
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message);
            }
        });
    }

    private void recheckSilently() {
        FundApi.getFinanceStatus(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                FundModels.FinanceStatus s =
                    FundModels.FinanceStatus.parse(FundModels.toObj(data));
                if (s.isOk()) {
                    finish();
                } else {
                    render(s.hasMethod, s.hasPassword, s.guidance);
                }
            }

            @Override
            public void onErr(String code, String message) {
            }
        });
    }
}
