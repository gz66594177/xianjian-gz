# 仙聊资金服务 — 变更记录

## 0010（2026-11-02）：手动充值 + 网关支付（与手动通道并存，同一轮交付）

- 迁移 0010（只加表加字段，不改现有数据）：`wallet_order` 扩展 `recharge_kind`（callback/manual/gateway，
  历史行默认 `callback`）+ `payment_method_id` / `gateway_ref` / `cashier_url` / `expires_at`；`channel` CHECK 扩到
  manual/gateway；手动行必须有方式、网关行必须有过期时间的 CHECK；索引 `ix_order_kind_status`；新表
  `wallet_payment_method`（类型/展示名/二维码文件引用/启用/排序/创建人）、`fund_upload_file`（qr/proof、
  内容类型、大小、服务端随机路径）、`recharge_proof`（订单唯一凭证）；`fund_app` 权限同步；迁移工具 up 改为
  0001 -> … -> 0010，down 逆序；Test14 迁移回滚测试同步覆盖 0010。
- 手动充值 API：管理端收款方式新增/改名/停用启用/列表（含停用）+ 二维码上传（替换旧文件清理）；
  用户侧 `GET /v1/recharge/methods`（仅启用项）、`POST /v1/recharge/manual`（方式启用 + 有码校验、
  金额整数分校验、幂等建单，状态 10）、`POST /v1/recharge/{id}/proof`（仅本人、仅 10/11，状态 11 重传替换）；
  管理端 `GET /v1/admin/recharge/pending`（状态 11）+ 双人复核通过（首复核不动余额、第二名不同管理员同一事务
  入账 type 20）/ 单人拒绝（状态 12 + 备注、不入账）；全部管理操作写审计。
- 网关支付 API：适配器接口 `GatewayAdapter`（`CreatePayment` + `VerifyCallback`）+ 配置开关（生产默认无适配器
  = 通道关闭：建单 400 `GATEWAY_NOT_CONFIGURED`、回调 501，绝不生成假收银台、绝不伪造回调；真实厂商与商户凭证
  未定，待定后再实现适配器）；`POST /v1/recharge/gateway`（状态 0 + 收银台地址 + 30 分钟过期）；
  `POST /v1/payments/callback/gateway`（验签失败 401、重复幂等、过期关单 409、渠道单号/金额不一致拒绝、
  失败只改状态、支付同一事务入账 type 20）；`POST /v1/admin/recharge/gateway/expire`（过期关单扫描）。
- 通用订单查询（只读，App 轮询用）：`GET /v1/recharge/orders`（本人，kind/status 过滤）与
  `GET /v1/recharge/orders/{id}`（本人，按 kind 三种详情形态；网关详情含 `expired` 计算字段，详情不懒关单）+
  `GET /v1/admin/recharge/orders`（管理端统一视图）；Phase 8 回调端点本轮起只服务 `kind='callback'` 的单
  （4 处查询加约束，回调单行为不变，修掉手动/网关单漏进回调列表的真问题）。
- 截图与二维码存储：仅 jpg/png 服务端嗅探、单张 ≤ 5MB、随机服务端文件名、只存本机磁盘（默认
  `/opt/xianliao/fund-uploads`，`FUND_UPLOAD_DIR` 可改）；取用只走鉴权接口 `GET /v1/uploads/{file_id}`
  （管理员恒可看、用户看二维码须方式启用、看凭证须为本人，其余统一 404 不泄露存在性，路径前缀防穿越）。
- 测试：新增 Test125–Test152（28/28），Phase 1–9 的 124 项全部回归通过，合计 152/152；`go vet` 通过；
  首轮 4 失败修复见测试报告（2 项测试期望码修正 + 1 项回调列表 kind 过滤真修复 + 编译期修复 2 处）。
- 部署：测试实例先行验证（新二进制 + 测试库 + 测试上传目录：18082 healthz ok、无鉴权 401、管理端新接口空列表）；
  生产部署顺序先停旧进程、等 18081 释放、换二进制、再起；生产计数部署前后一致
  （`wallet_account=2, wallet_txn=0, wallet_order=0`；account=2 的第二户为空钱包户，2026-11-02 09:20 +07 由用户侧
  App 钱包页自动建户，余额 0、无流水，非本次部署产生）。**如实记录的部署偏差**：测试库迁移一步中，
  `fund-test.env` 的 `FUND_ADMIN_DATABASE_URL` 指向生产库，致 0010 DDL 先于生产二进制部署打到生产库；
  事后核查：0010 只做加表加字段、生产计数与服务健康均未变（healthz ok、无鉴权 401），且测试/生产两 env 的
  `fund_app` 口令经比对一致（集群级单口令，ALTER ROLE 为等值写入），无连接影响；生产二进制部署仍按铁律
  走停→等→换→起完成，测试库 0010 来自 TestMain 自带迁移。
