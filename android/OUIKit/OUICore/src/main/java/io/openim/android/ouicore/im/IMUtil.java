package io.openim.android.ouicore.im;

import android.app.Activity;
import android.app.Notification;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.graphics.Color;
import android.net.Uri;
import android.os.Vibrator;
import android.text.Spannable;
import android.text.SpannableStringBuilder;
import android.text.TextPaint;
import android.text.TextUtils;
import android.text.style.ClickableSpan;
import android.text.style.ForegroundColorSpan;
import android.util.Patterns;
import android.view.LayoutInflater;
import android.view.MotionEvent;
import android.view.animation.DecelerateInterpolator;
import android.view.View;

import androidx.annotation.ColorRes;
import androidx.annotation.NonNull;
import androidx.appcompat.app.AppCompatActivity;

import com.alibaba.android.arouter.core.LogisticsCenter;
import com.alibaba.android.arouter.facade.Postcard;
import com.alibaba.android.arouter.launcher.ARouter;
import com.alibaba.fastjson.JSONArray;
import com.alibaba.fastjson2.JSONObject;
import com.bumptech.glide.Glide;
import com.bumptech.glide.RequestBuilder;
import com.google.gson.reflect.TypeToken;
import com.hjq.permissions.Permission;

import org.jetbrains.annotations.NotNull;

import java.lang.ref.WeakReference;
import java.lang.reflect.Type;
import java.math.BigDecimal;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import io.openim.android.ouicore.R;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.databinding.LayoutBottompopV3Binding;
import io.openim.android.ouicore.entity.AtMsgInfo;
import io.openim.android.ouicore.entity.BurnAfterReadingNotification;
import io.openim.android.ouicore.entity.CallHistory;
import io.openim.android.ouicore.entity.CustomEmojiEntity;
import io.openim.android.ouicore.entity.EnterGroupNotification;
import io.openim.android.ouicore.entity.GroupNotification;
import io.openim.android.ouicore.entity.GroupRightsTransferNotification;
import io.openim.android.ouicore.entity.JoinKickedGroupNotification;
import io.openim.android.ouicore.entity.LocationInfo;
import io.openim.android.ouicore.entity.LoginCertificate;
import io.openim.android.ouicore.entity.MeetingInfo;
import io.openim.android.ouicore.entity.MsgConversation;
import io.openim.android.ouicore.entity.MsgExpand;
import io.openim.android.ouicore.entity.MuteMemberNotification;
import io.openim.android.ouicore.entity.NotificationMsg;
import io.openim.android.ouicore.entity.OANotification;
import io.openim.android.ouicore.entity.QuitGroupNotification;
import io.openim.android.ouicore.ex.AtUser;
import io.openim.android.ouicore.ex.MultipleChoice;
import io.openim.android.ouicore.net.bage.GsonHel;
import io.openim.android.ouicore.utils.ActivityManager;
import io.openim.android.ouicore.utils.Constants;
import io.openim.android.ouicore.utils.GetFilePathFromUri;
import io.openim.android.ouicore.utils.HasPermissions;
import io.openim.android.ouicore.utils.L;
import io.openim.android.ouicore.utils.MediaPlayerUtil;
import io.openim.android.ouicore.utils.NotificationUtil;
import io.openim.android.ouicore.utils.Routes;
import io.openim.android.ouicore.utils.SharedPreferencesUtil;
import io.openim.android.ouicore.utils.TimeUtil;
import io.openim.android.ouicore.widget.BottomPopDialog;
import io.openim.android.ouicore.widget.PlaceHolderDrawable;
import io.openim.android.ouicore.widget.WebViewActivity;
import io.openim.android.sdk.OpenIMClient;
import io.openim.android.sdk.enums.ConversationType;
import io.openim.android.sdk.enums.GroupAtType;
import io.openim.android.sdk.enums.LoginStatus;
import io.openim.android.sdk.enums.MessageType;
import io.openim.android.sdk.listener.OnBase;
import io.openim.android.sdk.models.AtUserInfo;
import io.openim.android.sdk.models.ConversationInfo;
import io.openim.android.sdk.models.GroupMembersInfo;
import io.openim.android.sdk.models.Message;
import io.openim.android.sdk.models.NotificationElem;
import io.openim.android.sdk.models.OfflinePushInfo;
import io.openim.android.sdk.models.PictureElem;
import io.openim.android.sdk.models.PublicUserInfo;
import io.openim.android.sdk.models.QuoteElem;
import io.openim.android.sdk.models.RevokedInfo;
import io.openim.android.sdk.models.SignalingInfo;
import io.openim.android.sdk.models.SignalingInvitationInfo;
import io.openim.android.sdk.models.TextElem;
import io.openim.android.sdk.models.VideoElem;

