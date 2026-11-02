package io.openim.android.demo.fund;

import android.content.Intent;
import android.os.Bundle;

import org.json.JSONObject;

import io.openim.android.demo.databinding.ActivityFundTransferBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/** 转账页：即时到账，须准入 + 交易密码。 */
public class TransferActivity extends BaseActivity<BaseViewModel, ActivityFundTransferBinding> {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundTransferBinding.inflate(getLayoutInflater()));
        sink();
        view.btnSend.setOnClickListener(v -> send());
        FundUi.pressScale(view.btnSend, view.btnRecords);
        view.btnRecords.setOnClickListener(v -> FundUi.dialog(this)
            .setTitle("转账记录")
            .setItems(new String[]{"我发出的", "我收到的"}, (dialog, which) -> {
                Intent intent = new Intent(this, FundListActivity.class);
                intent.putExtra(FundListActivity.EXTRA_MODE, which == 0
                    ? FundListActivity.MODE_TRANSFERS_SENT
                    : FundListActivity.MODE_TRANSFERS_RECEIVED);
                startActivity(intent);
            })
            .setNegativeButton("取消", null)
            .show());
    }

    private void send() {
        final String receiver = view.etReceiver.getText().toString().trim();
        final long cents = Money.parseYuanToCents(view.etAmount.getText().toString());
        final String remark = view.etRemark.getText().toString().trim();
        if (receiver.isEmpty()) {
            toast("请填写收款方用户 ID");
            return;
        }
        if (cents <= 0) {
            toast("请输入有效金额（元，最多 2 位小数）");
            return;
        }
        if (remark.length() > 200) {
            toast("附言最多 200 字");
            return;
        }
        // 交易密码弹窗与发红包同款：6 格、输满自动提交、标题“输入交易密码”；
        // 密码错时弹窗不关、红字显示服务端文案、清空重输（核验中状态由弹窗自身展示，不另弹等待框）。
        FundGate.check(this, () -> TxPasswordDialog.showForVerification(this, "输入交易密码",
            (password, handle) -> {
            if (password == null || password.length() != 6) {
                handle.fail("请输入 6 位交易密码");
                return;
            }
            try {
                FundApi.createTransfer(receiver, cents, remark, password, new FundApi.Callback() {
                    @Override
                    public void onOk(Object data) {
                        JSONObject o = FundModels.toObj(data);
                        if (o == null) {
                            handle.complete();
                            return;
                        }
                        handle.complete();
                        view.tvResult.setText("转账成功\n订单号：" + o.optString("order_id", "")
                            + "\n收款方：" + o.optString("receiver_id", "")
                            + "\n金额：" + Money.formatCents(o.optLong("amount_cents", 0)));
                        FundUi.showSuccess(TransferActivity.this, "转账成功", null);
                    }

                    @Override
                    public void onErr(String code, String message) {
                        handle.fail(message);
                    }
                });
            } catch (Exception e) {
                handle.fail("转账失败，请重试");
            }
        }));
    }
}
