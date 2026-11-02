package io.openim.android.ouicore.base;

import android.annotation.SuppressLint;
import android.content.pm.ActivityInfo;
import android.os.Build;
import android.os.Bundle;
import android.view.View;
import android.view.Window;
import android.view.WindowManager;
import android.widget.Toast;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import androidx.appcompat.app.AppCompatActivity;
import androidx.databinding.ViewDataBinding;

import com.alibaba.android.arouter.launcher.ARouter;

import io.openim.android.ouicore.utils.ActivityManager;
import io.openim.android.ouicore.utils.Routes;
import io.openim.android.ouicore.utils.XlNightMode;

public class BasicActivity<T extends ViewDataBinding> extends AppCompatActivity {
    private int nightRecreateAttemptedFor = -1;
    boolean isRecycle;
    protected T view;
    private Toast to;

    @Override
    protected void onCreate(@Nullable Bundle savedInstanceState) {
        if (savedInstanceState != null) {
            nightRecreateAttemptedFor = savedInstanceState.getInt(XlNightMode.KEY, -1);
        }
        ActivityManager.push(this);
        requestedOrientation();
        super.onCreate(savedInstanceState);
        setLightStatus();
    }

    public void viewBinding(T viewDataBinding) {
        view = viewDataBinding;
        setContentView(view.getRoot());
        view.setLifecycleOwner(this);
    }

    @SuppressLint("SourceLockedOrientationActivity")
    protected void requestedOrientation() {
        setRequestedOrientation(ActivityInfo.SCREEN_ORIENTATION_PORTRAIT);
    }

    @Override
    protected void onSaveInstanceState(@NonNull Bundle outState) {
        super.onSaveInstanceState(outState);
        outState.putInt(XlNightMode.KEY, nightRecreateAttemptedFor);
    }

    @Override
    protected void onResume() {
        super.onResume();
        // 与 BaseActivity 同款深浅色实时跟随兜底：后台切回前台时资源仍是旧夜间模式则重建一次。
        if (!isFinishing() && !isChangingConfigurations()
            && XlNightMode.needsRecreate(this)
            && nightRecreateAttemptedFor != XlNightMode.systemNightMode(this)) {
            nightRecreateAttemptedFor = XlNightMode.systemNightMode(this);
            recreate();
            return;
        }
        setLightStatus();
    }

    @Override
    protected void onPause() {
        fasterDestroy();
        super.onPause();
    }

    @Override
    protected void onDestroy() {
        fasterDestroy();
        super.onDestroy();
    }

    void fasterDestroy() {
        if (isFinishing() && !isRecycle) {
            isRecycle = true;
            ActivityManager.remove(this);
            if (null != view)
                view.unbind();
            recycle();
        }
    }

    protected void recycle() {

    }

    public void toast(String tips) {
        Toast.makeText(this, tips, Toast.LENGTH_LONG).show();
    }

    public void singleInstanceToast(String tips) {
        if (to != null) {
            to.cancel();
        }
        to = Toast.makeText(this, tips, Toast.LENGTH_SHORT);
        to.show();
    }

    protected void setLightStatus() {
        Window window = getWindow();
        //After LOLLIPOP not translucent status bar
        window.clearFlags(WindowManager.LayoutParams.FLAG_TRANSLUCENT_STATUS);
        //Then call setStatusBarColor.
        window.addFlags(WindowManager.LayoutParams.FLAG_DRAWS_SYSTEM_BAR_BACKGROUNDS);

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            getWindow().getDecorView().setSystemUiVisibility(View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN
                | View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR);
        }
    }

    public void toBack(View view) {
        finish();
    }

    @Override
    public void onBackPressed() {
        super.onBackPressed();
    }
}