public class IMUtil {
    //android PlatformID 2
    public static final int PLATFORM_ID = 2;
    public static final String AT_ALL = "AtAllTag";
    private static final String TAG = "IMUtil";

    /**
     * 会话排序比较器
     */
    public static Comparator<MsgConversation> simpleComparator() {
        return (a, b) -> {
            if ((a.conversationInfo.isPinned() && b.conversationInfo.isPinned()) || (!a.conversationInfo.isPinned() && !b.conversationInfo.isPinned())) {
                long aCompare = Math.max(a.conversationInfo.getDraftTextTime(),
                    a.conversationInfo.getLatestMsgSendTime());
                long bCompare = Math.max(b.conversationInfo.getDraftTextTime(),
                    b.conversationInfo.getLatestMsgSendTime());
                return Long.compare(bCompare, aCompare);
            } else if (a.conversationInfo.isPinned() && !b.conversationInfo.isPinned()) {
                return -1;
            } else {
                return 1;
            }
        };
    }

    /**
     * 设置时间显示
     *
     * @param list
     * @return
     */
    public static List<Message> calChatTimeInterval(List<Message> list) {
        long lastShowTimeStamp = 0;
        for (int i = list.size() - 1; i >= 0; i--) {
            Message message = list.get(i);
            MsgExpand msgExpand = (MsgExpand) message.getExt();
            if (null == msgExpand) msgExpand = new MsgExpand();
            //重置
            msgExpand.isShowTime = false;
            if (message.getContentType() >= MessageType.NTF_BEGIN || message.getContentType() == MessageType.REVOKE_MESSAGE_NTF)
                continue;

            if (lastShowTimeStamp == 0 || (message.getSendTime() - lastShowTimeStamp > (1000 * 60 * 10))) {
                lastShowTimeStamp = message.getSendTime();
                msgExpand.isShowTime = true;
            }
            message.setExt(msgExpand);
        }
        return list;
    }

    /**
     * 是信令消息
     * @param msg
     * @return
     */
    public static boolean isSignalingMsg(Message msg) {
        if (msg.getContentType() == MessageType.CUSTOM) {
            Map map = JSONArray.parseObject(msg.getCustomElem().getData(), Map.class);
            if (map.containsKey(Constants.K_CUSTOM_TYPE)) {
                int customType = (int) map.get(Constants.K_CUSTOM_TYPE);
                return customType >= Constants.MsgType.callingInvite
                    && customType <= Constants.MsgType.callingHungup;
            }
        }
        return false;
    }

    public static Message buildExpandInfo(Message msg) {
        MsgExpand msgExpand = (MsgExpand) msg.getExt();
        if (null == msgExpand) msgExpand = new MsgExpand();
        msg.setExt(msgExpand);
        try {
            if (msg.getContentType() == MessageType.TEXT) {
                TextElem textElem = msg.getTextElem();
                SpannableStringBuilder stringBuilder = new SpannableStringBuilder(textElem.getContent());
                msgExpand.sequence = handleHyperLink(stringBuilder);
            }
        } catch (Exception e) {
            L.e(e.getMessage());
        }
        msg.setExt(msgExpand);

        return msg;
    }

