package io.openim.android.ouicontact.ui.fragment;

import android.app.Activity;
import android.content.Intent;
import android.os.Bundle;
import android.text.TextUtils;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;
import android.widget.FrameLayout;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import androidx.recyclerview.widget.LinearLayoutManager;
import androidx.recyclerview.widget.RecyclerView;
import androidx.lifecycle.ViewModelProvider;

import com.alibaba.android.arouter.facade.annotation.Route;
import com.alibaba.android.arouter.launcher.ARouter;

import java.util.ArrayList;
import java.util.List;

import io.openim.android.ouicontact.databinding.FragmentContactMainBinding;
import io.openim.android.ouicontact.databinding.ItemDepartmentBinding;
import io.openim.android.ouicontact.databinding.ViewContactHeaderBinding;
import io.openim.android.ouicontact.ui.AddRelationActivity;
import io.openim.android.ouicontact.ui.AllFriendActivity;
import io.openim.android.ouicontact.ui.GroupNoticeListActivity;
import io.openim.android.ouicontact.ui.MyGroupActivity;
import io.openim.android.ouicore.adapter.RecyclerViewAdapter;
import io.openim.android.ouicore.adapter.ViewHol;
import io.openim.android.ouicore.entity.ExUserInfo;
import io.openim.android.ouicore.utils.Common;
import io.openim.android.ouicore.vm.SocialityVM;
import io.openim.android.ouicore.widget.StickyDecoration;
import io.openim.android.sdk.models.UserInfo;
import io.openim.android.ouicontact.ui.NewFriendActivity;
import io.openim.android.ouicontact.vm.ContactVM;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.base.BaseFragment;
import io.openim.android.ouicore.base.vm.injection.Easy;
import io.openim.android.ouicore.ex.MultipleChoice;
import io.openim.android.ouicore.net.RXRetrofit.NetObserver;
import io.openim.android.ouicore.utils.ActivityManager;
import io.openim.android.ouicore.utils.Constants;
import io.openim.android.ouicore.utils.OnDedrepClickListener;
import io.openim.android.ouicore.utils.Routes;
import io.openim.android.ouicore.utils.SinkHelper;
import io.openim.android.ouicore.vm.NotificationVM;
import io.openim.android.ouicore.vm.SelectTargetVM;
import io.reactivex.Observable;
import io.reactivex.ObservableOnSubscribe;
import io.reactivex.android.schedulers.AndroidSchedulers;

@Route(path = Routes.Contact.HOME)
public class ContactFragment extends BaseFragment<ContactVM>  {
    private FragmentContactMainBinding view;
    private ViewContactHeaderBinding header;
    private SocialityVM socialityVM;
    private RecyclerViewAdapter<ExUserInfo, RecyclerView.ViewHolder> friendAdapter;
    private boolean friendLoaded;
    private NotificationVM notificationVM;

    @Override
    public void onCreate(@Nullable Bundle savedInstanceState) {
        bindVM(ContactVM.class);
        socialityVM = new ViewModelProvider(this).get(SocialityVM.class);
        socialityVM.setContext(getContext());
        socialityVM.setIView(this);
        notificationVM=Easy.find(NotificationVM.class);
        BaseApp.inst().putVM(vm);
        super.onCreate(savedInstanceState);
    }

    @Nullable
    @Override
    public View onCreateView(@NonNull LayoutInflater inflater, @Nullable ViewGroup container,
                             @Nullable Bundle savedInstanceState) {
        view = FragmentContactMainBinding.inflate(getLayoutInflater());
        header = view.header;
        FrameLayout.LayoutParams lp = (FrameLayout.LayoutParams) view.title.getLayoutParams();
        lp.setMargins(0, SinkHelper.getStatusBarHeight(), 0, 0);
        view.title.setLayoutParams(lp);

        initView();
        click();
        initFriendList();
        // SocialityVM 的 temp 列表会随每次 getAllFriend 累加，故只拉一次；
        // 之后进资料页回来不重拉，避免好友重复出现。
        if (!friendLoaded) {
            friendLoaded = true;
            socialityVM.getAllFriend();
        }

        return view.getRoot();
    }


    public ContactVM getVM() {
        return vm;
    }