- 诚实记录的局限：管理端前端 UI 未做（admin-front 是 dist-only 无源码，本次只交付 API，管理员先用 API
  配置收款方式与审核）；Android 端两个通道的页面重做由主题代理收尾后接手，本次未碰 Android 代码；
  真实网关适配器未实现（厂商与商户凭证未定）；“是，已付款”零入账为结构保证（无入账入口）+ 接口级测试断言。

## Phase 9（2026-11-02）：人民币提现（7 态全流程）

- 提现订单复用 `wallet_order`（`order_type=2`）+ 迁移 0009（最小扩展：`freeze_id` / `payout_proof_no` / `payout_at` / `payout_operator_id` / `payout_note`，含成功单必须有回单号的 CHECK 约束与回滚；新表 `fund_withdraw_config`：固定费用 + 费率万分比 + 单笔下限/单笔上限/单日金额上限/单日笔数上限，默认费用 0、限额不限；迁移工具 up 改为 0001 -> … -> 0009，down 逆序；Test14 迁移回滚测试同步覆盖 0009）。
- 接口：`POST /v1/withdrawals`（准入引导优先 → 验交易密码 → 方式归属 → 金额上下限与频率限制 → 余额校验；冻结金额 + 手续费；幂等）、`GET /v1/withdrawals/quote`（手续费试算，展示用、无资金变动）、`GET /v1/withdrawals/{id}` / `?status`（仅本人）、管理端列表/详情（Phase 7 已有，详情字段扩展 freeze_id / payout_* / failure_reason）、`POST /v1/admin/withdraw-orders/{id}/approve`（Phase 7 双人复核，通过只改状态、不打款）/ `reject`（真实单拒绝时同一事务内全额解冻 type 23；Phase 7 种子单无冻结行时走旧路径只改状态，Test90 断言不变）、`start-processing`（1 → 3，只改状态）、`confirm-payout`（3 → 4，必填回单号、冻结扣减 type 24，方式已解绑时拒绝打款）、`fail`（3 → 5，只改状态与失败原因）、`refund`（5 → 6，全额解冻 type 25）、`auto-payout`（恒 501 骨架）、`GET/PUT /v1/admin/withdraw-config`（手续费与限额配置读写，部分更新语义同 Phase 7 限额配置）。
- 状态机：合法流转只有 0→1、0→2、1→3、3→4、3→5、5→6，其余一律拒绝（新入口 409，旧 approve/reject 入口保持 400）；手续费公式 `fee = fixed + floor(amount × rate_bp / 10000)`，`actual = amount − fee`，`actual ≤ 0` 拒绝提交；频率统计口径为数据库当日已提交且未被拒绝的单（`status <> 2`）。
- 人工打款流程（无商户资质时的唯一打款路径）：双人复核通过 → 处理中 → 财务用对公账户/企业网银手工转账 → 回填凭证（回单号、打款时间、操作员）确认成功；严禁个人号垫付为常规流程；自动打款签约前置清单见 API 文档 §14.5。
- 测试：新增 Test108–Test124（17/17），Phase 1–8 的 107 项全部回归通过，合计 124/124；`go vet` 通过；首轮即全绿。
- 诚实记录的局限：自动打款未落地（只有 501 骨架）；手续费收入确认不在本账务口径内（订单行 `fee_cents` 为唯一事实记录）；日终人工打款对账为线下流程，未建自动对账任务；频率限制按数据库当日统计。

## Phase 8（2026-11-02）：人民币充值（回调入账）

