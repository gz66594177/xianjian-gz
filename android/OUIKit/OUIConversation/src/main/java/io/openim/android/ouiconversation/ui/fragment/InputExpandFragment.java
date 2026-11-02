package io.openim.android.ouiconversation.ui.fragment;


import android.annotation.SuppressLint;
import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ActivityInfo;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.Matrix;
import android.hardware.camera2.CameraManager;
import android.media.ExifInterface;
import android.net.Uri;
import android.os.Bundle;

import android.text.TextUtils;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.content.res.ColorStateList;
import android.widget.Toast;

import androidx.activity.result.ActivityResultLauncher;
import androidx.activity.result.contract.ActivityResultContracts;
import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import androidx.recyclerview.widget.GridLayoutManager;
import androidx.recyclerview.widget.RecyclerView;


import com.alibaba.android.arouter.launcher.ARouter;
import com.bumptech.glide.Glide;
import com.bumptech.glide.request.target.SimpleTarget;
import com.bumptech.glide.request.transition.Transition;
import com.hjq.permissions.Permission;
import com.luck.picture.lib.basic.PictureSelector;
import com.luck.picture.lib.config.SelectMimeType;
import com.luck.picture.lib.entity.LocalMedia;

import java.io.File;
import java.io.FileOutputStream;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;

import io.openim.android.ouiconversation.R;
import io.openim.android.ouiconversation.databinding.FragmentInputExpandBinding;
import io.openim.android.ouiconversation.databinding.ItemExpandMenuBinding;
import io.openim.android.ouiconversation.ui.ChatActivity;
import io.openim.android.ouiconversation.ui.ShootActivity;
import io.openim.android.ouiconversation.vm.ChatVM;
import io.openim.android.ouicore.adapter.RecyclerViewAdapter;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.base.BaseFragment;
import io.openim.android.ouicore.base.vm.injection.Easy;
import io.openim.android.ouicore.databinding.LayoutCommonDialogBinding;
import io.openim.android.ouicore.ex.MultipleChoice;
import io.openim.android.ouicore.services.CallingService;
import io.openim.android.ouicore.utils.ActivityManager;
import io.openim.android.ouicore.utils.Common;
import io.openim.android.ouicore.utils.Constants;
import io.openim.android.ouicore.utils.GetFilePathFromUri;
import io.openim.android.ouicore.utils.GlideEngine;
import io.openim.android.ouicore.utils.HasPermissions;
import io.openim.android.ouicore.utils.L;
import io.openim.android.ouicore.utils.MThreadTool;
import io.openim.android.ouicore.utils.MediaFileUtil;
import io.openim.android.ouicore.utils.Routes;
import io.openim.android.ouicore.vm.SelectTargetVM;
import io.openim.android.ouicore.widget.CommonDialog;
import io.openim.android.ouicore.widget.WebViewActivity;
import io.openim.android.sdk.OpenIMClient;
import io.openim.android.ouiconversation.RedPacketBridge;
import io.openim.android.sdk.models.CardElem;
import io.openim.android.sdk.models.Message;

public class InputExpandFragment extends BaseFragment<ChatVM> {
    public static List<Integer> menuIcons =
        Arrays.asList(io.openim.android.ouicore.R.drawable.ic_xl_chat_album,
            io.openim.android.ouicore.R.drawable.ic_xl_chat_redpacket,
            io.openim.android.ouicore.R.drawable.ic_xl_chat_custom_emoji,
            io.openim.android.ouicore.R.drawable.ic_xl_chat_file);
    public static List<String> menuTitles =
        Arrays.asList(BaseApp.inst().getString(io.openim.android.ouicore.R.string.album),
            "红包", "自定义表情", "文件");

    FragmentInputExpandBinding v;
    // permissions
    private HasPermissions hasStorage;

    @Override
    public void onCreate(@Nullable Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        MThreadTool.executorService.execute(() -> {
            hasStorage = new HasPermissions(getActivity(), Permission.MANAGE_EXTERNAL_STORAGE);
        });
    }

