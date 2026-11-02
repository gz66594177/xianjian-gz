# 仙聊资金服务 0010 — 自动化测试报告（Phase 1–9 的 124 项全部回归通过）

0010 新增测试 125–152（见 §A）；Phase 1–9 的 124 项回归结果见下方原报告（同一轮测试输出）。测试库与命令与 Phase 1–9 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），鉴权用 stub IM 校验器（token `imtok-<uid>`），管理接口用 `test-admin-secret` 测试夹具，网关用测试 stub 适配器（HMAC 夹具密钥、收银台测试域名 `gateway.test`，仅测试库）。原始输出：[test-output-0010.log](test-output-0010.log)。本轮 `go test ./internal/fund/ -v -count=1` 结果：**152/152 通过，0 失败**（`ok xianliao-fund/internal/fund 34.746s`），`go vet ./...` 通过。首轮 4 项失败已修复：Test134/Test135 的期望码错（已终态再审批/再拒绝是非法状态迁移，本服务统一 409，非 400，仅测试期望修正）；Test136/Test149 暴露的真问题——Phase 8 回调列表/详情未过滤 kind，手动/网关单会漏进回调列表——已修复为回调端点只服务 `kind='callback'`（4 处查询加约束，回调单行为不变），重跑后全绿；另 2 处编译期修复：`ok` 响应 helper 被同名局部变量遮蔽（改名 `okForm`）、测试 `auditCount` 与既有 helper 重名（删重复定义，复用既有签名）。

## A. 0010 新增测试（28/28 通过）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 125 | PayMethodCRUD | 管理无密钥 401、非法类型 400；创建/改名/停用；管理列表含停用项、用户列表不含 | 通过 |
| 126 | QRUploadValidation | 非图片 400、超 5MB 400；上传后 `qr_url` 挂接；用户取码字节一致、未鉴权 401 | 通过 |
| 127 | ManualCreateValidation | 无码方式 400、停用方式 400、未知方式 404；金额 0/负数/非整数分 400；正常单状态 10 + 方式名 + 二维码、建单余额不动 | 通过 |
| 128 | ManualCreateIdempotent | 同一幂等键两次返回同一单、订单行恰 1 行 | 通过 |
| 129 | ProofUploadValidation | 他人订单 404、非图片 400、超大 400；正常上传状态 11 + 凭证字段 | 通过 |
| 130 | ProofReuploadReplaces | 状态 11 重传替换文件：旧文件行删除、旧地址取用 404 | 通过 |
| 131 | ProofOnPaidOrderRejected | 已入账单再传凭证 409 | 通过 |
| 132 | AdminPendingList | 待审核列只含状态 11、待上传单不在列；`user_id` 过滤生效 | 通过 |
| 133 | ManualApproveDualReview | 首复核待第二复核且余额不动、同人两次 400、第二名不同管理员入账分毫不差（type 20、分录 2 行合计 0） | 通过 |
| 134 | ManualApproveRepeatRejected | 已入账单再审批 409（两名管理员均然） | 通过 |
| 135 | ManualRejectNoCredit | 拒绝后状态 12 + 备注、余额与 txn 数不动；已拒绝单再拒绝 409 | 通过 |
| 136 | ManualOrderVisibility | 他人详情 404；回调列表不含手动单；通用列表 kind 过滤双向正确 | 通过 |
| 137 | ManualAuditLogs | 创建/凭证上传/执行三类审计行均存在 | 通过 |
| 138 | GatewayUnconfigured | 无适配器服务：建单 400 GATEWAY_NOT_CONFIGURED、回调 501、余额不动 | 通过 |
| 139 | GatewayPollZeroCredit | 建单返回收银台地址 + 30 分钟过期；5 次只读轮询状态与余额均不动（Yes 按钮零入账） | 通过 |
| 140 | GatewayCallbackPaid | 回调入账：状态 1、余额 += 金额、type 20、分录合计 0 | 通过 |
| 141 | GatewayCallbackIdempotent | 同一支付回调重放：同一单、只入账一次、type 20 txn 恰 1 行 | 通过 |
| 142 | GatewayCallbackBadSignature | 错签名 401、余额不动、订单仍待支付 | 通过 |
| 143 | GatewayCallbackAmountMismatch | 金额不一致 400、余额不动、审计行存在 | 通过 |
| 144 | GatewayCallbackFailed | 失败回调状态 2、不入账 | 通过 |
| 145 | GatewayExpiredCallbackRefused | 详情 `expired` 计算字段为真但状态不动；过期回调 409、订单关为 4、不入账 | 通过 |
| 146 | GatewayExpireScan | 过期单关单、未过期单不动、返回 `closed_ids` | 通过 |
| 147 | GatewayCallbackUnknownOrder | 未知订单回调 404 | 通过 |
| 148 | UploadAuthIsolation | 凭证他人 404、本人 200、管理 200；停用方式二维码用户取用 404 | 通过 |
| 149 | ThreeKindsCoexist | 通用列表三类齐全；回调列表只含回调单 | 通过 |
| 150 | LedgerConservationManualGateway | 手动审核 + 网关回调两笔：用户余额增量 = 平台户增量 = 两单之和 | 通过 |
| 151 | CrossKindIsolation | 网关回调指手动单 404、手动审核指网关单 404、余额不动 | 通过 |
| 152 | ManualCreateNeedsNoAdmission | 全新用户（无提现方式、无交易密码）仍可建手动单（建单不产生资金变动） | 通过 |

