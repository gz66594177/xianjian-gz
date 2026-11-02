package io.openim.android.demo.fund;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Context;
import android.text.Editable;
import android.text.InputFilter;
import android.text.InputType;
import android.text.Spanned;
import android.text.TextWatcher;
import android.view.Gravity;
import android.view.KeyEvent;
import android.view.View;
import android.view.ViewGroup;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.TextView;

import androidx.core.content.ContextCompat;

/**
 * 交易密码输入弹窗：6 个小方格，每格 1 位数字。
 * 输满 6 位自动提交，不需要点确认；右上角 ❌ = 取消付款（关闭弹窗，不触发任何请求）。
 * 密码只在内存中流转到当次请求，绝不落盘、不进日志。
 * 视觉：shape_input（聚焦时主题色描边，日夜 token 已覆盖浅色/深色），文字走 txt_black/txt_shallow。
 *
 * 唯一用法 showForVerification()，全部调用方（发红包三类、提现、转账、提现方式设默认/解绑/修改）
 * 共用同一弹窗、同一标题“输入交易密码”：输满后弹窗不关、显示“核验中…”；
 * 核验成功调用方先 handle.complete() 关弹窗再继续，核验失败调用 handle.fail(msg)，
 * 弹窗清空 6 格、红字显示错误、允许重新输入，不会误发包、误提现、误转账。
 * 老 show() 接口已删除，旧的“输满即关弹窗、错了 toast 后重开”行为不再存在。
 */
public final class TxPasswordDialog {

    private TxPasswordDialog() {
    }

    public interface Verifier {
        void verify(String password, Handle handle);
    }

    public interface Handle {
        /** 核验成功：关弹窗，调用方随后继续原有成功逻辑。 */
        void complete();

        /** 核验失败：不清弹窗，清空 6 格、红字显示 msg、允许重新输入。 */
        void fail(String message);
    }

    public static void showForVerification(Activity activity, String title, final Verifier verifier) {
        open(activity, title, verifier);
    }