- 充值订单复用 `wallet_order`（`order_type=1`）+ 迁移 0008（充值专属字段 `channel` / `paid_txn_id` / `refund_txn_id` / `failure_reason` / `client_idempotency_key`，含回滚与“充值行必须有渠道”约束；迁移工具 up 改为 0001 -> … -> 0008，down 逆序；Test14 迁移回滚测试同步覆盖 0008；Test90 旧种子 helper 插无 channel 充值单撞约束，修为种子充值单补 `channel='mock'`，Phase 7 断言不变）。
- 接口：`POST /v1/recharges`（固定档位与自定义金额同一接口，档位 100/500/1000/5000/10000/30000/50000/100000 分为约定值、不做白名单硬限制；真实渠道未签约前只接受 mock，alipay/wechat 创建 400；创建幂等）、`GET /v1/recharges/{id}` / `?status&channel`（仅本人）、`GET /v1/admin/recharges` / `/{id}`（管理端列表/详情，按 `order_type=1` 过滤）；状态机 0 待支付 → 1 已支付 → 3 已退款、0 → 2 已失败，其余流转 409，同一目标状态重放幂等。
- 回调与验签框架（核心）：验签器接口 `PaymentVerifier`（`internal/fund/verify.go`）；本阶段只实现 HMAC mock 验签器（canonical 载荷 `order_id|channel_order_no|amount_cents|status`，hex HMAC-SHA256，恒时比较），验签失败 401 一律拒绝入账；真实支付宝/微信验签器只写 `UnconfiguredVerifier` 骨架（回调恒 501），绝不连真实渠道端点、不写真实商户密钥；回调处理同一事务（订单行锁 + 状态 CAS + 账务入账 + 审计）：入账 type 20（用户 CR / 平台 DR）、退款 type 21（用户 DR / 平台 CR，仅 mock 演练）、失败回调只改状态不入账；金额不一致拒绝入账并记审计（订单不动、资金不动）；退款余额不足整笔失败、订单与余额不动、另起短事务记审计；渠道单号唯一索引为第二道防线。
- Mock 隔离（红线）：mock 回调精确路由只在 `FUND_MOCK_PAYMENT=1` 时注册（开关关闭回 404，Test106）；开关开启要求 `FUND_MOCK_PAY_CALLBACK_SECRET` 非空否则拒绝启动；测试全部在 `xianliao_fund_test` 库跑，生产库只验证空表与健康检查；Mock 与真实订单以 `channel` 字段区分，管理端可过滤。
- 测试：新增 Test95–Test107（13/13），Phase 1–7 的 94 项全部回归通过，合计 107/107；`go vet` 通过；首轮 1 项失败（Test90 种子约束，见上）与 1 处编译期修复（txn 常量重复定义）后全绿。
- 诚实记录的局限：真实支付宝/微信验签器未落地（只有骨架 + 501），须商户签约后按 API 文档 §13.4 清单接入并先跑官方沙箱；渠道手续费现为 0（`fee_cents=0`，`actual=amount`），费率政策待签约后定；渠道日终对账任务未建，留签约后与验签器同批落地；充值创建不查准入（建单本身不产生资金变动，理由见 API 文档 §13.1），若未来要求充值前完成准入须另立规则。

## Phase 7（2026-11-02）：管理端资金中心 API

- 前置核查（只读，不伪造）：香港三只有 admin-front dist 镜像（`openim-admin-front:xianliao-20261102`，基于上游 `openim/openim-admin-front:release-v1.8.4-patch.2` + 覆盖层），无源码；上游组织 `openimsdk` 公开仓库列表中无 admin-front 源码仓；GitHub 全局检索唯一同名仓为第三方个人仓（来源未经证实，不采作上游）。缺口如实记录，不阻塞 API 交付；前端 UI 落地须等源码快照补齐，绝不凭空编前端。本阶段交付物是管理端资金中心 API + 接口规约（API 文档 §12，含未来前端接线规约 §12.8）。
- 新表 `fund_admin_adjustment` / `fund_risk_limit`（迁移 0007，含回滚；迁移工具 up 改为 0001 -> … -> 0007，down 逆序；Test14 迁移回滚测试同步覆盖 0007）+ `wallet_order` 5 个提现审核字段扩展。
- 管理端资金中心 API（全部走 `X-Admin-Secret`，双人复核类另须 `X-Admin-ID`）：钱包查询/流水/准入状态（只查）；提现账户列表与默认账户（只查、默认脱敏，管理端无修改入口）；交易密码状态查询（无哈希字段）与找回申请单列表/详情（复用 Phase 2 双人复核，不另起单人代设口子）；管理员资金调整单（创建/列表/详情/双人复核执行/拒绝；全部调整单统一双人复核，不设单人直通；执行走 txn type 19 双重记账同一事务；执行后不可改、不可撤销，纠错走反向单）；提现订单审核脚手架（列表/详情/双人通过/拒绝；通过只改状态，无打款动作；3–6 态为 Phase 9 流转，本阶段无写入口）；风控限额配置读写（单笔/单日，默认不限额；本阶段仅转账路径接单笔上限，单日上限与红包限额留 Phase 8/9，文档与测试均显式断言该边界）；审计日志管理查询（只读）。
- 转账路径接入单笔上限：超限 400 `AMOUNT_LIMIT_EXCEEDED`、不产生订单行；等于上限放行；默认不限额，回归不受影响。
- 测试：新增 Test82–Test94（13/13），Phase 1–6 的 81 项全部回归通过，合计 94/94；`go vet` 通过；首轮即全绿。
- 诚实记录的局限：admin-front 源码缺口仍在，UI 不落地；单日上限与红包限额未接入执行；调整单创建人可担任第一复核人（但一人无法完成两步），创建人与复核人完全分离如需强制须另立规则；提现审核通过后无打款，打款与回填在 Phase 9。

