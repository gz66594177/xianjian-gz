# 仙聊资金服务 Phase 1 — 数据库设计文档

> 资金库：独立 PostgreSQL 16（容器 `fund-postgres`，`127.0.0.1:15432`，库 `xianliao_fund`，数据卷 `/opt/xianliao/fund-postgres-data`）。
> 与 YiChat、OpenIM 聊天业务库严格分离：迁移只动资金库，不动 `openim_v3`、不动任何生产数据。
> 金额全部 `BIGINT` 分（int64），禁浮点。迁移：`migrations/0001_init.up.sql` / `0001_init.down.sql`（版本化、可回滚；回滚删表，执行前必须备份）。

## 1. 表清单（24 张：Phase 1 的 9 张 + Phase 2 的找回申请单 + Phase 3 的红包 2 张 + Phase 4 的群红包 4 张 + Phase 5 的群拼手气红包 4 张 + Phase 6 的转账订单 1 张 + Phase 7 的调整单与限额配置 2 张 + Phase 9 的提现手续费与限额配置 1 张；另 wallet_order 在 Phase 7 增加 5 个审核字段、在 Phase 8 增加 5 个充值字段、在 Phase 9 增加 5 个提现字段）

### wallet_order 提现字段扩展（Phase 9，迁移 0009）

新增：`freeze_id`（提交冻结行追溯，对应 `wallet_freeze` 行；Phase 7 种子单为空）/
`payout_proof_no`（人工打款凭证：银行/渠道回单号，≤256 字）/ `payout_at`（打款时间）/
`payout_operator_id`（打款确认操作员）/ `payout_note`（打款备注，≤500 字）。
约束：`ck_order_withdraw_payout`（`order_type=2 AND status=4` 的行必须有回单号，
人工打款确认接口保证回填后才置 4，约束为第二道防线；历史 Phase 7 种子单状态恒为 0/1/2，不受影响）。
索引：`ix_order_withdraw_user (user_id, created_at DESC)`（提现用户查询；管理端类型/状态查询已有
`ix_order_type_status` 覆盖）。

### fund_withdraw_config — 提现手续费与限额配置（Phase 9，迁移 0009）

单行 `config_key='default'`；`fixed_fee_cents`（≥ 0，默认 0）/ `rate_bp`（0–10000，默认 0）/
`min_amount_cents`（NULL = 除 >0 外不限，默认 NULL）/ `single_max_cents`（NULL = 不限额，默认 NULL）/
`daily_max_cents`（NULL = 不限额，默认 NULL）/ `daily_count_max`（NULL = 不限额，默认 NULL；运营建议值 5 笔/日）/
`updated_by` / `updated_at`。手续费公式：`fee = fixed + floor(amount × rate_bp / 10000)`，`actual = amount − fee`。
频率统计口径：当日已提交且未被拒绝的提现单（`status <> 2`），含待审核/已通过/处理中/成功/失败/已退款。

### 提现资金流转（与账务的对应关系）

- 提交冻结（txn type 22，`ref_type='withdraw'`，`ref_id=order_id`）：用户可用 DR / 用户冻结 CR
  （金额 + 手续费），同一事务内建 txn、分录 2 条、冻结行（`remaining = 金额 + 手续费`）、订单行（status 0）与审计行。
- 审核拒绝解冻（txn type 23）：用户冻结 DR / 用户可用 CR（金额 + 手续费），同一事务内冻结行
  `remaining → 0`（status 1 已解冻）、订单 status → 2 与审计行。
- 人工打款确认成功（txn type 24）：用户冻结 DR / 平台结算 CR（金额 + 手续费），同一事务内冻结行
  `remaining → 0`（status 2 已扣款）、订单 status → 4（凭证回填）与审计行；平台户余额同步减少金额 + 手续费，
  维护“平台户余额 = 全部用户余额之和”不变量；手续费收入确认不在本账务口径内，订单行 `fee_cents` 为唯一事实记录。
- 渠道处理失败（无 txn）：订单 status 3 → 5（`failure_reason` 回填）与审计行，无分录、余额与冻结不动。
- 失败后退款解冻（txn type 25）：用户冻结 DR / 用户可用 CR（金额 + 手续费），同一事务内冻结行
  `remaining → 0`（status 1 已解冻）、订单 status → 6 与审计行。
