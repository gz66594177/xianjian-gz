package io.openim.android.demo.ui.user;

import io.openim.android.demo.services.XianliaoKeepAlive;
import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.provider.Settings;
import android.os.Bundle;
import android.text.TextUtils;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import androidx.appcompat.app.AppCompatActivity;
import androidx.lifecycle.ViewModel;

import com.alibaba.android.arouter.launcher.ARouter;

import java.util.Observable;
import java.util.Observer;

import io.openim.android.demo.databinding.FragmentPersonalBinding;
import io.openim.android.demo.fund.FundApi;
import io.openim.android.demo.fund.FundModels;
import io.openim.android.demo.fund.FundSecurityActivity;
import io.openim.android.demo.fund.Money;
import io.openim.android.demo.fund.RedpacketRecordsActivity;
import io.openim.android.demo.fund.WalletActivity;
import io.openim.android.demo.ui.login.LoginActivity;
import io.openim.android.demo.vm.MainVM;
import io.openim.android.ouiconversation.ui.PreviewMediaActivity;
import io.openim.android.ouicore.base.BaseApp;
import io.openim.android.ouicore.base.BaseFragment;
import io.openim.android.ouicore.base.vm.injection.Easy;
import io.openim.android.ouicore.im.IMUtil;
import io.openim.android.ouicore.utils.ActivityManager;
import io.openim.android.ouicore.utils.Common;
import io.openim.android.ouicore.utils.Constants;
import io.openim.android.ouicore.utils.Obs;
import io.openim.android.ouicore.utils.OnDedrepClickListener;
import io.openim.android.ouicore.utils.Routes;
import io.openim.android.ouicore.vm.PreviewMediaVM;
import io.openim.android.ouicore.vm.UserLogic;
import io.openim.android.ouicore.widget.CommonDialog;
import io.openim.android.ouicore.widget.WaitDialog;
import io.openim.android.sdk.OpenIMClient;
import io.openim.android.sdk.listener.OnBase;
import io.openim.android.sdk.models.GroupInfo;
import io.openim.android.sdk.models.UserInfo;

import org.json.JSONObject;

import java.util.List;

public class PersonalFragment extends BaseFragment implements Observer {
    public FragmentPersonalBinding view;
    private final UserLogic user = Easy.find(UserLogic.class);
    private MainVM mainVM = null;
    public static PersonalFragment newInstance() {

        Bundle args = new Bundle();

        PersonalFragment fragment = new PersonalFragment();
        fragment.setArguments(args);
        return fragment;
    }

    public PersonalFragment setParentViewModel(ViewModel viewModel) {
        if (viewModel instanceof MainVM)
            mainVM = (MainVM) viewModel;
        return this;
    }


    @Nullable
    @Override
    public View onCreateView(@NonNull LayoutInflater inflater, @Nullable ViewGroup container, @Nullable Bundle savedInstanceState) {
        view = FragmentPersonalBinding.inflate(inflater);

        initView();
        listener();
        Obs.inst().addObserver(this);
        return view.getRoot();
    }

    @Override
    public void onResume() {
        super.onResume();
        // 钱包卡实时更新：我的页 Fragment 在底部导航里是 hide/show 复用、切回时不重建，
        // 只在 onCreateView 里拉一次余额就会一直停在旧数；回到前台就重拉，不用重启 App。
        if (view != null) loadWalletBalance();
    }

    @Override
    public void onHiddenChanged(boolean hidden) {
        super.onHiddenChanged(hidden);
        // 底部导航切 Tab 走 hide/show，不走 onResume；切回我的页时同样重拉。
        if (!hidden && view != null) loadWalletBalance();
    }

    private void initView() {
        user.info.observe(getActivity(),v-> {
            if (!TextUtils.isEmpty(v.getNickname())) {
                view.avatar.load(v.getFaceURL(),v.getNickname());
                view.name.setText(v.getNickname());
            }
        });
        view.userId.setText("ID：" + BaseApp.inst().loginCertificate.userID);
        loadStats();
        loadWalletBalance();
    }

    /** 数据条：好友/群聊数取 SDK 分页首批（与 SocialityVM 同口径）。 */
    private void loadStats() {
        OpenIMClient.getInstance().friendshipManager.getFriendListPage(new OnBase<List<UserInfo>>() {
            @Override
            public void onError(int code, String error) {
                setStat(view.statFriendCount, "--");
            }

            @Override
            public void onSuccess(List<UserInfo> data) {
                setStat(view.statFriendCount, data == null ? "--"
                    : (data.size() >= 500 ? "500+" : String.valueOf(data.size())));
            }
        }, 0, 500);
        OpenIMClient.getInstance().groupManager.getJoinedGroupListPage(new OnBase<List<GroupInfo>>() {
            @Override
            public void onError(int code, String error) {
                setStat(view.statGroupCount, "--");
            }

            @Override
            public void onSuccess(List<GroupInfo> data) {
                setStat(view.statGroupCount, data == null ? "--"
                    : (data.size() >= 300 ? "300+" : String.valueOf(data.size())));
            }
        }, 0, 300);
    }

    private void setStat(android.widget.TextView tv, String text) {
        if (getActivity() == null) return;
        getActivity().runOnUiThread(() -> tv.setText(text));
    }

