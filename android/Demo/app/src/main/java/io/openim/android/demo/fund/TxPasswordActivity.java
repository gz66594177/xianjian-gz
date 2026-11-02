package io.openim.android.demo.fund;

import android.os.Bundle;
import android.view.View;

import org.json.JSONObject;

import io.openim.android.demo.databinding.ActivityFundTxPasswordBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/** 交易密码页：设置（6 位数字、两次一致）/ 修改（须验旧密码）。
 * 条件显示：未设置只显示设置区，已设置只显示修改区；修改区自带新密码两栏，
 * 不依赖设置区。状态以服务端 getTxPasswordStatus 的 has_password 为准。 */
public class TxPasswordActivity extends BaseActivity<BaseViewModel, ActivityFundTxPasswordBinding> {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundTxPasswordBinding.inflate(getLayoutInflater()));
        sink();
        view.btnSet.setOnClickListener(v -> set());
        view.btnChange.setOnClickListener(v -> change());
        FundUi.pressScale(view.btnSet, view.btnChange);
        loadStatus();
    }

    /** 按是否已设置切换两区显示；切换时清空各密码栏，避免密码残留。 */
    private void applyState(boolean hasPassword) {
        view.groupSet.setVisibility(hasPassword ? View.GONE : View.VISIBLE);
        view.groupChange.setVisibility(hasPassword ? View.VISIBLE : View.GONE);
        view.etNew1.setText("");
        view.etNew2.setText("");
        view.etOld.setText("");
        view.etNewChange1.setText("");
        view.etNewChange2.setText("");
    }

    private void loadStatus() {
        FundApi.getTxPasswordStatus(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                JSONObject o = FundModels.toObj(data);
                if (o == null) {
                    return;
                }
                boolean has = o.optBoolean("has_password", false);
                boolean locked = o.optBoolean("locked", false);
                StringBuilder sb = new StringBuilder();
                sb.append(has ? "已设置交易密码" : "未设置交易密码");
                if (locked) {
                    sb.append("（已锁定，锁定至 ").append(o.optString("locked_until", "")).append("）");
                } else if (o.optInt("error_count", 0) > 0) {
                    sb.append("（连续输错 ").append(o.optInt("error_count", 0)).append(" 次）");
                }
                view.tvStatus.setText(sb.toString());
                applyState(has);
            }

            @Override
            public void onErr(String code, String message) {
                view.tvStatus.setText("状态加载失败：" + message);
                // 状态未知时的兜底：只显示设置区（服务端会在已设置时拒绝重复设置），
                // 不恢复两区同显。
                applyState(false);
            }
        });
    }

    private boolean isSixDigits(String s) {
        if (s == null || s.length() != 6) {
            return false;
        }
        for (int i = 0; i < 6; i++) {
            if (!Character.isDigit(s.charAt(i))) {
                return false;
            }
        }
        return true;
    }

    private void set() {
        String p1 = view.etNew1.getText().toString();
        String p2 = view.etNew2.getText().toString();
        if (!isSixDigits(p1)) {
            toast("交易密码须为 6 位数字");
            return;
        }
        if (!p1.equals(p2)) {
            toast("两次输入不一致");
            return;
        }
        showWaiting();
        try {
            FundApi.setTxPassword(p1, p2, new FundApi.Callback() {
                @Override
                public void onOk(Object data) {
                    cancelWaiting();
                    FundUi.showSuccess(TxPasswordActivity.this,
                        "设置成功", () -> {
                            // 设置成功后立即切到已设置态（修改区），不用退出重进。
                            applyState(true);
                            loadStatus();
                        });
                }

                @Override
                public void onErr(String code, String message) {
                    cancelWaiting();
                    toast(message);
                }
            });
        } catch (Exception e) {
            cancelWaiting();
            toast("设置失败，请重试");
        }
    }

    private void change() {
        String oldPw = view.etOld.getText().toString();
        // 修改区自带的新密码两栏，不再读设置区的 etNew1/etNew2。
        String p1 = view.etNewChange1.getText().toString();
        String p2 = view.etNewChange2.getText().toString();
        if (!isSixDigits(oldPw) || !isSixDigits(p1)) {
            toast("交易密码须为 6 位数字");
            return;
        }
        if (!p1.equals(p2)) {
            toast("两次输入不一致");
            return;
        }
        showWaiting();
        try {
            FundApi.changeTxPassword(oldPw, p1, p2, new FundApi.Callback() {
                @Override
                public void onOk(Object data) {
                    cancelWaiting();
                    FundUi.showSuccess(TxPasswordActivity.this,
                        "修改成功", () -> loadStatus());
                }

                @Override
                public void onErr(String code, String message) {
                    cancelWaiting();
                    toast(message);
                }
            });
        } catch (Exception e) {
            cancelWaiting();
            toast("修改失败，请重试");
        }
    }
}
