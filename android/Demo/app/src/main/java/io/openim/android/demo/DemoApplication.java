package io.openim.android.demo;

import android.app.Activity;
import android.view.LayoutInflater;
import io.openim.android.demo.databinding.DialogFundRedpacketTypeBinding;
import io.openim.android.demo.fund.FundUi;
import io.openim.android.ouicore.widget.BottomPopDialog;
import android.content.Context;
import android.content.Intent;

import androidx.appcompat.app.AppCompatDelegate;
import androidx.multidex.MultiDex;

import com.alibaba.android.arouter.launcher.ARouter;
import com.tencent.bugly.crashreport.CrashReport;
import com.vanniktech.emoji.EmojiManager;
import com.vanniktech.emoji.google.GoogleEmojiProvider;

import java.io.File;

import io.openim.android.demo.fund.FundApi;
import io.openim.android.demo.fund.RedPacket;
import io.openim.android.demo.fund.RedPacketDetailActivity;
import io.openim.android.demo.fund.SendRedPacketActivity;
import io.openim.android.demo.ui.login.LoginActivity;
import io.openim.android.ouiconversation.RedPacketBridge;
import io.openim.android.ouicore.BuildConfig;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.base.vm.injection.Easy;
import io.openim.android.ouicore.entity.LoginCertificate;
import io.openim.android.ouicore.im.IM;
import io.openim.android.ouicore.im.IMEvent;
import io.openim.android.ouicore.net.RXRetrofit.HttpConfig;
import io.openim.android.ouicore.net.RXRetrofit.N;
import io.openim.android.ouicore.utils.ActivityManager;
import io.openim.android.ouicore.utils.Constants;
import io.openim.android.ouicore.utils.L;
import io.openim.android.ouicore.vm.UserLogic;
import io.openim.android.ouicore.voice.SPlayer;
import io.openim.android.sdk.listener.OnConnListener;
import okhttp3.Request;


public class DemoApplication extends BaseApp {
    private static final String TAG = BaseApp.class.getSimpleName();

    @Override
    public void onCreate() {
        // 双主题：跟随手机系统浅色/深色切换，不另设手动开关。
        AppCompatDelegate.setDefaultNightMode(AppCompatDelegate.MODE_NIGHT_FOLLOW_SYSTEM);
        L.e(TAG, "-----onCreate------");
        super.onCreate();
        MultiDex.install(this);

        initFile();
        initARouter();
        initController();
        initNet();
        initBugly();
        initIM();
        initFundBridge();

        EmojiManager.install(new GoogleEmojiProvider());
        //音频播放
        SPlayer.init(this);
        SPlayer.instance().setCacheDirPath(Constants.AUDIO_DIR);
    }