- 冻结行 remaining 与订单总额（金额 + 手续费）不一致时解冻/扣减直接失败、整笔回滚（与红包异常防护同口径）。
- 订单为业务单据（状态机需 UPDATE），不在 append-only 之列；ledger / audit_log 仍禁 UPDATE/DELETE（见 0001）。
- 回滚：0009 down 删配置表与 5 个扩展列（执行前必须备份；回滚后已进入 3/4/5/6 状态的提现单无法再用 Phase 9 接口流转，
  生产回滚前须先确认无处理中提现单）。

### wallet_order 充值字段扩展（Phase 8，迁移 0008）

新增：`channel`（`mock` / `alipay` / `wechat`，CHECK 约束；充值行 `order_type=1` 必须有渠道，
`CHECK (order_type <> 1 OR channel IS NOT NULL)`，历史非充值行允许为空）/ `paid_txn_id`（已支付后必填的账务追溯，
对应 `wallet_txn` type 20）/ `refund_txn_id`（已退款后必填，对应 `wallet_txn` type 21）/ `failure_reason`（失败回调原因，
≤500 字）/ `client_idempotency_key`（创建幂等，`UNIQUE (user_id, client_idempotency_key)`，空键除外，
与 `wallet_txn` 的 partial unique 口径一致）。索引：`ux_order_user_client_idem`（创建幂等）、
`ix_order_recharge (order_type, channel, created_at DESC)`（充值管理查询）。

### 充值资金流转（与账务的对应关系）

- 充值入账（txn type 20，`ref_type='recharge'`，`ref_id=order_id`）：用户可用 CR / 平台结算户 DR，
  同一事务内建 txn、分录 2 条、订单 status 0 → 1（`channel_order_no` / `paid_txn_id` 回填）与审计行
  （`recharge_paid`）；维护“平台户余额 = 全部用户余额之和”不变量（与收入同向）。
- 充值失败（无 txn）：订单 status 0 → 2（`failure_reason` 回填）与审计行（`recharge_failed`），无分录、双方余额不动。
- 充值退款（txn type 21，仅 mock 渠道演练）：用户可用 DR / 平台结算户 CR，同一事务内建 txn、分录 2 条、
  订单 status 1 → 3（`refund_txn_id` 回填）与审计行（`recharge_refunded`）；用户余额不足时整笔回滚、
  订单与余额不动，另起短事务记审计（`recharge_refund_failed_insufficient`，审计行不落账、只留痕）。
- 回调幂等三层：① 订单行 `FOR UPDATE` + 状态 CAS（仅合法流转可写，同一目标状态重放直接返回原单）；
  ② 渠道单号唯一索引 `ux_order_channel`（串单时冲突拒绝）；③ 入账幂等键 `rch-pay-<order_id>` /
  退款幂等键 `rch-refund-<order_id>`（并发回调已入账时回滚后返回现状订单）。
- 订单为业务单据（状态机需 UPDATE），不在 append-only 之列；ledger / audit_log 仍禁 UPDATE/DELETE（见 0001）。

### fund_admin_adjustment — 管理员资金调整单（Phase 7，迁移 0007）

`adjustment_id` 主键；`user_id`；`direction`（1 增加可用余额 2 减少可用余额）；`amount_cents`（>0，分）；`reason`（1–500 字）；`status`（0 待复核 1 已执行 2 已拒绝）；`created_by_admin_id`（创建人）；`first_admin_id` / `second_admin_id`（两名不同复核人；`CHECK (first <> second)`）；`rejected_by_admin_id`；`original_adjustment_id`（纠错反向单注明原单号；建单时校验原单已执行且方向相反）；`executed_txn_id`（执行后必填，`CHECK (status<>1 OR executed_txn_id IS NOT NULL)`，对应 `wallet_txn` type 19）；`executed_at`。索引：用户/状态+创建时间。单据只查不改：无修改接口；执行后不可撤销、不可拒绝。

### fund_risk_limit — 风控限额配置（Phase 7，迁移 0007）

单行 `config_key='default'`；`single_max_cents` / `daily_max_cents`（NULL = 不限额，默认不限额；CHECK ≥ 0）；`updated_by` / `updated_at`。接入范围：本阶段仅转账发起路径校验单笔上限；单日上限只存配置、不接入执行；红包路径限额接入留 Phase 8/9（见 API 文档 §12.6）。

### wallet_order 提现审核字段扩展（Phase 7，迁移 0007）