    /**
     * 获取名字
     *
     * @return
     */
    public static String getSelfName(String uid, String nickName) {
        if (uid.equals(BaseApp.inst().loginCertificate.userID))
            return BaseApp.inst().getString(R.string.you);
        return nickName.trim();
    }

    public static CharSequence twoPeopleRevoker(Message msg, RevokedInfo revokedInfo, String txt) {
        List<MultipleChoice> choices = new ArrayList<>();
        MultipleChoice choice = new MultipleChoice(revokedInfo.getRevokerID());
        choice.name = IMUtil.getSelfName(revokedInfo.getRevokerID(),
            revokedInfo.getRevokerNickname());
        choice.groupId = msg.getGroupID();
        choices.add(choice);

        MultipleChoice choice2 = new MultipleChoice(revokedInfo.getSourceMessageSendID());
        choice2.name = IMUtil.getSelfName(revokedInfo.getSourceMessageSendID(),
            revokedInfo.getSourceMessageSenderNickname());
        choice2.groupId = msg.getGroupID();
        choices.add(choice2);

        return getMultipleSequence(new SpannableStringBuilder(txt), choices);
    }

    /**
     * 获取多跳转的Spannable
     *
     * @param sequence
     * @param choices
     * @return
     */
    private static CharSequence getMultipleSequence(CharSequence sequence,
                                                    List<MultipleChoice> choices) {
        for (MultipleChoice choice : choices) {
            buildClickAndColorSpannable((SpannableStringBuilder) sequence, choice.name,
                new ClickableSpan() {
                @Override
                public void onClick(@NonNull View widget) {
                    toPersonDetail(choice.key, choice.groupId);
                }
            });
        }
        return sequence;
    }

    /**
     * 获取只有单跳转的Spannable
     *
     * @param nickName 点击跳转的名字
     * @param uid      用户id
     * @param txt      显示文本
     * @return
     */
    @NonNull
    public static CharSequence getSingleSequence(String groupId, String nickName, String uid,
                                                 String txt) {
        return buildClickAndColorSpannable(new SpannableStringBuilder(txt), nickName,
            new ClickableSpan() {
            @Override
            public void onClick(@NonNull View widget) {
                toPersonDetail(uid, groupId);
            }
        });
    }

    private static void toPersonDetail(String uid, String groupId) {
        ARouter.getInstance().build(Routes.Main.PERSON_DETAIL).withString(Constants.K_ID, uid).withString(Constants.K_GROUP_ID, groupId).navigation();
    }

    private static SpannableStringBuilder handleHyperLink(@NonNull SpannableStringBuilder spannableString) {
        Matcher matcher = Patterns.WEB_URL.matcher(spannableString);
        while (matcher.find()) {
            int linkStartIndex = matcher.start();
            int linkEndIndex = matcher.end();
            String link = matcher.group();
            spannableString.setSpan(new ClickableSpan() {
                @Override
                public void onClick(@NonNull View widget) {
                    try {
                            widget.getContext().startActivity(new Intent(widget.getContext(), WebViewActivity.class).putExtra(WebViewActivity.LOAD_URL, link));
                    } catch (Exception e) {
                        if (!TextUtils.isEmpty(e.getMessage()))
                            L.e(e.getMessage());
                    }
                }
            }, linkStartIndex, linkEndIndex, Spannable.SPAN_EXCLUSIVE_INCLUSIVE);
        }
        return spannableString;
    }

    public static CharSequence buildClickAndColorSpannable(@NotNull SpannableStringBuilder spannableString, String tag, ClickableSpan clickableSpan) {
        return buildClickAndColorSpannable(spannableString, tag, R.color.theme, clickableSpan);
    }