    @Override
    public void onDestroyView() {
        super.onDestroyView();
    }

    @Nullable
    @Override
    public View onCreateView(@NonNull LayoutInflater inflater, @Nullable ViewGroup container,
                             @Nullable Bundle savedInstanceState) {
        v = FragmentInputExpandBinding.inflate(inflater);
        init();
        return v.getRoot();
    }

    private void init() {
        // 通话入口已全部去掉（顶栏 + + 面板，私聊群聊都不再显示），通话服务代码未删，需要时可恢复。
        final java.util.List<Integer> icons = new java.util.ArrayList<>(menuIcons);
        final java.util.List<String> titles = new java.util.ArrayList<>(menuTitles);
        v.getRoot().setLayoutManager(new GridLayoutManager(getContext(), 4));
        RecyclerViewAdapter adapter =
            new RecyclerViewAdapter<Object, ExpandHolder>(ExpandHolder.class) {

                @Override
                public void onBindView(@NonNull ExpandHolder holder, Object data, int position) {
                    holder.v.menu.setCompoundDrawablesRelativeWithIntrinsicBounds(null,
                        getContext().getDrawable(icons.get(position)), null, null);
                    holder.v.menu.setText(titles.get(position));
                    // 统一：文字走语义浅灰，红包与表情图标走主题色 tint（浅色雾绿/深色金，
                    // 由 @color/theme 日夜解析），不再用实心大红图标 + 红字高亮。
                    holder.v.menu.setTextColor(getContext().getColor(
                        io.openim.android.ouicore.R.color.txt_shallow));
                    // + 面板全部图标统一：同一套 1.8 描边矢量、同一 48dp 尺寸，
                    // 全部按 @color/theme 着色（浅色雾绿/深色金，日夜解析），
                    // 不再有蓝灰 PNG / 实心大红 / 黑线图标混搭；文字走语义浅灰。
                    // tint 全量应用：后续新增入口（如表情）也自动跟主题，不用再逐项补。
                    holder.v.menu.setCompoundDrawableTintList(ColorStateList.valueOf(
                        getContext().getColor(io.openim.android.ouicore.R.color.theme)));
                    holder.v.menu.setOnClickListener(v -> {
                        String title = titles.get(position);
                        if ("红包".equals(title)) {
                            // Phase 10 触点 2：输入扩展红包入口（经桥打开 Demo 发红包页）。
                            if (RedPacketBridge.handler != null && vm != null) {
                                RedPacketBridge.handler.openSend(getContext(), vm.userID, vm.groupID);
                            }
                        } else if ("自定义表情".equals(title)) {
                            showGifPicker();
                        } else if ("文件".equals(title)) {
                            showFilePicker();
                        } else {
                            showMediaPicker();
                        }
                    });
                }
            };
        v.getRoot().setAdapter(adapter);
        adapter.setItems(icons);
    }