    /**
     * Phase 10：注册红包 OUI 桥的 Demo 侧实现。
     * OUI 触点只经此桥打开资金页、回查包状态；OUI 层不直接依赖 Demo 类。
     */
    private void initFundBridge() {
        RedPacketBridge.handler = new RedPacketBridge.Handler() {
            @Override
            public void openDetail(Context context, String kind, String packetId) {
                Intent intent = new Intent(context, RedPacketDetailActivity.class);
                intent.putExtra(RedPacketDetailActivity.EXTRA_KIND, kind);
                intent.putExtra(RedPacketDetailActivity.EXTRA_PACKET_ID, packetId);
                startFrom(context, intent);
            }

            @Override
            public void openSend(Context context, String userId, String groupId) {
                if (groupId != null && !groupId.isEmpty()) {
                    // 群发红包类型选择：底部动画弹窗（复用注册弹窗 BottomPopDialog，
                    // 上滑+淡入 280ms / 退场 220ms），不再用居中系统弹窗。
                    // 选项与跳转原样：普通=群普通红包，拼手气=群拼手气红包，取消只关弹窗。
                    DialogFundRedpacketTypeBinding popup =
                        DialogFundRedpacketTypeBinding.inflate(LayoutInflater.from(context));
                    BottomPopDialog dialog = new BottomPopDialog(context, popup.getRoot());
                    popup.rowGroup.setOnClickListener(v -> {
                        dialog.dismiss();
                        openGroupSend(context, groupId, RedPacket.KIND_GROUP);
                    });
                    popup.rowLucky.setOnClickListener(v -> {
                        dialog.dismiss();
                        openGroupSend(context, groupId, RedPacket.KIND_LUCKY);
                    });
                    popup.btnCancel.setOnClickListener(v -> dialog.dismiss());
                    FundUi.pressScale(popup.rowGroup, popup.rowLucky, popup.btnCancel);
                    dialog.show();
                } else {
                    Intent intent = new Intent(context, SendRedPacketActivity.class);
                    intent.putExtra(SendRedPacketActivity.EXTRA_TARGET_USER_ID, userId);
                    intent.putExtra(SendRedPacketActivity.EXTRA_KIND, RedPacket.KIND_FRIEND);
                    startFrom(context, intent);
                }
            }

            @Override
            public void fetchStatus(String kind, String packetId, RedPacketBridge.StatusCallback cb) {
                RedPacket.getDetail(kind, packetId, new FundApi.Callback() {
                    @Override
                    public void onOk(Object data) {
                        org.json.JSONObject packet = RedPacket.extractPacket(data);
                        if (packet != null) {
                            cb.onResult(packet);
                        } else {
                            cb.onError();
                        }
                    }

                    @Override
                    public void onErr(String code, String message) {
                        cb.onError();
                    }
                });
            }

            private void openGroupSend(Context context, String groupId, String kind) {
                Intent intent = new Intent(context, SendRedPacketActivity.class);
                intent.putExtra(SendRedPacketActivity.EXTRA_TARGET_GROUP_ID, groupId);
                intent.putExtra(SendRedPacketActivity.EXTRA_KIND, kind);
                startFrom(context, intent);
            }

            private void startFrom(Context context, Intent intent) {
                if (!(context instanceof Activity)) {
                    intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                }
                context.startActivity(intent);
            }
        };
    }

    private void initFile() {
        buildDirectory(Constants.AUDIO_DIR);
        buildDirectory(Constants.VIDEO_DIR);
        buildDirectory(Constants.PICTURE_DIR);
        buildDirectory(Constants.File_DIR);
    }

    private boolean buildDirectory(String path) {
        File file = new File(path);
        if (file.exists())
            return true;
        return file.mkdirs();
    }

    private void initARouter() {
        ARouter.init(this);
//        if (io.openim.android.demo.BuildConfig.DEBUG){
//            ARouter.openLog();
//            ARouter.openDebug();
//        }
    }

    private void initBugly() {
        CrashReport.setAppChannel(this, BuildConfig.DEBUG ? "debug" : "release");
        CrashReport.initCrashReport(getApplicationContext(), "4d365d80d1", L.isDebug);
    }


    private void initController() {
        Easy.installVM(UserLogic.class);
    }

    private void initNet() {
        N.init(new HttpConfig().setBaseUrl(Constants.getAppAuthUrl()).setDebug(BuildConfig.DEBUG)
            .addInterceptor(chain -> {
            String token = "";
            try {
                token = BaseApp.inst().loginCertificate.chatToken;
            } catch (Exception ignored) {}
            Request request = chain.request().newBuilder()
                .addHeader("token", token)
                .addHeader("operationID", String.valueOf(System.currentTimeMillis()))
                .build();
                return chain.proceed(request);
        }));
    }

    private void initIM() {
        IM.initSdk(this);
        listenerIMOffline();
    }

    private void listenerIMOffline() {
        IMEvent.getInstance().addConnListener(new OnConnListener() {
            @Override
            public void onConnectFailed(int code, String error) {

            }

            @Override
            public void onConnectSuccess() {

            }

            @Override
            public void onConnecting() {

            }

            @Override
            public void onKickedOffline() {
                offline();
            }

            @Override
            public void onUserTokenExpired() {
                offline();
            }

            @Override
            public void onUserTokenInvalid(String reason) {
                offline();
            }

        });
    }

    public void offline() {
        LoginCertificate.clear();

        ActivityManager.finishAllExceptActivity();
        startActivity(new Intent(BaseApp.inst(), LoginActivity.class).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
//        if (dsConnectionStatus)
//            unbindService(serviceConnection);
    }
}