# 仙聊资金服务 Phase 9 — 自动化测试报告（Phase 1–8 的 107 项全部回归通过）

Phase 9 新增测试 108–124（见 §A）；Phase 1–8 的 107 项回归结果见下方原报告（同一轮测试输出）。测试库与命令与 Phase 1–8 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），鉴权用 stub IM 校验器（token `imtok-<uid>`），管理接口用 `test-admin-secret` 测试夹具。原始输出：[test-output-phase9.log](test-output-phase9.log)。本轮 `go test ./internal/fund/ -v -count=1` 结果：**124/124 通过，0 失败**（`ok xianliao-fund/internal/fund 33.762s`），`go vet ./...` 通过，首轮即全绿（无失败修复项；仅格式修复：新文件与改动文件经 `gofmt -w` 对齐，预存未格式化文件如 `reconcile.go` 未动）。

## A. Phase 9 新增测试（17/17 通过）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 108 | WithdrawSubmitSuccess | 提交冻结金额 + 手续费（默认费率 0）、订单待审核、冻结行剩余 = 总额、type 22 分录合计为 0 | 通过 |
| 109 | WithdrawInsufficient | 可用 < 金额 + 手续费时 400、余额不动、无订单行 | 通过 |
| 110 | WithdrawWrongPassword | 错密码 403 TX_PASSWORD_ERROR、余额不动、无订单 | 通过 |
| 111 | WithdrawAdmissionGuidance | 无方式 / 有方式无密码均 403 引导、不扣款、不建单；补齐准入后可提 | 通过 |
| 112 | WithdrawMethodOwnership | 他人方式 / 不存在方式均 404 WITHDRAW_METHOD_NOT_FOUND、不扣款、不建单 | 通过 |
| 113 | WithdrawAmountLimits | 低于下限 / 超单笔上限均 400（超单笔为 AMOUNT_LIMIT_EXCEEDED）、不扣款；等于上限放行；收尾复位配置 | 通过 |
| 114 | WithdrawFeeCalc | 固定 100 + 费率 100bp：10000 分手续费 200、到账 9800，试算接口与提交一致；手续费 ≥ 金额时 400 FEE_EXCEEDS_AMOUNT；收尾复位配置 | 通过 |
| 115 | WithdrawFrequencyLimit | 单日笔数上限（1 笔时第二笔 400）与单日金额上限（15000 时第二笔 10000 超限 400）；收尾复位配置 | 通过 |
| 116 | WithdrawRejectUnfreeze | 审核拒绝全额解冻（金额 + 手续费回可用）、状态 2、type 23 分录合计为 0；再拒 400 | 通过 |
| 117 | WithdrawApproveProcessPayout | 双人复核（同一人两次 400、第一复核状态仍 0 且余额不动、第二复核通过）；待审核态进处理中 409；无凭证确认 400；回填凭证确认成功：状态 4、冻结清零、平台户同步减少、type 24 分录合计为 0 | 通过 |
| 118 | WithdrawFailThenRefund | 处理失败状态 5 时冻结不动；退款后状态 6、余额全额恢复、type 25 分录合计为 0 | 通过 |
| 119 | WithdrawIllegalTransitions | 待审核态确认/失败/退款、已通过态失败/退款、成功后复审均拒绝（新入口 409、旧入口 400）、状态不动；自动打款骨架 501 | 通过 |
| 120 | WithdrawIdempotentSubmit | 同一幂等键两次返回同一单、只冻一次、订单行恰 1 行 | 通过 |
| 121 | WithdrawPermissionIsolation | 他人详情 404、他人列表为空、管理接口无密钥 401、试算无 token 401 | 通过 |
| 122 | WithdrawAdminQueryAndConfig | 管理端列表（用户/状态过滤）与详情（含 freeze_id/fee/actual）；双人复核两阶段；配置读写（负数 400、写后读回一致、收尾复位） | 通过 |
| 123 | WithdrawLedgerConservation | 多笔提交 + 拒绝 + 成功 + 失败退款后对账 diff=0，全部 type 22–25 txn 分录合计均为 0 | 通过 |
| 124 | WithdrawMethodDeletedBeforePayout | 流程中解绑方式后打款确认被拒（400，不向失效账户打款），走失败 → 退款后资金全额恢复 | 通过 |

# 仙聊资金服务 Phase 8 — 自动化测试报告（Phase 1–7 的 94 项全部回归通过）