新增：`review_first_admin_id` / `review_second_admin_id`（双人复核）/ `rejected_by_admin_id` / `review_note`（≤500 字）/ `reviewed_at`。提现 7 态：0 待审核 1 已通过 2 已拒绝 3 处理中 4 成功 5 失败 6 已退款；本阶段管理端仅允许 0 → 1（双人）与 0 → 2（拒绝），3–6 为 Phase 9 流转；审核通过只改订单状态，不产生打款动作。

### 管理员调整单资金流转（与账务的对应关系）

- 增加（txn type 19）：用户可用 CR / 平台结算 DR，同一事务内建 txn、分录 2 条、调整单 status → 1（`executed_txn_id` 回填）与审计行；与收入同向，维护“平台户余额 = 全部用户余额之和”不变量。
- 减少（txn type 19）：用户可用 DR / 平台结算 CR；用户余额不足或平台侧不足时整笔回滚，第二复核人不落单，单据仍为待第二复核。
- 纠错：执行后不可撤销；反向调整单（方向相反、注明原单号）经双人复核执行后余额恢复；原单行与已执行 txn 均不改写（历史事实）。

### transfer_order — 转账订单（Phase 6，迁移 0006）

`order_id` 主键；`sender_id` / `receiver_id`（`CHECK (sender_id <> receiver_id)`，禁自转）；`amount_cents`（>0，分）；`remark`（≤200 字）；`status`（1 成功 2 失败）；`failure_code`（仅失败行：`INSUFFICIENT_BALANCE` / `RECEIVER_FROZEN` / `RECEIVER_CLOSED`；成功行必为空）；`txn_id`（仅成功行：对应 `wallet_txn`，type 18；失败行必为空）；`idempotency_key`（成功订单 `UNIQUE (sender_id, idempotency_key)`，空键除外；失败审计行不带幂等键，同一幂等键事后重发不受阻挡）。索引：发出/收到/成功幂等。订单为业务单据（状态机与失败审计行需 INSERT），不在 append-only 之列；ledger / audit_log 仍禁 UPDATE/DELETE（见 0001）。

### 转账资金流转（与账务的对应关系，即时到账，无冻结/过期/领取环节）

- 转账（txn type 18）：同一 DB 事务、同一 txn_id 下两条分录（转出方可用 DR / 转入方可用 CR，金额相等，带符号合计为 0），同一事务内建订单行（status 1）与审计行；收款方钱包不存在时在同一事务内自动建钱包（事务回滚时该行一并消失）；收款方钱包冻结/注销、转出方余额不足时整笔回滚（无 txn、无分录、无成功订单行，双方余额不动），HTTP 层随后另起短事务写一行 status 2 失败审计行（不落账）。
- 冲正（txn type 8，复用已有冲正，不另立 type 19）：转出方凭原 txn 经 `POST /v1/ledger/reverse` 发起，全部分录取反；接收方余额不足时冲正整体失败、原转账不动；冲正不改写原订单行（订单为历史事实）。
- 模式定稿：全阶段只用一套资金状态机——即时到账，与红包即时结算模型一致；待确认模式不做（理由见 API 文档 §9 与迁移 0006 文件头）。

### group_lucky_redpacket_packet — 群拼手气红包包单（Phase 5，迁移 0005）

`packet_id` 主键；`sender_id` / `group_id`；`total_amount_cents`（>0，分）/ `total_count`（>0，≤1000；`CHECK (total_amount_cents >= total_count)` 保证每份 ≥ 1 分）；`claimed_count` / `claimed_amount_cents`（实时进度，`CHECK` 不超总量）；`greeting`（≤200 字）；`status`（0 待领取 1 已领完 2 已过期退款）；`freeze_id`（对应 wallet_freeze 行，发包冻结款）；`send_txn_id` / `refund_txn_id`（账务追溯；领取 txn 不记在包行，见领取表）；`idempotency_key`（`UNIQUE (sender_id, idempotency_key)`，重复发送幂等）；`expires_at`（创建 + 24 小时）；`claimed_at`（领完时）/ `refunded_at`。索引：发出/群/状态+过期时间。手气最佳不落表：详情与排名按领取表 `amount_cents DESC, claimed_at ASC, claim_id ASC` 取首条（金额并列时最早领取者胜）。

### group_lucky_redpacket_share — 群拼手气红包预拆分份（Phase 5，迁移 0005）

