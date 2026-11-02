package io.openim.android.demo.fund;

import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.provider.MediaStore;
import android.view.View;
import android.widget.AdapterView;
import android.widget.ArrayAdapter;

import androidx.core.content.FileProvider;

import org.json.JSONObject;

import java.io.File;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.ArrayList;
import java.util.List;

import io.openim.android.demo.databinding.ActivityFundMethodsBinding;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseViewModel;

/**
 * 提现方式管理（0011 后规则）：
 * - 已绑定方式不可解绑、不可修改，列表点开后只留“设为默认”（仍走统一交易密码弹窗）；
 * - 每种方式类型每账号最多绑一次，新增时同类型已存在即拦下；
 * - 微信/支付宝：填写账号 或 上传收款二维码二选一；银行卡：只填卡号，须开户银行；
 * - 选微信/支付宝时开户银行与开户支行栏隐藏且不提交；只有银行卡时才显示且开户银行必填。
 * 二维码只供人工打款时查看，不代表商户 API 自动打款。
 */
public class WithdrawMethodsActivity extends BaseActivity<BaseViewModel, ActivityFundMethodsBinding> {

    private static final int REQ_PICK_QR = 1001;
    private static final int REQ_TAKE_QR = 1002;

    private final List<JSONObject> methods = new ArrayList<>();
    private final List<String> lines = new ArrayList<>();
    private ArrayAdapter<String> adapter;

