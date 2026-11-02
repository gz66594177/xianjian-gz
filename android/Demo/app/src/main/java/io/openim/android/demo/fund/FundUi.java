package io.openim.android.demo.fund;

import android.animation.AnimatorSet;
import android.animation.ObjectAnimator;
import android.app.Activity;
import android.app.AlertDialog;
import android.app.Dialog;
import android.graphics.Color;
import android.graphics.drawable.ColorDrawable;
import android.os.Handler;
import android.os.Looper;
import android.view.MotionEvent;
import android.view.View;
import android.view.Window;
import android.view.animation.OvershootInterpolator;

import io.openim.android.demo.databinding.DialogFundSuccessBinding;

/**
 * 资金页统一视觉动效与弹窗（原生实现，不用 WebView 动画冒充）。
 * 动效克制、时长统一：按压回弹 120/160ms、选中 pop 约 200ms、
 * 对勾弹窗 320ms 入场 + 约 1150ms 自动消失、对话框 240/180ms 进出场。
 */
public final class FundUi {

    private FundUi() {
    }

    /** 按钮按压缩放：按下 0.96、松开回弹；返回 false，不吞点击事件。 */
    public static void pressScale(View... views) {
        for (View v : views) {
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

    /** 选中过渡小 pop：1 -> 1.08 -> 1，用于金额胶囊与通道卡片。 */
    public static void pop(View v) {
        if (v == null) {
            return;
        }
        ObjectAnimator upX = ObjectAnimator.ofFloat(v, "scaleX", 1f, 1.08f);
        ObjectAnimator upY = ObjectAnimator.ofFloat(v, "scaleY", 1f, 1.08f);
        ObjectAnimator downX = ObjectAnimator.ofFloat(v, "scaleX", 1.08f, 1f);
        ObjectAnimator downY = ObjectAnimator.ofFloat(v, "scaleY", 1.08f, 1f);
        upX.setDuration(90);
        upY.setDuration(90);
        downX.setDuration(120);
        downY.setDuration(120);
        AnimatorSet set = new AnimatorSet();
        set.play(upX).with(upY);
        set.play(downX).with(downY).after(upX);
        set.start();
    }

    /** 统一对话框 Builder：Material 进出场动画（XianliaoDialog）。 */
    public static AlertDialog.Builder dialog(Activity activity) {
        return new AlertDialog.Builder(activity,
            io.openim.android.ouicore.R.style.XianliaoDialog);
    }

    /**
     * 提交成功对勾弹窗：对勾 overshoot 放大入场，约 1150ms 后自动消失再跑 after。
     * 只表达“提交成功”，不代表服务端已入账/已审核通过。
     */
    public static void showSuccess(Activity activity, String message, final Runnable after) {
        if (activity == null || activity.isFinishing()) {
            if (after != null) {
                after.run();
            }
            return;
        }
        DialogFundSuccessBinding binding =
            DialogFundSuccessBinding.inflate(activity.getLayoutInflater());
        binding.tvMsg.setText(message == null ? "操作成功" : message);
        final Dialog dialog = new Dialog(activity);
        dialog.setContentView(binding.getRoot());
        dialog.setCancelable(true);
        Window w = dialog.getWindow();
        if (w != null) {
            w.setBackgroundDrawable(new ColorDrawable(Color.TRANSPARENT));
            w.setWindowAnimations(io.openim.android.ouicore.R.style.XianliaoDialogAnim);
        }
        dialog.show();
        binding.ivCheck.setScaleX(0.5f);
        binding.ivCheck.setScaleY(0.5f);
        binding.ivCheck.setAlpha(0f);
        binding.ivCheck.animate().scaleX(1f).scaleY(1f).alpha(1f)
            .setDuration(320)
            .setInterpolator(new OvershootInterpolator(1.6f))
            .start();
        new Handler(Looper.getMainLooper()).postDelayed(() -> {
            try {
                dialog.dismiss();
            } catch (Exception ignored) {
            }
            if (after != null) {
                after.run();
            }
        }, 1150);
    }

    /** 网关回页确认弹窗：“是否已经成功付款？” */
    public static void showPaidConfirm(Activity activity, String orderText,
                                       final Runnable onPaid, final Runnable onProblem) {
        dialog(activity)
            .setTitle("回到充值页")
            .setMessage("是否已经成功付款？\n" + (orderText == null ? "" : orderText))
            .setPositiveButton("是，已付款", (d, which) -> {
                if (onPaid != null) {
                    onPaid.run();
                }
            })
            .setNegativeButton("付款遇到问题？", (d, which) -> {
                if (onProblem != null) {
                    onProblem.run();
                }
            })
            .show();
    }

    /** 通用提示弹窗（用于尚无 App 内目的地的跳转占位，不编造跳转）。 */
    public static void showServiceNote(Activity activity, String title, String message) {
        dialog(activity)
            .setTitle(title == null ? "提示" : title)
            .setMessage(message == null ? "" : message)
            .setPositiveButton("确定", null)
            .show();
    }
}
