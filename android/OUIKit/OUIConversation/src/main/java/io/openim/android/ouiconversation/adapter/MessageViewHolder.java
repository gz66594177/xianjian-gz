package io.openim.android.ouiconversation.adapter;


import android.annotation.SuppressLint;
import android.app.Activity;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.Rect;
import android.graphics.drawable.ColorDrawable;
import android.text.Spannable;
import android.text.TextUtils;
import android.text.method.LinkMovementMethod;
import android.view.LayoutInflater;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewGroup;
import android.view.ViewStub;
import android.widget.CheckBox;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.PopupWindow;
import android.widget.TextView;

import androidx.annotation.NonNull;
import androidx.appcompat.content.res.AppCompatResources;
import androidx.recyclerview.widget.GridLayoutManager;
import androidx.recyclerview.widget.LinearLayoutManager;
import androidx.recyclerview.widget.RecyclerView;


import com.airbnb.lottie.LottieAnimationView;
import com.alibaba.android.arouter.launcher.ARouter;
import com.bumptech.glide.Glide;
import com.bumptech.glide.load.resource.bitmap.RoundedCorners;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicBoolean;

import io.openim.android.ouiconversation.R;


import io.openim.android.ouiconversation.databinding.LayoutLoadingSmallBinding;
import io.openim.android.ouiconversation.databinding.LayoutMsgImgLeftBinding;
import io.openim.android.ouiconversation.databinding.LayoutMsgImgRightBinding;
import io.openim.android.ouiconversation.databinding.LayoutMsgTxtLeftBinding;
import io.openim.android.ouiconversation.databinding.LayoutMsgTxtRightBinding;
import io.openim.android.ouiconversation.ui.ChatActivity;
import io.openim.android.ouiconversation.ui.PreviewMediaActivity;
import io.openim.android.ouiconversation.vm.CustomEmojiVM;
import io.openim.android.ouiconversation.widget.SendStateView;
import io.openim.android.ouiconversation.vm.ChatVM;
import io.openim.android.ouiconversation.RedPacketBridge;
import io.openim.android.ouiconversation.ui.fragment.InputExpandFragment;
import io.openim.android.ouicore.adapter.RecyclerViewAdapter;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.base.vm.injection.Easy;
import io.openim.android.ouicore.entity.CallHistory;
import io.openim.android.ouicore.entity.MsgExpand;
import io.openim.android.ouicore.ex.AtUser;
import io.openim.android.ouicore.im.IMUtil;
import io.openim.android.ouicore.net.bage.GsonHel;
import io.openim.android.ouicore.utils.ByteUtil;
import io.openim.android.ouicore.utils.Common;
import io.openim.android.ouicore.utils.Constants;
import io.openim.android.ouicore.utils.GetFilePathFromUri;
import io.openim.android.ouicore.utils.L;
import io.openim.android.ouicore.utils.MediaFileUtil;
import io.openim.android.ouicore.utils.OnDedrepClickListener;
import io.openim.android.ouicore.utils.Routes;
import io.openim.android.ouicore.utils.TimeUtil;
import io.openim.android.ouicore.vm.ForwardVM;
import io.openim.android.ouicore.vm.GroupVM;
import io.openim.android.ouicore.vm.SelectTargetVM;
import io.openim.android.ouicore.vm.PreviewMediaVM;
import io.openim.android.ouicore.voice.SPlayer;
import io.openim.android.ouicore.voice.listener.PlayerListener;
import io.openim.android.ouicore.voice.player.SMediaPlayer;
import io.openim.android.ouicore.widget.AvatarImage;
import io.openim.android.ouicore.widget.PlaceHolderDrawable;
import io.openim.android.sdk.enums.ConversationType;
import io.openim.android.sdk.enums.MessageStatus;
import io.openim.android.sdk.enums.MessageType;
import io.openim.android.sdk.models.AttachedInfoElem;
import io.openim.android.sdk.models.CardElem;
import io.openim.android.sdk.models.MergeElem;
import io.openim.android.sdk.models.Message;
import io.openim.android.sdk.models.QuoteElem;
import io.openim.android.sdk.models.VideoElem;
import io.reactivex.exceptions.UndeliverableException;
import io.reactivex.observers.DisposableObserver;
import io.reactivex.plugins.RxJavaPlugins;