Phase 8 新增测试 95–107（见 §A）；Phase 1–7 的 94 项回归结果见下方原报告（同一轮测试输出）。测试库与命令与 Phase 1–7 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），鉴权用 stub IM 校验器（token `imtok-<uid>`），管理接口用 `test-admin-secret` 测试夹具，mock 回调签名密钥为测试夹具（仅测试库，绝不指向生产库）。原始输出：[test-output-phase8.log](test-output-phase8.log)。本轮 `go test ./internal/fund/ -v -count=1` 结果：**107/107 通过，0 失败**（`ok xianliao-fund/internal/fund 29.324s`），`go vet ./...` 通过。首轮 1 项失败已修复：Test90 的旧种子 helper 插了无 channel 的充值单，撞上 0008 的“充值行必须有渠道”约束——修法为种子充值单补 `channel='mock'`（仅测试 helper 改动，Phase 7 断言不变），重跑后全绿；另 1 处编译期修复：txn 常量 20/21 在 fund.go 与 recharge.go 重复定义，删 recharge.go 一份后通过。

## A. Phase 8 新增测试（13/13 通过）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 95 | RechargeCreate | 固定档位（10000 分）与自定义金额（12345 分）建单（status 0、fee 0、actual=amount、channel mock）；金额 0/负数 400；alipay/wechat 创建 400 CHANNEL_NOT_AVAILABLE；同一幂等键两次创建返回同一单、恰 1 行 | 通过 |
| 96 | RechargeCallbackPaid | mock 回调验签通过入账：订单已支付、paid_txn_id 与 channel_order_no 回填、余额 += 金额、txn type 20、分录合计为 0 | 通过 |
| 97 | RechargeCallbackBadSignature | 错签名 401 SIGNATURE_INVALID、篡改金额后签名失效 401、未知订单 + 有效签名 404；订单仍待支付、余额 0 | 通过 |
| 98 | RechargeCallbackIdempotent | 同一支付回调两次：同一 paid_txn_id、只入账一次、type 20 txn 恰 1 行 | 通过 |
| 99 | RechargeCallbackAmountMismatch | 签名有效但回调金额 ≠ 订单金额：400 CALLBACK_AMOUNT_MISMATCH、订单仍待支付、余额 0、审计行存在 | 通过 |
| 100 | RechargeCallbackFailed | 失败回调：订单已失败、不入账；失败重放幂等 200；失败后支付回调 409 非法流转、仍失败 | 通过 |
| 101 | RechargeRefund | 退款回调扣回：订单已退款、refund_txn_id 回填、type 21 分录合计为 0、余额归零；重复退款幂等、type 21 恰 1 行 | 通过 |
| 102 | RechargeRefundInsufficient | 充值 10000 后花掉 8000 再退款：409 REFUND_INSUFFICIENT、订单仍已支付、余额 2000 不动、审计行存在 | 通过 |
| 103 | RechargeIllegalTransition | 待支付退款、已支付再失败、已退款再支付均 409 INVALID_TRANSITION，订单状态不动 | 通过 |
| 104 | RechargePermissionIsolation | 他人详情 404、他人列表不含该单、坏 token 401 | 通过 |
| 105 | RechargeAdminQuery | 管理端列表（用户/状态过滤）与详情、提现单详情 400 且不在充值列表、无密钥 401 | 通过 |
| 106 | RechargeMockSwitchOff | Mock 开关关闭的服务实例上 mock 回调 404；真实渠道回调 501 CHANNEL_NOT_CONFIGURED；未知渠道 404；订单仍待支付 | 通过 |
| 107 | RechargeLedgerConservation | 多笔充值 + 退款后对账 diff=0，全部 type 20/21 txn 分录合计均为 0 | 通过 |

# 仙聊资金服务 Phase 7 — 自动化测试报告（Phase 1–6 的 81 项全部回归通过）

Phase 7 新增测试 82–94（见 §A）；Phase 1–6 的 81 项回归结果见下方原报告（同一轮测试输出）。测试库与命令与 Phase 1–6 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），鉴权用 stub IM 校验器（token `imtok-<uid>`），管理接口用 `test-admin-secret` 测试夹具。原始输出：[test-output-phase7.log](test-output-phase7.log)。本轮 `go test ./internal/fund/ -v -count=1` 结果：**94/94 通过，0 失败**（`ok xianliao-fund/internal/fund 28.994s`），`go vet ./...` 通过，首轮即全绿（无失败修复项；仅 1 处格式修复：新文件经 `gofmt -w` 对齐）。