`share_id` 主键；`packet_id`；`seq`（1..total_count，`UNIQUE (packet_id, seq)`）；`amount_cents`（>0，服务端建包时 `crypto/rand` 二倍均值法预拆分：前 count-1 份在 `[1, min(2*floor(R/k)-1, R-k+1)]` 内随机、最后一份取剩余全部，总和严格等于总金额，每份 ≥ 1 分）；`status`（0 未领 1 已领）；`claimer_id` / `claim_id` / `claimed_at`（领取时回填）。领取按 seq 升序取最早未领份；份行金额建包后不再变动，事后求和即为审计依据。

### group_lucky_redpacket_claim — 群拼手气红包领取记录（Phase 5，迁移 0005）

`claim_id` 主键；`packet_id`；`claimer_id`；`share_id`；`amount_cents`（= 所领份金额，服务端定，客户端不得指定）；`claimed_at`。`UNIQUE (packet_id, claimer_id)`——每人限领一次（第二道防线；第一道为领取事务内的包行锁 + 进度 CAS）。

### group_lucky_redpacket_member — 群拼手气红包包内 receiver 集合（Phase 5，迁移 0005）

`packet_id` + `user_id` 联合主键；发包时可选传入 member_ids（≤1000 人，发送方恒在集合内）；集合为空表示开放包（IM 群成员关系核验由 Phase 10/11 的 chat/App 层在发包前调 OpenIM 群接口核验，资金服务不直读 IM 库表）。

### 群拼手气红包资金流转（与账务的对应关系，与群普通红包同模型、表分离）

- 发包（txn type 15）：发送方可用 DR / 发送方冻结 CR，同一事务内建 wallet_freeze 行（remaining = 总金额）、包行、N 个随机预拆分份行与 receiver 集合行。
- 领取（txn type 16）：发送方冻结 DR / 领取方可用 CR（按份金额），同一事务内份行 status → 1、建领取行、包 claimed_count/claimed_amount 递增（CAS）、冻结行 remaining 递减；领完时包 status → 1、冻结行 status → 2（已扣款完毕）。同一领取人重领返回原领取记录；并发领取由包行 `FOR UPDATE` 串行化，恰好 total_count 条领取行、金额总和等于已领总额。发送方领自己的包时只做一次账户更新（可用 += 份额、冻结 -= 份额）。
- 过期退款（txn type 17）：发送方冻结 DR / 发送方可用 CR（按剩余金额 = total - claimed_amount），同一事务内冻结行 remaining → 0（status 1 已解冻）、包 status → 2。退款幂等：包状态 CAS（仅 status 0 可退款）+ 退款幂等键 `lky-refund-<packet_id>`；扫描与领取惰性退款互斥于同一包行锁。冻结行 remaining 与剩余金额不一致时领取/退款直接失败、整笔回滚（Test64 覆盖）。

### group_redpacket_packet — 群普通红包包单（Phase 4，迁移 0004）

`packet_id` 主键；`sender_id` / `group_id`；`total_amount_cents`（>0，分）/ `total_count`（>0，≤1000；`CHECK (total_amount_cents >= total_count)` 保证每份 ≥ 1 分）；`claimed_count` / `claimed_amount_cents`（实时进度，`CHECK` 不超总量）；`greeting`（≤200 字）；`status`（0 待领取 1 已领完 2 已过期退款）；`freeze_id`（对应 wallet_freeze 行，发包冻结款）；`send_txn_id` / `refund_txn_id`（账务追溯；领取 txn 不记在包行，见领取表）；`idempotency_key`（`UNIQUE (sender_id, idempotency_key)`，重复发送幂等）；`expires_at`（创建 + 24 小时）；`claimed_at`（领完时）/ `refunded_at`。索引：发出/群/状态+过期时间。

### group_redpacket_share — 群红包预拆分份（Phase 4，迁移 0004）

`share_id` 主键；`packet_id`；`seq`（1..total_count，`UNIQUE (packet_id, seq)`）；`amount_cents`（>0，服务端预拆分：base = total/count，前 total%count 份 base+1）；`status`（0 未领 1 已领）；`claimer_id` / `claim_id` / `claimed_at`（领取时回填）。领取按 seq 升序取最早未领份。

### group_redpacket_claim — 群红包领取记录（Phase 4，迁移 0004）

