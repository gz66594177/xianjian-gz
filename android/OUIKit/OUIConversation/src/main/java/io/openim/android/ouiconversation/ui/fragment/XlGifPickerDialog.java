package io.openim.android.ouiconversation.ui.fragment;

import android.app.Dialog;
import android.content.Context;
import android.view.Gravity;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.view.Window;
import android.widget.ImageView;
import android.widget.TextView;

import androidx.annotation.NonNull;
import androidx.recyclerview.widget.GridLayoutManager;
import androidx.recyclerview.widget.RecyclerView;

import com.bumptech.glide.Glide;

import java.io.File;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

import io.openim.android.ouiconversation.R;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.entity.CustomEmoji;
import io.openim.android.ouicore.utils.Common;
import io.realm.RealmResults;

/**
 * GIF 选择器：底部弹窗，网格预览（Glide 默认播 GIF 动图，不只首帧）。
 * 网格第一格就是自定义上传入口；随后是“我的表情”（本机 Realm，用户自己的库），
 * 再后是内置大肥鱼 6 个主题分组（日常 + 抽象反应 + 亲密陪伴 + 打工摸鱼 + 情绪状态 + 祝福庆祝）；自定义与内置严格分开，自定义不混进内置。
 */
public class XlGifPickerDialog extends Dialog {

    public interface Listener {
        void onBuiltinGifSelected(String assetPath);

        void onCustomGifSelected(String filePath);

        void onUploadClicked();
    }

    private static final int TYPE_UPLOAD = 0;
    private static final int TYPE_HEADER = 1;
    private static final int TYPE_GIF = 2;

    private static class Row {
        int type;
        String header;
        XlGifPack.Item builtin;
        CustomEmoji custom;

        Row(int type) {
            this.type = type;
        }
    }

    private final Listener listener;
    private final List<CustomEmoji> customList = new ArrayList<>();
    private final List<Row> rows = new ArrayList<>();
    private RecyclerView.Adapter<?> adapter;

    public XlGifPickerDialog(@NonNull Context context, @NonNull Listener listener) {
        super(context);
        this.listener = listener;
        setContentView(R.layout.dialog_xl_gif_picker);
        Window window = getWindow();
        if (window != null) {
            window.setGravity(Gravity.BOTTOM);
            window.setLayout(ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT);
            window.setBackgroundDrawableResource(android.R.color.transparent);
            window.setWindowAnimations(io.openim.android.ouicore.R.style.XianliaoBottomAnim);
        }
        findViewById(R.id.close).setOnClickListener(v -> dismiss());
        RecyclerView grid = findViewById(R.id.gifGrid);
        GridLayoutManager layoutManager = new GridLayoutManager(context, 4);
        layoutManager.setSpanSizeLookup(new GridLayoutManager.SpanSizeLookup() {
            @Override
            public int getSpanSize(int position) {
                return rows.get(position).type == TYPE_HEADER ? 4 : 1;
            }
        });
        grid.setLayoutManager(layoutManager);
        adapter = new RecyclerView.Adapter<RecyclerView.ViewHolder>() {
            @NonNull
            @Override
            public RecyclerView.ViewHolder onCreateViewHolder(@NonNull ViewGroup parent,
                                                              int viewType) {
                LayoutInflater inflater = LayoutInflater.from(parent.getContext());
                if (viewType == TYPE_HEADER) {
                    return new RecyclerView.ViewHolder(
                        inflater.inflate(R.layout.item_xl_gif_header, parent, false)) {
                    };
                }
                if (viewType == TYPE_UPLOAD) {
                    return new RecyclerView.ViewHolder(
                        inflater.inflate(R.layout.item_xl_gif_upload, parent, false)) {
                    };
                }
                return new RecyclerView.ViewHolder(
                    inflater.inflate(R.layout.item_xl_gif, parent, false)) {
                };
            }

            @Override
            public void onBindViewHolder(@NonNull RecyclerView.ViewHolder holder,
                                         int position) {
                Row row = rows.get(position);
                if (row.type == TYPE_HEADER) {
                    ((TextView) holder.itemView.findViewById(R.id.headerText))
                        .setText(row.header);
                    return;
                }
                if (row.type == TYPE_UPLOAD) {
                    holder.itemView.setOnClickListener(v -> listener.onUploadClicked());
                    return;
                }
                ImageView img = holder.itemView.findViewById(R.id.gifImg);
                TextView name = holder.itemView.findViewById(R.id.gifName);
                if (row.builtin != null) {
                    name.setText(row.builtin.name);
                    Glide.with(holder.itemView.getContext()).clear(img);
                    img.setImageDrawable(null);
                    Glide.with(holder.itemView.getContext())
                        .load("file:///android_asset/" + row.builtin.asset)
                        .into(img);
                    holder.itemView.setOnClickListener(
                        v -> listener.onBuiltinGifSelected(row.builtin.asset));
                } else if (row.custom != null) {
                    name.setText("我的表情");
                    Glide.with(holder.itemView.getContext()).clear(img);
                    img.setImageDrawable(null);
                    Glide.with(holder.itemView.getContext())
                        .load(new File(row.custom.getSourceUrl()))
                        .into(img);
                    holder.itemView.setOnClickListener(
                        v -> listener.onCustomGifSelected(row.custom.getSourceUrl()));
                }
            }

            @Override
            public int getItemViewType(int position) {
                return rows.get(position).type;
            }

            @Override
            public int getItemCount() {
                return rows.size();
            }
        };
        grid.setAdapter(adapter);
        rebuildRows();
        loadCustom();
    }