    private File qrFile;
    private File cameraFile;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindViewDataBinding(ActivityFundMethodsBinding.inflate(getLayoutInflater()));
        sink();
        adapter = new ArrayAdapter<>(this, android.R.layout.simple_list_item_1, lines);
        view.list.setAdapter(adapter);
        view.list.setOnItemClickListener((parent, v, position, id) -> onMethodClick(methods.get(position)));
        ArrayAdapter<String> typeAdapter = new ArrayAdapter<>(this,
            android.R.layout.simple_spinner_item,
            new String[]{"支付宝", "微信支付", "银行卡"});
        typeAdapter.setDropDownViewResource(android.R.layout.simple_spinner_dropdown_item);
        view.spType.setAdapter(typeAdapter);
        view.spType.setOnItemSelectedListener(new AdapterView.OnItemSelectedListener() {
            @Override
            public void onItemSelected(AdapterView<?> parent, View v, int position, long id) {
                updateTypeVisibility();
            }

            @Override
            public void onNothingSelected(AdapterView<?> parent) {
            }
        });
        view.rgMode.setOnCheckedChangeListener((group, checkedId) -> updateModeVisibility());
        view.btnPickQr.setOnClickListener(v -> pickQrFromGallery());
        view.btnCameraQr.setOnClickListener(v -> takeQrPhoto());
        view.btnRemoveQr.setOnClickListener(v -> clearQr());
        FundUi.pressScale(view.btnPickQr);
        FundUi.pressScale(view.btnCameraQr);
        FundUi.pressScale(view.btnRemoveQr);
        view.btnAdd.setOnClickListener(v -> add());
        FundUi.pressScale(view.btnAdd);
        updateTypeVisibility();
        load();
    }

    private void load() {
        showWaiting();
        FundApi.getWithdrawMethods(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                cancelWaiting();
                methods.clear();
                lines.clear();
                for (JSONObject m : FundModels.toList(data)) {
                    methods.add(m);
                    lines.add(FundModels.methodLine(m));
                }
                adapter.notifyDataSetChanged();
                view.emptyGroupMethods.setVisibility(
                    methods.isEmpty() ? View.VISIBLE : View.GONE);
            }

            @Override
            public void onErr(String code, String message) {
                cancelWaiting();
                toast(message);
            }
        });
    }

    /** 类型切换：银行卡显示银行栏、隐藏账号/二维码切换；微信/支付宝隐藏银行栏并清空其旧值。 */
    private void updateTypeVisibility() {
        int type = view.spType.getSelectedItemPosition() + 1;
        boolean isBank = type == 3;
        view.rgMode.setVisibility(isBank ? View.GONE : View.VISIBLE);
        int bankVis = isBank ? View.VISIBLE : View.GONE;
        view.tvBankNameLabel.setVisibility(bankVis);
        view.etBankName.setVisibility(bankVis);
        view.tvBankBranchLabel.setVisibility(bankVis);
        view.etBankBranch.setVisibility(bankVis);
        if (!isBank) {
            view.etBankName.setText("");
            view.etBankBranch.setText("");
        }
        updateModeVisibility();
    }

    /** 账号/二维码切换：二维码方式下隐藏账号栏、显示二维码区；账号方式下相反。 */
    private void updateModeVisibility() {
        int type = view.spType.getSelectedItemPosition() + 1;
        boolean qrMode = type != 3 && view.rbQr.isChecked();
        view.tvAccountLabel.setVisibility(qrMode ? View.GONE : View.VISIBLE);
        view.etAccountNo.setVisibility(qrMode ? View.GONE : View.VISIBLE);
        view.groupQr.setVisibility(qrMode ? View.VISIBLE : View.GONE);
    }

    private void add() {
        String realName = view.etRealName.getText().toString().trim();
        if (realName.isEmpty()) {
            toast("请填写真实姓名");
            return;
        }
        // 0013 实名一致：服务端按 bind_time 升序返回，首条即第一次绑定姓名基准。
        if (!methods.isEmpty()) {
            String baseName = methods.get(0).optString("real_name", "");
            if (!baseName.isEmpty() && !realName.equals(baseName)) {
                toast("须与第一次绑定姓名一致");
                return;
            }
        }
        int type = view.spType.getSelectedItemPosition() + 1;
        // 一型一次（客户端先拦；服务端 addMethod 亦有同规则兜底）。
        for (JSONObject m : methods) {
            if (m.optInt("type", 0) == type) {
                toast("该方式已绑定，如需解绑或修改请联系客服");
                return;
            }
        }
        boolean qrMode = type != 3 && view.rbQr.isChecked();
        String accountNo = qrMode ? "" : view.etAccountNo.getText().toString().trim();
        String bankName = type == 3 ? view.etBankName.getText().toString().trim() : "";
        String bankBranch = type == 3 ? view.etBankBranch.getText().toString().trim() : "";
        if (!qrMode && accountNo.isEmpty()) {
            toast("请填写账号");
            return;
        }
        if (type == 3 && bankName.isEmpty()) {
            toast("银行卡须填写开户银行");
            return;
        }
        if (qrMode && qrFile == null) {
            toast("请上传收款二维码");
            return;
        }
        showWaiting();
        if (qrMode) {
            FundApi.uploadWithdrawMethodQR(qrFile, new FundApi.Callback() {
                @Override
                public void onOk(Object data) {
                    JSONObject o = FundModels.toObj(data);
                    String qrId = o == null ? "" : o.optString("qr_file_id", "");
                    if (qrId.isEmpty()) {
                        cancelWaiting();
                        toast("二维码上传失败，请重试");
                        return;
                    }
                    doAdd(type, realName, "", "", "", qrId);
                }

                @Override
                public void onErr(String code, String message) {
                    cancelWaiting();
                    toast(message);
                }
            });
        } else {
            doAdd(type, realName, accountNo, bankName, bankBranch, null);
        }
    }

    private void doAdd(int type, String realName, String accountNo,
                       String bankName, String bankBranch, String qrFileId) {
        try {
            FundApi.addWithdrawMethod(type, realName, accountNo, bankName, bankBranch,
                qrFileId, view.cbDefault.isChecked(), new FundApi.Callback() {
                    @Override
                    public void onOk(Object data) {
                        cancelWaiting();
                        clearQr();
                        FundUi.showSuccess(WithdrawMethodsActivity.this,
                            "添加成功", () -> load());
                    }

                    @Override
                    public void onErr(String code, String message) {
                        cancelWaiting();
                        toast(message);
                    }
                });
        } catch (Exception e) {
            cancelWaiting();
            toast("添加失败，请重试");
        }
    }

    /** 已绑定方式只留设默认；解绑与修改入口已按新规则撤掉（不编造客服联系方式与跳转）。 */
    private void onMethodClick(final JSONObject m) {
        final String id = m.optString("method_id", "");
        FundUi.dialog(this)
            .setTitle(FundModels.methodLine(m))
            .setItems(new String[]{"设为默认"},
                (dialog, which) -> askTxPassword((pw, handle) -> doSetDefault(id, pw, handle)))
            .setNegativeButton("取消", null)
            .show();
    }

    private void pickQrFromGallery() {
        Intent intent = new Intent(Intent.ACTION_GET_CONTENT);
        intent.setType("image/*");
        intent.addCategory(Intent.CATEGORY_OPENABLE);
        try {
            startActivityForResult(Intent.createChooser(intent, "选择收款二维码"), REQ_PICK_QR);
        } catch (ActivityNotFoundException e) {
            toast("无法打开图片选择器");
        }
    }

    private void takeQrPhoto() {
        try {
            File file = new File(getFilesDir(), "qr_camera_" + System.currentTimeMillis() + ".jpg");
            Uri uri = FileProvider.getUriForFile(this, getPackageName() + ".fileProvider", file);
            cameraFile = file;
            Intent intent = new Intent(MediaStore.ACTION_IMAGE_CAPTURE);
            intent.putExtra(MediaStore.EXTRA_OUTPUT, uri);
            intent.addFlags(Intent.FLAG_GRANT_WRITE_URI_PERMISSION);
            startActivityForResult(intent, REQ_TAKE_QR);
        } catch (Exception e) {
            toast("无法打开相机，请重试");
        }
    }

    private void clearQr() {
        qrFile = null;
        view.ivQrPreview.setImageDrawable(null);
        view.ivQrPreview.setVisibility(View.GONE);
        view.tvQrHint.setText("请上传微信 / 支付宝收款二维码图片（jpg/png，≤5MB）；二维码只供人工打款时查看，不会自动打款");
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (resultCode != RESULT_OK) {
            return;
        }
        try {
            if (requestCode == REQ_PICK_QR && data != null && data.getData() != null) {
                File out = new File(getCacheDir(), "qr_pick_" + System.currentTimeMillis() + ".jpg");
                try (InputStream in = getContentResolver().openInputStream(data.getData());
                     OutputStream os = new FileOutputStream(out)) {
                    if (in == null) {
                        toast("读取图片失败，请重试");
                        return;
                    }
                    byte[] buf = new byte[8192];
                    int n;
                    while ((n = in.read(buf)) != -1) {
                        os.write(buf, 0, n);
                    }
                }
                qrFile = out;
                view.ivQrPreview.setImageURI(Uri.fromFile(out));
                view.ivQrPreview.setVisibility(View.VISIBLE);
                view.tvQrHint.setText("已选择二维码图片");
            } else if (requestCode == REQ_TAKE_QR && cameraFile != null && cameraFile.exists()
                && cameraFile.length() > 0) {
                qrFile = cameraFile;
                view.ivQrPreview.setImageURI(Uri.fromFile(cameraFile));
                view.ivQrPreview.setVisibility(View.VISIBLE);
                view.tvQrHint.setText("已拍摄二维码图片");
            }
        } catch (Exception e) {
            toast("读取图片失败，请重试");
        }
    }

    /**
     * 交易密码弹窗与发红包同款：6 格、输满自动提交、标题一律“输入交易密码”；
     * 密码错时弹窗不关、红字显示服务端文案、清空重输（核验中状态由弹窗自身展示，不另弹等待框）。
     */
    private void askTxPassword(final TxPasswordDialog.Verifier verifier) {
        TxPasswordDialog.showForVerification(this, "输入交易密码", verifier);
    }

    private void doSetDefault(String id, String pw, TxPasswordDialog.Handle handle) {
        try {
            FundApi.setDefaultWithdrawMethod(id, pw, verifyReload(handle));
        } catch (Exception e) {
            handle.fail("操作失败，请重试");
        }
    }

    private FundApi.Callback verifyReload(final TxPasswordDialog.Handle handle) {
        return new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                handle.complete();
                FundUi.showSuccess(WithdrawMethodsActivity.this,
                    "操作成功", () -> load());
            }

            @Override
            public void onErr(String code, String message) {
                handle.fail(message);
            }
        };
    }
}