public class MessageViewHolder {
    public static RecyclerView.ViewHolder createViewHolder(@NonNull ViewGroup parent,
                                                           int viewType) {
        if (viewType == Constants.LOADING) return new LoadingView(parent);
        if (viewType == MessageType.PICTURE) return new IMGView(parent);
        if (viewType == MessageType.VIDEO) return new VIDView(parent);
        if (viewType == MessageType.VOICE) return new SoundView(parent);
        if (viewType == MessageType.FILE) return new FileView(parent);
        return new TXTView(parent);
    }

    public abstract static class MsgViewHolder extends RecyclerView.ViewHolder {
        protected RecyclerView recyclerView;
        protected MessageAdapter messageAdapter;

        protected PopupWindow popupWindow;
        protected Message message;
        private RecyclerViewAdapter adapter;
        protected ChatVM chatVM = BaseApp.inst().getVMByCache(ChatVM.class);

        private boolean leftIsInflated = false, rightIsInflated = false;
        private final ViewStub right;
        private final ViewStub left;

        public MsgViewHolder(ViewGroup itemView) {
            super(buildRoot(itemView));
            left = this.itemView.findViewById(R.id.left);
            right = this.itemView.findViewById(R.id.right);

            left.setOnInflateListener((stub, inflated) -> leftIsInflated = true);
            right.setOnInflateListener((stub, inflated) -> rightIsInflated = true);
        }

        public static View buildRoot(ViewGroup parent) {
            return LayoutInflater.from(parent.getContext()).inflate(R.layout.layout_msg, parent,
                false);
        }

        protected abstract int getLeftInflatedId();

        protected abstract int getRightInflatedId();

        protected abstract void bindLeft(View itemView, Message message);

        protected abstract void bindRight(View itemView, Message message);

        /**
         * 是否是自己发的消息
         */
        protected boolean isOwn = false;

        //绑定数据
        public void bindData(Message message, int position) {
            this.message = message;
            try {
                isOwn = getSendWay(message);
                if (isOwn) {
                    if (leftIsInflated) left.setVisibility(View.GONE);
                    if (rightIsInflated) right.setVisibility(View.VISIBLE);
                    if (!rightIsInflated) {
                        right.setLayoutResource(getRightInflatedId());
                        right.inflate();
                    }
                    bindRight(itemView, message);
                } else {
                    if (leftIsInflated) left.setVisibility(View.VISIBLE);
                    if (rightIsInflated) right.setVisibility(View.GONE);
                    if (!leftIsInflated) {
                        left.setLayoutResource(getLeftInflatedId());
                        left.inflate();
                    }
                    bindLeft(itemView, message);
                }
                unifiedProcess(position);
            } catch (Exception e) {
                e.printStackTrace();
            }
        }

        protected boolean getSendWay(Message message) {
            return message.getSendID().equals(BaseApp.inst().loginCertificate.userID);
        }

        /**
         * 统一处理
         */
        protected void unifiedProcess(int position) {
            MsgExpand msgExpand = (MsgExpand) message.getExt();
            hFirstItem(position);

            hAvatar();
            hName();
            showTime(msgExpand);
            hSendState();
        }

        public void hFirstItem(int position) {
            View root = itemView.findViewById(R.id.root);
            root.setPadding(0, position == 0 ? Common.dp2px(15) : 0, 0, 0);
        }

        /**
         * 处理发送状态
         */
        private void hSendState() {
            if (isOwn) {
                SendStateView sendStateView = itemView.findViewById(R.id.sendState2);
                if (null == sendStateView) return;
                sendStateView.setOnClickListener(new OnDedrepClickListener() {
                    @Override
                    public void click(View v) {
                        chatVM.sendMsg(message, true);
                    }
                });
            }
        }

        /**
         * 处理名字
         */
        @SuppressLint("SetTextI18n")
        public void hName() {
            TextView nickName;
            if (isOwn) nickName = itemView.findViewById(R.id.nickName2);
            else nickName = itemView.findViewById(R.id.nickName);
            if (null != nickName) {
                nickName.setVisibility(View.VISIBLE);
                nickName.setMaxLines(1);
                nickName.setMaxEms(18);
                nickName.setEllipsize(TextUtils.TruncateAt.MIDDLE);

                boolean isSending = message.getStatus() == MessageStatus.SENDING;
                String time = TimeUtil.getTimeString(isSending ? System.currentTimeMillis() :
                    message.getSendTime());
                if (isSending || message.getSessionType() == ConversationType.SINGLE_CHAT) {
                    nickName.setText(time);
                } else nickName.setText(message.getSenderNickname() + "  " + time);
            }
        }