    private final ActivityResultLauncher<Intent> captureLauncher =
        registerForActivityResult(new ActivityResultContracts.StartActivityForResult(), result -> {
            try {
                if (result.getResultCode() == Activity.RESULT_OK) {
                    Intent data = result.getData();
                    ArrayList<LocalMedia> files = PictureSelector.obtainSelectorList(data);

                    for (LocalMedia file : files) {
                        String path = GetFilePathFromUri.getFileAbsolutePath(InputExpandFragment.this.getActivity() ,Uri.parse(file.getAvailablePath()));
                        Message msg = null;
                        if (MediaFileUtil.isImageType(path)) {
                            // 聊天速度：大图先压再传。手机原图常为 3~8MB，走移动上行上传要数秒；
                            // 压到 1600px / JPEG 85 后通常只剩 300~500KB，上传从数秒降到 1 秒内。
                            // GIF 不压（要保留动画），1MB 以下原图不压（不牺牲画质），任何失败都回落原图绝不阻塞发送。
                            String imgPath = compressImageIfNeeded(InputExpandFragment.this.getActivity(), path);
                            msg =
                                OpenIMClient.getInstance().messageManager.createImageMessageFromFullPath(imgPath);
                        }
                        if (MediaFileUtil.isVideoType(path)) {
                            final String videoPath = path;
                            Glide.with(this).asBitmap().load(videoPath).into(new SimpleTarget<Bitmap>() {
                                @Override
                                public void onResourceReady(@NonNull Bitmap resource,
                                                            @Nullable Transition<? super Bitmap> transition) {
                                    String firstFame = MediaFileUtil.saveBitmap(resource,
                                        Constants.PICTURE_DIR, false);
                                    long duration = MediaFileUtil.getDuration(videoPath) / 1000;
                                    Message msg =
                                        OpenIMClient.getInstance().messageManager.createVideoMessageFromFullPath(videoPath, MediaFileUtil.getFileType(videoPath).mimeType, duration, firstFame);
                                    vm.sendMsg(msg);
                                }
                            });
                            continue;
                        }
                        if (null == msg)
                            msg =
                                OpenIMClient.getInstance().messageManager.createTextMessage("[" + getString(io.openim.android.ouicore.R.string.unsupported_type) + "]");
                        vm.sendMsg(msg);
                    }
                }
            } catch (Exception e) {
                L.e(e.getMessage());
            }
        });

    /**
     * 聊天速度：大图发送前压缩。规则：仅 jpg/jpeg/png、文件大于 1MB 才压；
     * GIF/WebP/小图原样返回。压到最长边 1600px、JPEG 质量 85，并按 EXIF 方向摆正；
     * 压完不比原图小或任何一步失败都返回原图，绝不阻塞发送。
     */
    private static String compressImageIfNeeded(Context ctx, String path) {
        try {
            if (ctx == null || path == null) return path;
            String low = path.toLowerCase();
            if (!(low.endsWith(".jpg") || low.endsWith(".jpeg") || low.endsWith(".png"))) return path;
            File src = new File(path);
            if (!src.exists() || src.length() <= 1024 * 1024) return path;
            BitmapFactory.Options bounds = new BitmapFactory.Options();
            bounds.inJustDecodeBounds = true;
            BitmapFactory.decodeFile(path, bounds);
            int w = bounds.outWidth;
            int h = bounds.outHeight;
            if (w <= 0 || h <= 0) return path;
            int sample = 1;
            while (Math.max(w, h) / sample > 1600) sample *= 2;
            BitmapFactory.Options opts = new BitmapFactory.Options();
            opts.inSampleSize = sample;
            Bitmap bmp = BitmapFactory.decodeFile(path, opts);
            if (bmp == null) return path;
            try {
                ExifInterface exif = new ExifInterface(path);
                int ori = exif.getAttributeInt(ExifInterface.TAG_ORIENTATION, ExifInterface.ORIENTATION_NORMAL);
                Matrix m = new Matrix();
                if (ori == ExifInterface.ORIENTATION_ROTATE_90) m.postRotate(90);
                else if (ori == ExifInterface.ORIENTATION_ROTATE_180) m.postRotate(180);
                else if (ori == ExifInterface.ORIENTATION_ROTATE_270) m.postRotate(270);
                else if (ori == ExifInterface.ORIENTATION_FLIP_HORIZONTAL) m.postScale(-1, 1);
                if (!m.isIdentity()) {
                    Bitmap rotated = Bitmap.createBitmap(bmp, 0, 0, bmp.getWidth(), bmp.getHeight(), m, true);
                    if (rotated != bmp) bmp.recycle();
                    bmp = rotated;
                }
            } catch (Exception ignored) {
            }
            File dir = new File(ctx.getCacheDir(), "xl_img_send");
            if (!dir.exists()) dir.mkdirs();
            File out = new File(dir, "c_" + System.currentTimeMillis() + ".jpg");
            FileOutputStream fos = new FileOutputStream(out);
            try {
                bmp.compress(Bitmap.CompressFormat.JPEG, 85, fos);
                fos.flush();
            } finally {
                try {
                    fos.close();
                } catch (Exception ignored) {
                }
                bmp.recycle();
            }
            if (out.exists() && out.length() > 0 && out.length() < src.length()) {
                return out.getAbsolutePath();
            }
            return path;
        } catch (Exception e) {
            return path;
        }
    }