## A. Phase 7 新增测试（13/13 通过）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 82 | AdminWalletLedgerAdmission | 管理端钱包（余额/冻结/状态）、流水列表、准入状态（ok/next_step none）查询；不存在用户 404；用户 token 调管理接口 401 | 通过 |
| 83 | AdminWithdrawMethodsMasked | 管理端绑定列表脱敏（`****7890`、响应体无完整卡号）、默认账户查询；管理端 PUT 无修改入口（非 200）且绑定数不变 | 通过 |
| 84 | AdminTxPasswordStatusNoHash | 管理端密码状态（已设/未设、锁定、last_changed_at）；响应体不含 `password_hash` 与 `$2a$` | 通过 |
| 85 | AdminRecoveryList | 找回申请单管理列表（含待复核单）与详情；用户 token 调管理列表 401 | 通过 |
| 86 | AdminAdjustmentSingleApproveNoExec | 单人复核只返回 awaiting、余额不动、单据仍待复核且无 executed_txn | 通过 |
| 87 | AdminAdjustmentDualExec | 双人复核执行：余额变动、分录合计为 0、executed_txn_id 回填、执行审计行操作人为第二复核人 | 通过 |
| 88 | AdminAdjustmentRejectAndReverse | 拒绝关闭后 approve/reject 均 400；执行后 approve/reject 均 400；反向纠错单（注明已执行原单、方向相反）双人执行后余额恢复；原单未执行/方向相同拒绝建单 | 通过 |
| 89 | AdminAdjustmentDecreaseInsufficient | 同一人两次复核 400；减额超余额时第二复核 400、余额不动、单据仍待第二复核且第二复核人不落单；随后可拒绝关闭 | 通过 |
| 90 | AdminWithdrawOrderReview | 提现列表排除充值单；第一复核状态仍 0、余额与 txn 数不变（无打款副作用）；第二复核状态置 1；拒绝置 2 附注；已通过单再审 400；非提现单审核 400 | 通过 |
| 91 | AdminRiskLimitTransfer | 限额读写（部分更新语义：缺省字段保持原值、显式 null 改不限额）；超单笔上限转账 400 AMOUNT_LIMIT_EXCEEDED、无订单行、余额不动；等于上限放行；红包路径本阶段不接限额（超限仍可发出，文档边界）；单日上限只存配置不执行（超单日转账仍成功）；负数上限 400；收尾复位不限额 | 通过 |
| 92 | AdminPermissionIsolation | 无密钥/错密钥/用户 token 调全部管理接口 401；双人复核与创建调整单缺 X-Admin-ID 401 | 通过 |
| 93 | AdminAuditLogQuery | 审计日志管理查询含创建与执行条目、执行条目含单号；审计响应无完整卡号 | 通过 |
| 94 | AdminAdjustmentLedgerConservation | 多笔调整 + 转账后对账 diff=0，全部 type 19 txn 分录合计均为 0 | 通过 |

# 仙聊资金服务 Phase 6 — 自动化测试报告（Phase 1–5 的 65 项全部回归通过）

> Phase 6 新增测试 66–81（见 §A）；Phase 1–5 的 65 项回归结果见下方原报告（同一轮测试输出）。测试库与命令与 Phase 1–5 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），鉴权用 stub IM 校验器（token `imtok-<uid>`）。原始输出：[test-output-phase6.log](test-output-phase6.log)。本轮 `go test ./internal/fund/ -v -count=1` 结果：**81/81 通过，0 失败**（`ok xianliao-fund/internal/fund 27.678s`），`go vet ./...` 通过，首轮即全绿（无失败修复项；仅 1 处格式修复：新文件 `transfer.go` 经 `gofmt -w` 对齐）。

## A. Phase 6 新增测试（16/16 通过）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 66 | TransferSuccess | 转账成功：双方余额、分录合计为 0、订单记录、双方流水均含该 txn 且对手方正确、详情双方可见 | 通过 |
| 67 | TransferInsufficient | 余额不足 400 INSUFFICIENT_BALANCE、双方余额不动、无 type 18 txn、失败审计行 status=2 | 通过 |
| 68 | TransferWrongPassword | 错密码 403 TX_PASSWORD_ERROR、余额不动、无订单 | 通过 |
| 69 | TransferAdmissionGuidance | 转出方未完成准入（无方式/有方式无密码）均 403 引导、不扣款；补齐后可转 | 通过 |
| 70 | TransferSelfRejected | 自转 400 TRANSFER_SELF、余额不动、无订单 | 通过 |
| 71 | TransferIdempotent | 同一幂等键两次返回同一订单/同一 txn、只扣一次、成功订单恰 1 行 | 通过 |
| 72 | TransferConcurrent | 20 并发各转 100（余额 1000）：恰好 10 笔成功、最终余额 0、零超扣、收款方恰得 1000 | 通过 |
| 73 | TransferReceiverAutoCreate | 收款方从未建钱包仍可入账；收款方无准入（无方式无密码）不阻挡被动收款 | 通过 |
| 74 | TransferReceiverFrozenClosed | 收款方冻结/注销均 400 RECEIVER_WALLET_ABNORMAL、双方余额不动、无成功订单 | 通过 |
| 75 | TransferListsAndDetail | sent 含单、received 含单、陌生人 received 为空、第三方详情 403 | 通过 |
| 76 | TransferAbnormalRollback | 收款方冻结时整笔回滚：无 type 18 txn、无该 txn 分录、双方余额不动、仅失败审计行 | 通过 |
| 77 | TransferLedgerConservation | 成功 + 失败尝试后对账 diff=0，全部转账 txn 分录合计均为 0 | 通过 |
| 78 | TransferPermissionIsolation | 请求体 user_id 与 token 不一致 403、平台结算户为收款方 403、管理接口无密钥 401 | 通过 |
| 79 | TransferAmountValidation | 金额 0/负数/附言超 200 字均 400 且不扣款、无成功订单 | 通过 |
| 80 | TransferAdminQuery | 管理端详情含订单 + txn（type 18）+ 双方 2 条分录（合计为 0）；按转出方过滤的列表含单；无密钥 401 | 通过 |
| 81 | TransferReversal | 转出方凭原 txn 冲正（type 8）后双方余额恢复；接收方已花掉款项时冲正失败、原转账不动；纠错全程只用公开接口、不直接改余额 | 通过 |

