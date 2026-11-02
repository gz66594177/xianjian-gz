package io.openim.android.ouiapplet;

import android.graphics.Bitmap;
import android.os.Bundle;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.view.LayoutInflater;
import android.view.View;
import android.view.ViewGroup;

import androidx.annotation.NonNull;
import androidx.annotation.Nullable;
import androidx.fragment.app.Fragment;

import com.alibaba.android.arouter.facade.annotation.Route;

import io.openim.android.ouiapplet.databinding.ActivityAppletBinding;
import io.openim.android.ouiapplet.databinding.FragmentAppletBinding;
import io.openim.android.ouiapplet.vm.AppletVM;
import io.openim.android.ouicore.base.BaseFragment;
import io.openim.android.ouicore.base.vm.injection.Easy;
import io.openim.android.ouicore.utils.Routes;
import io.openim.android.ouicore.vm.UserLogic;


@Route(path = Routes.Applet.HOME)
public class AppletFragment extends BaseFragment {
    private ActivityAppletBinding view;
    private AppletVM appletVM;

    @Nullable
    @Override
    public View onCreateView(@NonNull LayoutInflater inflater, @Nullable ViewGroup container, @Nullable Bundle savedInstanceState) {
        view=ActivityAppletBinding.inflate(getLayoutInflater());

        initView();
        return view.getRoot();
    }

    private void initView() {
        // 发现页 = 后台客户端配置的 discoverPageURL 整页 WebView 壳（默认 docs.openim.io），
        // 不设商城/朋友圈/直播/扫一扫/附近的人/任务中心/客服中心/我的二维码等自编入口。
        view.webView.getSettings().setJavaScriptEnabled(true);
        view.webView.getSettings().setDomStorageEnabled(true);
        view.webView.setWebViewClient(new WebViewClient() {
            @Override
            public void onPageStarted(WebView v, String url, Bitmap favicon) {
                view.loading.setVisibility(View.VISIBLE);
            }

            @Override
            public void onPageFinished(WebView v, String url) {
                view.loading.setVisibility(View.GONE);
            }
        });
        Easy.find(UserLogic.class).discoverPageURL
            .observe(getViewLifecycleOwner(),
            s -> {
                if (s == null || s.isEmpty()) {
                    view.webView.loadUrl("https://docs.openim.io/");
                } else {
                    view.webView.loadUrl(s);
                }
            });
    }


}
