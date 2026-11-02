package io.openim.android.demo.services;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.net.Uri;
import android.os.Build;
import android.os.PowerManager;
import android.provider.Settings;

import androidx.core.content.ContextCompat;

/**
 * 后台保活入口：启动/停止前台服务 + 电池优化白名单引导。
 *
 * 必须向用户明说的系统限制：Android 不允许 App 静默把自己加进电池优化
 * 白名单，必须由用户在系统授权页上点一次“允许”。所以这里只能做到：
 * 首次进入主界面时自动检测，未在白名单就主动打开系统授权页请用户点一次；
 * 用户点过（无论允许还是拒绝）之后不再重复骚扰。拒绝也不影响前台服务
 * 本身启动，只是厂商省电策略仍可能杀进程，保活效果会打折。
 */
public final class XianliaoKeepAlive {

    private static final String PREFS = "xianliao_keepalive";
    private static final String KEY_BATTERY_PROMPTED = "battery_prompted";
    private static final String KEY_STOPPED_BY_LOGOUT = "stopped_by_logout";

    private XianliaoKeepAlive() {
    }

    /** 登录后进入主界面时调用：拉起保活服务，并（仅首次）引导电池优化授权。 */
    public static void startOnMainScreen(Activity activity) {
        setStoppedByLogout(activity, false);
        start(activity);
        maybeRequestBatteryOptimization(activity);
    }

    public static void start(Context context) {
        Intent intent = new Intent(context, XianliaoKeepAliveService.class);
        try {
            ContextCompat.startForegroundService(context, intent);
        } catch (Exception ignored) {
            // Android 12+ 后台起前台服务受限时静默失败，不崩溃；
            // 下次回到前台（主界面）时 startOnMainScreen 会再拉一次。
        }
    }

    /** 退出登录时调用：停止保活服务，退出后不偷跑。 */
    public static void stop(Context context) {
        setStoppedByLogout(context, true);
        try {
            context.stopService(new Intent(context, XianliaoKeepAliveService.class));
        } catch (Exception ignored) {
        }
    }

    public static boolean isStoppedByLogout(Context context) {
        return prefs(context).getBoolean(KEY_STOPPED_BY_LOGOUT, false);
    }

    private static void setStoppedByLogout(Context context, boolean stopped) {
        prefs(context).edit().putBoolean(KEY_STOPPED_BY_LOGOUT, stopped).apply();
    }

    public static boolean isIgnoringBatteryOptimizations(Context context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) {
            return true;
        }
        PowerManager pm = (PowerManager) context.getSystemService(Context.POWER_SERVICE);
        return pm != null && pm.isIgnoringBatteryOptimizations(context.getPackageName());
    }

    /**
     * 仅首次调用：未在白名单时打开系统授权页。用户点过一次后记 flag，
     * 之后进入主界面不再重复弹出。
     */
    public static void maybeRequestBatteryOptimization(Activity activity) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) {
            return;
        }
        if (isIgnoringBatteryOptimizations(activity)) {
            return;
        }
        SharedPreferences prefs = prefs(activity);
        if (prefs.getBoolean(KEY_BATTERY_PROMPTED, false)) {
            return;
        }
        prefs.edit().putBoolean(KEY_BATTERY_PROMPTED, true).apply();
        try {
            Intent intent = new Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS);
            intent.setData(Uri.parse("package:" + activity.getPackageName()));
            activity.startActivity(intent);
        } catch (Exception e) {
            // 部分厂商 ROM 不支持直接授权页，退到系统电池优化设置列表页。
            try {
                activity.startActivity(
                    new Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS));
            } catch (Exception ignored) {
            }
        }
    }

    private static SharedPreferences prefs(Context context) {
        return context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }
}
