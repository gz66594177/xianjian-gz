package io.openim.android.demo.ui.login;

import android.content.Intent;
import android.os.Bundle;
import android.text.TextUtils;
import android.widget.Toast;

import androidx.appcompat.app.AppCompatActivity;

import org.json.JSONObject;

import io.openim.android.demo.databinding.ActivityPhoneBindBinding;
import io.openim.android.demo.fund.FundApi;
import io.openim.android.demo.fund.FundModels;
import io.openim.android.demo.fund.FundUi;
import io.openim.android.demo.services.XianliaoKeepAlive;
import io.openim.android.demo.ui.main.MainActivity;
import io.openim.android.demo.vm.LoginVM;
import io.openim.android.ouicore.base.BaseActivity;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.utils.Constants;
import io.openim.android.ouicore.utils.SharedPreferencesUtil;
import io.openim.android.sdk.OpenIMClient;
import io.openim.android.sdk.listener.OnBase;
import io.openim.android.ouicore.im.IMUtil;

/**
 * 邮箱注册用户首次进页强制绑定手机号（0013）。
 * 无跳过按钮、无返回绕过：只能填手机号 + 聊天服务验证码核验 + 资金服务绑定成功后
 * 进主界面；唯一出口是退出登录。手机注册用户不走本页（MainActivity 门禁只对
 * 邮箱登录类型生效），邮箱绑定对手机用户始终不强制。
 * 验证码复用聊天服务 account/code/send + verify（usedFor=1，与注册同款），
 * 资金服务再做格式 + 跨账号唯一 + 一人一号（见 profile_phone.go 的残口说明）。
 */
public class PhoneBindActivity extends BaseActivity<LoginVM, ActivityPhoneBindBinding> implements LoginVM.ViewAction {

    public static final String KEY_PHONE_BOUND_PREFIX = "xl_phone_bound_";

    public static String boundKey(String userId) {
        return KEY_PHONE_BOUND_PREFIX + userId;
    }

    public static String myUserId() {
        try {
            if (BaseApp.inst() != null && BaseApp.inst().loginCertificate != null) {
                return BaseApp.inst().loginCertificate.userID;
            }
        } catch (Exception ignored) {
        }
        return "";
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        bindVM(LoginVM.class);
        bindViewDataBinding(ActivityPhoneBindBinding.inflate(getLayoutInflater()));
        sink();
        vm.isPhone.setValue(true);
        view.countryCode.changeDefaultLanguage(LoginActivity.buildDefaultLanguage());
        FundUi.pressScale(view.btnGetCode, view.btnSubmit, view.btnLogout);
        view.btnGetCode.setOnClickListener(v -> sendCode());
        view.btnSubmit.setOnClickListener(v -> submit());
        view.btnLogout.setOnClickListener(v -> logout());
        vm.countdown.observe(this, v -> {
            if (v == null) return;
            if (v == 0) {
                view.btnGetCode.setText("获取验证码");
                view.btnGetCode.setEnabled(true);
            } else {
                view.btnGetCode.setText("重新发送(" + v + "s)");
                view.btnGetCode.setEnabled(false);
            }
        });
        // 已在资金服务绑过（换设备等）直接放行，不重复索要号码。
        FundApi.getProfilePhone(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                JSONObject o = FundModels.toObj(data);
                if (o != null && o.optBoolean("has_phone", false)) {
                    markBoundAndEnter();
                }
            }

            @Override
            public void onErr(String code, String message) {
                // 查不到不拦表单：绑定提交时服务端仍会校验；网络异常时用户可用退出登录离开。
            }
        });
    }

    @Override
    public void onBackPressed() {
        // 强制页：返回键不绕过，只能绑定成功或退出登录。
        Toast.makeText(this, "请先绑定手机号", Toast.LENGTH_SHORT).show();
    }

    private String phone() {
        return view.etPhone.getText().toString().trim();
    }

    private String areaCode() {
        return "+" + view.countryCode.getSelectedCountryCode();
    }

    private boolean validPhone(String p) {
        return p != null && p.matches("[0-9]{6,15}");
    }

    private void sendCode() {
        String p = phone();
        if (!validPhone(p)) {
            Toast.makeText(this, "请填写正确的手机号码", Toast.LENGTH_SHORT).show();
            return;
        }
        vm.account.setValue(p);
        vm.areaCode.setValue(areaCode());
        vm.countdown.setValue(LoginVM.MAX_COUNTDOWN);
        vm.countdown();
        vm.getVerificationCode(1);
    }

    private void submit() {
        String p = phone();
        String code = view.etCode.getText().toString().trim();
        if (!validPhone(p)) {
            Toast.makeText(this, "请填写正确的手机号码", Toast.LENGTH_SHORT).show();
            return;
        }
        if (code.length() != 6) {
            Toast.makeText(this, "请填写 6 位验证码", Toast.LENGTH_SHORT).show();
            return;
        }
        vm.account.setValue(p);
        vm.areaCode.setValue(areaCode());
        vm.checkVerificationCode(code, 1);
    }

    private void bindToFund() {
        try {
            FundApi.bindProfilePhone(areaCode(), phone(), new FundApi.Callback() {
                @Override
                public void onOk(Object data) {
                    markBoundAndEnter();
                }

                @Override
                public void onErr(String code, String message) {
                    Toast.makeText(PhoneBindActivity.this, message, Toast.LENGTH_SHORT).show();
                }
            });
        } catch (Exception e) {
            Toast.makeText(this, "绑定失败，请重试", Toast.LENGTH_SHORT).show();
        }
    }

    private void markBoundAndEnter() {
        String uid = myUserId();
        if (!TextUtils.isEmpty(uid)) {
            SharedPreferencesUtil.get(this).setCache(boundKey(uid), true);
        }
        startActivity(new Intent(this, MainActivity.class));
        finish();
    }

    private void logout() {
        OpenIMClient.getInstance().logout(new OnBase<String>() {
            @Override
            public void onError(int code, String error) {
                Toast.makeText(PhoneBindActivity.this, error + code, Toast.LENGTH_SHORT).show();
            }

            @Override
            public void onSuccess(String data) {
                try {
                    XianliaoKeepAlive.stop(PhoneBindActivity.this);
                } catch (Exception ignored) {
                }
                IMUtil.logout((AppCompatActivity) PhoneBindActivity.this, LoginActivity.class);
            }
        });
    }

    @Override
    public void jump() {
    }

    @Override
    public void err(String msg) {
        Toast.makeText(this, msg, Toast.LENGTH_SHORT).show();
    }

    @Override
    public void succ(Object o) {
        if ("checkVerificationCode".equals(o)) {
            bindToFund();
        } else {
            Toast.makeText(this, io.openim.android.ouicore.R.string.send_succ, Toast.LENGTH_SHORT).show();
        }
    }

    @Override
    public void initDate() {
    }
}