        /**
         * 处理头像
         */
        public void hAvatar() {
            AvatarImage avatarImage = itemView.findViewById(R.id.avatar);
            AvatarImage avatarImage2 = itemView.findViewById(R.id.avatar2);
            if (null != avatarImage) {
                avatarImage.load(message.getSenderFaceUrl(), message.getSenderNickname());
                AtomicBoolean isLongClick = new AtomicBoolean(false);
                avatarImage.setOnClickListener(v -> {
                    if (isLongClick.get()) {
                        isLongClick.set(false);
                        return;
                    }
                    ARouter.getInstance().build(Routes.Main.PERSON_DETAIL).withString(Constants.K_ID, message.getSendID()).withString(Constants.K_GROUP_ID, message.getGroupID()).navigation();
                });
            }
            if (null != avatarImage2) {
                avatarImage2.load(message.getSenderFaceUrl(), message.getSenderNickname());
                avatarImage2.setOnClickListener(v -> ARouter.getInstance().build(Routes.Main.PERSON_DETAIL).withString(Constants.K_ID, message.getSendID()).withString(Constants.K_GROUP_ID, message.getGroupID()).navigation());
            }
        }

        private void showTime(MsgExpand msgExpand) {
            TextView notice = itemView.findViewById(R.id.notice);
            if (msgExpand.isShowTime) {
                //显示时间
                String time = TimeUtil.getTimeString(message.getSendTime());
                notice.setVisibility(View.VISIBLE);
                notice.setText(time);
            } else notice.setVisibility(View.GONE);
        }

        public void bindRecyclerView(RecyclerView recyclerView) {
            this.recyclerView = recyclerView;
        }

        public void setMessageAdapter(MessageAdapter messageAdapter) {
            this.messageAdapter = messageAdapter;
        }

        /**
         * 预览图片或视频
         *
         * @param view
         * @param url           地址
         * @param firstFrameUrl 缩略图
         */
        public void toPreview(View view, String url, String firstFrameUrl) {
            view.setOnClickListener(v -> {
                PreviewMediaVM previewMediaVM = Easy.installVM(PreviewMediaVM.class);
                PreviewMediaVM.MediaData mediaData =
                    new PreviewMediaVM.MediaData(message.getClientMsgID());
                mediaData.mediaUrl = url;
                mediaData.isVideo = MediaFileUtil.isVideoType(url);
                mediaData.thumbnail = firstFrameUrl;
                previewMediaVM.preview(mediaData);
                view.getContext().startActivity(new Intent(view.getContext(),
                    PreviewMediaActivity.class));
            });
        }
    }


    //加载中...
    public static class LoadingView extends RecyclerView.ViewHolder {
        public LoadingView(ViewGroup parent) {
            super(LayoutLoadingSmallBinding.inflate(LayoutInflater.from(parent.getContext()),
                parent, false).getRoot());
        }
    }

    //文本消息
    public static class TXTView extends MessageViewHolder.MsgViewHolder {

        public TXTView(ViewGroup parent) {
            super(parent);
        }

        @Override
        protected int getLeftInflatedId() {
            return R.layout.layout_msg_txt_left;
        }

        @Override
        protected int getRightInflatedId() {
            return R.layout.layout_msg_txt_right;
        }

        @Override
        protected void bindLeft(View itemView, Message message) {
            LayoutMsgTxtLeftBinding v = LayoutMsgTxtLeftBinding.bind(itemView);
            v.avatar.load(message.getSenderFaceUrl(), message.getSenderNickname());
            resetTextBubble(v.content, false);
            if (bindFriendNtf(v.content, message)) return;
            if (bindCallHistory(v.content, message)) return;
            if (bindRedPacket(v.content, message)) return;
            v.content.setOnClickListener(null);
            v.content.setTag(null);
            String content = message.getTextElem() == null ?
                BaseApp.inst().getString(io.openim.android.ouicore.R.string.unsupported_type) : message.getTextElem().getContent();
            v.content.setText(content);
        }

        @Override
        protected void bindRight(View itemView, Message message) {
            LayoutMsgTxtRightBinding v = LayoutMsgTxtRightBinding.bind(itemView);
            v.avatar2.load(message.getSenderFaceUrl(), message.getSenderNickname());
            resetTextBubble(v.content2, true);
            v.sendState2.setSendState(message.getStatus());
            if (bindFriendNtf(v.content2, message)) return;
            if (bindCallHistory(v.content2, message)) return;
            if (bindRedPacket(v.content2, message)) return;
            v.content2.setOnClickListener(null);
            v.content2.setTag(null);
            String content = message.getTextElem() == null ?
                BaseApp.inst().getString(io.openim.android.ouicore.R.string.unsupported_type) : message.getTextElem().getContent();
            v.content2.setText(content);
        }