`claim_id` 主键；`packet_id`；`claimer_id`；`share_id`；`amount_cents`（= 所领份金额，服务端定，客户端不得指定）；`claimed_at`。`UNIQUE (packet_id, claimer_id)`——每人限领一次（第二道防线；第一道为领取事务内的包行锁 + 进度 CAS）。

### group_redpacket_member — 群红包包内 receiver 集合（Phase 4，迁移 0004）

`packet_id` + `user_id` 联合主键；发包时可选传入 member_ids（≤1000 人，发送方恒在集合内）；集合为空表示开放包（IM 群成员关系核验由 Phase 10/11 的 chat/App 层在发包前调 OpenIM 群接口核验，资金服务不直读 IM 库表）。

### 群红包资金流转（与账务的对应关系）

- 发包（txn type 12）：发送方可用 DR / 发送方冻结 CR，同一事务内建 wallet_freeze 行（remaining = 总金额）、包行、N 个预拆分份行与 receiver 集合行。
- 领取（txn type 13）：发送方冻结 DR / 领取方可用 CR（按份金额），同一事务内份行 status → 1、建领取行、包 claimed_count/claimed_amount 递增（CAS）、冻结行 remaining 递减；领完时包 status → 1、冻结行 status → 2（已扣款完毕）。同一领取人重领返回原领取记录；并发领取由包行 `FOR UPDATE` 串行化，恰好 total_count 条领取行、金额总和等于已领总额。发送方领自己的包时只做一次账户更新（可用 += 份额、冻结 -= 份额）。
- 过期退款（txn type 14）：发送方冻结 DR / 发送方可用 CR（按剩余金额 = total - claimed_amount），同一事务内冻结行 remaining → 0（status 1 已解冻）、包 status → 2。退款幂等：包状态 CAS（仅 status 0 可退款）+ 退款幂等键 `grp-refund-<packet_id>`；扫描与领取惰性退款互斥于同一包行锁。冻结行 remaining 与剩余金额不一致时领取/退款直接失败、整笔回滚（Test47 覆盖）。

### redpacket_packet — 好友红包包单（Phase 3，迁移 0003）

`packet_id` 主键；`sender_id` / `receiver_id`（`CHECK (sender_id <> receiver_id)`，禁自发）；`amount_cents`（>0，分）；`greeting`（≤200 字）；`status`（0 待领取 1 已领取 2 已过期退款）；`freeze_id`（对应 wallet_freeze 行，发包冻结款）；`send_txn_id` / `claim_txn_id` / `refund_txn_id`（账务追溯）；`idempotency_key`（`UNIQUE (sender_id, idempotency_key)`，重复发送幂等）；`expires_at`（创建 + 24 小时）；`claimed_at` / `refunded_at`。索引：发出/收到/状态+过期时间。

### redpacket_claim — 红包领取记录（Phase 3，迁移 0003）

`claim_id` 主键；`packet_id`（`UNIQUE (packet_id)`——好友包一人一包，领取行唯一约束为第二道防线，第一道为领取事务内的包行锁 + 状态 CAS）；`claimer_id`（必须等于包的 receiver_id）；`amount_cents`（= 包金额，服务端定，客户端不得指定）。

### 红包资金流转（与账务的对应关系）

- 发包（txn type 9）：发送方可用 DR / 发送方冻结 CR，同一事务内建 wallet_freeze 行（remaining = 包金额）与包行（status 0）。
- 领取（txn type 10）：发送方冻结 DR / 领取方可用 CR，同一事务内冻结行 remaining → 0（status 2 已扣款）、建领取行、包 status → 1。领取人重复领取返回原领取记录；并发领取由包行 `FOR UPDATE` 串行化，恰好一条领取行、恰好入账一次。
- 过期退款（txn type 11）：发送方冻结 DR / 发送方可用 CR，同一事务内冻结行 remaining → 0（status 1 已解冻）、包 status → 2。退款幂等：包状态 CAS（仅 status 0 可退款）+ 退款幂等键 `rp-refund-<packet_id>`；扫描与领取惰性退款互斥于同一包行锁。
- 冻结行 remaining 与包金额不一致（如被异常篡改）时领取/退款直接失败、整笔回滚，包状态与领取记录均不动（Test34 覆盖）。