    /** 钱包卡余额只做展示；钱包不存在时不在此建钱包（进钱包页时 WalletActivity 会幂等创建）。 */
    private void loadWalletBalance() {
        FundApi.getWallet(new FundApi.Callback() {
            @Override
            public void onOk(Object data) {
                JSONObject w = FundModels.toObj(data);
                if (w != null && getActivity() != null) {
                    getActivity().runOnUiThread(() -> view.tvWalletBalance.setText(
                        Money.formatCents(w.optLong("balance_cents", 0))));
                }
            }

            @Override
            public void onErr(String code, String message) {
                // 新用户尚无钱包记录时直接建钱包再取余额，卡片显示 ¥0.00 而不是 --。
                // 钱包页本身已是这个口径，这里只是让我的页不用先进钱包页才正常。
                if (("WALLET_NOT_FOUND".equals(code) || "NOT_FOUND".equals(code))
                    && getActivity() != null) {
                    FundApi.createWallet(new FundApi.Callback() {
                        @Override
                        public void onOk(Object data) {
                            loadWalletBalance();
                        }

                        @Override
                        public void onErr(String c, String m) {
                        }
                    });
                }
            }
        });
    }

    @Override
    public void onDestroy() {
        super.onDestroy();
        Obs.inst().deleteObserver(this);
    }

    private void listener() {
        view.avatar.setOnClickListener(v -> {
            PreviewMediaVM mediaVM = Easy.installVM(PreviewMediaVM.class);
            if (user.info.val() != null && !TextUtils.isEmpty(user.info.val().getNickname()) && !TextUtils.isEmpty(user.info.val().getFaceURL())) {
                PreviewMediaVM .MediaData mediaData =new PreviewMediaVM.MediaData(user.info.val().getNickname());
                mediaData.mediaUrl=user.info.val().getFaceURL();
                mediaVM.preview(mediaData);
                v.getContext().startActivity(
                    new Intent(v.getContext(), PreviewMediaActivity.class));
            }
        });
        // 钱包卡：余额 + 进入钱包（卡上不再摊开充值/提现按钮）。
        view.walletLy.setOnClickListener(v -> {
            startActivity(new Intent(getActivity(), WalletActivity.class));
        });
        // 常用功能宫格。
        view.tileFundSecurity.setOnClickListener(v -> {
            startActivity(new Intent(getActivity(), FundSecurityActivity.class));
        });
        view.tileRedpacketRecords.setOnClickListener(v -> {
            startActivity(new Intent(getActivity(), RedpacketRecordsActivity.class));
        });
        view.tilePersonalInfo.setOnClickListener(new OnDedrepClickListener(1000) {
            @Override
            public void click(View v) {
                startActivity(new Intent(getActivity(), PersonalInfoActivity.class));
            }
        });
        view.tileInvite.setOnClickListener(v -> {
            Intent share = new Intent(Intent.ACTION_SEND);
            share.setType("text/plain");
            share.putExtra(Intent.EXTRA_TEXT, "我在用仙聊，和我一起聊天吧：https://im.yuni110.com");
            startActivity(Intent.createChooser(share, "邀请好友"));
        });
        view.tileHelp.setOnClickListener(v -> {
            startActivity(new Intent(getActivity(), AboutUsActivity.class));
        });
        // 设置与支持宫格。
        view.tileNotify.setOnClickListener(v -> {
            // 消息通知无应用内开关页：直接打开系统通知设置（真实功能，不做假开关）。
            Intent intent;
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                intent = new Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS);
                intent.putExtra(Settings.EXTRA_APP_PACKAGE, getActivity().getPackageName());
            } else {
                intent = new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS);
                intent.setData(Uri.parse("package:" + getActivity().getPackageName()));
            }
            startActivity(intent);
        });
        view.tileGeneral.setOnClickListener(v -> {
            startActivity(new Intent(getActivity(), AccountSettingActivity.class));
        });
        io.openim.android.demo.fund.FundUi.pressScale(view.walletLy, view.tileFundSecurity,
            view.tileRedpacketRecords, view.tilePersonalInfo, view.tileInvite, view.tileHelp,
            view.tileNotify, view.tileGeneral, view.quit);
        view.userIdLy.setOnClickListener(v -> {
            Common.copy(BaseApp.inst().loginCertificate.userID);
            toast(getString(io.openim.android.ouicore.R.string.copy_succ));
        });
        view.quit.setOnClickListener(v -> {
            CommonDialog commonDialog = new CommonDialog(getActivity()).atShow();
            commonDialog.getMainView().tips.
                setText(io.openim.android.ouicore.R.string.quit_tips);
            commonDialog.getMainView().cancel.setOnClickListener(v2 -> commonDialog.dismiss());
            commonDialog.getMainView().confirm.setOnClickListener(v2 -> {
                commonDialog.dismiss();
                WaitDialog waitDialog = new WaitDialog(getActivity());
                waitDialog.show();
                OpenIMClient.getInstance().logout(new OnBase<String>() {
                    @Override
                    public void onError(int code, String error) {
                        waitDialog.dismiss();
                        toast(error + code);
                    }

                    @Override
                    public void onSuccess(String data) {
                        waitDialog.dismiss();
                        if (mainVM != null)
                            mainVM.clearOfflineNotificationConfig(ActivityManager.getActivityStack().pop(), BaseApp.inst().loginCertificate.userID);
                        XianliaoKeepAlive.stop(getActivity());
                        IMUtil.logout((AppCompatActivity) getActivity(), LoginActivity.class);
                    }
                });

            });
        });
    }

    @Override
    public void update(Observable observable, Object o) {
        Obs.Message message = (Obs.Message) o;
        if (message.tag == Constants.Event.USER_INFO_UPDATE) {
            view.avatar.load(BaseApp.inst().loginCertificate.faceURL,BaseApp.inst().loginCertificate.nickname);
            view.name.setText(BaseApp.inst().loginCertificate.nickname);
        }
    }
}