        /** 好友通知（1201 已通过 / 1204 已添加等）：此前落到 textElem==null 的“暂不支持”
         * 分支。本轮只拦截 1200-1299 好友通知段并按类型给准确文案，其余未知类型仍走不支持。 */
        private boolean bindFriendNtf(final TextView contentView, Message message) {
            int t = message.getContentType();
            if (t < 1200 || t > 1299) return false;
            String text;
            if (t == 1201 || t == 1204) text = "你们已成为好友，现在可以开始聊天了";
            else if (t == 1202) text = "好友申请已被拒绝";
            else if (t == 1203) text = "收到好友申请";
            else text = "好友通知";
            contentView.setText(text);
            contentView.setOnClickListener(null);
            contentView.setTag(null);
            return true;
        }

        /**
         * 通话记录气泡（customType=-110，见 IMUtil.getCallHistoryText）：渲染为
         * “语音通话 00:32 / 视频通话 未接通”，不再落到 textElem==null 的“暂不支持”分支。
         * 非通话记录返回 false。
         */
        private boolean bindCallHistory(final TextView contentView, Message message) {
            String text = IMUtil.getCallHistoryText(message);
            if (null == text) return false;
            contentView.setText(text);
            contentView.setOnClickListener(null);
            contentView.setTag(null);
            return true;
        }

        /**
         * Phase 10 触点 1：红包气泡渲染。
         * 命中红包消息（customType=901）时按服务端状态渲染气泡文案（不放金额、不读本地缓存金额），
         * 点击打开红包详情页；非红包消息返回 false，走原文本逻辑。
         * 状态由 Demo 层经 RedPacketBridge 回查资金服务详情接口；View 复用时以 tag 比对防串位。
         */
        private boolean bindRedPacket(final TextView contentView, Message message) {
            final RedPacketBridge.PacketRef ref = RedPacketBridge.parse(message);
            if (ref == null) return false;
            styleRedPacketBubble(contentView, "仙聊红包 · 点击领取");
            contentView.setTag(ref.packetId);
            contentView.setOnClickListener(v -> {
                if (RedPacketBridge.handler != null) {
                    RedPacketBridge.handler.openDetail(v.getContext(), ref.kind, ref.packetId);
                }
            });
            if (RedPacketBridge.handler != null) {
                RedPacketBridge.handler.fetchStatus(ref.kind, ref.packetId,
                    new RedPacketBridge.StatusCallback() {
                        @Override
                        public void onResult(org.json.JSONObject packet) {
                            contentView.post(() -> {
                                if (ref.packetId.equals(contentView.getTag())) {
                                    styleRedPacketBubble(contentView, bubbleText(ref.kind, packet));
                                }
                            });
                        }

                        @Override
                        public void onError() {
                        }
                    });
            }
            return true;
        }

        /** 大红包图：红底金边、金字、信封图标置顶、宽高加大，不再是浅色文字气泡。
         * View 复用时 resetTextBubble 会先把样式还原，红包再重设，不会串到普通文本上。 */
        private void styleRedPacketBubble(TextView contentView, String bubble) {
            String title = "仙聊红包";
            String status = "点击领取";
            if (bubble != null) {
                String s = bubble.replace("🧧 ", "").replace("🧧", "");
                if (s.contains("·")) {
                    String[] parts = s.split("·", 2);
                    if (parts.length == 2) {
                        title = parts[0].trim();
                        status = parts[1].trim();
                    } else {
                        status = s.trim();
                    }
                } else {
                    status = s.trim();
                }
            }
            contentView.setBackground(AppCompatResources.getDrawable(contentView.getContext(),
                io.openim.android.ouicore.R.drawable.shape_redpacket_bubble));
            contentView.setTextColor(contentView.getContext().getColor(
                io.openim.android.ouicore.R.color.redpacket_bubble_text));
            contentView.setTextSize(android.util.TypedValue.COMPLEX_UNIT_SP, 17);
            contentView.setGravity(android.view.Gravity.CENTER);
            int px20 = Common.dp2px(20);
            int px28 = Common.dp2px(28);
            contentView.setPadding(px20, px28, px20, px28);
            contentView.setMinimumWidth(Common.dp2px(180));
            contentView.setMinimumHeight(Common.dp2px(120));
            android.graphics.drawable.Drawable icon = AppCompatResources.getDrawable(
                contentView.getContext(), io.openim.android.ouicore.R.drawable.ic_xl_chat_redpacket);
            if (icon != null) {
                int sz = Common.dp2px(48);
                icon.setBounds(0, 0, sz, sz);
                contentView.setCompoundDrawables(null, icon, null, null);
                contentView.setCompoundDrawableTintList(android.content.res.ColorStateList.valueOf(
                    contentView.getContext().getColor(
                        io.openim.android.ouicore.R.color.redpacket_bubble_text)));
                contentView.setCompoundDrawablePadding(Common.dp2px(8));
            }
            contentView.setText(title + "\n" + status);
            contentView.setAlpha(status.contains("已领取") || status.contains("已过期") || status.contains("已领完") ? 0.75f : 1f);
        }