    @SuppressLint("unchecked")
    private void showMediaPicker() {
        hasStorage.safeGo(() -> {
            try {
                PictureSelector.create(this)
                    .openGallery(SelectMimeType.ofAll())
                    .setImageEngine(GlideEngine.createGlideEngine())
                    .setMaxVideoSelectNum(9)
                    .setMaxSelectNum(9)
                    .forResult(captureLauncher);
            } catch (Exception e) {
                L.e(e.getMessage());
            }
        });
    }


    public void setChatVM(ChatVM vm) {
        this.vm = vm;
    }

    public static class ExpandHolder extends RecyclerView.ViewHolder {
        public ItemExpandMenuBinding v;

        public ExpandHolder(@NonNull View itemView) {
            super(ItemExpandMenuBinding.inflate(LayoutInflater.from(itemView.getContext())).getRoot());
            v = ItemExpandMenuBinding.bind(this.itemView);
        }
    }

    // ---- GIF 表情：+ 面板第 5 项（私聊与群聊共用同一 + 面板，两处同时出现）----
    private XlGifPickerDialog gifDialog;

    private final ActivityResultLauncher<Intent> gifUploadLauncher =
        registerForActivityResult(new ActivityResultContracts.StartActivityForResult(), result -> {
            try {
                if (result.getResultCode() == Activity.RESULT_OK && result.getData() != null
                    && result.getData().getData() != null) {
                    handleCustomGifUpload(result.getData().getData());
                }
            } catch (Exception e) { L.e(e.getMessage()); }
        });

    private void showGifPicker() {
        if (getActivity() == null || vm == null) return;
        if (gifDialog != null && gifDialog.isShowing()) return;
        gifDialog = new XlGifPickerDialog(getActivity(), new XlGifPickerDialog.Listener() {
            @Override
            public void onBuiltinGifSelected(String assetPath) {
                if (getActivity() == null) return;
                try {
                    File dir = new File(getActivity().getCacheDir(), "xl_gif_send");
                    if (!dir.exists()) dir.mkdirs();
                    String name = assetPath.substring(assetPath.lastIndexOf('/') + 1);
                    File dest = new File(dir, name);
                    try (java.io.InputStream in = getActivity().getAssets().open(assetPath);
                         java.io.OutputStream out = new java.io.FileOutputStream(dest)) {
                        byte[] buf = new byte[8192];
                        int n;
                        while ((n = in.read(buf)) != -1) out.write(buf, 0, n);
                    }
                    sendGifFile(dest.getAbsolutePath());
                } catch (Exception e) {
                    L.e(e.getMessage());
                    Toast.makeText(getActivity(), "GIF 发送失败，请重试", Toast.LENGTH_SHORT).show();
                }
            }

            @Override
            public void onCustomGifSelected(String filePath) {
                sendGifFile(filePath);
            }

            @Override
            public void onUploadClicked() {
                if (getActivity() == null) return;
                try {
                    Intent intent = new Intent(Intent.ACTION_GET_CONTENT);
                    intent.setType("image/*");
                    intent.putExtra(Intent.EXTRA_MIME_TYPES,
                        new String[]{"image/gif", "image/png", "image/jpeg", "image/webp"});
                    intent.addCategory(Intent.CATEGORY_OPENABLE);
                    gifUploadLauncher.launch(intent);
                } catch (Exception e) {
                    L.e(e.getMessage());
                    Toast.makeText(getActivity(), "无法打开相册", Toast.LENGTH_SHORT).show();
                }
            }
        });
        gifDialog.setOnDismissListener(d -> gifDialog = null);
        gifDialog.show();
    }


