package io.openim.android.demo.fund;

import android.app.Activity;
import android.content.Intent;

/**
 * 资金准入引导门（客户端侧前置检查；服务端 requireAdmission 仍是最终守卫）。
 * 准入未完成时只引导、不进业务、不扣款；准入状态一律以服务端 GET /v1/finance/status 为准。
 *
 * 注意：资金服务的状态接口要求钱包已存在；新用户首次进资金功能时服务端回
 * WALLET_NOT_FOUND，此时先调创建钱包（幂等）再复查一次，而不是直接报错。
 */
public final class FundGate {

    private FundGate() {
    }

    /** 检查准入：ok 则执行 onOk，否则打开准入引导页。 */
    public static void check(final Activity activity, final Runnable onOk) {
        check(activity, onOk, false);
    }

    private static void check(final Activity activity, final Runnable onOk, final boolean retried) {
        FundApi.getFinanceStatus(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                FundModels.FinanceStatus status =
                    FundModels.FinanceStatus.parse(FundModels.toObj(data));
                if (status.isOk()) {
                    if (onOk != null) {
                        onOk.run();
                    }
                } else {
                    Intent intent = new Intent(activity, AdmissionActivity.class);
                    intent.putExtra(AdmissionActivity.EXTRA_NEXT_STEP, status.nextStep);
                    intent.putExtra(AdmissionActivity.EXTRA_GUIDANCE, status.guidance);
                    intent.putExtra(AdmissionActivity.EXTRA_HAS_METHOD, status.hasMethod);
                    intent.putExtra(AdmissionActivity.EXTRA_HAS_PASSWORD, status.hasPassword);
                    activity.startActivity(intent);
                }
            }

            @Override
            public void onErr(String code, String message) {
                if (("WALLET_NOT_FOUND".equals(code) || "NOT_FOUND".equals(code)) && !retried) {
                    FundApi.createWallet(new FundApi.Callback() {
                        @Override
                        public void onOk(Object data) {
                            check(activity, onOk, true);
                        }

                        @Override
                        public void onErr(String c2, String m2) {
                            toast(activity, m2);
                        }
                    });
                } else {
                    toast(activity, message);
                }
            }
        });
    }

    private static void toast(Activity activity, String message) {
        if (activity instanceof io.openim.android.ouicore.base.BaseActivity) {
            ((io.openim.android.ouicore.base.BaseActivity<?, ?>) activity).toast(message);
        }
    }
}