### wallet_tx_password_recovery — 交易密码安全找回申请单（Phase 2，迁移 0002）

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| request_id | VARCHAR(128) PK | 申请单号（`rec_` + 随机） |
| user_id | VARCHAR(128) NOT NULL，索引 (user_id, created_at DESC) | 申请人 |
| status | SMALLINT DEFAULT 0 CHECK IN (0,1,2,3,4) | 0 待复核 1 双人通过 2 已使用 3 已拒绝 4 已过期 |
| first_admin_id / second_admin_id | VARCHAR(128) NULL | 两名复核管理员（须不同人；第二人通过后状态置 1） |
| expires_at | TIMESTAMPTZ NOT NULL | 申请后 24 小时；过期读取时惰性标记为 4 |
| created_at / updated_at | TIMESTAMPTZ | — |

注意：身份核验材料（实名、后 4 位）不落库、不进审计；申请单只存流程状态。索引：(user_id, created_at DESC)、(status)。回滚：0002 down 仅删本表（须先于 0001 down 执行）。

### wallet_account — 用户钱包账户

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| user_id | VARCHAR(128) PK | 对应 IM user_id；不建到 IM 库的物理外键（跨库，应用层校验） |
| balance_cents | BIGINT NOT NULL DEFAULT 0 CHECK ≥ 0 | 可用余额（分） |
| frozen_cents | BIGINT NOT NULL DEFAULT 0 CHECK ≥ 0 | 冻结余额（分） |
| status | SMALLINT DEFAULT 0 | 0 正常 1 整户冻结 2 注销 |
| version | BIGINT DEFAULT 0 | 乐观锁版本号 |
| created_at / updated_at | TIMESTAMPTZ | — |

种子行：`user_id='platform:settlement'`（平台结算户，非真实用户，API 层禁止查询/操作）。

### wallet_txn — 资金交易头（幂等）

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| txn_id | VARCHAR(128) PK | 服务端生成（`txn_` + 随机） |
| user_id | VARCHAR(128) NOT NULL，索引 (user_id, created_at DESC) | 归属用户 |
| type | SMALLINT NOT NULL | 1 收入 2 支出 3 冻结 4 解冻 5 冻结扣款 6 冻结退款 7 退款 8 冲正 |
| ref_type / ref_id | VARCHAR，联合索引 | 业务引用（红包/订单等，Phase 1 预留） |
| status | SMALLINT DEFAULT 1 | 0 待处理 1 成功 2 失败（Phase 1 同步成功才落库） |
| idempotency_key | VARCHAR(256)，`UNIQUE(user_id, idempotency_key)`（空键除外，partial index） | 幂等键 |
| created_at / updated_at | TIMESTAMPTZ | — |

### wallet_ledger — 资金账务流水（分录，append-only）

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| entry_id | BIGSERIAL PK | 分录号，单调递增 |
| txn_id | VARCHAR NOT NULL，索引 | 归属交易 |
| user_id | VARCHAR NOT NULL，索引 (user_id, created_at DESC) | 归属用户 |
| account_type | SMALLINT NOT NULL | 1 可用 2 冻结 |
| direction | SMALLINT NOT NULL | 1 借（DR） 2 贷（CR） |
| amount_cents | BIGINT NOT NULL CHECK > 0 | 分录金额（分，正数，方向由 direction 定） |
| balance_after_cents / frozen_after_cents | BIGINT NOT NULL | 该分录后该户可用/冻结余额快照 |
| opposite_user_id / opposite_account | VARCHAR NULL | 对手方用户/账户（平台户、冻结户等） |
| created_at | TIMESTAMPTZ NOT NULL | 无 updated_at |

### wallet_order — 资金业务订单（Phase 1 结构预留）

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| order_id | VARCHAR PK | 业务订单号 |
| user_id | VARCHAR NOT NULL，索引 | 归属用户 |
| order_type | SMALLINT NOT NULL | 1 充值 2 提现 3 红包 4 转账 |
| amount_cents | BIGINT CHECK > 0 | 订单金额（分） |
| fee_cents | BIGINT DEFAULT 0 CHECK ≥ 0 | 手续费（分） |
| actual_cents | BIGINT CHECK ≥ 0 | 实际到账金额（分） |
| status | SMALLINT，索引 | 充值/提现 7 态或红包/转账状态机（Phase 2+ 落地状态流转） |
| withdraw_method_id | VARCHAR NULL | 本次提现使用的方式（逻辑引用，不跨库物理外键） |
| channel_order_no | VARCHAR，UNIQUE（非空） | 渠道订单号（回调幂等） |
| created_at / updated_at | TIMESTAMPTZ | — |