    public static CharSequence buildClickAndColorSpannable(@NotNull SpannableStringBuilder spannableString, String tag, @ColorRes int colorId, ClickableSpan clickableSpan) {
        try {
            ForegroundColorSpan colorSpan =
                new ForegroundColorSpan(BaseApp.inst().getResources().getColor(colorId));
            int start = spannableString.toString().indexOf(tag);
            int end = spannableString.toString().indexOf(tag) + tag.length();
            if (null != clickableSpan)
                spannableString.setSpan(clickableSpan, start, end,
                    Spannable.SPAN_EXCLUSIVE_INCLUSIVE);
            spannableString.setSpan(colorSpan, start, end, Spannable.SPAN_EXCLUSIVE_EXCLUSIVE);
        } catch (Exception ignore) {
        }
        return spannableString;
    }




    /**
     * 通话记录本地消息（CallDialog.insertChatHistory 写入，customType=-110）转气泡文案，
     * 如“语音通话 00:32 / 视频通话 未接通”。非通话记录消息返回 null，走原有分支；
     * 绝不把未知类型伪装成文本。
     */
    public static String getCallHistoryText(Message msg) {
        try {
            if (null == msg || msg.getContentType() != MessageType.CUSTOM || null == msg.getCustomElem() || TextUtils.isEmpty(msg.getCustomElem().getData()))
                return null;
            Map map = JSONArray.parseObject(msg.getCustomElem().getData(), Map.class);
            if (null == map || !map.containsKey(Constants.K_CUSTOM_TYPE)) return null;
            int customType = ((Number) map.get(Constants.K_CUSTOM_TYPE)).intValue();
            if (customType != Constants.MsgType.LOCAL_CALL_HISTORY) return null;
            Object dataObj = map.get(Constants.K_DATA);
            if (!(dataObj instanceof Map)) return null;
            Map data = (Map) dataObj;
            boolean isVideo = Constants.MediaType.VIDEO.equals(String.valueOf(data.get("type")));
            String kind = BaseApp.inst().getString(isVideo ? R.string.video_calls : R.string.voice_calls);
            boolean success = Boolean.TRUE.equals(data.get("success"));
            if (success) {
                int seconds = data.get("duration") instanceof Number ? ((Number) data.get("duration")).intValue() / 1000 : 0;
                if (seconds >= 1) {
                    if (seconds >= 3600)
                        return kind + " " + String.format("%d:%02d:%02d", seconds / 3600, (seconds % 3600) / 60, seconds % 60);
                    return kind + " " + String.format("%02d:%02d", seconds / 60, seconds % 60);
                }
                return kind;
            }
            int failedState = data.get("failedState") instanceof Number ? ((Number) data.get("failedState")).intValue() : -1;
            String reason;
            switch (failedState) {
                case 1:
                    reason = BaseApp.inst().getString(R.string.call_canceled);
                    break;
                case 2:
                    reason = BaseApp.inst().getString(R.string.call_rejected_by_other);
                    break;
                case 3:
                    reason = BaseApp.inst().getString(R.string.call_rejected);
                    break;
                case 4:
                    reason = BaseApp.inst().getString(R.string.call_no_answer);
                    break;
                default:
                    reason = BaseApp.inst().getString(R.string.call_failed);
                    break;
            }
            return kind + " " + reason;
        } catch (Exception e) {
            L.e(e.getMessage());
            return null;
        }
    }

    public static CharSequence getMsgParse(Message msg) {
        return getMsgParse(msg, false);
    }

    /**
     * 解析消息内容
     *
     * @param msg
     * @return
     */
    public static CharSequence getMsgParse(Message msg, boolean isShowSenderNickname) {
        CharSequence lastMsg = isShowSenderNickname ? msg.getSenderNickname() + ": " : "";
        try {
            switch (msg.getContentType()) {
                case MessageType.TEXT:
                    lastMsg += msg.getTextElem().getContent();
                    break;
                case MessageType.PICTURE:
                    lastMsg += "[" + BaseApp.inst().getString(io.openim.android.ouicore.R.string.picture) + "]";
                    break;
                case MessageType.VOICE:
                    lastMsg += "[语音]";
                    break;
                case MessageType.VIDEO:
                    lastMsg += "[视频]";
                    break;
                case MessageType.FILE:
                    lastMsg += "[文件]";
                    break;
                case 1201:
                case 1204:
                    lastMsg += "[你们已成为好友]";
                    break;
                case 1202:
                    lastMsg += "[好友申请已被拒绝]";
                    break;
                case MessageType.CUSTOM:
                    String callText = getCallHistoryText(msg);
                    lastMsg += null == callText ? "[" + BaseApp.inst().getString(R.string.unsupported_type) + "]" : callText;
                    break;
                default:
                    lastMsg += "[" + BaseApp.inst().getString(R.string.unsupported_type) + "]";
                    break;
            }
        } catch (Exception e) {
            L.e(e.getMessage());
        }
        return lastMsg;
    }