    private void click() {
        view.addFriend.setOnClickListener(view1 -> startActivity(new Intent(getActivity(), AddRelationActivity.class)));

        header.groupNotice.setOnClickListener(v -> {
            notificationVM.clearDot(notificationVM.groupDot);
            startActivity(new Intent(getActivity(), GroupNoticeListActivity.class));
        });

        header.newFriendNotice.setOnClickListener(v -> {
            notificationVM.clearDot(notificationVM.friendDot);
            startActivity(new Intent(getActivity(), NewFriendActivity.class));
        });

        header.myGroup.setOnClickListener(v -> {
            startActivity(new Intent(getActivity(), MyGroupActivity.class));
        });
    }


    private void initFriendList() {
        view.friendRecyclerView.setLayoutManager(new LinearLayoutManager(getContext()));
        friendAdapter = new RecyclerViewAdapter<ExUserInfo, RecyclerView.ViewHolder>() {
            @NonNull
            @Override
            public RecyclerView.ViewHolder onCreateViewHolder(@NonNull ViewGroup parent,
                                                              int viewType) {
                return new ViewHol.ItemViewHo(parent);
            }

            @Override
            public void onBindView(@NonNull RecyclerView.ViewHolder holder, ExUserInfo data,
                                   int position) {
                ViewHol.ItemViewHo itemViewHo = (ViewHol.ItemViewHo) holder;
                UserInfo friendInfo = data.userInfo;
                itemViewHo.view.avatar.load(friendInfo.getFaceURL());
                itemViewHo.view.nickName.setText(friendInfo.getNickname());
                itemViewHo.view.select.setVisibility(View.GONE);
                itemViewHo.view.getRoot().setOnClickListener(v -> {
                    // 原“我的好友”按钮的最终效果就是进个人资料页，这里直接进资料页。
                    ARouter.getInstance().build(Routes.Main.PERSON_DETAIL)
                        .withString(Constants.K_ID, friendInfo.getUserID())
                        .navigation(getActivity());
                });
            }
        };
        view.friendRecyclerView.setAdapter(friendAdapter);
        view.friendRecyclerView.addItemDecoration(new StickyDecoration(getContext(),
            position -> friendAdapter.getItems().get(position).sortLetter));
        socialityVM.letters.observe(getActivity(), v -> {
            if (null == v || v.isEmpty()) return;
            StringBuilder letters = new StringBuilder();
            for (String s : v) {
                letters.append(s);
            }
            view.friendSortView.setLetters(letters.toString());
        });
        socialityVM.exUserInfo.observe(getActivity(), v -> {
            // 与 AllFriendActivity 同路：空列表时不设数据，列表区留空，
            // 不遮挡顶部入口行，加载中与空好友均不伪造数据。
            if (null == v || v.isEmpty()) return;
            friendAdapter.setItems(new ArrayList<>(v));
        });
        view.friendSortView.setOnLetterChangedListener((letter, position) -> {
            for (int i = 0; i < friendAdapter.getItems().size(); i++) {
                ExUserInfo exUserInfo = friendAdapter.getItems().get(i);
                if (exUserInfo.sortLetter.equalsIgnoreCase(letter)) {
                    Common.smoothMoveToPosition(view.friendRecyclerView, i);
                    return;
                }
            }
        });
        view.friendRecyclerView.addOnScrollListener(new RecyclerView.OnScrollListener() {
            @Override
            public void onScrollStateChanged(@NonNull RecyclerView recyclerView, int newState) {
                super.onScrollStateChanged(recyclerView, newState);
                if (Common.mShouldScroll && RecyclerView.SCROLL_STATE_IDLE == newState) {
                    Common.mShouldScroll = false;
                    Common.smoothMoveToPosition(view.friendRecyclerView, Common.mToPosition);
                }
            }
        });
    }

    private void initView() {
        notificationVM.groupDot.observe(getActivity(), v -> {
            header.badge.badge.setVisibility(v.isEmpty() ? View.GONE : View.VISIBLE);
            header.badge.badge.setText(v.size() > 100 ? "99+" : String.valueOf(v.size()));
        });
        notificationVM.friendDot.observe(getActivity(), v -> {
            header.newFriendNoticeBadge.badge.setVisibility(v.isEmpty() ? View.GONE : View.VISIBLE);
            header.newFriendNoticeBadge.badge.setText(v.size() > 100 ? "99+" : String.valueOf(v.size()));
        });
    }

    @Override
    public void onDestroy() {
        super.onDestroy();
        BaseApp.inst().removeCacheVM(ContactVM.class);
    }
}
