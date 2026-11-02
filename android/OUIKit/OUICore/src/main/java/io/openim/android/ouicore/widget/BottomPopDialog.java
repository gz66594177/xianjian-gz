package io.openim.android.ouicore.widget;

import android.content.Context;
import android.view.Gravity;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowManager;

import androidx.annotation.NonNull;

import io.openim.android.ouicore.R;
import io.openim.android.ouicore.base.BaseDialog;
import io.openim.android.ouicore.databinding.DialogPhotographAlbumBinding;
import io.openim.android.ouicore.databinding.LayoutBottompopV3Binding;
import io.openim.android.ouicore.databinding.LayoutCommonDialogBinding;

public class BottomPopDialog extends BaseDialog {
    private DialogPhotographAlbumBinding mainView;
    private View customMainView;

    public BottomPopDialog(@NonNull Context context, View mainView) {
        super(context);
        this.customMainView = mainView;
        initView();
    }

    public BottomPopDialog(@NonNull Context context) {
        super(context);
        initView();
    }

    private void initView() {
        mainView = DialogPhotographAlbumBinding.inflate(getLayoutInflater());
        if (null == customMainView) setContentView(mainView.getRoot());
        else setContentView(customMainView);

        WindowManager.LayoutParams params = getWindow().getAttributes();
        params.gravity = Gravity.BOTTOM;
        params.height = ViewGroup.LayoutParams.WRAP_CONTENT;
        params.width = ViewGroup.LayoutParams.MATCH_PARENT;
        params.windowAnimations = R.style.XianliaoBottomAnim;
        getWindow().setAttributes(params);
        getWindow().setBackgroundDrawableResource(android.R.color.transparent);
        // 按钮按压缩放（与资金页 FundUi.pressScale 同参数：按下 0.96/120ms，松开回弹 160ms），
        // 只改表现，不改选项与跳转；return false 不吞点击事件。
        pressScale(mainView.menu1, mainView.menu2, mainView.menu3);
    }

    private static void pressScale(android.view.View... views) {
        for (android.view.View v : views) {
            if (v == null) {
                continue;
            }
            v.setOnTouchListener((view, event) -> {
                switch (event.getAction()) {
                    case MotionEvent.ACTION_DOWN:
                        view.animate().scaleX(0.96f).scaleY(0.96f).setDuration(120).start();
                        break;
                    case MotionEvent.ACTION_UP:
                    case MotionEvent.ACTION_CANCEL:
                        view.animate().scaleX(1f).scaleY(1f).setDuration(160).start();
                        break;
                    default:
                        break;
                }
                return false;
            });
        }
    }

    public DialogPhotographAlbumBinding getMainView() {
        return mainView;
    }

    public View getCustomMainView() {
        return customMainView;
    }


}