    private static void open(final Activity activity, final String title,
                             final Verifier verifier) {
        if (activity == null || activity.isFinishing()) {
            return;
        }
        final float density = activity.getResources().getDisplayMetrics().density;

        LinearLayout root = new LinearLayout(activity);
        root.setOrientation(LinearLayout.VERTICAL);
        int pad = (int) (20 * density);
        root.setPadding(pad, pad, pad, pad);

        // 标题行：标题 + 右上角 ❌（取消付款）
        LinearLayout header = new LinearLayout(activity);
        header.setOrientation(LinearLayout.HORIZONTAL);
        header.setGravity(Gravity.CENTER_VERTICAL);
        TextView tvTitle = new TextView(activity);
        tvTitle.setText(title == null ? "输入交易密码" : title);
        tvTitle.setTextSize(18);
        tvTitle.setTypeface(null, android.graphics.Typeface.BOLD);
        tvTitle.setTextColor(ContextCompat.getColor(activity,
            io.openim.android.ouicore.R.color.txt_black));
        header.addView(tvTitle, new LinearLayout.LayoutParams(
            0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f));
        final TextView tvClose = new TextView(activity);
        tvClose.setText("❌");
        tvClose.setTextSize(20);
        tvClose.setContentDescription("取消");
        tvClose.setPadding((int) (8 * density), (int) (8 * density),
            (int) (8 * density), (int) (8 * density));
        header.addView(tvClose, new LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        root.addView(header, new LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));

        TextView tvHint = new TextView(activity);
        tvHint.setText("请输入 6 位交易密码");
        tvHint.setTextSize(14);
        tvHint.setTextColor(ContextCompat.getColor(activity,
            io.openim.android.ouicore.R.color.txt_shallow));
        tvHint.setGravity(Gravity.CENTER_HORIZONTAL);
        LinearLayout.LayoutParams hintLp = new LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        hintLp.topMargin = (int) (12 * density);
        root.addView(tvHint, hintLp);

        // 状态共享（holder 数组，供闭包与提交逻辑共用）
        final EditText[] boxes = new EditText[6];
        final boolean[] internalChange = new boolean[]{false};
        final boolean[] submitted = new boolean[]{false};
        final boolean[] verifying = new boolean[]{false};
        final boolean[] cancelled = new boolean[]{false};
        final AlertDialog[] dialogRef = new AlertDialog[1];
        final TextView[] statusRef = new TextView[1];
        final TextView[] closeRef = new TextView[]{tvClose};
        // 提交入口先声明、后赋值：循环里的 TextWatcher/粘贴回调只调 submit[0].run()
        final Runnable[] submit = new Runnable[1];

        LinearLayout row = new LinearLayout(activity);
        row.setOrientation(LinearLayout.HORIZONTAL);
        LinearLayout.LayoutParams rowLp = new LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        rowLp.topMargin = (int) (16 * density);
        for (int i = 0; i < 6; i++) {
            final int index = i;
            final EditText box = new EditText(activity);
            box.setBackgroundResource(io.openim.android.ouicore.R.drawable.shape_input);
            box.setTextColor(ContextCompat.getColor(activity,
                io.openim.android.ouicore.R.color.txt_black));
            box.setInputType(InputType.TYPE_CLASS_NUMBER
                | InputType.TYPE_NUMBER_VARIATION_PASSWORD);
            box.setGravity(Gravity.CENTER);
            box.setTextSize(22);
            box.setTypeface(null, android.graphics.Typeface.BOLD);
            box.setFilters(new InputFilter[]{
                new InputFilter.LengthFilter(1),
                new InputFilter() {
                    @Override
                    public CharSequence filter(CharSequence source, int start, int end,
                                               Spanned dest, int dstart, int dend) {
                        if (source == null || internalChange[0]) {
                            return null;
                        }
                        StringBuilder digits = new StringBuilder();
                        for (int k = start; k < end; k++) {
                            char c = source.charAt(k);
                            if (c >= '0' && c <= '9') {
                                digits.append(c);
                            }
                        }
                        if (digits.length() == 0) {
                            return "";
                        }
                        if (digits.length() > 1) {
                            // 粘贴多位：拦截并分发到后续格子
                            final String all = digits.toString();
                            box.post(() -> {
                                internalChange[0] = true;
                                try {
                                    for (int j = 0; j < all.length() && index + j < 6; j++) {
                                        boxes[index + j].setText(String.valueOf(all.charAt(j)));
                                    }
                                } finally {
                                    internalChange[0] = false;
                                }
                                boxes[Math.min(index + all.length() - 1, 5)].requestFocus();
                                if (submit[0] != null) {
                                    submit[0].run();
                                }
                            });
                            return "";
                        }
                        return null;
                    }
                }
            });
            box.addTextChangedListener(new TextWatcher() {
                @Override
                public void beforeTextChanged(CharSequence s, int start, int count, int after) {
                }

                @Override
                public void onTextChanged(CharSequence s, int start, int before, int count) {
                }

                @Override
                public void afterTextChanged(Editable s) {
                    if (internalChange[0] || verifying[0]) {
                        return;
                    }
                    if (s.length() == 0) {
                        return;
                    }
                    char c = s.charAt(0);
                    if (c < '0' || c > '9') {
                        internalChange[0] = true;
                        box.setText("");
                        internalChange[0] = false;
                        return;
                    }
                    if (index < 5) {
                        boxes[index + 1].requestFocus();
                    } else if (submit[0] != null) {
                        submit[0].run();
                    }
                }
            });
            // 空格按删除：退到上一格并清空上一格
            box.setOnKeyListener((v, keyCode, event) -> {
                if (keyCode == KeyEvent.KEYCODE_DEL && event.getAction() == KeyEvent.ACTION_DOWN
                    && box.getText().length() == 0 && index > 0 && !verifying[0]) {
                    internalChange[0] = true;
                    boxes[index - 1].setText("");
                    internalChange[0] = false;
                    boxes[index - 1].requestFocus();
                    return true;
                }
                return false;
            });
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                0, (int) (56 * density), 1f);
            lp.leftMargin = (int) (4 * density);
            lp.rightMargin = (int) (4 * density);
            row.addView(box, lp);
            boxes[i] = box;
        }
        root.addView(row, rowLp);

