package io.openim.android.demo.update;

import android.app.Activity;
import android.app.DownloadManager;
import android.content.Context;
import android.content.pm.PackageInfo;
import android.net.Uri;
import android.os.Environment;
import android.os.Handler;
import android.os.Looper;
import android.widget.Toast;

import androidx.appcompat.app.AlertDialog;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * 仙聊自有更新检查：不再走 Demo 原生的 Pgyer（那个 appKey 是官方 Demo 的，永远查不到仙聊的包）。
 * 版本描述文件由发布流程随 APK 一起放到 https://im.yuni110.com/dl-p10/version.json，
 * 字段：versionCode / versionName / apkUrl / fileSize / md5 / updateLog。
 * 有新版时弹提示，用户点“立即下载”走系统 DownloadManager 下载，下载完成点通知安装；
 * 无新版时直接提示已是最新版本。
 */
public final class XianliaoUpdate {

    public static final String VERSION_URL = "https://im.yuni110.com/dl-p10/version.json";

    private static final ExecutorService EXEC = Executors.newSingleThreadExecutor();
    private static final Handler MAIN = new Handler(Looper.getMainLooper());

    private XianliaoUpdate() {
    }

    public static void check(Activity activity) {
        EXEC.execute(() -> {
            try {
                HttpURLConnection conn = (HttpURLConnection) new URL(VERSION_URL).openConnection();
                conn.setConnectTimeout(15000);
                conn.setReadTimeout(30000);
                conn.setRequestMethod("GET");
                int code = conn.getResponseCode();
                if (code != 200) {
                    toast(activity, "检查失败（" + code + "），请稍后重试");
                    return;
                }
                StringBuilder sb = new StringBuilder();
                try (BufferedReader r = new BufferedReader(new InputStreamReader(
                    conn.getInputStream(), StandardCharsets.UTF_8))) {
                    String line;
                    while ((line = r.readLine()) != null) sb.append(line);
                }
                JSONObject v = new JSONObject(sb.toString());
                int serverCode = v.optInt("versionCode", 0);
                String serverName = v.optString("versionName", "");
                String apkUrl = v.optString("apkUrl", "");
                String log = v.optString("updateLog", "");
                long size = v.optLong("fileSize", 0);
                int current = currentVersionCode(activity);
                MAIN.post(() -> {
                    if (serverCode <= 0 || apkUrl.isEmpty()) {
                        Toast.makeText(activity, "检查失败：版本信息不完整", Toast.LENGTH_SHORT).show();
                        return;
                    }
                    if (serverCode <= current) {
                        Toast.makeText(activity, "已是最新版本", Toast.LENGTH_SHORT).show();
                        return;
                    }
                    String msg = "新版本 " + serverName + "\n" + log
                        + (size > 0 ? "\n大小约 " + (size / 1024 / 1024) + " MB" : "");
                    new AlertDialog.Builder(activity)
                        .setTitle("发现新版本")
                        .setMessage(msg)
                        .setPositiveButton("立即下载", (d, w) -> download(activity, apkUrl, serverName))
                        .setNegativeButton("稍后", null)
                        .show();
                });
            } catch (Exception e) {
                toast(activity, "检查失败：" + e.getMessage());
            }
        });
    }

    private static void download(Activity activity, String apkUrl, String versionName) {
        try {
            DownloadManager.Request req = new DownloadManager.Request(Uri.parse(apkUrl));
            req.setTitle("仙聊 " + versionName);
            req.setDescription("正在下载最新版本");
            req.setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED);
            req.setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS,
                "xianliao-" + versionName + ".apk");
            req.setAllowedOverMetered(true);
            ((DownloadManager) activity.getSystemService(Context.DOWNLOAD_SERVICE)).enqueue(req);
            Toast.makeText(activity, "开始下载，完成后点通知安装", Toast.LENGTH_SHORT).show();
        } catch (Exception e) {
            Toast.makeText(activity, "下载启动失败：" + e.getMessage(), Toast.LENGTH_SHORT).show();
        }
    }

    private static int currentVersionCode(Activity activity) {
        try {
            PackageInfo info = activity.getPackageManager().getPackageInfo(activity.getPackageName(), 0);
            if (android.os.Build.VERSION.SDK_INT >= 28) return (int) info.getLongVersionCode();
            return info.versionCode;
        } catch (Exception ignored) {
            return 0;
        }
    }

    private static void toast(Activity activity, String msg) {
        MAIN.post(() -> Toast.makeText(activity, msg, Toast.LENGTH_SHORT).show());
    }
}
