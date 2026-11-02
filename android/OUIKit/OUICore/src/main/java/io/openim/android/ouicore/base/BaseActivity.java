package io.openim.android.ouicore.base;


import android.content.Context;
import android.content.pm.ActivityInfo;
import android.content.res.Configuration;
import android.graphics.Rect;
import android.os.Build;
import android.os.Bundle;
import android.view.MotionEvent;
import android.view.View;
import android.view.Window;
import android.view.WindowManager;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.widget.Toast;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import androidx.appcompat.app.AppCompatActivity;
import androidx.databinding.ViewDataBinding;
import androidx.lifecycle.ViewModelProvider;

import com.alibaba.android.arouter.launcher.ARouter;

import io.openim.android.ouicore.base.vm.injection.Easy;
import io.openim.android.ouicore.net.RXRetrofit.N;

import io.openim.android.ouicore.utils.ActivityManager;
import io.openim.android.ouicore.utils.XlNightMode;
import io.openim.android.ouicore.utils.LanguageUtil;
import io.openim.android.ouicore.utils.Routes;
import io.openim.android.ouicore.utils.SinkHelper;
import io.openim.android.ouicore.widget.WaitDialog;


@Deprecated
public class BaseActivity<T extends BaseViewModel, A extends ViewDataBinding> extends AppCompatActivity implements IView {
    //是否释放资源
    private boolean isRelease = true;
    //已经释放资源
    private boolean released = false;
    private boolean isFaster = false;

    protected T vm;
    protected A view;
    // 已为哪个夜间模式重建过一次（防重建死循环），见 onSaveInstanceState。
    private int nightRecreateAttemptedFor = -1;
    private String vmCanonicalName;
    private WaitDialog waitDialog;


    @Override
    protected void onCreate(@Nullable Bundle savedInstanceState) {
        if (savedInstanceState != null) {
            nightRecreateAttemptedFor = savedInstanceState.getInt(XlNightMode.KEY, -1);
        }
        ActivityManager.push(this);
        requestedOrientation();
        super.onCreate(savedInstanceState);
        if (null != vm) {
            vm.viewCreate();
        }
        setLightStatus();
        if (waitDialog != null) {
            waitDialog.dismiss();
            waitDialog = null;
        }
        waitDialog = new WaitDialog(this);
    }

    @Override
    protected void attachBaseContext(Context newBase) {
        //多语言适配
        super.attachBaseContext(LanguageUtil.getNewLocalContext(newBase));
    }

    @Override
    public void applyOverrideConfiguration(Configuration overrideConfiguration) {
        // 兼容androidX在部分手机切换语言失败问题
        if (overrideConfiguration != null) {
            int uiMode = overrideConfiguration.uiMode;
            overrideConfiguration.setTo(getBaseContext().getResources().getConfiguration());
            overrideConfiguration.uiMode = uiMode;
        }
        super.applyOverrideConfiguration(overrideConfiguration);
    }


    protected void requestedOrientation() {
        setRequestedOrientation(ActivityInfo.SCREEN_ORIENTATION_PORTRAIT);
    }

    protected void bindViewDataBinding(A viewDataBinding) {
        bindViewDataBinding(viewDataBinding, true);
    }

    protected void bindViewDataBinding(A viewDataBinding, boolean lifecycleOwner) {
        view = viewDataBinding;
        setContentView(view.getRoot());
        if (lifecycleOwner) {
            view.setLifecycleOwner(this);
        }
    }

    @Deprecated
    protected void bindVM(Class<T> vm) {
        this.vm = new ViewModelProvider(this).get(vm);
        vmCanonicalName = this.vm.getClass().getCanonicalName();
        bind();
    }

    @Deprecated
    protected void bindVM(Class<T> vm, boolean shareVM) {
        bindVM(vm);
        if (shareVM) {
            BaseApp.inst().putVM(this.vm);
        }
    }

    private void bind() {
        if (null == vm) return;
        vm.setContext(this);
        vm.setIView(this);
    }