## Phase 6（2026-11-02）：用户之间余额转账（即时到账）

- 新表 `transfer_order`（迁移 0006，含回滚；迁移工具 up 改为 0001 -> 0002 -> 0003 -> 0004 -> 0005 -> 0006，down 逆序；Test14 迁移回滚测试同步覆盖 0006）。
- 资金模型：即时到账，同一 DB 事务、同一 txn_id（txn type 18，`ref_type='transfer'`）下两条分录（转出方可用 DR / 转入方可用 CR，金额相等，带符号合计为 0）；无冻结行、无过期、无领取环节；订单行与账务 txn、分录同事务落库。
- 模式定稿：全阶段只用一套资金状态机——即时到账，与红包即时结算模型一致；待确认模式不做（待确认模式须另起冻结 + 过期 + 领取/退款状态机，会使同一资金服务内并存两套状态机与两套争议路径）。
- 订单状态：1 成功（唯一由成功转账写入的状态）；2 失败（仅结算失败审计行：余额不足 / 收款方冻结 / 注销时写入，`failure_code` 非空、`txn_id` 为空，不落账；失败行不带幂等键，事后重发不受阻挡；参数错误、自转、鉴权/准入/密码失败不产生订单行）。
- 冲正定稿（二选一结果）：冲正复用已有 type 8，不另立 type 19；异常纠错只走冲正交易，接收方余额不足时冲正整体失败、原转账不动；冲正不改写原订单行；管理端只查不改，绝不直接改余额。
- 门禁：转出方须准入 ok + 验交易密码；收款方被动收款（钱包不存在时同一事务内自动建钱包即可入账，不设准入关；收款方准入在其下次主动使用资金时 enforcement；与红包领取须准入的区别已在 API 文档 §9 如实写清）；收款方冻结/注销时整笔拒绝；请求体 user_id 与 token 不一致 403。
- 并发与幂等：涉及账户按 user_id 排序加锁防死锁 + version 条件更新 + 余额校验；转出方 Redis 辅助锁仅协调；成功订单 `UNIQUE (sender_id, idempotency_key)` + 先查后插 + 唯一冲突回查。
- 接口：`POST /v1/transfers`（准入引导优先、再验交易密码）、`GET /v1/transfers/{id}` / `?role=sent|received`、`GET /v1/admin/transfers/{id}`（订单 + txn + 全部分录）/ `?sender_id&receiver_id`；转账分录同时出现在双方 `GET /v1/wallet/ledger` 中（`opposite_user_id` 为对方）。
- 测试：新增 Test66–Test81（16/16），Phase 1–5 的 65 项全部回归通过，合计 81/81；`go vet` 通过；首轮即全绿（仅 1 处格式修复：新文件经 `gofmt -w` 对齐）。
- 诚实记录的局限：IM 好友关系校验不在资金服务内，须 Phase 10/11 由 chat/App 层在发起前核验；单笔/单日上限属 Phase 7 风控配置，不硬编码；冲正由转出方本人凭原 txn 发起，管理端不代发起；转账 IM 业务通知由 chat 服务/客户端发送；UI 落地在 Phase 10/11。

## Phase 5（2026-11-02）：群拼手气红包（随机预拆分）