    /// 会话前缀标签
    public static CharSequence getPrefixTag(CharSequence lastMsg, ConversationInfo info) {
        String prefix = "";
        try {

            switch (info.getGroupAtType()) {
                case GroupAtType.AT_ALL:
                    prefix = "[" + BaseApp.inst().getString(R.string.at_all) + "]";
                    break;
                case GroupAtType.AT_ALL_AT_ME:
                    prefix = "[" + BaseApp.inst().getString(R.string.at_all_at_me) + "]";
                    break;
                case GroupAtType.AT_ME:
                    prefix = "[" + BaseApp.inst().getString(R.string.a_person_at_you) + "]";
                    break;
                case GroupAtType.GROUP_NOTIFICATION:
                    prefix = "[" + BaseApp.inst().getString(R.string.group_bulletin) + "]";
                    break;
            }
            if (TextUtils.isEmpty(prefix)) return prefix;
            lastMsg =
                IMUtil.buildClickAndColorSpannable(new SpannableStringBuilder(prefix + lastMsg),
                    prefix, R.color.theme, null);
        } catch (Exception e) {
            e.printStackTrace();
        }
        return lastMsg;
    }

    /**
     * build SignalingInfo
     *
     * @param isVideoCalls   if true, called by video.
     * @param inviteeUserIDs invited user
     * @return calling parameter
     */
    public static SignalingInfo buildSignalingInfo(boolean isVideoCalls, List<String> inviteeUserIDs) {
        SignalingInfo signalingInfo = new SignalingInfo();
        String inId = BaseApp.inst().loginCertificate.userID;
        signalingInfo.setOpUserID(inId);
        SignalingInvitationInfo signalingInvitationInfo = new SignalingInvitationInfo();
        signalingInvitationInfo.setInviterUserID(inId);
        signalingInvitationInfo.setInviteeUserIDList(inviteeUserIDs);
        signalingInvitationInfo.setRoomID(UUID.randomUUID().toString().replaceAll("\u200B", ""));
        signalingInvitationInfo.setTimeout(30);
        signalingInvitationInfo.setInitiateTime(System.currentTimeMillis());
        signalingInvitationInfo.setMediaType(isVideoCalls ? Constants.MediaType.VIDEO :
            Constants.MediaType.AUDIO);
        signalingInvitationInfo.setPlatformID(IMUtil.PLATFORM_ID);
        signalingInvitationInfo.setSessionType(ConversationType.SINGLE_CHAT);
        signalingInfo.setInvitation(signalingInvitationInfo);
        signalingInfo.setOfflinePushInfo(new OfflinePushInfo());
        return signalingInfo;
    }