### wallet_freeze — 冻结记录

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| freeze_id | VARCHAR PK | 冻结记录号（客户端可指定，否则服务端生成） |
| user_id | VARCHAR NOT NULL，索引 | 归属用户 |
| txn_id / order_id | VARCHAR NULL | 建冻结的交易/订单 |
| amount_cents | BIGINT CHECK > 0 | 原冻结金额 |
| remaining_cents | BIGINT CHECK ≥ 0 | 剩余冻结金额（解冻/扣款/退款后递减） |
| status | SMALLINT DEFAULT 0 | 0 冻结中 1 已解冻 2 已扣款（部分操作后 remaining>0 仍为 0） |
| created_at / updated_at | TIMESTAMPTZ | — |

### wallet_tx_password — 交易密码安全信息

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| user_id | VARCHAR PK | 归属用户 |
| password_hash | VARCHAR NOT NULL | bcrypt 哈希，禁明文、禁管理员查看 |
| error_count | INT DEFAULT 0 CHECK ≥ 0 | 连续错误次数 |
| locked_until | TIMESTAMPTZ NULL | 锁定时限（错 5 次锁 30 分钟） |
| last_changed_at | TIMESTAMPTZ NULL | 最近修改时间 |
| created_at / updated_at | TIMESTAMPTZ | — |

### wallet_withdraw_method — 提现方式绑定信息

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| method_id | VARCHAR PK | 方式记录号 |
| user_id | VARCHAR NOT NULL，索引 (user_id, type) | 归属用户 |
| type | SMALLINT NOT NULL | 1 支付宝 2 微信 3 银行卡 |
| real_name | VARCHAR NOT NULL | 实名姓名（默认脱敏返回口径，见 API 文档） |
| account_no_enc | TEXT NOT NULL | 账号/卡号 AES-GCM 密文（base64 nonce‖密文），禁明文 |
| bank_name / bank_branch | VARCHAR NULL | 开户银行/开户支行（银行卡用） |
| is_default | BOOLEAN DEFAULT FALSE | 是否默认；`UNIQUE(user_id) WHERE is_default AND status=0`（partial unique index）+ 事务清旧默认双重保证 |
| status | SMALLINT DEFAULT 0 | 0 正常 1 已解绑（软删） |
| bind_time / updated_at | TIMESTAMPTZ | — |

### wallet_audit_log — 资金操作审计日志（append-only）

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| log_id | BIGSERIAL PK | 日志号 |
| user_id | VARCHAR NOT NULL，索引 | 操作用户 |
| operator_id | VARCHAR NULL | 操作人（本人/管理员/系统任务） |
| action | VARCHAR NOT NULL | 设置密码/改密码/绑卡/解绑/冻结/解冻/找回申请与确认等 |
| detail_masked | TEXT NULL | 脱敏后的操作详情（不得含完整卡号、密码、哈希） |
| ip / device | VARCHAR NULL | 操作来源 |
| created_at | TIMESTAMPTZ NOT NULL | 无 updated_at |

### wallet_reconcile — 账务对账记录

| 字段 | 类型/约束 | 说明 |
|---|---|---|
| reconcile_id | VARCHAR PK | 对账批次号 |
| reconcile_date | DATE NOT NULL，索引 | 对账日期（UTC 日期） |
| channel | VARCHAR NULL | 渠道（本阶段恒为 `internal`） |
| expected_cents / actual_cents / diff_cents | BIGINT NOT NULL | 账面合计 / 分录派生合计 / 差异（分） |
| status | SMALLINT DEFAULT 0 | 0 平账 1 差异待处理 2 已处理 |
| handler_id / handled_at | VARCHAR/TIMESTAMP NULL | 处理人与处理时间（Phase 1 仅记录结构，处理流程留后续阶段） |
| created_at | TIMESTAMPTZ NOT NULL | — |

## 2. append-only 的数据库层强制

迁移以管理员账号执行后，业务账号 `fund_app` 的权限为：

- `wallet_account, wallet_txn, wallet_order, wallet_freeze, wallet_tx_password, wallet_withdraw_method, wallet_reconcile`：SELECT / INSERT / UPDATE；
- `wallet_ledger, wallet_audit_log`：仅 SELECT / INSERT，并显式 `REVOKE UPDATE, DELETE`；
- 序列：USAGE / SELECT。