        /** 把 TextView 还原成普通文本气泡样式（红包复用后普通消息不再带红底）。 */
        private void resetTextBubble(TextView contentView, boolean right) {
            contentView.setBackground(AppCompatResources.getDrawable(contentView.getContext(),
                right ? io.openim.android.ouiconversation.R.drawable.sty_radius_unright_30
                      : io.openim.android.ouiconversation.R.drawable.sty_radius_unleft_30));
            contentView.setTextColor(contentView.getContext().getColor(
                io.openim.android.ouicore.R.color.txt_black));
            contentView.setTextSize(android.util.TypedValue.COMPLEX_UNIT_SP, 17);
            contentView.setGravity(android.view.Gravity.CENTER_VERTICAL);
            int px7 = Common.dp2px(7);
            contentView.setPadding(px7, px7, px7, px7);
            contentView.setMinimumWidth(0);
            contentView.setMinimumHeight(Common.dp2px(40));
            contentView.setCompoundDrawables(null, null, null, null);
            contentView.setCompoundDrawableTintList(null);
            contentView.setAlpha(1f);
        }

        /** 气泡文案只含状态与进度，不含金额；金额只在详情页展示。 */
        private String bubbleText(String kind, org.json.JSONObject packet) {
            if (packet == null) return "🧧 仙聊红包，点击查看";
            int status = packet.optInt("status", 0);
            boolean expired = packet.optBoolean("expired", false);
            if ("friend".equals(kind)) {
                if (status == 1) return "🧧 仙聊红包 · 已领取";
                if (status == 2 || expired) return "🧧 仙聊红包 · 已过期退款";
                return "🧧 仙聊红包 · 待领取，点击领取";
            }
            int claimed = packet.optInt("claimed_count", 0);
            int total = packet.optInt("total_count", 0);
            if (status == 1) return "🧧 群红包 · 已领完（" + claimed + "/" + total + "）";
            if (status == 2 || expired) return "🧧 群红包 · 已过期（" + claimed + "/" + total + "）";
            return "🧧 群红包 · " + claimed + "/" + total + " 已领，点击领取";
        }
    }

    public static class IMGView extends MessageViewHolder.MsgViewHolder {

        public IMGView(ViewGroup itemView) {
            super(itemView);
        }

        @Override
        protected int getLeftInflatedId() {
            return R.layout.layout_msg_img_left;
        }

        @Override
        protected int getRightInflatedId() {
            return R.layout.layout_msg_img_right;
        }

        @Override
        protected void bindLeft(View itemView, Message message) {
            LayoutMsgImgLeftBinding v = LayoutMsgImgLeftBinding.bind(itemView);

            v.sendState.setSendState(message.getStatus());
            String url = loadIMG(v.content, message);
            toPreview(v.content, url, null);
        }