    private void rebuildRows() {
        rows.clear();
        // 第一格：自定义上传入口（永远在最前）。
        rows.add(new Row(TYPE_UPLOAD));
        Row customHeader = new Row(TYPE_HEADER);
        customHeader.header = customList.isEmpty()
            ? "我的表情（暂无，点 + 上传）" : "我的表情";
        rows.add(customHeader);
        for (CustomEmoji custom : customList) {
            Row row = new Row(TYPE_GIF);
            row.custom = custom;
            rows.add(row);
        }
        // 内置系列按 Group 分组展示（日常组在前，5 个新组在后）；
        // “我的表情”自定义分组在上，内置各组在下，两者严格分开。
        for (XlGifPack.Group group : XlGifPack.groups()) {
            Row groupHeader = new Row(TYPE_HEADER);
            groupHeader.header = group.title;
            rows.add(groupHeader);
            for (XlGifPack.Item item : group.items) {
                Row row = new Row(TYPE_GIF);
                row.builtin = item;
                rows.add(row);
            }
        }
        if (adapter != null) adapter.notifyDataSetChanged();
    }

    /** 从本机 Realm 读当前登录用户自己的自定义表情（不读他人、不读全量）。 */
    public void loadCustom() {
        try {
            if (BaseApp.inst() == null || BaseApp.inst().loginCertificate == null) return;
            final String uid = BaseApp.inst().loginCertificate.userID;
            BaseApp.inst().realm.executeTransactionAsync(realm -> {
                RealmResults<CustomEmoji> results = realm.where(CustomEmoji.class)
                    .equalTo("userID", uid).findAll();
                List<CustomEmoji> copy = realm.copyFromRealm(results);
                Common.UIHandler.post(() -> {
                    customList.clear();
                    // 最新上传的排在自定义分组最前。
                    Collections.reverse(copy);
                    customList.addAll(copy);
                    rebuildRows();
                });
            });
        } catch (Exception ignored) {
            // 读库失败时只显示内置系列，不阻塞选择器。
        }
    }

    /** 上传成功后由调用方刷新（不重建弹窗）。 */
    public void refreshCustom() {
        loadCustom();
    }
}