    protected void setLightStatus() {
        Window window = getWindow();
        //After LOLLIPOP not translucent status bar
        window.clearFlags(WindowManager.LayoutParams.FLAG_TRANSLUCENT_STATUS);
        //Then call setStatusBarColor.
        window.addFlags(WindowManager.LayoutParams.FLAG_DRAWS_SYSTEM_BAR_BACKGROUNDS);

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            // 深色下不用 LIGHT_STATUS_BAR：深色图标压在深色顶栏上会直接看不见；
            // 浅色下保留深色图标。Configuration 已在文件顶部 import。
            int vis = View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN;
            boolean night = (getResources().getConfiguration().uiMode
                & Configuration.UI_MODE_NIGHT_MASK) == Configuration.UI_MODE_NIGHT_YES;
            if (!night) {
                vis |= View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR;
            }
            getWindow().getDecorView().setSystemUiVisibility(vis);
        }
    }


    public void toBack(View view) {
        finish();
    }

    boolean touchClearFocus = true;

    public void setTouchClearFocus(boolean touchClearFocus) {
        this.touchClearFocus = touchClearFocus;
    }

    @Deprecated
    public void bindVMByCache(Class<T> vm) {
        try {
            this.vm = Easy.find(vm);
            isRelease = false;
            bind();
        } catch (Exception ignore) {
        }
    }

    @Deprecated
    public void removeCacheVM() {
        if (null != vm) {
            vm.context.clear();
            vm.releaseRes();
            BaseApp.inst().removeCacheVM(vm.getClass());
        }
    }

    @Override
    protected void onSaveInstanceState(@NonNull Bundle outState) {
        super.onSaveInstanceState(outState);
        outState.putInt(XlNightMode.KEY, nightRecreateAttemptedFor);
    }

    /**
     * 深浅色实时跟随兜底：后台切回前台时，若本页资源仍是旧夜间模式
     * （系统切深浅色后 AppCompat 未重建本页），立即重建一次。
     * 重建走标准 savedInstanceState：输入框草稿、列表滚动等视图状态由系统恢复，
     * 页面栈不丢；若重建后仍不一致（极端机型），只拦一次，不死循环。
     */
    private boolean checkNightModeRecreate() {
        if (isFinishing() || isChangingConfigurations()) return false;
        if (!XlNightMode.needsRecreate(this)) return false;
        int systemNight = XlNightMode.systemNightMode(this);
        if (nightRecreateAttemptedFor == systemNight) return false;
        nightRecreateAttemptedFor = systemNight;
        recreate();
        return true;
    }

    @Override
    protected void onResume() {
        super.onResume();
        if (checkNightModeRecreate()) return;
        setLightStatus();
        bind();
        if (null != vm) vm.viewResume();
    }


    @Override
    protected void onPause() {
        exeFaster();
        if (null != vm) {
            vm.viewPause();
            releaseRes();
        }
        super.onPause();
    }

    protected void fasterDestroy() {

    }

    private void releaseRes() {
        if (vm == null) return;
        if (isFinishing() && isRelease && !released) {
            released = true;
            vm.releaseRes();
        }
    }

    @Override
    protected void onDestroy() {
        ActivityManager.remove(this);
        exeFaster();
        N.clearDispose(this);
        releaseRes();
        if (waitDialog != null) {
            waitDialog.dismiss();
            waitDialog = null;
        }
        super.onDestroy();
    }

    private void exeFaster() {
        if (isFinishing() && !isFaster) {
            isFaster = true;
            fasterDestroy();
        }
    }


    /**
     * 点击非获取焦点EditText隐藏键盘
     */
    @Override
    public boolean dispatchTouchEvent(MotionEvent ev) {
        if (!touchClearFocus) return super.dispatchTouchEvent(ev);
        View v = getCurrentFocus();
        if (v instanceof EditText) {
            Rect outRect = new Rect();
            v.getGlobalVisibleRect(outRect);
            if (!outRect.contains((int) ev.getRawX(), (int) ev.getRawY())) {
                v.clearFocus(); //在根布局添加focusableInTouchMode="true"
                InputMethodManager imm =
                    (InputMethodManager) getSystemService(Context.INPUT_METHOD_SERVICE);
                if (imm != null) {
                    imm.hideSoftInputFromWindow(v.getWindowToken(), 0);
                }
            }
        }
        return super.dispatchTouchEvent(ev);
    }

    /**
     * 沉侵式状态栏
     */
    public void sink(View view) {
        setLightStatus();
        SinkHelper.get(this).setTranslucentStatus(view);
    }

    public void sink() {
        sink(view.getRoot());
    }

    @Override
    public void onError(String error) {

    }

    @Override
    public void onSuccess(Object body) {

    }

    @Override
    public void toast(String tips) {
        Toast.makeText(this, tips, Toast.LENGTH_LONG).show();
    }

    @Override
    public void close() {
        finish();
    }

    public void showWaiting() {
        if (waitDialog == null) return;
        if (!waitDialog.isShowing()) {
            waitDialog.setNotDismiss();
            waitDialog.show();
        }
    }

    public void cancelWaiting() {
        if (waitDialog == null) return;
        if (waitDialog.isShowing()) {
            waitDialog.dismiss();
        }
    }


    @Override
    public void onBackPressed() {
        super.onBackPressed();
    }
}
