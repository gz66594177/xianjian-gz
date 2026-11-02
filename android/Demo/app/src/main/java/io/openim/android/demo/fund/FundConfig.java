package io.openim.android.demo.fund;

import android.text.TextUtils;

import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.utils.SharedPreferencesUtil;

/**
 * 仙聊资金服务接入配置（Phase 10）。
 *
 * <p>资金服务地址以配置项接入，不把测试地址写进正式构建：
 * 默认走 im.yuni110.com 体系的 /fund/ 网关路径（nginx 代理到资金服务回环端口，
 * IM token 由调用方原样透传，资金服务以 token 解析结果为准）。
 * 调试需要指向测试资金实例时，可用 SharedPreferences 的 FUND_BASE_URL 覆盖，
 * 正式构建不得预置该值。</p>
 */
public final class FundConfig {

    private FundConfig() {
    }

    /** 默认资金网关（生产路径）。 */
    public static final String DEFAULT_BASE_URL = "https://im.yuni110.com/fund";

    /** 充值固定档位（分，CNY；与资金服务 API 文档 §13.1 推荐值一致，仅展示用）。 */
    public static final long[] RECHARGE_DENOMINATIONS_CENTS =
        new long[]{100, 500, 1000, 5000, 10000, 30000, 50000, 100000};

    /** 资金服务地址（配置项优先，缺省用默认网关）。 */
    public static String getBaseUrl() {
        String override = null;
        try {
            override = SharedPreferencesUtil.get(BaseApp.inst())
                .getString("FUND_BASE_URL");
        } catch (Exception ignored) {
        }
        if (!TextUtils.isEmpty(override)) {
            return trimTrailingSlash(override.trim());
        }
        return DEFAULT_BASE_URL;
    }

    static String trimTrailingSlash(String url) {
        while (url.endsWith("/")) {
            url = url.substring(0, url.length() - 1);
        }
        return url;
    }
}