> Phase 5 新增测试 49–65（见 §A）；Phase 1–4 的 48 项回归结果见下方原报告（同一轮测试输出）。测试库与命令与 Phase 1–4 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），鉴权用 stub IM 校验器（token `imtok-<uid>`）。原始输出：[test-output-phase5.log](test-output-phase5.log)。本轮 `go test ./internal/fund/ -v -count=1` 结果：**65/65 通过，0 失败**（`ok xianliao-fund/internal/fund 20.075s`），`go vet ./...` 通过，首轮即全绿（无失败修复项；仅 1 处编译期修复：新文件复用了 Phase 4 的 `validGroupID`，删重复定义后通过）。

## A. Phase 5 新增测试（17/17 通过）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 49 | LuckySplitInvariants | 1000 次拆分 × 7 组总额/份数：每份 ≥ 1 分、总和严格等于总额、无负数、每份不超剩（为其余份各留 ≥ 1 分）；总额 = 份数时全为 1 分；非法输入拒绝 | 通过 |
| 50 | LuckySplitDistribution | 首份硬上界 2×均值−1；500 次首份均值落在 [80,120]（理论 100）；首份取值多样（非退化均分） | 通过 |
| 51 | LuckySendSplit | 建包扣款 + 预拆分份行求和严格等于总额；未领取包手气最佳为 null | 通过 |
| 52 | LuckyInvalidParams | 总额 < 份数/份数 0/总额 0/份数超上限均 400 且不扣款 | 通过 |
| 53 | LuckyClaimProgress | 领取金额等于预拆分份序金额；进度实时更新；份行金额领取后不变（可审计性） | 通过 |
| 54 | LuckyBestAndRanking | 并列规则单测（最早领取者胜、同时间 claim_id 小者胜）+ 接口端详情最佳 = 排名首位、排名金额降序序号连续 | 通过 |
| 55 | LuckyDuplicateClaim | 同一领取人重领返回原记录、余额与计数不动 | 通过 |
| 56 | LuckyConcurrentClaim | 3 名额 10 并发：恰好 3 人成功、金额总和等于总额、不超发、领完冻结清零 | 通过 |
| 57 | LuckyFullyClaimed | 领完状态 + 冻结清零；新领取人 409 LUCKY_PACKET_FULL | 通过 |
| 58 | LuckyExpiryPartialRefund | 部分领取后过期：剩余退回发送方、已领金额保留、二次扫描不重复退、退款分录合计为 0 | 通过 |
| 59 | LuckyExpiryLazyRefundOnClaim | 领取时惰性过期：先退剩余、再报过期，发送方恢复、领取人不入账 | 通过 |
| 60 | LuckyAdmissionGuidance | 发送/领取未完成准入只返回引导、不动资金；补齐后可继续 | 通过 |
| 61 | LuckyLedgerConservation | 发包→领完 + 发包→部分领取→过期退款后对账 diff=0，6 笔拼手气分录合计均为 0 | 通过 |
| 62 | LuckyPermissionIsolation | 集合外的人领不到（NOT_GROUP_MEMBER）/看不到（含排名）；发送方可领自己的包（一次账户更新） | 通过 |
| 63 | LuckyIdempotentSend | 同一幂等键两次发送只建一包、只冻一次、份数不变 | 通过 |
| 64 | LuckyAbnormalFreezeGuard | 冻结行被篡改时领取与过期退款均失败、包状态与领取记录均不动、领取人不入账 | 通过 |
| 65 | LuckyListEndpoints | sent 含包、领取方 received 含包、陌生人 received 为空 | 通过 |

> Phase 4 新增测试 35–48（见下方原 §A）；Phase 1–3 的 34 项回归结果见下方原报告（同一轮测试输出）。测试库与命令与 Phase 1–4 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），鉴权用 stub IM 校验器（token `imtok-<uid>`）。原始输出：[test-output-phase4.log](test-output-phase4.log)。Phase 4 当轮 `go test ./internal/fund/ -v -count=1` 结果：**48/48 通过，0 失败**（`ok xianliao-fund/internal/fund 12.308s`），`go vet ./...` 通过，首轮即全绿（无失败修复项）。