    // ---- 文件发送：+ 面板第 6 项（私聊与群聊共用同一 + 面板，两处同时出现）----
    // 不限 mime：文档、PDF、压缩包等任意文件均可选；走 SDK 文件消息
    // createFileMessageFromFullPath + vm.sendMsg，与图片/视频同一上传链路。
    private final ActivityResultLauncher<Intent> filePickerLauncher =
        registerForActivityResult(new ActivityResultContracts.StartActivityForResult(), result -> {
            try {
                if (result.getResultCode() == Activity.RESULT_OK && result.getData() != null
                    && result.getData().getData() != null) {
                    sendPickedFile(result.getData().getData());
                }
            } catch (Exception e) { L.e(e.getMessage()); }
        });

    private void showFilePicker() {
        if (getActivity() == null || vm == null) return;
        try {
            Intent intent = new Intent(Intent.ACTION_GET_CONTENT);
            intent.setType("*/*");
            intent.addCategory(Intent.CATEGORY_OPENABLE);
            filePickerLauncher.launch(intent);
        } catch (Exception e) {
            L.e(e.getMessage());
            Toast.makeText(getActivity(), "无法打开文件选择器", Toast.LENGTH_SHORT).show();
        }
    }

    private void sendPickedFile(Uri uri) {
        if (getActivity() == null || vm == null) return;
        try {
            String name = null;
            try (android.database.Cursor c = getActivity().getContentResolver().query(uri, null, null, null, null)) {
                if (c != null && c.moveToFirst()) {
                    int idx = c.getColumnIndex(android.provider.OpenableColumns.DISPLAY_NAME);
                    if (idx >= 0) name = c.getString(idx);
                }
            } catch (Exception ignored) { }
            String path = GetFilePathFromUri.getFileAbsolutePath(getActivity(), uri);
            if (path == null) {
                // 兜底：部分 content Uri 取不到绝对路径时把流拷到缓存目录再发
                if (name == null) name = "file_" + System.currentTimeMillis();
                java.io.File dir = new java.io.File(getActivity().getCacheDir(), "xl_file_send");
                if (!dir.exists()) dir.mkdirs();
                java.io.File dest = new java.io.File(dir, name);
                try (java.io.InputStream in = getActivity().getContentResolver().openInputStream(uri);
                     java.io.OutputStream out = new java.io.FileOutputStream(dest)) {
                    if (in == null) throw new java.io.IOException("empty stream");
                    byte[] buf = new byte[8192];
                    int n;
                    while ((n = in.read(buf)) != -1) out.write(buf, 0, n);
                }
                path = dest.getAbsolutePath();
            }
            if (name == null) name = new java.io.File(path).getName();
            java.io.File f = new java.io.File(path);
            if (!f.exists() || f.length() == 0) {
                Toast.makeText(getActivity(), "文件不存在或为空", Toast.LENGTH_SHORT).show();
                return;
            }
            Message msg = OpenIMClient.getInstance().messageManager.createFileMessageFromFullPath(path, name);
            if (msg == null) {
                Toast.makeText(getActivity(), "文件发送失败，请重试", Toast.LENGTH_SHORT).show();
                return;
            }
            vm.sendMsg(msg);
        } catch (Exception e) {
            L.e(e.getMessage());
            Toast.makeText(getActivity(), "文件发送失败，请重试", Toast.LENGTH_SHORT).show();
        }
    }

