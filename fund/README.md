# 仙聊资金服务（Phase 9）

独立 Go 资金服务：人民币钱包数据库及统一账务核心 + 交易密码完整服务 + 提现账户三选多绑完整流程 + IM token 鉴权 + 好友红包（单聊红包）+ 群普通红包 + 群拼手气红包 + 用户之间余额转账（即时到账）+ 管理端资金中心 API（钱包/流水/准入查询、提现账户与交易密码管理、管理员资金调整单双人复核、提现订单审核、风控限额配置）+ 人民币充值（回调入账）+ 人民币提现（7 态全流程、人工打款）。与 YiChat、OpenIM 聊天业务严格分离；不做真实渠道自动打款、不用真实支付渠道、不碰数字货币/区块链。

## 目录

- `cmd/server/` — HTTP 服务入口（监听回环地址，配置全走环境变量）
- `cmd/migrate/` — 版本化迁移工具（管理员账号执行，只动资金库；up 按 0001 -> 0002 -> 0003 -> 0004 -> 0005 -> 0006 -> 0007 -> 0008 -> 0009，down 逆序）
- `internal/fund/` — 账务核心、交易密码、找回双人复核、提现账户、准入、对账、好友红包、群普通红包、群拼手气红包、转账、管理端资金中心、充值、提现、HTTP API、124 项测试
- `migrations/` — `0001_init`（9 张表）/ `0002_recovery`（找回申请单）/ `0003_redpacket`（红包包单与领取记录）/ `0004_group_redpacket`（群红包）/ `0005_group_lucky_redpacket`（群拼手气红包）/ `0006_transfer`（转账订单）/ `0007_admin`（管理员调整单、风控限额、提现审核字段）/ `0008_recharge`（充值专属字段）/ `0009_withdraw`（提现扩展字段、手续费与限额配置表）
- `docs/` — `API.md`（接口文档）、`DATABASE.md`（数据库设计）、`TEST_REPORT.md`（测试报告）、`SECURITY_CHECK.md`（安全检查）、`CHANGELOG.md`（变更记录）、`test-output.log`（测试原始输出）
- `deploy/` — systemd 单元参考（生产单元在香港三 `/etc/systemd/system/fund-xianliao.service`，密钥文件不在仓库内）

## 部署现状（香港三）

- Postgres 容器 `fund-postgres`：`127.0.0.1:15432`，库 `xianliao_fund`，数据卷 `/opt/xianliao/fund-postgres-data`
- Redis 容器 `fund-redis`：`127.0.0.1:16381`，仅 `fund:` 前缀键
- systemd 服务 `fund-xianliao`：监听 `127.0.0.1:18081`，`EnvironmentFile=/opt/xianliao/fund-service/fund.env`（0600 root 专属）
- 源码部署目录：`/opt/xianliao/fund-service/src`；二进制：`/opt/xianliao/fund-service/fund-server`、`fund-migrate`

## 本地开发 / 复现测试

```bash
# 1) 迁移（管理员 DSN，只动资金库）
FUND_ADMIN_DATABASE_URL=... FUND_DATABASE_URL=... MIGRATIONS_DIR=./migrations go run ./cmd/migrate up

# 2) 启动（配置来自环境变量；缺 AES 密钥或缺 IM API 地址拒绝启动）
# 用户鉴权：Authorization: Bearer <IM token>，资金服务调 FUND_IM_API_BASE 的 /auth/parse_token 解析；
# FUND_INTERNAL_SECRET 已废除（Phase 2 起不再接受内部口令头）。
FUND_ADDR=127.0.0.1:18081 FUND_DATABASE_URL=... FUND_REDIS_ADDR=127.0.0.1:16381 \
FUND_ADMIN_SECRET=<YOUR_ADMIN_SECRET>

# 3) 测试（测试库 xianliao_fund_test，跑前跑后清理）
FUND_TEST_ADMIN_URL=... FUND_TEST_REDIS_ADDR=127.0.0.1:16381 go test ./internal/fund/ -v -count=1
```

Go 版本：香港三 `/opt/go1.26/bin/go`（go 1.26.0）。

## 阶段边界

Phase 2 在 Phase 1 基础上做：IM token 鉴权（parse_token）、交易密码完整服务（设置/修改/锁定/找回：身份核验 + 双人复核 + 本人重置）、提现账户三选多绑完整流程（递补默认、删唯一、去重、格式校验）、准入引导（next_step/guidance）与业务守卫。
不做：好友红包、群红包、拼手气红包、真实充值、真实提现、数字货币、区块链、真实支付渠道。