    public static void showBottomCallsPopMenu(Context context, View.OnKeyListener v) {
        HasPermissions hasPermissions = new HasPermissions(context, Permission.CAMERA,
            Permission.RECORD_AUDIO);

        LayoutBottompopV3Binding v3Binding =
            LayoutBottompopV3Binding.inflate(LayoutInflater.from(context));
        BottomPopDialog dialog = new BottomPopDialog(context, v3Binding.getRoot());
        dialog.show();
        // 视觉升级：窗口动画本就是 XianliaoBottomAnim（上滑淡入 280ms/退场 220ms），
        // 这里再补行级效果——三行依次淡入上浮（0/60/120ms 错峰）+ 按压缩放；
        // 信令、权限、跳转、取消逻辑原样不动。
        xlCallPopAnim(v3Binding.voiceCall, v3Binding.videoCall, v3Binding.cancel);
        v3Binding.cancel.setOnClickListener(v1 -> dialog.dismiss());

        v3Binding.voiceCall.setOnClickListener(v1 -> {
            hasPermissions.safeGo(() -> {
                v.onKey(v1, 1, null);
                dialog.dismiss();
            });
        });
        v3Binding.videoCall.setOnClickListener(v1 -> {
            hasPermissions.safeGo(() -> {
                v.onKey(v1, 2, null);
                dialog.dismiss();
            });
        });
    }

    /** 通话弹窗行级动画：依次淡入上浮 + 按压缩放（0.96/120ms，松开回弹 160ms，
     * 与 BottomPopDialog/FundUi 同参数）；touch 监听 return false，不吞点击事件。 */
    private static void xlCallPopAnim(View... views) {
        for (int i = 0; i < views.length; i++) {
            View v = views[i];
            if (v == null) {
                continue;
            }
            v.setAlpha(0f);
            v.setTranslationY(24f);
            v.animate().alpha(1f).translationY(0f).setStartDelay(i * 60L).setDuration(240)
                .setInterpolator(new DecelerateInterpolator()).start();
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

    /**
     * 已登录或登录中
     *
     * @return
     */
    public static boolean isLogged() {
        long status = OpenIMClient.getInstance().getLoginStatus();
        return status == LoginStatus.Logging || status == LoginStatus.Logged;
    }

    /**
     * 退出
     *
     * @param from
     * @param to
     */
    public static void logout(AppCompatActivity from, Class<?> to) {
        from.startActivity(new Intent(from, to));
        LoginCertificate.clear();
        BaseApp.inst().loginCertificate = null;
        from.finish();
    }

    public static void sendNotice(int id) {
        Postcard postcard = ARouter.getInstance().build(Routes.Main.SPLASH);
        LogisticsCenter.completion(postcard);
        Intent hangIntent = new Intent(BaseApp.inst(), postcard.getDestination());
        PendingIntent hangPendingIntent = PendingIntent.getActivity(BaseApp.inst(), 1002,
            hangIntent, PendingIntent.FLAG_MUTABLE);

        NotificationUtil.sendNotify(id,
            NotificationUtil.builder(NotificationUtil.MSG_NOTIFICATION).setContentTitle(BaseApp.inst().getString(R.string.app_name)).setContentText(BaseApp.inst().getString(R.string.a_message_is_received)).setSmallIcon(R.mipmap.ic_logo).setContentIntent(hangPendingIntent).setAutoCancel(true).setDefaults(Notification.DEFAULT_SOUND | Notification.DEFAULT_VIBRATE).setSound(Uri.parse("android.resource://" + BaseApp.inst().getPackageName() + "/" + R.raw.message_ring)).build());
    }

    //播放提示音
    public static void playPrompt() {
        MediaPlayerUtil.INSTANCE.initMedia(BaseApp.inst(), R.raw.message_ring);
        MediaPlayerUtil.INSTANCE.playMedia();
    }

    //震动milliseconds毫秒
    public static void vibrate(long milliseconds) {
        Vibrator vib = (Vibrator) BaseApp.inst().getSystemService(Service.VIBRATOR_SERVICE);
        vib.vibrate(milliseconds);
    }

    public static String atD(String str) {
        return "@" + str + " ";
    }

    /**
     * 成功监听
     *
     * @param <T>
     */
    public interface OnSuccessListener<T> {
        void onSuccess(T data);
    }

    public static class IMCallBack<T> implements OnBase<T> {
        @Override
        public void onError(int code, String error) {
            L.e("IMCallBack", "onError:(" + code + ")" + error);
        }

        public void onSuccess(T data) {

        }
    }
}