实测校验（主库）：`has_table_privilege('fund_app','wallet_ledger','UPDATE') = false`，
`has_table_privilege('fund_app','wallet_audit_log','DELETE') = false`，
`has_table_privilege('fund_app','wallet_ledger','INSERT') = true`。

## 2.5 迁移 0010：手动充值 + 网关支付（只加表加字段，不改现有数据）

- `wallet_order` 扩展列：`recharge_kind VARCHAR(16) NOT NULL DEFAULT 'callback' CHECK IN ('callback','manual','gateway')`
  （历史回调行默认回填 `callback`）、`payment_method_id`（手动单引用收款方式）、`gateway_ref`、
  `cashier_url`、`expires_at`（网关单 30 分钟过期）；`channel` CHECK（`wallet_order_channel_check`）
  扩到 `mock/alipay/wechat/manual/gateway`；`order_type=1` 行 channel 非空规则不变；新增 CHECK：
  手动行必须有 `payment_method_id`、网关行必须有 `expires_at`；索引 `ix_order_kind_status
  (recharge_kind, status, created_at DESC)`；`channel_order_no` 部分唯一索引（非空）为渠道单号
  第二道防线（网关回调单号不一致时先行拦截）。
- 新表 `wallet_payment_method`：`method_id`（主键，`pm_` 前缀）、`type CHECK IN ('wechat','alipay','bank')`、
  `display_name VARCHAR(128)`、`qr_file_id`（可空，建方式时无码、上传后回填）、`enabled BOOLEAN DEFAULT TRUE`、
  `sort_order`、`created_by_admin_id`、`created_at/updated_at`；索引 `(enabled, sort_order, created_at)`。
- 新表 `fund_upload_file`：`file_id`（主键，`upl_` 前缀）、`kind CHECK IN ('qr','proof')`、
  `content_type`、`size_bytes CHECK > 0`、`storage_path VARCHAR(512)`（服务端随机文件名，绝不含用户可控内容）、
  `created_at`。
- 新表 `recharge_proof`：`proof_id`（主键，`prf_` 前缀）、`order_id UNIQUE`（一单一凭证，重传替换旧行）、
  `file_id`、`uploaded_by_user_id`、`created_at/updated_at`；索引 `(uploaded_by_user_id, created_at DESC)`。
- 权限（`fund_app`）：`wallet_payment_method` SELECT/INSERT/UPDATE；`fund_upload_file` 与 `recharge_proof`
  SELECT/INSERT/UPDATE/DELETE（DELETE 仅用于凭证/二维码替换时的旧文件清理）。
- 状态命名空间（按 `recharge_kind` 区分，查询须恒带 kind 过滤）：manual `10 待上传凭证 → 11 待审核 → 1 已入账`、
  `11 → 12 已拒绝`；gateway `0 待支付 → 1 已支付 / 2 失败`、`0 → 4 超时关闭`；callback `0/1/2/3` 不变。
  入账分录：手动审核通过与网关回调入账均走 txn type 20（用户可用 CR / 平台结算 DR，`ref_type='recharge'`）。

## 3. 对账派生公式

逐户核对 `sum(分录) = 账户余额`，可用与冻结分别核对：

- 用户户：派生可用 = Σ(可用分录：贷 + / 借 −)；派生冻结同理。
- 平台结算户：派生余额取反（借增贷减，见 API 文档 §2），即派生 = −Σ(贷 + / 借 −)。
- 批次行：`expected = Σ账户余额`，`actual = Σ派生余额`，`diff = expected − actual`；逐户任一差额非 0 或总差非 0 均置 `status=1`（差异待处理）。

## 4. 迁移工具

`cmd/migrate`（管理员 DSN 执行，只动资金库；按版本顺序执行，up 为 0001 -> 0002 -> 0003 -> 0004 -> 0005 -> 0006 -> 0007 -> 0008 -> 0009 -> 0010，down 为 0010 -> 0009 -> 0008 -> 0007 -> 0006 -> 0005 -> 0004 -> 0003 -> 0002 -> 0001）：

```
FUND_ADMIN_DATABASE_URL=... FUND_DATABASE_URL=... MIGRATIONS_DIR=./migrations ./fund-migrate up
./fund-migrate down   # 删表！执行前必须备份
```

up 在建表后还会以业务 DSN 解析的口令设置 `fund_app` 角色口令（口令不打印、不进仓库）。