## A. Phase 4 新增测试（14/14 通过）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 35 | GroupSendSplit | 建包扣款 + 预拆分 1000 分 3 份 = 334/333/333，总和严格等于总额 | 通过 |
| 36 | GroupSplitRules | 均分余数规则（100/3、101/10、5/5、10000/7）+ 总额 < 份数/份数 0/总额 0/份数超上限均 400 且不扣款 | 通过 |
| 37 | GroupClaimProgress | 领取入账 + 详情进度实时更新 + 领取列表顺序与金额 | 通过 |
| 38 | GroupDuplicateClaim | 同一领取人重领返回原记录、余额与计数不动 | 通过 |
| 39 | GroupConcurrentClaim | 3 名额 10 并发：恰好 3 人成功、金额总和等于总额、不超发、领完冻结清零 | 通过 |
| 40 | GroupFullyClaimed | 领完状态 + 冻结清零；新领取人 409 GROUP_PACKET_FULL | 通过 |
| 41 | GroupExpiryPartialRefund | 部分领取后过期：剩余退回发送方、已领金额保留、二次扫描不重复退、退款分录合计为 0 | 通过 |
| 42 | GroupExpiryLazyRefundOnClaim | 领取时惰性过期：先退剩余、再报过期，发送方恢复、领取人不入账 | 通过 |
| 43 | GroupAdmissionGuidance | 发送/领取未完成准入只返回引导、不动资金；补齐后可继续 | 通过 |
| 44 | GroupLedgerConservation | 发包→领完 + 发包→部分领取→过期退款后对账 diff=0，6 笔群包分录合计均为 0 | 通过 |
| 45 | GroupPermissionIsolation | 集合外的人领不到（NOT_GROUP_MEMBER）/看不到；发送方可领自己的包（一次账户更新） | 通过 |
| 46 | GroupIdempotentSend | 同一幂等键两次发送只建一包、只冻一次、份数不变 | 通过 |
| 47 | GroupAbnormalFreezeGuard | 冻结行被篡改时领取失败、包状态与领取记录均不动、领取人不入账 | 通过 |
| 48 | GroupListEndpoints | sent 含包、领取方 received 含包、陌生人 received 为空 | 通过 |

> Phase 3 新增测试 23–34（见下方原 §A）；Phase 1/2 的 22 项回归结果见下方原报告（同一轮测试输出）。测试库与命令与 Phase 1/2 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），鉴权用 stub IM 校验器（token `imtok-<uid>`）。原始输出：[test-output-phase3.log](test-output-phase3.log)。Phase 3 当轮 `go test ./internal/fund/ -v -count=1` 结果：**34/34 通过，0 失败**（`ok xianliao-fund/internal/fund 7.006s`）。

## A2. Phase 3 新增测试（12/12 通过，原 §A）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 23 | 发包扣款 + 领取入账 | 发包后 A 可用 5000/冻结 5000；领取后 A 5000/0、B 5000/0；发包与领取两笔账务分录合计均为 0 | 通过 |
| 24 | 重复发送幂等 | 同一幂等键两次发送返回同一 packet_id、只建一包、只冻一次（A 7000/3000） | 通过 |
| 25 | 重复领取拦截 | 同一领取人重领返回原 claim_id；他人领已领包 409 PACKET_ALREADY_CLAIMED；领取行恰好 1 条、B 只入账一次 | 通过 |
| 26 | 并发领取 | 同一领取人 20 并发（含 429 重试）收敛到同一 claim_id、领取行 1 条、B 恰好入账 4000 | 通过 |
| 27 | 余额不足 | 超额发包 400 拒绝、不建包、A 余额 100/0 不变 | 通过 |
| 28 | 未完成准入的发送引导 | 无方式 / 有方式无密码均 403 ADMISSION_NOT_SATISFIED、不扣款；补齐密码后可发、错密码 403 | 通过 |
| 29 | 未完成准入的领取引导 | 未准入领取 403 引导、包状态保持 0、冻结不动、无领取行；补齐准入后可领 | 通过 |
| 30 | 用户权限隔离 | 第三方领取/详情/领取记录均 403；收发双方详情 200 | 通过 |
| 31 | 过期自动退款（扫描） | 过期包经扫描退款：A 恢复 10000/0、退款分录合计 0；二次扫描不重复退款；事后领取 409 PACKET_EXPIRED | 通过 |
| 32 | 领取时惰性过期 | 过期包被领取时先自动退款（A 恢复 10000/0、B 0 分文未得）、再报 409 PACKET_EXPIRED | 通过 |
| 33 | 账务守恒 | 发包→领取 + 发包→过期退款后，红包交易恰好 4 笔、分录合计均为 0、对账 diff=0 | 通过 |
| 34 | 异常防护 | 冻结行 remaining 被篡改时领取失败、包状态保持 0、无领取行、领取方 0 入账 | 通过 |

