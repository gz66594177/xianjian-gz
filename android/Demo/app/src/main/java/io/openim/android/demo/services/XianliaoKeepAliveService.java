package io.openim.android.demo.services;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.os.Build;
import android.os.IBinder;

import androidx.annotation.Nullable;
import androidx.core.app.NotificationCompat;

import io.openim.android.demo.ui.main.MainActivity;

/**
 * 仙聊 IM 长连接保活前台服务。
 *
 * 背景：IM SDK（OpenIMClient）在 DemoApplication 里随应用进程初始化，长连接
 * 只活在应用进程里；切到其他 App 后进程随时可被系统回收，长连接就断了。
 * 本服务不做任何业务逻辑、不发消息、不读写资金数据，只以一个低优先级常驻
 * 通知把进程优先级抬到前台服务一档，让系统在内存紧张时最后才回收它。
 * 注意：前台服务不是“永不被杀”的保证，厂商激进省电策略仍可能杀进程，
 * 因此必须配合电池优化白名单（见 XianliaoKeepAlive）才有实际效果。
 * 退出登录时由 XianliaoKeepAlive.stop() 显式停止，退出后不偷跑。
 */
public class XianliaoKeepAliveService extends Service {

    public static final int NOTIFICATION_ID = 9001;
    public static final String CHANNEL_ID = "xianliao-keepalive";

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        createChannel();
        Notification notification = buildNotification();
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIFICATION_ID, notification,
                android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC);
        } else {
            startForeground(NOTIFICATION_ID, notification);
        }
        // 系统杀掉后尽量自动拉起（资源允许时）；真正断线后 SDK 自带重连，
        // 服务拉起只是让进程有机会继续跑，本身不重建 IM 连接。
        return START_STICKY;
    }

    @Override
    public void onTaskRemoved(Intent rootIntent) {
        // 用户从最近任务划掉 App 时，系统会调这里。划掉不等于退出登录，
        // 只要不是退出登录主动停的，就尝试把服务拉回来（后台起前台服务
        // 在 Android 12+ 上受限，失败时静默吞掉，不崩溃、不循环弹通知）。
        if (!XianliaoKeepAlive.isStoppedByLogout(this)) {
            try {
                XianliaoKeepAlive.start(this);
            } catch (Exception ignored) {
            }
        }
        super.onTaskRemoved(rootIntent);
    }

    private void createChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            NotificationChannel channel = new NotificationChannel(
                CHANNEL_ID, "仙聊后台运行", NotificationManager.IMPORTANCE_LOW);
            channel.setDescription("保持仙聊消息连接，切换到其他应用时也不会断线");
            NotificationManager manager =
                (NotificationManager) getSystemService(Context.NOTIFICATION_SERVICE);
            if (manager != null) {
                manager.createNotificationChannel(channel);
            }
        }
    }

    private Notification buildNotification() {
        Intent intent = new Intent(this, MainActivity.class);
        intent.setFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP);
        int flags = PendingIntent.FLAG_UPDATE_CURRENT;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
            flags |= PendingIntent.FLAG_IMMUTABLE;
        }
        PendingIntent pendingIntent =
            PendingIntent.getActivity(this, 0, intent, flags);
        return new NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("仙聊正在运行")
            .setContentText("保持消息连接，切换到其他应用也不会断线")
            .setSmallIcon(io.openim.android.demo.R.mipmap.ic_launcher)
            .setContentIntent(pendingIntent)
            .setOngoing(true)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .build();
    }

    @Nullable
    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
