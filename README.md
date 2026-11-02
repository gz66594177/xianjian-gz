# 仙聊 xianliao

基于 OpenIM 开源代码二次开发的全新 IM 产品，非 OpenIM 官方产品。

## 目录

- `android/`：安卓客户端源码（OpenIM Demo 二开，包名 `com.xianliao.im`）
- `fund/`：资金服务源码（Go 实现；仅人民币 CNY、金额整数分存储、双重记账、流水不可删）

## 说明

- 本仓库以 AGPL-3.0 公开（见 `android/LICENSE`）。
- 上游 Demo 的 README 与 LICENSE 文件在“是否允许商业使用”表述上存在不一致，尚未取得权利人书面澄清；本仓库不对外宣称已获官方确认。
- 仙聊与 YiChat 在代码、配置、域名、后台、资金逻辑上严格分离，互不复用。