首轮失败与修复：本轮 12 项新测试首轮即全绿，无失败修复；`go vet ./...` 通过（仅 `internal/fund/reconcile.go` 为 Phase 1 遗留 gofmt 未格式化文件，本阶段未动它）。

生产资金库核查（部署后）：`redpacket_packet=0`、`redpacket_claim=0`、`platform:settlement=1`——测试数据未进生产库；部署冒烟仅用 `GET /healthz` 与无鉴权请求（钱包与红包接口均 401），未写生产库。openim / yichat / yudao 容器原样运行。

# 仙聊资金服务 Phase 2 — 自动化测试报告（Phase 1 的 15 项全部回归通过）

> Phase 2 新增测试 16–22（见 §5）；Phase 1 的 15 项回归结果见 §1（同一轮测试输出）。测试库与命令与 Phase 1 相同（`xianliao_fund_test`，香港三 Go 1.26，`FUND_TEST_REDIS_ADDR=127.0.0.1:16381`），另：鉴权改用 stub IM 校验器（token `imtok-<uid>`），真实 IM 联调见 §6（部署后集成脚本，真实 chat 登录 token）。

## 5. Phase 2 新增测试（7/7 通过）

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 16 | IM token 鉴权 | 无 token 401、伪造 token 401、平台户 token 403、IM 不可达 503（不 fail-open）、非 Bearer 方案 401、旧内部口令头 401 | 通过 |
| 17 | token 冒用隔离 | B 删/改 A 的方式 → 400 且 A 的方式仍在；B 的列表为空，不含 A 的任何信息 | 通过 |
| 18 | 找回全流程 | 实名错/后 4 位错 → 400；正确申请；待复核重复申请返回同一单；未双人通过重置 → 400；管理员 1 批 → awaiting_second_approval；同一人再批 → 400；无 X-Admin-ID → 401；管理员 2 批 → approved；两次不一致重置 → 400；正确重置后旧密码失效、新密码生效；复用 → 400；本人查单状态为已使用且不含身份材料 | 通过 |
| 19 | 找回拒绝与过期 | 拒绝后重置 → 400，管理查询状态为已拒绝；人为置过期后 approve/reset 均拒绝，查询为已过期 | 通过 |
| 20 | 方式边界 | 首绑自动默认；改默认后恰好 1 个默认；取消默认自动递补最早绑定；删默认递补；删唯一返回 was_last_method 且准入变 need_method；设默认幂等 | 通过 |
| 21 | 方式校验 | 同类型同账号重复 → 400；同账号不同类型允许；银行卡 11 位/非数字 → 400，19 位通过；修改时格式同样校验 | 通过 |
| 22 | 准入引导 | 新用户 need_method/bind_method；已绑无密码 need_password/set_password；完整 ok/none；冻结 frozen/contact_support；`requireAdmission` 守卫四态行为一致 | 通过 |

首轮失败与修复（不隐瞒）：① `verifyRecoverIdentity` 无匹配时误返回 true（循环结束返回值写错）→ 改为 found 标志；② 测试 stub 把 `imtok-bad` 也当有效 token（前缀匹配）→ stub 显式拒绝 `imtok-bad`。修复后重跑 22/22。

## 6. 真实 IM 联调（部署后集成脚本，见最终汇报）

- 真实 chat 登录 token（测试号，非真实用户）调资金服务：建钱包、查准入、绑方式均 200；伪造 token → 401；A 的 token 配 B 的 user_id → 403。
- 生产资金库核查：`wallet_account=1`（仅平台户）、`wallet_txn=0`——集成测试数据未进生产库（测试用 IM 账号仅建钱包查状态，未产生资金交易）。

---

# 仙聊资金服务 Phase 1 — 自动化测试报告（原文保留）

> 测试库：同一 Postgres 上的 `xianliao_fund_test`（与生产资金库 `xianliao_fund` 分离；测试数据不得进生产库，跑前跑后清理）。
> 测试命令（香港三，源码目录 `/opt/xianliao/fund-service/src`，Go 1.26）：
> `FUND_TEST_ADMIN_URL=<测试库管理员 DSN> FUND_TEST_REDIS_ADDR=127.0.0.1:16381 go test ./internal/fund/ -v -count=1`
> 完整原始输出见同目录 `test-output.log`。测试用密钥为进程内随机 AES 密钥与 `test-internal-secret` / `test-admin-secret` 测试夹具，与生产 `fund.env` 无关。

## 1. 结果总览（15/15 通过）