    /** GIF 复用图片消息发送链路（GIF 本来就判为图片类型），不另写发送协议。 */
    private void sendGifFile(String path) {
        if (vm == null || getActivity() == null) return;
        try {
            File f = new File(path);
            if (!f.exists()) {
                Toast.makeText(getActivity(), "该表情文件已丢失", Toast.LENGTH_SHORT).show();
                return;
            }
            Message msg = OpenIMClient.getInstance().messageManager.createImageMessageFromFullPath(path);
            if (msg == null) {
                Toast.makeText(getActivity(), "GIF 发送失败，请重试", Toast.LENGTH_SHORT).show();
                return;
            }
            if (gifDialog != null) gifDialog.dismiss();
            vm.sendMsg(msg);
        } catch (Exception e) {
            L.e(e.getMessage());
            Toast.makeText(getActivity(), "GIF 发送失败，请重试", Toast.LENGTH_SHORT).show();
        }
    }

    /**
     * 自定义表情上传：相册选图 -> 拷到 App 私有目录 -> 本机 Realm 记一条
     * （CustomEmoji，userID = 当前登录用户，sourceUrl = 私有文件路径）。
     * 不走服务端自定义表情上传接口、不建新服务端接口；发送时才走图片消息
     * 正常附件上传。失败不进库（拷贝失败直接 return，入库失败删文件）。
     */
    private void handleCustomGifUpload(Uri uri) {
        if (getActivity() == null) return;
        try {
            String mime = getActivity().getContentResolver().getType(uri);
            String ext = null;
            if ("image/gif".equals(mime)) ext = "gif";
            else if ("image/png".equals(mime)) ext = "png";
            else if ("image/jpeg".equals(mime)) ext = "jpg";
            else if ("image/webp".equals(mime)) ext = "webp";
            if (ext == null) {
                Toast.makeText(getActivity(), "请选择 GIF 或图片", Toast.LENGTH_SHORT).show();
                return;
            }
            File dir = new File(getActivity().getFilesDir(), "xl_custom_gif");
            if (!dir.exists()) dir.mkdirs();
            final File dest = new File(dir, "custom_" + System.currentTimeMillis() + "." + ext);
            try (java.io.InputStream in = getActivity().getContentResolver().openInputStream(uri);
                 java.io.OutputStream out = new java.io.FileOutputStream(dest)) {
                if (in == null) throw new java.io.IOException("empty stream");
                byte[] buf = new byte[8192];
                int n;
                while ((n = in.read(buf)) != -1) out.write(buf, 0, n);
            } catch (Exception e) {
                L.e(e.getMessage());
                Toast.makeText(getActivity(), "上传失败，请重试", Toast.LENGTH_SHORT).show();
                return;
            }
            int w = 0, h = 0;
            try {
                android.graphics.BitmapFactory.Options opts = new android.graphics.BitmapFactory.Options();
                opts.inJustDecodeBounds = true;
                android.graphics.BitmapFactory.decodeFile(dest.getAbsolutePath(), opts);
                w = opts.outWidth;
                h = opts.outHeight;
            } catch (Exception ignored) {
            }
            final int fw = w, fh = h;
            final String uid = BaseApp.inst().loginCertificate.userID;
            BaseApp.inst().realm.executeTransactionAsync(realm -> {
                io.openim.android.ouicore.entity.CustomEmoji emoji =
                    new io.openim.android.ouicore.entity.CustomEmoji();
                emoji.setUserID(uid);
                emoji.setSourceUrl(dest.getAbsolutePath());
                emoji.setSourceW(fw);
                emoji.setSourceH(fh);
                realm.insertOrUpdate(emoji);
            }, () -> Common.UIHandler.post(() -> {
                Toast.makeText(getActivity(), "已加入我的表情", Toast.LENGTH_SHORT).show();
                if (gifDialog != null && gifDialog.isShowing()) gifDialog.refreshCustom();
            }), error -> Common.UIHandler.post(() -> {
                dest.delete();
                Toast.makeText(getActivity(), "上传失败，请重试", Toast.LENGTH_SHORT).show();
            }));
        } catch (Exception e) {
            L.e(e.getMessage());
            Toast.makeText(getActivity(), "上传失败，请重试", Toast.LENGTH_SHORT).show();
        }
    }
}
