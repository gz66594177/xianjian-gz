package io.openim.android.ouiconversation.ui.fragment;

import android.os.Bundle;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.widget.TextView;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import androidx.recyclerview.widget.GridLayoutManager;
import androidx.recyclerview.widget.RecyclerView;

import io.openim.android.ouiconversation.R;
import io.openim.android.ouiconversation.vm.ChatVM;
import io.openim.android.ouicore.base.BaseFragment;

/**
 * 输入框 emoji 面板：由 BottomInputCote 托管在 layout_input_cote 的 fragment_container 里，
 * 与 + 面板（InputExpandFragment）共用同一容器、同一套切换逻辑。
 * 点 emoji 只回调给 BottomInputCote 插入到输入框光标处，不直接发送；
 * 渲染走输入框本身的 EmojiEditText 与会话的 EmojiTextView（vanniktech GoogleEmojiProvider，
 * DemoApplication 已安装），本文件不另起一套 emoji 渲染。
 */
public class XlEmojiFragment extends BaseFragment<ChatVM> {

    public interface OnEmojiInputListener {
        void onEmojiSelected(String emoji);

        void onEmojiDelete();
    }

    private OnEmojiInputListener listener;

    public void setListener(OnEmojiInputListener listener) {
        this.listener = listener;
    }

    // 常用 emoji：笑脸 / 手势 / 爱心 / 动物 / 食物，一屏网格内滑动浏览。
    // 用字面量写（工程内已有中文字符串字面量，UTF-8 编译无问题），不要改成图片表情，
    // 以免与 + 面板里的 GIF 表情（图片消息）混淆：这里是文本 emoji，随文本消息发出。
    private static final String[] EMOJIS = new String[]{
            "😀", "😃", "😄", "😁", "😆", "😅", "😂", "🤣",
            "😊", "😇", "🙂", "🙃", "😉", "😌", "😍", "🥰",
            "😘", "😗", "😙", "😚", "😋", "😛", "😝", "😜",
            "🤪", "🤨", "🧐", "🤓", "😎", "🤩", "🥳", "😏",
            "😒", "😞", "😔", "😟", "😕", "🙁", "😣", "😖",
            "😫", "😩", "🥺", "😢", "😭", "😤", "😡", "😠",
            "🤯", "😳", "🥵", "🥶", "😱", "😨", "😰", "😥",
            "😓", "🤗", "🤔", "🤭", "🤫", "🤥", "😶", "😐",
            "😑", "😬", "🙄", "😯", "😦", "😧", "😮", "😲",
            "🥱", "😴", "🤤", "😪", "😵", "🤐", "🥴", "🤢",
            "👍", "👎", "👏", "🙌", "🤝", "✌️", "🤞", "🤟",
            "🤘", "👌", "👈", "👉", "👆", "👇", "✋", "👋",
            "🤙", "💪", "🙏", "❤️", "🧡", "💛", "💚", "💙",
            "💜", "🖤", "🤍", "💔", "💕", "💖", "💗", "💓",
            "💞", "💯", "💥", "💫", "💦", "💨", "🎉", "🎊",
            "🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼",
            "🐨", "🐯", "🦁", "🐮", "🐷", "🐸", "🐵", "🙈",
            "🍎", "🍊", "🍋", "🍉", "🍇", "🍓", "🍒", "🍑",
            "🥭", "🍍", "🥝", "🍅", "🥑", "🍞", "🧀", "🥚",
            "🍗", "🌭", "🍔", "🍟", "🍕", "🍝", "🍜", "🍣",
            "🍱", "🍙", "🍚", "🍰", "🎂", "🍭", "🍬", "🍫",
            "🍿", "🍩", "🍪", "☕", "🍵", "🥤", "🍺", "🍻",
    };

    @Nullable
    @Override
    public View onCreateView(@NonNull LayoutInflater inflater, @Nullable ViewGroup container,
                             @Nullable Bundle savedInstanceState) {
        View root = inflater.inflate(R.layout.fragment_xl_emoji, container, false);
        RecyclerView grid = root.findViewById(R.id.xlEmojiGrid);
        grid.setLayoutManager(new GridLayoutManager(getContext(), 8));
        grid.setAdapter(new RecyclerView.Adapter<EmojiHolder>() {
            @NonNull
            @Override
            public EmojiHolder onCreateViewHolder(@NonNull ViewGroup parent, int viewType) {
                View item = LayoutInflater.from(parent.getContext())
                        .inflate(R.layout.item_xl_emoji, parent, false);
                return new EmojiHolder(item);
            }

            @Override
            public void onBindViewHolder(@NonNull EmojiHolder holder, int position) {
                holder.text.setText(EMOJIS[position]);
                holder.text.setOnClickListener(v -> {
                    if (listener != null) listener.onEmojiSelected(EMOJIS[position]);
                });
            }

            @Override
            public int getItemCount() {
                return EMOJIS.length;
            }
        });
        root.findViewById(R.id.xlEmojiDelete).setOnClickListener(v -> {
            if (listener != null) listener.onEmojiDelete();
        });
        return root;
    }

    private static class EmojiHolder extends RecyclerView.ViewHolder {
        final TextView text;

        EmojiHolder(@NonNull View itemView) {
            super(itemView);
            text = (TextView) itemView;
        }
    }
}