        private String loadIMG(ImageView img, Message message) {
            String url = message.getPictureElem().getSourcePicture().getUrl();
            int w = message.getPictureElem().getSourcePicture().getWidth();
            int h = message.getPictureElem().getSourcePicture().getHeight();
            scale(img, w, h);
            // 聊天速度：本机已有原图时直接显示本机文件，不等上传完成回填缩略图地址。
            // 否则发送中的图片气泡在上传完成前一直是占位图，看起来就是“发出去很久才显示”。
            // 收到消息的 sourcePath 为空，走原有缩略图逻辑，不受影响；已下载过的收图走本机文件更快且是同一张图。
            String localPath = null;
            try {
                localPath = message.getPictureElem().getSourcePath();
            } catch (Exception ignored) {
            }
            if (localPath != null && !localPath.isEmpty()) {
                java.io.File localFile = new java.io.File(localPath);
                if (localFile.exists() && localFile.length() > 0) {
                    Glide.with(BaseApp.inst()).clear(img);
                    img.setImageDrawable(null);
                    if (localPath.toLowerCase().endsWith(".gif")) {
                        Glide.with(BaseApp.inst()).asGif().load(localFile).fitCenter()
                            .placeholder(new PlaceHolderDrawable(BaseApp.inst())).into(img);
                    } else {
                        Glide.with(BaseApp.inst()).load(localFile).placeholder(new PlaceHolderDrawable(BaseApp.inst())).error(io.openim.android.ouicore.R.mipmap.ic_chat_photo).fitCenter().transform(new RoundedCorners(15)).into(img);
                    }
                    return url;
                }
            }
            String sourceUrl = message.getPictureElem().getSourcePicture().getUrl();
            if (sourceUrl != null) {
                String low = sourceUrl.toLowerCase();
                int q = low.indexOf('?');
                if (q >= 0) low = low.substring(0, q);
                if (low.endsWith(".gif")) {
                    // GIF 不走静态缩略图：直接播原图动图；不用圆角变换，
                    // 避免 Bitmap 变换破坏 GifDrawable 动画。
                    // 复用闪旧图根因：GIF 支路原来没 placeholder，Glide 新图没解码完之前
                    // ImageView 里还留着上一条消息的图；先清掉再加载，占位图顶上。
                    Glide.with(BaseApp.inst()).clear(img);
                    img.setImageDrawable(null);
                    Glide.with(BaseApp.inst()).asGif().load(sourceUrl).fitCenter()
                        .placeholder(new PlaceHolderDrawable(BaseApp.inst())).into(img);
                    return url;
                }
            }
            Glide.with(BaseApp.inst()).clear(img);
            img.setImageDrawable(null);
            Glide.with(BaseApp.inst()).load(message.getPictureElem().getSnapshotPicture().getUrl()).placeholder(new PlaceHolderDrawable(BaseApp.inst())).error(io.openim.android.ouicore.R.mipmap.ic_chat_photo).fitCenter().transform(new RoundedCorners(15)).into(img);
            return url;
        }

        public void scale(View img, int sourceW, int sourceH) {
            int pictureWidth = Common.dp2px(180);
            int _trulyWidth;
            int _trulyHeight;
            if (sourceW == 0) {
                sourceW = 1;
            }
            if (sourceH == 0) {
                sourceH = 1;
            }
            if (pictureWidth > sourceW) {
                _trulyWidth = sourceW;
                _trulyHeight = sourceH;
            } else {
                _trulyWidth = pictureWidth;
                _trulyHeight = _trulyWidth * sourceH / sourceW;
            }
            ViewGroup.LayoutParams params = img.getLayoutParams();
            params.width = _trulyWidth;
            params.height = _trulyHeight;
            img.setLayoutParams(params);
        }

        @Override
        protected void bindRight(View itemView, Message message) {
            LayoutMsgImgRightBinding v = LayoutMsgImgRightBinding.bind(itemView);
            v.avatar2.load(message.getSenderFaceUrl(), message.getSenderNickname());
            v.videoPlay2.setVisibility(View.GONE);
            v.mask2.setVisibility(View.GONE);

            v.sendState2.setSendState(message.getStatus());
            String url = loadIMG(v.content2, message);
            toPreview(v.content2, url, null);
        }

    }