```
=== RUN   Test01WalletCreate        --- PASS (0.01s)
=== RUN   Test02Balance             --- PASS (0.02s)
=== RUN   Test03DoubleEntry         --- PASS (0.01s)
=== RUN   Test04IncomeRefundReversal--- PASS (0.02s)
=== RUN   Test05Expense             --- PASS (0.02s)
=== RUN   Test06FreezeUnfreeze      --- PASS (0.04s)
=== RUN   Test07IdempotentRepeat    --- PASS (0.01s)
=== RUN   Test08ConcurrentDebit     --- PASS (0.16s)
=== RUN   Test09NegativeBalanceProtection --- PASS (0.01s)
=== RUN   Test10TxPassword          --- PASS (0.43s)
=== RUN   Test11PasswordLock        --- PASS (0.43s)
=== RUN   Test12WithdrawMethod      --- PASS (0.38s)
=== RUN   Test13PermissionIsolation --- PASS (0.00s)
=== RUN   Test14MigrationRollback   --- PASS (0.11s)
=== RUN   Test15ServerRecovery      --- PASS (0.01s)
PASS
ok      xianliao-fund/internal/fund 1.757s
```

## 2. 逐项证据

| # | 测试 | 断言要点 | 结果 |
|---|---|---|---|
| 01 | 钱包创建 | 两次创建同一用户，余额/冻结均为 0，同一行 | 通过 |
| 02 | 余额查询 | 收入 12345 分后余额 12345、冻结 0 | 通过 |
| 03 | 双重记账 | 收入一笔 ≥2 条分录、带符号合计 0；触发对账 diff=0、status=0；对账结果可查询 | 通过 |
| 04 | 收入/退款/冲正 | 收入 10000 + 退款 2000 = 12000；冲正原收入后 = 2000 | 通过 |
| 05 | 支出 | 支出 1000 后 3000→2000；超额支出拒绝且余额不变 | 通过 |
| 06 | 冻结及解冻 | 冻结 4000（6000/4000）→ 解冻 1000（7000/3000）→ 冻结扣款 1000（7000/2000）→ 冻结退款 2000（9000/0）；超冻结额解冻拒绝 | 通过 |
| 07 | 重复请求 | 同一幂等键两次返回同一 txn_id，余额只记一次 7777 | 通过 |
| 08 | 并发扣款 | 20 并发各扣 100（余额 1000，429 按设计重试），恰好 10 笔成功，最终余额 0，零超扣 | 通过 |
| 09 | 负余额防护 | 超额支出/冻结/负金额均拒绝；DB 层 `UPDATE balance=-1` 被 CHECK 约束拒绝；余额保持 500/0 | 通过 |
| 10 | 交易密码 | 两次不一致拒绝、非 6 位数字拒绝、设置/验密/错旧密改密拒绝/正确改密成功；库中哈希为 `$2a$` bcrypt 前缀、非明文 | 通过 |
| 11 | 错误密码锁定 | 连续错 5 次后 status locked=true 且 locked_until 非空；锁定期间正确密码亦拒绝（verified=false, locked=true） | 通过 |
| 12 | 提现账户绑定 | 支付宝+银行卡添加；首方式自动默认；银行卡脱敏 `****7890`；设默认切换后恰好 1 个默认；错密换绑拒绝、正确换绑成功；解绑验密成功；准入 ok；新用户准入 need_method | 通过 |
| 13 | 用户权限隔离 | body user_id 越权 403、query user_id 越权 403、无密钥 401、平台户访问 403、管理接口无密钥 401 | 通过 |
| 14 | 迁移与回滚 | down 后 wallet_account 表消失；up 后 9 表恢复；fund_app 对 ledger 无 UPDATE、对 audit_log 无 DELETE（数据库层）；平台户重建为 1 行；up 后新钱包可正常记账。注：down 删表会清空测试库数据，属回滚预期行为，测试内已说明 | 通过 |
| 15 | 服务端异常恢复 | 未提交事务（INSERT 后 Rollback）的分录数为 0；新建服务实例连同一库，余额 4321 与流水均不变 | 通过 |

## 3. 首轮失败与修复（不隐瞒，失败项未标通过）

首轮 11/15，4 项失败，全部定位并修复后重跑 15/15：

1. Test06：`wallet_freeze` INSERT 占位符与参数个数不一致（`$4` 复用但少传 amount 参数）→ 补传参数。
2. Test08：Redis 辅助锁被持有时按设计回 429，测试初版把 429 计为失败 → 测试改为 429 时换新幂等键重试（模拟真实客户端），最终恰好 10 笔成功。
3. Test11：验密接口语义为 `200 + {verified, locked}`（检查型接口），测试初版误按状态码断言 → 测试改为按标志断言。
4. Test12（根因为服务代码）：`txPasswordStatus` 的 SQL 复用 `$1` 却传 3 个参数 → 改写为单参数查询 + `ErrNoRows` 表示未设密码。

另：`go vet ./...` 通过；`go build ./...` 通过（Go 1.26）。

## 4. 清理核查

- 测试库跑后：`wallet_account=1`（仅平台户）、`wallet_txn=0`、`wallet_ledger=0`。
- 生产资金库（`xianliao_fund`）：`wallet_account=1`（仅平台户）、`wallet_txn=0`——测试数据未进生产库；部署冒烟仅用 `GET /healthz` 与无鉴权请求（401），未写生产库。
