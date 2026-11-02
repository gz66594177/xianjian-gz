package io.openim.android.ouicore.utils;

import android.app.Activity;
import android.content.Context;
import android.content.res.Configuration;
import android.content.res.Resources;

/**
 * 深浅色实时跟随判定：跟随系统（MODE_NIGHT_FOLLOW_SYSTEM），不设手动开关。
 * 期望值取系统配置，当前值取 Activity 自身资源配置；两者不一致即需重建。
 */
public final class XlNightMode {
    /** onSaveInstanceState 里记录“已为哪个夜间模式重建过一次”，防重建死循环。 */
    public static final String KEY = "xl_night_recreate";

    private XlNightMode() {
    }

    public static int systemNightMode(Context context) {
        int mask = Resources.getSystem().getConfiguration().uiMode
            & Configuration.UI_MODE_NIGHT_MASK;
        if (mask == Configuration.UI_MODE_NIGHT_UNDEFINED && context != null) {
            mask = context.getApplicationContext().getResources().getConfiguration().uiMode
                & Configuration.UI_MODE_NIGHT_MASK;
        }
        return mask;
    }

    public static int activityNightMode(Activity activity) {
        return activity.getResources().getConfiguration().uiMode
            & Configuration.UI_MODE_NIGHT_MASK;
    }

    public static boolean needsRecreate(Activity activity) {
        int sys = systemNightMode(activity);
        int act = activityNightMode(activity);
        return sys != Configuration.UI_MODE_NIGHT_UNDEFINED
            && act != Configuration.UI_MODE_NIGHT_UNDEFINED
            && sys != act;
    }
}