    // 视频消息：此前走 TXTView 兜底显示“暂不支持”。复用图片布局，封面 + 播放角标 + 时长，
    // 点击走原有 toPreview（按 URL 判视频，走 PreviewMediaActivity 播放）。
    public static class VIDView extends MessageViewHolder.MsgViewHolder {
        public VIDView(ViewGroup parent) { super(parent); }
        @Override protected int getLeftInflatedId() { return R.layout.layout_msg_img_left; }
        @Override protected int getRightInflatedId() { return R.layout.layout_msg_img_right; }
        private void loadVid(ImageView img, Message message) {
            try {
                String snap = message.getVideoElem().getSnapshotUrl();
                int w = message.getVideoElem().getSnapshotWidth();
                int h = message.getVideoElem().getSnapshotHeight();
                if (w == 0) w = 1; if (h == 0) h = 1;
                int pw = Common.dp2px(180);
                int tw = pw > w ? w : pw;
                int th = tw * h / w;
                ViewGroup.LayoutParams lp = img.getLayoutParams();
                lp.width = tw; lp.height = th; img.setLayoutParams(lp);
                Glide.with(BaseApp.inst()).clear(img);
                img.setImageDrawable(null);
                Glide.with(BaseApp.inst()).load(snap).placeholder(new PlaceHolderDrawable(BaseApp.inst()))
                    .error(io.openim.android.ouicore.R.mipmap.ic_chat_photo).fitCenter()
                    .transform(new RoundedCorners(15)).into(img);
            } catch (Exception ignored) { }
        }
        @Override protected void bindLeft(View itemView, Message message) {
            LayoutMsgImgLeftBinding v = LayoutMsgImgLeftBinding.bind(itemView);
            v.sendState.setSendState(message.getStatus());
            loadVid(v.content, message);
            try { v.playBtn.setVisibility(View.VISIBLE); } catch (Exception ignored) { }
            try { v.durationLeft.setText(message.getVideoElem().getDuration() + "\""); } catch (Exception ignored) { }
            String url = message.getVideoElem().getVideoUrl();
            toPreview(v.content, url, message.getVideoElem().getSnapshotUrl());
        }
        @Override protected void bindRight(View itemView, Message message) {
            LayoutMsgImgRightBinding v = LayoutMsgImgRightBinding.bind(itemView);
            v.avatar2.load(message.getSenderFaceUrl(), message.getSenderNickname());
            v.sendState2.setSendState(message.getStatus());
            loadVid(v.content2, message);
            try { v.videoPlay2.setVisibility(View.VISIBLE); v.mask2.setVisibility(View.VISIBLE); } catch (Exception ignored) { }
            try { v.duration2.setText(message.getVideoElem().getDuration() + "\""); } catch (Exception ignored) { }
            String url = message.getVideoElem().getVideoUrl();
            toPreview(v.content2, url, message.getVideoElem().getSnapshotUrl());
        }
    }

    // 语音消息：此前走 TXTView 兜底显示“暂不支持”。复用文本气泡，显示时长，点击用 SPlayer 播放。
    public static class SoundView extends MessageViewHolder.MsgViewHolder {
        public SoundView(ViewGroup parent) { super(parent); }
        @Override protected int getLeftInflatedId() { return R.layout.layout_msg_txt_left; }
        @Override protected int getRightInflatedId() { return R.layout.layout_msg_txt_right; }
        private String label(Message m) {
            long d = 0;
            try { d = m.getSoundElem().getDuration(); } catch (Exception ignored) { }
            return "\uD83D\uDD0A 语音 " + d + "\"\uFF0C点击播放";
        }
        private void play(Message m) {
            try {
                if (SPlayer.instance().isPlaying()) { SPlayer.instance().stop(); return; }
                String url = m.getSoundElem().getSourceUrl();
                if (url == null || url.isEmpty()) url = m.getSoundElem().getSoundPath();
                if (url == null || url.isEmpty()) return;
                SPlayer.instance().playByUrl(url, new PlayerListener() {
                    @Override public void LoadSuccess(SMediaPlayer p) { }
                    @Override public void Loading(SMediaPlayer p, int i) { }
                    @Override public void onCompletion(SMediaPlayer p) { }
                    @Override public void onError(Exception e) { }
                });
            } catch (Exception ignored) { }
        }
        @Override protected void bindLeft(View itemView, Message message) {
            LayoutMsgTxtLeftBinding v = LayoutMsgTxtLeftBinding.bind(itemView);
            v.avatar.load(message.getSenderFaceUrl(), message.getSenderNickname());
            v.content.setText(label(message));
            v.content.setOnClickListener(vv -> play(message));
            v.content.setTag(null);
        }
        @Override protected void bindRight(View itemView, Message message) {
            LayoutMsgTxtRightBinding v = LayoutMsgTxtRightBinding.bind(itemView);
            v.avatar2.load(message.getSenderFaceUrl(), message.getSenderNickname());
            v.sendState2.setSendState(message.getStatus());
            v.content2.setText(label(message));
            v.content2.setOnClickListener(vv -> play(message));
            v.content2.setTag(null);
        }
    }