        final TextView tvStatus = new TextView(activity);
        tvStatus.setTextSize(14);
        tvStatus.setGravity(Gravity.CENTER_HORIZONTAL);
        tvStatus.setVisibility(View.GONE);
        LinearLayout.LayoutParams statusLp = new LinearLayout.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        statusLp.topMargin = (int) (12 * density);
        root.addView(tvStatus, statusLp);
        statusRef[0] = tvStatus;

        // 提交逻辑：输满 6 位自动触发（无确认按钮）
        submit[0] = () -> {
            if (submitted[0] || verifying[0] || cancelled[0]) {
                return;
            }
            StringBuilder sb = new StringBuilder();
            for (EditText b : boxes) {
                if (b.getText().length() != 1) {
                    return;
                }
                sb.append(b.getText().toString());
            }
            String password = sb.toString();
            if (password.length() != 6) {
                return;
            }
            submitted[0] = true;
            verifying[0] = true;
            for (EditText b : boxes) {
                b.setEnabled(false);
            }
            closeRef[0].setClickable(false);
            closeRef[0].setAlpha(0.3f);
            tvStatus.setText("核验中…");
            tvStatus.setTextColor(ContextCompat.getColor(activity,
                io.openim.android.ouicore.R.color.txt_shallow));
            tvStatus.setVisibility(View.VISIBLE);
            AlertDialog d = dialogRef[0];
            if (d != null) {
                d.setCancelable(false);
            }
            final Handle handle = new Handle() {
                private boolean done = false;

                @Override
                public void complete() {
                    if (done || cancelled[0]) {
                        return;
                    }
                    done = true;
                    AlertDialog dd = dialogRef[0];
                    if (dd != null) {
                        dd.dismiss();
                    }
                }

                @Override
                public void fail(String message) {
                    if (done || cancelled[0]) {
                        return;
                    }
                    AlertDialog dd = dialogRef[0];
                    if (dd == null || !dd.isShowing()) {
                        return;
                    }
                    verifying[0] = false;
                    submitted[0] = false;
                    internalChange[0] = true;
                    try {
                        for (EditText b : boxes) {
                            b.setText("");
                            b.setEnabled(true);
                        }
                    } finally {
                        internalChange[0] = false;
                    }
                    closeRef[0].setClickable(true);
                    closeRef[0].setAlpha(1f);
                    dd.setCancelable(true);
                    tvStatus.setText(message == null || message.isEmpty()
                        ? "交易密码错误，请重新输入" : message);
                    tvStatus.setTextColor(ContextCompat.getColor(activity,
                        io.openim.android.ouicore.R.color.redpacket_red));
                    tvStatus.setVisibility(View.VISIBLE);
                    boxes[0].requestFocus();
                    InputMethodManager imm = (InputMethodManager) activity.getSystemService(
                        Context.INPUT_METHOD_SERVICE);
                    if (imm != null) {
                        imm.showSoftInput(boxes[0], InputMethodManager.SHOW_IMPLICIT);
                    }
                }
            };
            verifier.verify(password, handle);
        };

        AlertDialog dialog = FundUi.dialog(activity)
            .setView(root)
            .setCancelable(true)
            .create();
        dialogRef[0] = dialog;
        dialog.setOnCancelListener(d -> cancelled[0] = true);
        tvClose.setOnClickListener(v -> {
            if (!verifying[0]) {
                dialog.dismiss();
            }
        });
        dialog.show();
        boxes[0].post(() -> {
            boxes[0].requestFocus();
            InputMethodManager imm = (InputMethodManager) activity.getSystemService(
                Context.INPUT_METHOD_SERVICE);
            if (imm != null) {
                imm.showSoftInput(boxes[0], InputMethodManager.SHOW_IMPLICIT);
            }
        });
    }
}