- 新表 `group_lucky_redpacket_packet` / `group_lucky_redpacket_share` / `group_lucky_redpacket_claim` / `group_lucky_redpacket_member`（迁移 0005，含回滚；迁移工具 up 改为 0001 -> 0002 -> 0003 -> 0004 -> 0005，down 逆序；Test14 迁移回滚测试同步覆盖 0005）。
- 资金模型：群拼手气发包（可用 → 冻结，txn type 15）/ 群拼手气领取（发送方冻结 → 领取方可用，按预拆分份金额，txn type 16）/ 群拼手气过期退款（冻结 → 发送方可用，按剩余金额，txn type 17），全部同一 DB 事务 + 双重记账；过期规则统一为创建后 24 小时，剩余退款由管理端扫描或领取时惰性触发，详情 GET 不触发退款；与群普通红包表分离，不复用均分包的表。
- 随机预拆分：建包时服务端用 `crypto/rand` 二倍均值法一次算定并落表（前 count-1 份在 `[1, min(2*floor(R/k)-1, R-k+1)]` 内随机、最后一份取剩余全部），领取时绝不现算现分；硬约束：每份 ≥ 1 分、总和严格等于总额、无负数、每份 ≤ 当时剩余金额且为其余份各留 ≥ 1 分（总额 < 份数时建包拒绝）；领取按 seq 升序取最早未领份，客户端不得指定金额。
- 手气最佳：金额最大的份，金额并列时最早领取者胜（领取时间升序、claim_id 升序决胜）；详情返回 `best_claimer_id` / `best_amount_cents`（无领取时为 null），新增手气排名接口（金额降序、领取时间升序，`rank` 为该顺序下的序号）。
- 防超发三层：预拆分份行 `UNIQUE (packet_id, seq)` + 包行 `FOR UPDATE` + 进度 CAS + 领取行 `UNIQUE (packet_id, claimer_id)`（每人限领一次）；发送方可领自己的包（同人时只做一次账户更新）。
- 接口：`POST /v1/group-lucky-redpackets`（准入引导优先、再验交易密码）、`POST /v1/group-lucky-redpackets/{id}/claim`（不重复验密但须准入 ok）、`GET /v1/group-lucky-redpackets/{id}` / `/{id}/claims` / `/{id}/ranking` / `?role=sent|received`、`POST /v1/admin/group-lucky-redpackets/expire`（单包或批量扫描，部分领取后过期只退剩余）。
- IM 载体契约与好友/群普通红包一致：自定义消息体只放 packetId，不放金额；UI 落地在 Phase 10/11。
- 测试：新增 Test49–Test65（17/17），Phase 1–4 的 48 项全部回归通过，合计 65/65；`go vet` 通过；首轮即全绿。
- 诚实记录的局限：IM 群成员关系校验不在资金服务内（只校验包内 receiver 集合；开放包无集合时不校验），须 Phase 10/11 由 chat/App 层在发包前调 OpenIM 群接口核验；退款 IM 通知由 chat 服务/客户端发送；单包份数上限 1000 与集合上限 1000 为本阶段硬编码 sanity cap，单包/单日金额上限属 Phase 7 风控配置。

## Phase 4（2026-11-02）：群普通红包（定额均分）

- 新表 `group_redpacket_packet` / `group_redpacket_share` / `group_redpacket_claim` / `group_redpacket_member`（迁移 0004，含回滚；迁移工具 up 改为 0001 -> 0002 -> 0003 -> 0004，down 逆序；Test14 迁移回滚测试同步覆盖 0004）。
- 资金模型：群发包（可用 → 冻结，txn type 12）/ 群领取（发送方冻结 → 领取方可用，按份金额，txn type 13）/ 群过期退款（冻结 → 发送方可用，按剩余金额，txn type 14），全部同一 DB 事务 + 双重记账；过期规则统一为创建后 24 小时，剩余退款由管理端扫描或领取时惰性触发，详情 GET 不触发退款。
- 均分规则：建包时服务端预拆分，base = total/count，前 total%count 份 base+1，总和严格等于总额，每份 ≥ 1 分（总额 < 份数时建包拒绝）；领取按 seq 升序取最早未领份，客户端不得指定金额。
- 防超发三层：预拆分份行 `UNIQUE (packet_id, seq)` + 包行 `FOR UPDATE` + 进度 CAS + 领取行 `UNIQUE (packet_id, claimer_id)`（每人限领一次）；发送方可领自己的包（同人时只做一次账户更新）。
- 接口：`POST /v1/group-redpackets`（准入引导优先、再验交易密码）、`POST /v1/group-redpackets/{id}/claim`（不重复验密但须准入 ok）、`GET /v1/group-redpackets/{id}` / `/{id}/claims` / `?role=sent|received`、`POST /v1/admin/group-redpackets/expire`（单包或批量扫描，部分领取后过期只退剩余）。
- IM 载体契约与好友红包一致：自定义消息体只放 packetId，不放金额；UI 落地在 Phase 10/11。
- 测试：新增 Test35–Test48（14/14），Phase 1–3 的 34 项全部回归通过，合计 48/48；`go vet` 通过；首轮即全绿。
- 诚实记录的局限：IM 群成员关系校验不在资金服务内（只校验包内 receiver 集合；开放包无集合时不校验），须 Phase 10/11 由 chat/App 层在发包前调 OpenIM 群接口核验；退款 IM 通知由 chat 服务/客户端发送；单包份数上限 1000 与集合上限 1000 为本阶段硬编码 sanity cap，单包/单日金额上限属 Phase 7 风控配置。