    // 文件消息：此前无分发分支，走 TXTView 兜底显示“暂不支持”。
    // 复用文本气泡，显示文件名 + 大小；点击：本地已有直接打开，否则下载到
    // Constants.File_DIR 后打开，下载/打开失败均 toast，不静默无反应。
    public static class FileView extends MessageViewHolder.MsgViewHolder {
        public FileView(ViewGroup parent) { super(parent); }
        @Override protected int getLeftInflatedId() { return R.layout.layout_msg_txt_left; }
        @Override protected int getRightInflatedId() { return R.layout.layout_msg_txt_right; }
        private String label(Message m) {
            String name = "";
            long size = 0;
            try { name = m.getFileElem().getFileName(); size = m.getFileElem().getFileSize(); } catch (Exception ignored) { }
            if (name == null || name.isEmpty()) name = "文件";
            return "\uD83D\uDCC4 " + name + "\n" + formatSize(size) + " \u00B7 点击下载/打开";
        }
        private static String formatSize(long s) {
            if (s <= 0) return "未知大小";
            if (s < 1024) return s + " B";
            if (s < 1024 * 1024) return String.format("%.1f KB", s / 1024.0);
            return String.format("%.1f MB", s / 1024.0 / 1024.0);
        }
        private void openOrDownload(android.view.View view, Message m) {
            android.content.Context ctx = view.getContext();
            try {
                String local = null;
                try { local = m.getFileElem().getFilePath(); } catch (Exception ignored) { }
                if (local != null && !local.isEmpty() && GetFilePathFromUri.fileIsExists(local)) {
                    GetFilePathFromUri.openFile(ctx, local);
                    return;
                }
                String url = null;
                try { url = m.getFileElem().getSourceUrl(); } catch (Exception ignored) { }
                if (url == null || url.isEmpty()) {
                    android.widget.Toast.makeText(ctx, "文件地址为空，无法下载", android.widget.Toast.LENGTH_SHORT).show();
                    return;
                }
                String suffix = GetFilePathFromUri.getFileSuffix(url);
                String cached = Constants.File_DIR + Common.md5(url) + (suffix.isEmpty() ? "" : "." + suffix);
                if (GetFilePathFromUri.fileIsExists(cached)) {
                    GetFilePathFromUri.openFile(ctx, cached);
                    return;
                }
                if (!url.startsWith("http")) {
                    android.widget.Toast.makeText(ctx, "文件地址不可用，无法下载", android.widget.Toast.LENGTH_SHORT).show();
                    return;
                }
                android.widget.Toast.makeText(ctx, "正在下载文件\u2026", android.widget.Toast.LENGTH_SHORT).show();
                final String fileUrl = url;
                final String cachedPath = cached;
                Common.downloadFile(url, Constants.File_DIR, null).subscribe(new io.reactivex.Observer<Boolean>() {
                    @Override public void onSubscribe(io.reactivex.disposables.Disposable d) { }
                    @Override public void onNext(Boolean ok) {
                        String downloaded = Constants.File_DIR + fileUrl.substring(fileUrl.lastIndexOf("/") + 1);
                        if (GetFilePathFromUri.fileIsExists(downloaded)) {
                            GetFilePathFromUri.openFile(ctx, downloaded);
                        } else if (GetFilePathFromUri.fileIsExists(cachedPath)) {
                            GetFilePathFromUri.openFile(ctx, cachedPath);
                        } else {
                            android.widget.Toast.makeText(ctx, "文件下载失败，请重试", android.widget.Toast.LENGTH_SHORT).show();
                        }
                    }
                    @Override public void onError(Throwable e) {
                        android.widget.Toast.makeText(ctx, "文件下载失败，请重试", android.widget.Toast.LENGTH_SHORT).show();
                    }
                    @Override public void onComplete() { }
                });
            } catch (Exception e) {
                android.widget.Toast.makeText(ctx, "文件打开失败，请重试", android.widget.Toast.LENGTH_SHORT).show();
            }
        }
        @Override protected void bindLeft(View itemView, Message message) {
            LayoutMsgTxtLeftBinding v = LayoutMsgTxtLeftBinding.bind(itemView);
            v.avatar.load(message.getSenderFaceUrl(), message.getSenderNickname());
            v.content.setText(label(message));
            v.content.setOnClickListener(vv -> openOrDownload(vv, message));
            v.content.setTag(null);
        }
        @Override protected void bindRight(View itemView, Message message) {
            LayoutMsgTxtRightBinding v = LayoutMsgTxtRightBinding.bind(itemView);
            v.avatar2.load(message.getSenderFaceUrl(), message.getSenderNickname());
            v.sendState2.setSendState(message.getStatus());
            v.content2.setText(label(message));
            v.content2.setOnClickListener(vv -> openOrDownload(vv, message));
            v.content2.setTag(null);
        }
    }
}