- 新表 `redpacket_packet` / `redpacket_claim`（迁移 0003，含回滚；迁移工具 up 改为 0001 -> 0002 -> 0003，down 逆序）。
- 资金模型：发包（可用 → 冻结，txn type 9）/ 领取（发送方冻结 → 领取方可用，txn type 10）/ 过期退款（冻结 → 发送方可用，txn type 11），全部同一 DB 事务 + 双重记账；过期规则统一为创建后 24 小时，退款由管理端扫描或领取时惰性触发，详情 GET 不触发退款。
- 接口：`POST /v1/redpackets`（准入引导优先、再验交易密码）、`POST /v1/redpackets/{id}/claim`（仅指定领取人、不重复验密但须准入 ok）、`GET /v1/redpackets/{id}` / `/{id}/claims` / `?role=sent|received`、`POST /v1/admin/redpackets/expire`（单包或批量扫描）。
- IM 载体契约：自定义消息体只放 packetId，不放金额；客户端不得指定领取金额；自定义消息类型常量名 `XIANLIAO_RED_PACKET`（数值建议 901，Phase 10 核对 SDK 区间后定稿）；App 层 UI 落地在 Phase 10/11。
- 测试：新增 Test23–Test34（12/12），Phase 1/2 的 22 项全部回归通过，合计 34/34；`go vet` 通过。
- 诚实记录的局限：IM 好友关系校验不在资金服务内（只校验领取人 = receiver_id），须 Phase 10/11 由 chat/App 层接线；退款 IM 通知由 chat 服务/客户端发送；单包上限属 Phase 7 风控配置，不硬编码。

## Phase 3（2026-11-02）：好友红包（单聊红包）

## Phase 2（2026-11-02）：钱包密码与提现绑定完整服务

- 鉴权：用户接口从 `X-User-ID + X-Internal-Secret` 改为 `Authorization: Bearer <IM token>`（调 OpenIM `POST /auth/parse_token` 解析 userID；IM 不可达回 503，绝不 fail-open）；旧内部口令头一律 401；`FUND_INTERNAL_SECRET` 废除，部署时从 `fund.env` 移除；新增配置 `FUND_IM_API_BASE` / `FUND_IM_API_TIMEOUT_MS`。
- 找回：新表 `wallet_tx_password_recovery`（迁移 0002）；流程为申请（实名 + 已绑方式后 4 位同方式匹配核验，材料不落库）→ 两名不同管理员先后 approve → 用户本人凭 request_id 重置（一次性、24h 有效）；单名管理员可 reject；Phase 1 的单管理员代设密码接口已删除。
- 提现方式：删默认/取消默认自动递补最早绑定的剩余方式；删唯一方式允许并返回 `was_last_method`；同类型同账号解密比对去重；银行卡号 12–19 位数字校验（新增与修改均校验）。
- 准入：`GET /v1/finance/status` 增加 `method_count` / `has_default_method` / `next_step` / `guidance`；新增服务端守卫 `requireAdmission` 供红包/转账/提现业务模块调用（非 ok 只引导、不扣款）。
- 迁移工具：`cmd/migrate` 改为按版本顺序执行多文件（up 0001 -> 0002，down 0002 -> 0001）。
- 测试：新增 Test16–Test22（IM 鉴权、token 冒用隔离、找回全流程、找回拒绝与过期、方式边界、方式校验、准入引导），Phase 1 的 15 项全部回归通过；合计 22/22。
- 首轮失败与修复（不隐瞒）：① `verifyRecoverIdentity` 无匹配时误返回 true（循环结束返回值写错）→ 改为 found 标志，测试捕获；② 测试 stub 把 `imtok-bad` 也当有效 token（前缀匹配）→ stub 显式拒绝 `imtok-bad`。

## Phase 1（2026-11-02）：人民币钱包数据库及统一账务核心

- 9 张表（迁移 0001）、双重记账账务服务、交易密码基础服务、提现账户基础服务、统一准入检查、15 项自动化测试全部通过。详见 TEST_REPORT.md。
