# 仙聊资金服务 Phase 1 — API 文档

> 服务：独立 Go 进程（net/http，不引 OpenIM 依赖），部署监听 `127.0.0.1:18081`（仅回环，不公网暴露）。
> 金额：全部 `int64` 分，禁浮点。成功包：`{"ok":true,"data":...}`；失败包：`{"ok":false,"error":{"code":...,"message":...}}`。

## 1. 鉴权（Phase 2 起：用户接口用 IM token；内部口令已废除）

| 接口组 | 请求头 | 说明 |
|---|---|---|
| 用户接口（`/v1/wallet/*`、`/v1/ledger/*`、`/v1/tx-password/*`（除管理复核外）、`/v1/withdraw-methods/*`、`/v1/finance/status`、`/v1/transfers/*`） | `Authorization: Bearer <IM token>` | 资金服务调 OpenIM server `POST /auth/parse_token`（白名单接口，须带 `operationID` 头）解析 userID；缺失/无效 → 401；IM 校验服务不可达 → 503（绝不 fail-open、不回落口令） |
| 管理接口（`/v1/admin/*`） | `X-Admin-Secret`（须与 `fund.env` 一致） | 错误/缺失 → 401；双人复核类接口另须 `X-Admin-ID`（区分两名管理员，缺失 → 401） |
| 越权规则 | — | 一切 `user_id` 参数（body/query/path 语义）必须等于 token 解析出的用户，否则 403；`platform:settlement` 一律 403（平台结算户禁 API 操作） |

**网关约定（Phase 10/11 集成时遵守）：** 调用方网关必须把用户本人的 IM token 原样透传到资金服务，绝不能用共享密钥冒充用户；资金服务不接受、不解析任何内部口令头。`X-User-ID` / `X-Internal-Secret` 自 Phase 2 起不再被接受（旧头一律 401）。

**Phase 2 破坏性变更（尚无真实调用方，预发布阶段可接受）：** ① 用户鉴权从内部口令改为 IM token；② 找回流程替换（见 §3，旧的单管理员代设密码接口已删除）。

## 2. 钱包与账务

| 方法与路径 | 说明 | 请求（关键字段） | 成功返回 |
|---|---|---|---|
| `GET /healthz` | 存活检查（无需鉴权） | — | `{"status":"ok"}` |
| `POST /v1/wallet/create` | 创建钱包（幂等，已存在返回原钱包） | —（用户取自请求头） | wallet_account |
| `GET /v1/wallet` / `GET /v1/wallet/balance` | 钱包信息/余额 | — | `{user_id, balance_cents, frozen_cents, status, version, created_at, updated_at}` |
| `GET /v1/wallet/ledger?limit&offset` | 流水查询（倒序，limit 默认 50、上限 200） | — | ledger entry 数组 |
| `POST /v1/ledger/income` | 收入：用户可用 +X，平台结算户记对手方 | `{amount_cents>0, idempotency_key?, ref_type?, ref_id?}` | txn |
| `POST /v1/ledger/expense` | 支出：用户可用 −X，平台结算户记对手方；余额不足拒绝且不变 | 同上 | txn |
| `POST /v1/ledger/refund` | 退款：与收入同向、不同 txn type | 同上 | txn |
| `POST /v1/ledger/freeze` | 冻结：可用 −X、冻结 +X（同一事务内建 wallet_freeze 行） | `{amount_cents, freeze_id?, idempotency_key?, ref_type?, ref_id?}` | `{txn, freeze_id}` |
| `POST /v1/ledger/unfreeze` | 解冻：冻结 −X、可用 +X | `{freeze_id, amount_cents}` | txn |
| `POST /v1/ledger/freeze-charge` | 冻结扣款：冻结 −X、平台结算户记对手方 | 同上 | txn |
| `POST /v1/ledger/freeze-refund` | 冻结退款：冻结 −X、可用 +X（与解冻同向、不同 txn type） | 同上 | txn |
| `POST /v1/ledger/reverse` | 冲正：把原 txn 全部用户分录取反生成新 txn | `{original_txn_id, idempotency_key?}` | txn（type=8） |

txn type：1 收入 2 支出 3 冻结 4 解冻 5 冻结扣款 6 冻结退款 7 退款 8 冲正 9–17 红包（见 §6、§7、§8）18 转账（见 §9）19 管理员资金调整（见 §12，`ref_type='admin_adjustment'`）20 充值入账 21 充值退款（见 §13，`ref_type='recharge'`）22 提现提交冻结 23 审核拒绝解冻 24 人工打款确认成功 25 失败后退款解冻（见 §14，`ref_type='withdraw'`）。
ledger 字段：`entry_id, txn_id, user_id, account_type（1 可用 2 冻结）, direction（1 借 2 贷）, amount_cents>0, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account, created_at`（无 updated_at）。

### 双重记账与并发规则（实现承诺）

- 每笔业务同一 DB 事务内完成，至少 2 条分录；同一 `txn_id` 下借贷分录带符号合计（贷+ / 借−）必须为 0，应用层校验失败整笔回滚。
- 余额增减方向按账户性质：用户户贷增借减；平台结算户借增贷减（资产性质）。因此收入时平台户余额增加，不会因 CHECK ≥ 0 被拒；对账派生余额对平台户取反（见 DATABASE.md）。
- 余额更新：`SELECT ... FOR UPDATE` 行锁（涉及账户按 user_id 排序加锁防死锁）+ `version = old` 条件更新（第二道防线），不用无锁先读后写。
- 幂等：`UNIQUE(user_id, idempotency_key)`（空键除外）+ 先查后插 + 唯一冲突回查，重复请求直接返回原交易，不重复记账；并发重复同样安全。
- Redis 锁：`fund:user:<user_id>`，`SET NX PX 10s + UUID + Lua compare-and-delete`，仅辅助协调；锁被持有回 429（不得静默放行，客户端应重试）；Redis 不可用时降级为纯 DB 保证并记日志，绝不以 Redis 计数为准出账。Kafka 不用。

## 3. 交易密码

| 方法与路径 | 说明 | 请求 | 返回/错误 |
|---|---|---|---|
| `GET /v1/tx-password/status` | 状态查询 | — | `{has_password, error_count, locked, locked_until}` |
| `POST /v1/tx-password/set` | 设置（仅未设时可设） | `{password, confirm_password}`（6 位纯数字、两次一致） | `{set:true}`；已设 → 400 |
| `POST /v1/tx-password/change` | 修改（须验旧密码） | `{old_password, new_password, confirm_password}` | `{changed:true}` |
| `POST /v1/tx-password/verify` | 验密（检查型接口：恒回 200，以标志为准） | `{password}` | `{verified, locked}`；未设 → 400 |
| `POST /v1/tx-password/recover/request` | 安全找回第 1 步：申请（须身份核验，返回 request_id） | `{real_name, account_last4}` | `{request_id}`；核验失败只回通用错误（不透露哪项不对、有无绑定）；待复核单重复申请返回同一 request_id |
| `POST /v1/admin/tx-password/recover/approve` | 安全找回第 2 步：双人复核（管理接口，须 `X-Admin-ID`） | `{request_id}` | 第 1 人 → `{stage:"awaiting_second_approval"}`；第 2 名不同管理员 → `{stage:"approved"}`；同一人两次 → 400 |
| `POST /v1/admin/tx-password/recover/reject` | 安全找回拒绝（管理接口，须 `X-Admin-ID`） | `{request_id}` | `{rejected:true}`；仅待复核单可拒绝 |
| `POST /v1/tx-password/recover/reset` | 安全找回第 3 步：用户本人重置（一次性、24 小时有效） | `{request_id, new_password, confirm_password}` | `{reset:true}`；未双人通过/已过期/已使用/非本人 → 400 |
| `GET /v1/tx-password/recover/requests` | 查询本人找回申请单（仅状态字段） | — | 数组 `{request_id, user_id, status, first_admin_id, second_admin_id, created_at, expires_at}`；不含实名、后 4 位等身份材料 |
| `GET /v1/admin/tx-password/recover/{id}` | 管理查询申请单（管理接口） | — | 同上单条；不存在 → 404 |

规则：bcrypt 保存（`x/crypto/bcrypt`，DefaultCost）；连续错 5 次锁定 30 分钟（`locked_until`），锁定期间正确密码亦拒绝；成功验密清零。找回身份核验：实名与已绑方式的后 4 位须匹配同一条正常方式（实时解密比对，核验材料不落库、不进审计、不返回）；申请单 24 小时有效，过期读取时惰性标记；管理员全程不接触、不设定用户新密码（Phase 1 的单管理员代设密码接口已删除）。

## 4. 提现账户

| 方法与路径 | 说明 | 请求 | 返回 |
|---|---|---|---|
| `GET /v1/withdraw-methods` | 查询本人正常方式列表 | — | 数组（卡号已脱敏） |
| `POST /v1/withdraw-methods` | 添加（先绑方式、再设密码：添加本身不要求交易密码，否则准入死锁） | `{type（1 支付宝 2 微信 3 银行卡）, real_name, account_no, bank_name（银行卡必填）, bank_branch?, is_default?}` | method（脱敏） |
| `PUT /v1/withdraw-methods/{id}` | 修改（须验交易密码；未设密码 → 403 提示先设密码） | `{real_name?, account_no?, bank_name?, bank_branch?, is_default?, tx_password}` | method（脱敏） |
| `DELETE /v1/withdraw-methods/{id}` | 解绑（软删 status=1，须验交易密码） | `{tx_password}` | `{deleted:true, was_last_method, new_default_method_id}`；删默认时自动把最早绑定的剩余方式升为默认；删唯一方式允许，`was_last_method=true`，准入随即变 `need_method`（Phase 9 提现提交时仍须重查准入与方式存在性） |
| `POST /v1/withdraw-methods/{id}/set-default` | 设默认（同一事务内清旧默认，须验交易密码） | `{tx_password}` | `{set_default:true}`；对已是默认的方式重复设置幂等成功 |

约束：同一用户仅一个正常默认，由事务清旧默认 + `partial unique index (user_id) WHERE is_default AND status=0` 双重保证；取消当前默认（`is_default=false`）时同样自动递补最早绑定的剩余方式；卡号 AES-GCM 加密存（密钥只来自 `fund.env`，服务缺密钥拒绝启动）；返回与审计详情一律脱敏（`**** + 后 4 位`），日志与审计中不得出现完整卡号、密码、哈希。新增/修改校验：银行卡号须为 12–19 位数字，支付宝/微信账号非空且 ≤128 字符；同用户同类型同账号重复绑定拒绝（解密比对，密文随机 nonce 无法靠唯一索引）。

## 5. 准入与对账

| 方法与路径 | 说明 | 返回 |
|---|---|---|
| `GET /v1/finance/status` | 统一准入状态（全部服务端判断） | `{has_withdrawal_method, has_transaction_password, finance_account_status（ok / need_method / need_password / frozen / closed）, wallet_status, method_count, has_default_method, next_step（bind_method / set_password / none / contact_support）, guidance}`；顺序：先绑方式、再设密码，两项齐 + 账户正常才为 ok；**业务方（红包/转账/提现提交）必须先查本状态：非 ok 时只把 guidance 返回客户端做引导，绝不擅自扣款、不改变业务单据状态、不判成功**（服务端守卫 `requireAdmission` 供后续业务模块调用） |
| `POST /v1/admin/reconcile` | 触发账务核对（管理接口） | `{reconcile_id, reconcile_date, channel:"internal", expected_cents, actual_cents, diff_cents, status（0 平账 1 差异待处理）, details[]（逐户余额/派生余额/差额）}` |
| `GET /v1/admin/reconcile/{id}` | 对账结果查询（管理接口） | reconcile 行 |

## 6. 好友红包（Phase 3，单聊红包）

资金模型：发包时发送方可用 → 冻结（与建包同一事务）；领取时发送方冻结 → 领取方可用；过期时发送方冻结 → 发送方可用（退款）。全部走双重记账、同一 DB 事务，流水 append-only。

| 方法与路径 | 说明 | 请求（关键字段） | 成功返回 / 错误 |
|---|---|---|---|
| `POST /v1/redpackets` | 发包（个数固定为 1，即好友单聊红包） | `{receiver_id, amount_cents>0, greeting?（≤200 字）, idempotency_key?, tx_password}`；守卫顺序：准入引导优先（非 ok 只返回引导、不扣款）→ 验交易密码 | packet；`ADMISSION_NOT_SATISFIED` 403 / `TX_PASSWORD_ERROR` 403 / 余额不足 `BUSINESS_ERROR` 400；同一幂等键重复发送返回原包（不重复扣款） |
| `POST /v1/redpackets/{id}/claim` | 领取（仅包的指定领取人可领） | —（领取人取自 IM token；**不重复验交易密码**，但须已完成准入） | `{claim, packet}`；未完成准入 → `ADMISSION_NOT_SATISFIED` 403（包状态不动、不扣款）；非领取人 → `FORBIDDEN` 403；已领 → `PACKET_ALREADY_CLAIMED` 409（同一领取人重领返回原领取记录）；已过期 → `PACKET_EXPIRED` 409（领取时先惰性退款、再报过期） |
| `GET /v1/redpackets/{id}` | 包详情（仅发送方/领取方可见，第三方 403） | — | `{packet, claim?}`；packet 含 `status`（0 待领取 1 已领取 2 已过期退款）、`expired`（计算字段：待领取且已过 expires_at；GET 不触发退款） |
| `GET /v1/redpackets/{id}/claims` | 领取记录（好友包最多 1 条；仅收发双方可见） | — | claim 数组 |
| `GET /v1/redpackets?role=sent\|received&limit&offset` | 发出/收到列表（倒序，limit 默认 50、上限 200） | — | packet 数组 |
| `POST /v1/admin/redpackets/expire` | 过期扫描（管理接口） | `{packet_id?}`（指定一包）或 `{}` / `{limit?}`（批量扫已过期的待领取包，默认 100、上限 500） | `{expired_ids}`（指定包时另含 packet）；未到期/已终态的包不改动、不重复退款 |

packet 字段：`packet_id, sender_id, receiver_id, amount_cents（分）, greeting, status, expired, created_at, expires_at（创建 + 24 小时）, claimed_at, refunded_at`。claim 字段：`claim_id, packet_id, claimer_id, amount_cents, claimed_at`。

txn type 新增：9 发包 10 领取 11 过期退款（`ref_type='redpacket', ref_id=packet_id`；领取 txn 的 user_id 为领取人，退款 txn 的 user_id 为发送人；领取幂等键 `rp-claim-<packet_id>`、退款幂等键 `rp-refund-<packet_id>`）。

**IM 消息载体（与客户端的契约，服务端 API 已按 Demo App/Example 层可直接调用方式设计，UI 落地在 Phase 10/11）：** 红包消息为 IM 自定义消息类型，消息体只放 `packetId`，**绝不放金额、绝不让客户端指定领取金额**；金额、状态、领取记录全部以服务端 `GET /v1/redpackets/{id}` 为准。客户端自定义消息类型常量名定为 `XIANLIAO_RED_PACKET`，数值建议 901，Phase 10 须先核对 SDK 自定义消息类型区间未被占用后再定稿。气泡渲染：客户端凭 packetId 调详情接口，按 status 渲染（待领取/已领取/已过期退款），不得用本地缓存金额冒充服务端状态。

**过期规则（统一规则）：** 创建后 24 小时未领取即过期；过期退款由管理端扫描（建议每分钟跑一次 `POST /v1/admin/redpackets/expire`）执行，或领取时惰性触发；详情查询不触发退款。退款通知：退款接口返回的 packet 即为通知载荷，实际 IM 业务通知（告知发送方“红包已过期退款”）由 chat 服务/客户端在 Phase 10/11 侧发送，资金服务不直接发 IM 消息。

**单包金额上下限：** 本阶段只要求 `amount_cents > 0`（≥ 1 分）且不超过发送方可用余额；不设硬编码单包上限——单包/单日上限属于 Phase 7 风控配置，届时以配置表落地，不写死在红包代码里。

## 7. 群普通红包（Phase 4，定额均分）

资金模型：发包时发送方全额可用 → 冻结（与建包 + 预拆分同一事务）；领取时按份金额发送方冻结 → 领取方可用；过期时剩余金额发送方冻结 → 发送方可用（退款）。全部走双重记账、同一 DB 事务，流水 append-only。

**均分规则（服务端预拆分，建包时落表 `group_redpacket_share`）：** base = total / count（整数除法），rem = total % count；前 rem 份各为 base+1，其余为 base；总和严格等于总金额，每份 ≥ 1 分（故建包要求 total_amount_cents ≥ total_count，否则 400 拒绝）。领取按 seq 升序取最早未领份，客户端绝不能指定领取金额。

| 方法与路径 | 说明 | 请求（关键字段） | 成功返回 / 错误 |
|---|---|---|---|
| `POST /v1/group-redpackets` | 群发包 | `{group_id, total_amount_cents>0, total_count（1..1000）, greeting?（≤200 字）, member_ids?（包内 receiver 集合，≤1000 人，发送方恒在集合内）, idempotency_key?, tx_password}`；守卫顺序：准入引导优先（非 ok 只返回引导、不扣款）→ 验交易密码 | group packet（含 `remaining_count` / `remaining_amount_cents` 实时剩余）；`ADMISSION_NOT_SATISFIED` 403 / `TX_PASSWORD_ERROR` 403 / 余额不足 `BUSINESS_ERROR` 400 / 总额 < 份数 `BAD_REQUEST` 400；同一幂等键重复发送返回原包（不重复扣款） |
| `POST /v1/group-redpackets/{id}/claim` | 群领取（每人限领一次） | —（领取人取自 IM token；**不重复验交易密码**，但须已完成准入） | `{claim, packet}`；未完成准入 → `ADMISSION_NOT_SATISFIED` 403（包状态不动、不扣款）；集合外的人 → `NOT_GROUP_MEMBER` 403；已领完 → `GROUP_PACKET_FULL` 409（同一领取人重领返回原领取记录）；已过期 → `GROUP_PACKET_EXPIRED` 409（领取时先退剩余、再报过期） |
| `GET /v1/group-redpackets/{id}` | 群包详情 | — | packet；含 `status`（0 待领取 1 已领完 2 已过期退款）、`claimed_count` / `claimed_amount_cents` / `remaining_count` / `remaining_amount_cents`（实时）、`expired`（计算字段：待领取且已过 expires_at；GET 不触发退款）；可见性：发送方恒可见，集合非空时仅集合内成员与已领取人可见，开放包（无集合）任意已鉴权用户可见 |
| `GET /v1/group-redpackets/{id}/claims` | 领取列表（按领取时间升序） | — | claim 数组（`claim_id, packet_id, claimer_id, share_id, amount_cents, claimed_at`）；可见性同详情 |
| `GET /v1/group-redpackets?role=sent\|received&limit&offset` | 发出/收到列表（倒序，limit 默认 50、上限 200） | — | sent：发送方发出的群包；received：领取人已领取过的群包 |
| `POST /v1/admin/group-redpackets/expire` | 过期扫描（管理接口） | `{packet_id?}`（指定一包）或 `{}` / `{limit?}`（批量扫已过期的待领取包，默认 100、上限 500） | `{expired_ids}`（指定包时另含 packet）；未到期/已终态的包不改动、不重复退款；部分领取后过期只退剩余金额 |

group packet 字段：`packet_id, sender_id, group_id, total_amount_cents（分）, total_count, claimed_count, claimed_amount_cents, remaining_count, remaining_amount_cents, greeting, status, expired, created_at, expires_at（创建 + 24 小时）, claimed_at（领完时）, refunded_at`。

txn type 新增：12 群发包 13 群领取 14 群过期退款（`ref_type='group_redpacket', ref_id=packet_id`；领取 txn 的 user_id 为领取人，退款 txn 的 user_id 为发送人；领取幂等键 `grp-claim-<packet_id>-<claimer_id>`、退款幂等键 `grp-refund-<packet_id>`）。

**防超发三层：** ① 建包预拆分（份数 = total_count，份行 `UNIQUE (packet_id, seq)`）；② 领取事务内包行 `FOR UPDATE` + 进度 CAS（`claimed_count` 原值比对）；③ 领取行 `UNIQUE (packet_id, claimer_id)`（每人限领一次）。并发领取由包行锁串行化，恰好 total_count 条领取行、金额总和等于已领总额。

**领取人归属边界（诚实说明）：** 资金服务侧只校验“包内 receiver 集合”——发包时若传 member_ids，领取人必须在集合内；若不传，则为开放包，IM 群成员关系核验由 Phase 10/11 的 chat/App 层在发包前调 OpenIM 群接口（`get_group_member_list`）核验，资金服务不直读 IM 库表。发送方可领自己的群包（与微信群包一致；同人时只做一次账户更新：可用 += 份额、冻结 -= 份额）。

**IM 消息载体（与好友红包同一契约）：** 红包消息为 IM 自定义消息类型，消息体只放 `packetId`，**绝不放金额、绝不让客户端指定领取金额**；金额、状态、领取记录全部以服务端详情接口为准；UI 落地在 Phase 10/11（群聊气泡渲染 + 领取页 + 领取列表 + 已领完/已过期态）。

**过期规则（统一规则）：** 创建后 24 小时未领完即过期；剩余退款由管理端扫描（建议每分钟跑一次 `POST /v1/admin/group-redpackets/expire`）执行，或领取时惰性触发；详情查询不触发退款。退款通知由 chat 服务/客户端在 Phase 10/11 侧发送，资金服务不直接发 IM 消息。

## 8. 群拼手气红包（Phase 5，随机预拆分）

资金模型：与群普通红包同模型、表分离——发包时发送方全额可用 → 冻结（与建包 + 预拆分同一事务）；领取时按预拆分份金额发送方冻结 → 领取方可用；过期时剩余金额发送方冻结 → 发送方可用（退款）。全部走双重记账、同一 DB 事务，流水 append-only。

**随机预拆分规则（服务端建包时一次算定并落表 `group_lucky_redpacket_share`，领取时绝不现算现分；份金额只存在服务端）：** 二倍均值法（`splitLucky`，随机源 `crypto/rand`，非 `math/rand`，客户端不可预测或复现）：对前 `count-1` 份，设当时剩余金额 `R`、剩余份数 `k`（含本份），本份在 `[1, min(2*floor(R/k)-1, R-k+1)]` 内均匀随机抽取；最后一份取剩余全部。硬约束：每份 ≥ 1 分；总和严格等于总金额；无负数；每份 ≤ 当时剩余金额且为其余份各留 ≥ 1 分（故建包要求 `total_amount_cents ≥ total_count`，否则 400 拒绝）。领取按 `seq` 升序取最早未领份，客户端绝不能指定领取金额。份行建包后金额不再变动，事后按份行求和即可审计（预拆分结果建包时即确定）。

**手气最佳规则：** 金额最大的份；金额并列时最早领取者为手气最佳（领取时间升序，`claim_id` 升序决胜）。详情接口返回 `best_claimer_id` / `best_amount_cents`（无领取时均为 `null`）；排名接口按金额降序、领取时间升序排列，`rank` 为该顺序下的序号（同金额先领者排前，与手气最佳规则一致）。

| 方法与路径 | 说明 | 请求（关键字段） | 成功返回 / 错误 |
|---|---|---|---|
| `POST /v1/group-lucky-redpackets` | 群拼手气发包 | `{group_id, total_amount_cents>0, total_count（1..1000）, greeting?（≤200 字）, member_ids?（包内 receiver 集合，≤1000 人，发送方恒在集合内）, idempotency_key?, tx_password}`；守卫顺序：准入引导优先（非 ok 只返回引导、不扣款）→ 验交易密码 | lucky packet（含 `remaining_count` / `remaining_amount_cents` 实时剩余 + 手气最佳）；`ADMISSION_NOT_SATISFIED` 403 / `TX_PASSWORD_ERROR` 403 / 余额不足 `BUSINESS_ERROR` 400 / 总额 < 份数 `BAD_REQUEST` 400；同一幂等键重复发送返回原包（不重复扣款） |
| `POST /v1/group-lucky-redpackets/{id}/claim` | 群拼手气领取（每人限领一次） | —（领取人取自 IM token；**不重复验交易密码**，但须已完成准入） | `{claim, packet}`；未完成准入 → `ADMISSION_NOT_SATISFIED` 403（包状态不动、不扣款）；集合外的人 → `NOT_GROUP_MEMBER` 403；已领完 → `LUCKY_PACKET_FULL` 409（同一领取人重领返回原领取记录）；已过期 → `LUCKY_PACKET_EXPIRED` 409（领取时先退剩余、再报过期） |
| `GET /v1/group-lucky-redpackets/{id}` | 群拼手气包详情 | — | packet；含 `status`（0 待领取 1 已领完 2 已过期退款）、`claimed_count` / `claimed_amount_cents` / `remaining_count` / `remaining_amount_cents`（实时）、`best_claimer_id` / `best_amount_cents`（手气最佳）、`expired`（计算字段：待领取且已过 expires_at；GET 不触发退款）；可见性：发送方恒可见，集合非空时仅集合内成员与已领取人可见，开放包（无集合）任意已鉴权用户可见 |
| `GET /v1/group-lucky-redpackets/{id}/claims` | 领取列表（按领取时间升序） | — | claim 数组（`claim_id, packet_id, claimer_id, share_id, amount_cents, claimed_at`）；可见性同详情 |
| `GET /v1/group-lucky-redpackets/{id}/ranking` | 手气排名（金额降序、领取时间升序） | — | rank 数组（`rank, claim_id, packet_id, claimer_id, share_id, amount_cents, claimed_at`）；可见性同详情 |
| `GET /v1/group-lucky-redpackets?role=sent\|received&limit&offset` | 发出/收到列表（倒序，limit 默认 50、上限 200） | — | sent：发送方发出的拼手气包；received：领取人已领取过的拼手气包 |
| `POST /v1/admin/group-lucky-redpackets/expire` | 过期扫描（管理接口） | `{packet_id?}`（指定一包）或 `{}` / `{limit?}`（批量扫已过期的待领取包，默认 100、上限 500） | `{expired_ids}`（指定包时另含 packet）；未到期/已终态的包不改动、不重复退款；部分领取后过期只退剩余金额 |

lucky packet 字段：`packet_id, sender_id, group_id, total_amount_cents（分）, total_count, claimed_count, claimed_amount_cents, remaining_count, remaining_amount_cents, best_claimer_id, best_amount_cents, greeting, status, expired, created_at, expires_at（创建 + 24 小时）, claimed_at（领完时）, refunded_at`。

txn type 新增：15 群拼手气发包 16 群拼手气领取 17 群拼手气过期退款（`ref_type='group_lucky_redpacket', ref_id=packet_id`；领取 txn 的 user_id 为领取人，退款 txn 的 user_id 为发送人；领取幂等键 `lky-claim-<packet_id>-<claimer_id>`、退款幂等键 `lky-refund-<packet_id>`）。

**防超发三层（与群普通红包一致）：** ① 建包预拆分（份数 = total_count，份行 `UNIQUE (packet_id, seq)`）；② 领取事务内包行 `FOR UPDATE` + 进度 CAS（`claimed_count` 原值比对）；③ 领取行 `UNIQUE (packet_id, claimer_id)`（每人限领一次）。并发领取由包行锁串行化，恰好 total_count 条领取行、金额总和等于已领总额。

**领取人归属边界（诚实说明，与 Phase 4 一致）：** 资金服务侧只校验“包内 receiver 集合”——发包时若传 member_ids，领取人必须在集合内；若不传，则为开放包，IM 群成员关系核验由 Phase 10/11 的 chat/App 层在发包前调 OpenIM 群接口（`get_group_member_list`）核验，资金服务不直读 IM 库表。发送方可领自己的拼手气包（与微信群包一致；同人时只做一次账户更新：可用 += 份额、冻结 -= 份额）。

**IM 消息载体（与好友/群普通红包同一契约）：** 红包消息为 IM 自定义消息类型，消息体只放 `packetId`，**绝不放金额、绝不让客户端指定领取金额**；金额、状态、领取记录、手气最佳全部以服务端详情/排名接口为准；UI 落地在 Phase 10/11（群聊气泡渲染 + 领取页 + 领取列表 + 手气排名 + 已领完/已过期态）。

**过期规则（统一规则）：** 创建后 24 小时未领完即过期；剩余退款由管理端扫描（建议每分钟跑一次 `POST /v1/admin/group-lucky-redpackets/expire`）执行，或领取时惰性触发；详情查询不触发退款。退款通知由 chat 服务/客户端在 Phase 10/11 侧发送，资金服务不直接发 IM 消息。

## 9. 转账（Phase 6，即时到账）

资金模型：同一 DB 事务、同一 `txn_id` 下两条分录（转出方可用 DR / 转入方可用 CR，金额相等，带符号合计为 0）；无冻结行、无过期、无领取环节。全部走双重记账、同一 DB 事务，流水 append-only。

**模式定稿（只用一套资金状态机，不混用两种模式）：** 转账为即时到账（instant），与红包即时结算模型一致；**待确认模式不做**——待确认模式须另起冻结 + 过期 + 领取/退款状态机，会使同一资金服务内并存两套状态机与两套争议路径，Phase 6 不引入该复杂度；未来如需待确认转账，须另立阶段并扩展订单状态，不得在本阶段代码里留半截待确认分支。

| 方法与路径 | 说明 | 请求（关键字段） | 成功返回 / 错误 |
|---|---|---|---|
| `POST /v1/transfers` | 发起转账（即时到账） | `{receiver_id, amount_cents>0, remark?（≤200 字）, idempotency_key?, tx_password}`；守卫顺序：准入引导优先（非 ok 只返回引导、不扣款）→ 验交易密码 | transfer order；`ADMISSION_NOT_SATISFIED` 403 / `TX_PASSWORD_ERROR` 403 / 余额不足 `INSUFFICIENT_BALANCE` 400 / 自转 `TRANSFER_SELF` 400 / 收款方冻结或注销 `RECEIVER_WALLET_ABNORMAL` 400 / 平台结算户为收款方 `FORBIDDEN` 403；同一幂等键重复提交返回原订单（不重复扣款） |
| `GET /v1/transfers/{id}` | 转账订单详情（仅转出方/收款方可见，第三方 403） | — | order；不存在 → `TRANSFER_NOT_FOUND` 404 |
| `GET /v1/transfers?role=sent\|received&limit&offset` | 发出/收到列表（倒序，limit 默认 50、上限 200） | — | order 数组（失败审计行同样在列，`status` 字段可区分） |
| `GET /v1/admin/transfers/{id}` | 管理端全链路查询（管理接口） | — | `{order, txn, entries}`：成功订单的 entries 恰好 2 条（转出 DR / 转入 CR）；失败审计行无 txn、无分录 |
| `GET /v1/admin/transfers?sender_id&receiver_id&limit&offset` | 管理端订单列表（管理接口，sender/receiver 可选过滤） | — | order 数组 |

transfer order 字段：`order_id, sender_id, receiver_id, amount_cents（分）, remark, status, failure_code（仅失败行）, txn_id（仅成功行）, idempotency_key, created_at, updated_at`。

**订单状态（成功 / 失败）：** `1 成功`（唯一由成功转账写入的状态；订单行与账务 txn、分录在同一 DB 事务内落库）；`2 失败`（仅结算失败审计行：余额不足 / 收款方钱包冻结 / 注销时，HTTP 层在主事务回滚后另起短事务写入一行 `status=2`、`failure_code` 非空、`txn_id` 为空的审计行；**失败行不落账**——无 `wallet_txn`、无分录、双方余额不动；失败行不带幂等键，同一幂等键事后补足余额重发仍可成功，成功幂等只匹配 `status=1` 的行）。下列失败不产生订单行（请求本身无效，非一次结算尝试）：参数错误（金额 ≤ 0、附言超长、收款方非法）、自转、平台结算户为收款方、鉴权失败、转出方准入未完成、交易密码错误——在任何资金变动发生前即被拒绝，错误响应本身就是完整记录。

txn type 新增：18 转账（`ref_type='transfer', ref_id=order_id`；txn 的 `user_id` 为转出方；txn 幂等键为内部键 `trf-<order_id>`，客户端幂等键只存在订单行，避免跨接口键命名空间碰撞）。**冲正定稿（二选一结果）：冲正复用已有 type 8，不另立 type 19**——异常纠错只走已有冲正（`POST /v1/ledger/reverse`，全部分录取反生成新 txn）；接收方余额不足时冲正整体失败、原转账不动。冲正不改写原订单行（订单为历史事实），冲正链路凭原 `txn_id` 在管理端详情接口查到。管理端可查转账订单与全链路流水，但管理端只查不改：**绝不直接改余额、不 UPDATE 订单行改状态**。

**门禁与准入（与红包领取规则的区别，如实写清）：** 转出方须已完成准入（`financeStatus == ok`，否则只返回引导、不扣款）+ 验交易密码；收款方为被动收款：钱包不存在时在同一事务内自动建钱包即可入账，**不设准入关**——转账由转出方发起，收款方准入在其下次主动使用资金时 enforcement。区别于红包领取须准入：红包领取是领取方的主动资金操作（领取即确认收款并可随即使用），故领取时查准入；转账入账是被动记账，不查收款方准入。收款方钱包冻结/注销时整笔拒绝（不落账）。转出方与收款方 `user_id` 取自 IM token：转出方即 token 用户；请求体 `user_id` 不一致则 403（`decodeAndCheckUser` 统一校验）。

**并发与幂等：** 涉及账户按 `user_id` 排序加锁防死锁 + `version` 条件更新（第二道防线）+ 余额校验；转出方 Redis 辅助锁（`fund:user:<sender>`）仅协调，锁被持有回 429。成功订单 `UNIQUE (sender_id, idempotency_key)`（空键除外）；同一幂等键重复提交返回原订单（先查后插 + 唯一冲突回查），并发重复同样安全。

**资金流水中的转账条目：** 转账分录出现在双方各自的 `GET /v1/wallet/ledger` 中（`opposite_user_id` 为对方，`txn_id` 相同），管理端详情接口一次返回该 `txn_id` 的全部分录，即为全链路流水。

**IM 消息载体（与客户端的契约，UI 落地在 Phase 10/11）：** 转账不在 IM 消息体内放金额；客户端凭订单号调详情接口展示状态与金额，金额、状态全部以服务端详情接口为准。单笔/单日上限不硬编码，属 Phase 7 风控配置。

## 10. 错误码

| HTTP | code | 含义 |
|---|---|---|
| 400 | `AMOUNT_LIMIT_EXCEEDED` | 金额超过 Phase 7 风控单笔上限（转账发起路径；超限不产生订单行） |
| 400 | `BAD_REQUEST` / `BUSINESS_ERROR` | 参数错误 / 业务拒绝（余额不足、冻结不足、密码错误、方式不存在等），不落账 |
| 401 | `UNAUTHORIZED` | 缺失/无效的 IM token、缺失或错误的管理密钥、双人复核缺 `X-Admin-ID` |
| 403 | `FORBIDDEN` / `TX_PASSWORD_ERROR` / `ADMISSION_NOT_SATISFIED` | 越权（user_id 不一致、平台户操作、非红包收发方）/ 交易密码门禁失败 / 资金准入未完成（只引导、不扣款） |
| 409 | `PACKET_ALREADY_CLAIMED` / `PACKET_EXPIRED` | 红包已被领取 / 红包已过期（领取时先退款、再报过期） |
| 409 | `GROUP_PACKET_FULL` / `GROUP_PACKET_EXPIRED` | 群红包已领完 / 群红包已过期（领取时先退剩余、再报过期） |
| 409 | `LUCKY_PACKET_FULL` / `LUCKY_PACKET_EXPIRED` | 群拼手气红包已领完 / 群拼手气红包已过期（领取时先退剩余、再报过期） |
| 403 | `NOT_GROUP_MEMBER` | 领取人不在群红包/群拼手气红包 receiver 集合内（仅集合非空的包） |
| 503 | `SERVICE_UNAVAILABLE` | IM token 校验服务不可达（绝不 fail-open） |
| 404 | `NOT_FOUND` / `WALLET_NOT_FOUND` | 资源不存在 / 钱包未创建 |
| 429 | `SYSTEM_BUSY` | Redis 辅助锁被持有，请重试（不得静默放行） |
| 500 | `INTERNAL` | 服务端异常（事务已回滚） |
| 400 | `CHANNEL_NOT_AVAILABLE` | 充值创建指定了真实渠道（alipay/wechat），商户未签约前只接受 mock |
| 400 | `CALLBACK_AMOUNT_MISMATCH` | 回调金额 ≠ 订单金额，拒绝入账并记审计（订单不动、资金不动） |
| 400 | `CHANNEL_ORDER_MISMATCH` | 回调渠道单号与订单已有单号不一致，拒绝（防串单） |
| 401 | `SIGNATURE_INVALID` | 回调验签失败，拒绝入账（不透露订单是否存在） |
| 404 | `RECHARGE_NOT_FOUND` | 充值订单不存在（用户详情查他人订单同样 404，不透露归属） |
| 404 | `WITHDRAW_NOT_FOUND` / `WITHDRAW_METHOD_NOT_FOUND` | 提现订单不存在（他人订单同样 404，不透露归属）/ 提现方式不存在、非本人或已解绑（三种情况同一码，不透露他人方式存在性） |
| 400 | `FEE_EXCEEDS_AMOUNT` | 提现手续费 ≥ 提现金额，实际到账金额 ≤ 0，拒绝提交 |
| 409 | `INVALID_TRANSITION` | 提现订单状态非法流转（合法流转见 §14.1；Phase 7 已有 approve/reject 入口保持原 400 口径） |
| 409 | `INVALID_TRANSITION` | 充值订单状态非法流转（合法流转见 §13） |
| 409 | `REFUND_INSUFFICIENT` | 充值退款时用户余额不足，整笔失败、订单与余额不动、记审计 |
| 409 | `CHANNEL_ORDER_CONFLICT` | 渠道单号已被另一订单占用（唯一索引第二道防线） |
| 501 | `CHANNEL_NOT_CONFIGURED` | 真实渠道回调验签器未配置（商户未签约），绝不入账 |

## 11. 阶段边界（Phase 6 不提供）

真实充值、真实提现、数字货币、区块链、真实支付渠道；`wallet_order` 的充值/提现 7 态仅为结构预留，本阶段不产生真实充值提现订单。ledger primitives 仍为业务模块间口径：调用方须持用户本人 IM token（网关透传），资金服务以 token 解析结果为准。交易密码门禁已统一为 `requireTxPassword`（换绑/解绑/设默认/改密/发包/转账均走同一门禁）；红包与转账提交时的准入守卫为 `requireAdmission`（见 §5），红包领取时不重复验密但须准入 ok（见 §6、§7、§8），转账收款方为被动入账不设准入关（见 §9）。

## 12. 管理端资金中心（Phase 7）

> 本阶段交付物是管理端资金中心 API + 本节机器可读的接口规约（供未来前端接线），不是 UI。
> 前置核查结论（只读核查，不伪造）：香港三只有 admin-front dist 镜像（`openim-admin-front:xianliao-20261102`，
> 基于上游镜像 `openim/openim-admin-front:release-v1.8.4-patch.2` + `/opt/openim-admin-patch/html` 覆盖层），
> 无源码；上游组织 `openimsdk` 的公开仓库列表中无 admin-front 源码仓（首批 100 个仓库已核对，无此仓名），
> Docker Hub 仅提供镜像（`openim/openim-admin-front`，最近更新 2026-01-18）；GitHub 全局检索唯一同名仓为
> 第三方个人仓 `50mkw/openim-admin-front`（ClawCloud nginx 代理用途，来源未经证实，**不采作上游**）。
> 因此 admin-front 源码缺口如实记为缺口：本阶段 API 交付不阻塞，前端 UI 落地须等源码快照补齐，
> 绝不凭空编一个前端出来。本节字段规约即为未来前端接线契约。

全部管理接口走 `X-Admin-Secret` 鉴权（错误/缺失 → 401）；双人复核类接口另须 `X-Admin-ID`（缺失 → 401）。
管理端绝不直接改余额、绝不看密码或哈希、不得修改用户提现绑定信息（只查）。

### 12.1 用户钱包管理（只查）

| 方法与路径 | 说明 | 成功返回 / 错误 |
|---|---|---|
| `GET /v1/admin/wallets/{user_id}` | 按 user_id 查钱包（余额/冻结/状态） | wallet_account；不存在 → `NOT_FOUND` 404 |
| `GET /v1/admin/wallets/{user_id}/ledger?limit&offset` | 查资金流水（分页，倒序，limit 默认 50、上限 200） | ledger entry 数组（无卡号类敏感字段） |
| `GET /v1/admin/wallets/{user_id}/finance-status` | 查资金准入状态 | 同 §5：`{has_withdrawal_method, has_transaction_password, finance_account_status, wallet_status, method_count, has_default_method, next_step, guidance}` |

### 12.2 提现账户管理（只查；修改走用户端接口）

| 方法与路径 | 说明 | 成功返回 / 错误 |
|---|---|---|
| `GET /v1/admin/withdraw-methods?user_id=` | 按用户查绑定列表 | method 数组，卡号默认脱敏（`**** + 后 4 位`），绝不返回卡号明文或解密值 |
| `GET /v1/admin/withdraw-methods/default?user_id=` | 查默认账户 | method（脱敏）；无默认 → `NOT_FOUND` 404 |

管理端对提现账户无 PUT/DELETE/POST 入口（对应方法返回 405），修改、解绑、设默认一律走用户端接口（须验交易密码）。

### 12.3 交易密码管理（禁看密码/哈希；找回复核复用 Phase 2 双人复核）

| 方法与路径 | 说明 | 成功返回 / 错误 |
|---|---|---|
| `GET /v1/admin/tx-password/status?user_id=` | 按用户查密码状态 | `{user_id, has_password, error_count, locked, locked_until, last_changed_at}`；**响应中无 `password_hash` 字段、无 bcrypt 哈希值**（查询 SQL 不 SELECT 该列，Test84 断言响应体不含 `password_hash` 与 `$2a$`） |
| `GET /v1/admin/tx-password/recover?user_id&status` | 找回申请单列表（status 可选过滤） | RecoveryRequest 数组（仅状态字段，不含实名、后 4 位等身份材料） |
| `GET /v1/admin/tx-password/recover/{id}` | 找回申请单详情（Phase 2 已有） | 同上单条；不存在 → 404 |
| `POST /v1/admin/tx-password/recover/approve` / `reject` | 找回双人复核 / 拒绝（Phase 2 已有，不另起单人代设口子） | 见 §3；管理员全程不接触、不设定用户新密码 |
| `POST /v1/admin/tx-password/reset-request` | 管理员发起重置（迁移 0015；管理接口，须 `X-Admin-Secret` + `X-Admin-ID`） | `{user_id, reason}`（理由必填）；建待复核单（status=0，**发起不等于通过**），仍须两名不同管理员 approve，用户随后用 §3 的 reset 自设新密码；同一用户已有未过期待复核单时返回原单；无资金记录用户 → 404；响应含 `initiated_by_admin_id` 与 `reason`，无密码无哈希；用户侧 `GET /v1/tx-password/recover/requests` 按 user_id 查同表，管理员发起的记录天然可见（用户侧不返回 reason）。面板位置：IM 用户管理每行“重置交易密码”（弹框显示密码状态 + 理由必填 + 二次确认），权限 `xianliao:fund:txreset`（独立于 `fund:edit`/`im:edit`，默认只给超管），代理层 `@PreAuthorize` 与按钮 gating 双重卡 |

### 12.4 管理员资金调整单（本阶段新增核心，迁移 0007）

任何管理员余额调整必须先建独立调整单。**全部调整单统一双人复核**（不设单人直通阈值，避免单人调账口子）：
创建人不受限，但执行仍需两名不同复核人——创建人可担任第一复核人，不可一人完成两步。
执行时走统一账务服务（txn type 19，`ref_type='admin_adjustment'`，双重记账、同一事务、审计日志）；
执行后单据状态与账务一一对应；调整单只查不改，执行后不可撤销、不可拒绝——纠错另建反向调整单并注明原单号。

| 方法与路径 | 说明 | 请求（关键字段） | 成功返回 / 错误 |
|---|---|---|---|
| `POST /v1/admin/adjustments` | 创建调整单（须 `X-Admin-ID`，记为创建人） | `{user_id, direction（1 增加可用余额 2 减少可用余额）, amount_cents>0, reason（1–500 字）, original_adjustment_id?}`；反向单须原单已执行且方向相反，否则 400；平台结算户 400；钱包不存在 400 | adjustment（status 0 待复核） |
| `GET /v1/admin/adjustments?user_id&status&limit&offset` | 调整单列表 | status 可选过滤 | adjustment 数组 |
| `GET /v1/admin/adjustments/{id}` | 调整单详情 | — | adjustment |
| `POST /v1/admin/adjustments/{id}/approve` | 双人复核（须 `X-Admin-ID`） | — | 第 1 人 → `{stage:"awaiting_second_approval"}`（余额不动）；第 2 名不同管理员 → `{stage:"executed"}`（余额变动 + `executed_txn_id`）；同一人两次 → 400；非待复核单 → 400；减额超余额时执行事务整体回滚，第二复核人不落单，单据仍为待第二复核（可重试或拒绝） |
| `POST /v1/admin/adjustments/{id}/reject` | 拒绝关闭（须 `X-Admin-ID`） | — | `{rejected:true}`；仅待复核单可拒绝 |

adjustment 字段：`adjustment_id, user_id, direction, amount_cents（分）, reason, status（0 待复核 1 已执行 2 已拒绝）, created_by_admin_id, first_admin_id, second_admin_id, rejected_by_admin_id, original_adjustment_id, executed_txn_id, executed_at, created_at, updated_at`。
资金方向：增加 = 用户可用 CR / 平台结算 DR（与收入同向）；减少 = 用户可用 DR / 平台结算 CR（与支出同向）；
平台户余额恒等于全部用户余额之和（收入/支出/调整同向维护该不变量），减额时平台侧不足同样整笔回滚。
敏感操作审计：创建/第一复核/执行/拒绝各记一条 `wallet_audit_log`（`admin_adjustment_create` / `admin_adjustment_approve` / `admin_adjustment_execute` / `admin_adjustment_reject`，detail 仅含单号/方向/金额/txn 号，已脱敏）。

### 12.5 提现订单审核脚手架（Phase 9 预留，不提前做真实打款）

基于 `wallet_order` 现有结构 + 迁移 0007 最小字段扩展（`review_first_admin_id` / `review_second_admin_id` /
`rejected_by_admin_id` / `review_note` / `reviewed_at`）。提现 7 态：0 待审核 1 已通过 2 已拒绝 3 处理中 4 成功 5 失败 6 已退款。
本阶段管理端仅允许 0 → 1（双人复核）与 0 → 2（任一管理员拒绝）；3/4/5/6 为 Phase 9 流转，本阶段不提供写入口。
**审核通过只改订单状态，不产生任何打款动作**（打款在 Phase 9；Test90 断言审核前后余额与 txn 数均不变）。

| 方法与路径 | 说明 | 成功返回 / 错误 |
|---|---|---|
| `GET /v1/admin/withdraw-orders?user_id&status&limit&offset` | 提现订单列表（仅 `order_type=2`） | order 数组 |
| `GET /v1/admin/withdraw-orders/{id}` | 提现订单详情 | order；不存在 → 404；非提现单 → 400 |
| `POST /v1/admin/withdraw-orders/{id}/approve` | 双人复核（须 `X-Admin-ID`） | 第 1 人 → `{stage:"awaiting_second_approval"}`（状态仍 0）；第 2 名不同管理员 → `{stage:"approved"}`（状态置 1，`reviewed_at` 落库）；同一人两次 / 非待审核单 → 400 |
| `POST /v1/admin/withdraw-orders/{id}/reject` | 拒绝（须 `X-Admin-ID`，任一管理员可拒） | `{rejected:true}`（状态置 2，`review_note` ≤500 字）；非待审核单 → 400 |

order 字段（提现视图）：`order_id, user_id, order_type（恒 2）, amount_cents, fee_cents, actual_cents, status（7 态）, withdraw_method_id, channel_order_no, review_first_admin_id, review_second_admin_id, rejected_by_admin_id, review_note, reviewed_at, created_at, updated_at`。

### 12.6 风控配置基础

| 方法与路径 | 说明 | 成功返回 / 错误 |
|---|---|---|
| `GET /v1/admin/risk-limits` | 读限额配置 | `{single_max_cents（null = 不限额）, daily_max_cents（null = 不限额）, updated_by, updated_at}`；默认不限额 |
| `PUT /v1/admin/risk-limits` | 写限额配置（按字段部分更新；缺省字段保持原值，显式 null 改为不限额；变更记审计 `admin_risk_limit_update`） | 同上；负数 → 400 |

接入范围（诚实写清）：本阶段**只在转账发起路径接入单笔上限校验**（`POST /v1/transfers`：超限 → 400 `AMOUNT_LIMIT_EXCEEDED`，
在任何资金变动前拒绝，不产生订单行，与参数错误同级；等于上限放行）。单日上限本阶段只存配置、不接入执行；
红包路径（好友/群普通/群拼手气）的限额接入留 Phase 8/9 统一做——Test91 显式断言这两项本阶段行为（超单日上限转账仍成功、
超单笔上限红包仍可发出），避免未来误读为“限额已全面生效”。

### 12.7 审计日志管理查询（只读）

| 方法与路径 | 说明 |
|---|---|
| `GET /v1/admin/audit-logs?user_id&action&limit&offset` | 审计日志倒序查询（detail 写入时已脱敏，不含卡号明文、密码、哈希） |

### 12.8 未来前端接线规约（admin-front 源码补齐后按此接线）

- 管理端资金中心页面 = 本节 12.1–12.7 的接口集合；前端只做展示与复核操作入口，不直接调用户端接口冒充管理员，
  不在前端拼余额、不缓存余额冒充服务端状态。
- 调整单页：创建表单（用户、方向、金额分、原因、原单号可选）→ 列表（状态过滤）→ 详情（含 `executed_txn_id` 链路）→
  复核按钮（第一复核与第二复核须不同管理员登录态；执行后按钮置灰，纠错走新建反向单）。
- 提现审核页：列表（7 态过滤）→ 详情 → 通过（双人）/拒绝（附注）；通过后页面必须明示“仅审核通过，未打款”，
  打款入口在 Phase 9 另行接线，不得在本页拼打款按钮。
- 限额配置页：单笔/单日上限输入（空 = 不限额），保存后提示“单日上限当前仅保存、未接入执行；红包限额未接入”。
- 阻塞项：admin-front 源码快照未补齐前，上述页面不落地、不伪造 UI；API 先行交付，UI 等源码。

## 13. 充值（Phase 8，人民币、回调入账）

资金模型：充值订单复用 `wallet_order`（`order_type=1`，迁移 0008 加充值专属字段，见 DATABASE.md）；
入账只走可信支付回调——客户端创建订单、页面操作、交易密码均不能直接增加余额
（计划 §9.5：不得用虚假支付成功页面增加余额）。入账分录（txn type 20，`ref_type='recharge'`）：
用户可用 CR / 平台结算户 DR，同一事务；退款分录（txn type 21，仅 mock 渠道演练）：
用户可用 DR / 平台结算户 CR，同一事务。

### 13.1 订单与状态机

| 方法与路径 | 说明 | 请求（关键字段） | 成功返回 / 错误 |
|---|---|---|---|
| `POST /v1/recharges` | 创建充值订单（建单不产生资金变动；钱包不存在时建单事务内自动建钱包） | `{amount_cents>0, channel?（默认 mock）, idempotency_key?}`；固定档位与自定义金额走同一接口（档位只是约定值，不做白名单硬限制） | order（status 0 待支付，fee 0，actual = amount）；金额 ≤ 0 → 400；channel=alipay/wechat → 400 `CHANNEL_NOT_AVAILABLE`（商户未签约前只接受 mock）；同一幂等键重复创建返回原单 |
| `GET /v1/recharges/{id}` | 订单详情（仅本人可见，他人 404） | — | order |
| `GET /v1/recharges?status&channel&limit&offset` | 本人充值记录（倒序，limit 默认 50、上限 200；status 0–3，非法值 400） | — | order 数组 |
| `GET /v1/admin/recharges?user_id&status&channel&limit&offset` | 管理端订单列表（管理接口；按 `order_type=1` 过滤，提现单不在列） | — | order 数组 |
| `GET /v1/admin/recharges/{id}` | 管理端订单详情（管理接口） | — | order；不存在 → 404；非充值单 → 400 |

order 字段：`order_id, user_id, order_type（恒 1）, amount_cents, fee_cents（现为 0，渠道手续费政策待签约后定）, actual_cents（= amount_cents）, status, channel, channel_order_no, paid_txn_id, refund_txn_id, failure_reason, client_idempotency_key, created_at, updated_at`。

**状态机（仅 `order_type=1` 的行；与提现 7 态共用 `status` 列但命名空间按 `order_type` 区分，查询须恒带 `order_type` 过滤）：**
`0 待支付 → 1 已支付 → 3 已退款`；`0 待支付 → 2 已失败`。合法流转只有这三条；
其余流转一律 409 `INVALID_TRANSITION`。同一目标状态的重复回调视为幂等重放（返回原订单，不重复入账）：
已支付 + 支付回调、已失败 + 失败回调、已退款 + 退款回调均为重放。

**固定金额档位（分，CNY，客户端展示用推荐值）：** `100, 500, 1000, 5000, 10000, 30000, 50000, 100000`
（即 ¥1 / ¥5 / ¥10 / ¥50 / ¥100 / ¥300 / ¥500 / ¥1000）；服务端接受任意 `>0` 整数分，
档位金额与自定义金额（如 ¥123.45 = 12345 分）走同一创建接口。创建不设准入/交易密码门禁：
建单本身不产生资金变动，真正的资金变动只发生在渠道验签通过的回调事务里。

### 13.2 回调与验签规约（核心）

| 方法与路径 | 说明 | 请求 | 成功返回 / 错误 |
|---|---|---|---|
| `POST /v1/payments/callback/mock` | mock 渠道回调入口（**仅 `FUND_MOCK_PAYMENT=1` 时注册**；开关关闭时该路径落入 fallback 回 404，见 Test106） | 见回调体规约 | order；验签失败 → 401 `SIGNATURE_INVALID`（不透露订单是否存在）；金额不一致 → 400 `CALLBACK_AMOUNT_MISMATCH` 并记审计；非法流转 → 409；退款余额不足 → 409 `REFUND_INSUFFICIENT` |
| `POST /v1/payments/callback/alipay` / `.../wechat` | 真实渠道回调入口（验签骨架未配置，恒回 501，绝不入账） | — | 501 `CHANNEL_NOT_CONFIGURED` |
| `POST /v1/payments/callback/<其他>` | 未知渠道 | — | 404 |

**回调体（mock 渠道，JSON）：**
`{order_id, channel_order_no, amount_cents, status（paid | failed | refunded）, signature, failure_reason?（失败回调可选，≤500 字）}`。
回调接口**无用户鉴权**——签名验签是唯一门禁：验签失败一律拒绝入账，订单与资金均不动，签名原文与密钥绝不进日志。

**签名规约（mock 渠道，HMAC）：** canonical 载荷 = `order_id|channel_order_no|amount_cents|status`
（字段顺序固定，杜绝 JSON 字段重排导致验签不一致；金额与状态均参与签名，任一字段被篡改即验签失败）；
签名 = hex(HMAC-SHA256(`FUND_MOCK_PAY_CALLBACK_SECRET`, canonical 载荷))，服务端恒时比较。
验签器做成接口（`PaymentVerifier`，见 `internal/fund/verify.go`）：本阶段只实现 HMAC mock 验签器；
真实支付宝/微信验签器只写接口骨架（`UnconfiguredVerifier`，恒返回未配置），绝不连真实渠道端点、不写真实商户密钥。

**回调处理（同一 DB 事务）：** 订单行 `FOR UPDATE` + 金额一致性校验 + 状态 CAS + 账务入账 + 审计，任一步失败整笔回滚。
- 支付回调（待支付 → 已支付）：用户可用 CR / 平台结算 DR（type 20），订单置已支付并回填 `channel_order_no` / `paid_txn_id`；
  渠道单号唯一索引是回调幂等的第二道防线（串单时 409 `CHANNEL_ORDER_CONFLICT`）。
- 失败回调（待支付 → 已失败）：只改订单状态与 `failure_reason`，不入账。
- 退款回调（已支付 → 已退款，仅 mock 渠道演练）：用户可用 DR / 平台结算 CR（type 21）；
  余额不足时整笔回滚、订单与余额不动，另起短事务记审计 `recharge_refund_failed_insufficient`
  （主事务回滚后审计仍需留痕，口径同转账失败审计行：审计行不落账、只留痕）。
- 金额不一致的回调：拒绝入账并记审计 `recharge_callback_amount_mismatch`（订单不动、资金不动），
  即使签名有效（签名只能证明回调体未被篡改，不能证明金额与订单一致——两层校验缺一不可）。
- 钱包冻结/注销时回调入账/退款：整笔回滚、订单不动，记审计 `recharge_callback_wallet_abnormal`，渠道重试待钱包恢复后可继续。
- 并发回调：订单行锁串行化，第二笔看到新状态后按幂等重放或非法流转处理，不重复入账。

### 13.3 Mock 支付环境隔离（红线）

1. **开关控制注册：** Mock 回调入口只在服务端显式环境开关 `FUND_MOCK_PAYMENT=1` 时注册精确路由；
   开关开启还要求 `FUND_MOCK_PAY_CALLBACK_SECRET` 非空，否则服务拒绝启动（避免无签名 mock 入口）。
2. **生产部署禁开开关：** 生产部署的 `fund.env` 必须不开启该开关——部署检查清单（§13.5）要求部署时
   实测确认 `fund.env` 无 `FUND_MOCK_PAYMENT` 启用项；生产资金服务以开关关闭状态运行，mock 回调一律 404。
3. **测试库隔离：** Mock 回调测试全部在 `xianliao_fund_test` 库跑（测试服务直连测试库，stub IM token）；
   生产库只验证空表与健康检查，不写生产数据（部署验证见 §13.5）。
4. **数据库层区分：** Mock 订单与真实订单以 `channel` 字段区分（`mock` / `alipay` / `wechat`），
   管理端列表可按 `channel` 过滤；真实渠道行现阶段不存在（创建侧已拒绝非 mock 渠道）。

### 13.4 真实渠道签约前置清单（签约前不得启用真实渠道）

以下全部完成前，alipay/wechat 回调入口保持 501，创建侧保持拒绝非 mock 渠道：

- [ ] 商户资质：支付宝企业账户（或个体工商户）+ 支付宝开放平台应用（AppID）创建并通过审核；
  微信支付商户号（mchid）开通并完成实名/结算账户验证；银联入网（如走银行卡直连，Phase 9 提现侧复核）。
- [ ] 密钥与证书（只进生产 `fund.env` / 证书文件，0600 权限，绝不进 Git）：支付宝应用私钥 + 支付宝公钥
  （RSA2 验签）、微信支付 APIv3 密钥 + 商户证书 + 平台证书（WECHATPAY2-SHA256-RSA2048 验签）。
- [ ] 验签器落地：按官方规约实现 `PaymentVerifier`（替换 `UnconfiguredVerifier`），先在官方沙箱跑通
  支付/退款/金额不一致/重复回调全量用例，再谈生产启用；验签失败、金额不一致、重复回调的处理须与 §13.2 一致。
- [ ] 回调地址与对账：生产回调 URL（仅回环服务经网关暴露回调路径时，须限制来源 IP / 渠道签名双重校验）、
  日终渠道对账任务（渠道账单 vs `wallet_order` + 分录，差异进 `wallet_reconcile` 流程）。
- [ ] 官方文档（接入时以官方最新版为准）：支付宝开放平台文档 https://opendocs.alipay.com/open/ ；
  微信支付商户平台文档 https://pay.weixin.qq.com/doc/ 。

### 13.5 部署检查清单（Phase 8 部署时逐项打勾）

1. 备份旧二进制与 `fund.env`（只备份文件，不打印密钥值）。
2. 迁移：`fund-migrate up`（0001 → 0008；含 0008 充值字段；up 幂等，已有列/索引跳过）。
3. **确认生产 `fund.env` 未开启 Mock 开关**（检查键名存在性，不输出值）：无 `FUND_MOCK_PAYMENT=1` / `true`。
4. 部署顺序铁律（先停旧进程、等端口释放、再换二进制、再起）：`systemctl stop fund-xianliao` →
   确认 18081 端口释放 → 替换 `fund-server` → `systemctl start fund-xianliao`。
5. 验证：`GET /healthz` 200；无鉴权调用户接口回 401；生产库 `wallet_order` 中 `order_type=1` 行数为 0
   （只读计数，不写生产数据）；其余容器（openim/yichat/yudao）原样运行。

## 14. 提现（Phase 9，人民币、7 态全流程）

资金模型：提现订单复用 `wallet_order`（`order_type=2`，迁移 0009 做最小扩展，见 DATABASE.md）；
不另建订单表、不另建账本。冻结金额 = 提现金额 + 手续费；全部走双重记账、同一 DB 事务，流水 append-only。

### 14.1 订单与状态机

| 方法与路径 | 说明 | 请求（关键字段） | 成功返回 / 错误 |
|---|---|---|---|
| `POST /v1/withdrawals` | 提交提现（提交即冻结，不直接打款） | `{withdraw_method_id, amount_cents>0, tx_password, idempotency_key?}`；守卫顺序：准入引导优先（非 ok 只返回引导、不扣款、不建单）→ 验交易密码 → 方式归属（本人正常方式）→ 金额上下限与频率限制 → 余额校验（可用 ≥ 金额 + 手续费） | order（status 0 待审核、fee/actual 齐全）；`ADMISSION_NOT_SATISFIED` 403 / `TX_PASSWORD_ERROR` 403 / 方式非本人或不存在 `WITHDRAW_METHOD_NOT_FOUND` 404 / 金额超限 `AMOUNT_LIMIT_EXCEEDED` 400 / 手续费 ≥ 金额 `FEE_EXCEEDS_AMOUNT` 400 / 余额不足 `INSUFFICIENT_BALANCE` 400；同一幂等键重复提交返回原单（不重复冻结；被拒绝订单的幂等键同样被占用，被拒后重提须换新幂等键，与 Phase 8 充值创建口径一致） |
| `GET /v1/withdrawals/quote?amount_cents=` | 手续费试算（展示用，无资金变动） | —（仅鉴权，不设准入/交易密码门禁；试算结果不作为提交依据，提交时服务端按当时配置重算并校验） | `{amount_cents, fee_cents, actual_cents, min_amount_cents, single_max_cents, daily_max_cents, daily_count_max}` |
| `GET /v1/withdrawals/{id}` | 订单详情（仅本人可见，他人 404） | — | order |
| `GET /v1/withdrawals?status&limit&offset` | 本人提现记录（倒序，limit 默认 50、上限 200；status 0–6，非法值 400） | — | order 数组 |
| `GET /v1/admin/withdraw-orders?user_id&status&limit&offset` | 管理端订单列表（Phase 7 已有；Phase 9 详情字段扩展 freeze_id / payout_* / failure_reason） | — | order 数组 |
| `GET /v1/admin/withdraw-orders/{id}` | 管理端订单详情（管理接口） | — | order；不存在 → 404；非提现单 → 400 |
| `POST /v1/admin/withdraw-orders/{id}/approve` | 审核通过双人复核（Phase 7 已有；须 `X-Admin-ID`） | — | 第 1 人 → `{stage:"awaiting_second_approval"}`（状态仍 0、余额不动）；第 2 名不同管理员 → `{stage:"approved"}`（状态置 1，`reviewed_at` 落库）；同一人两次 / 非待审核单 → 400；**审核通过只改订单状态，不产生任何资金变动、不打款** |
| `POST /v1/admin/withdraw-orders/{id}/reject` | 审核拒绝（Phase 7 已有；须 `X-Admin-ID`，任一管理员可拒） | `{note?（≤500 字）}` | `{rejected:true}`（状态置 2）；**Phase 9 真实单拒绝时在同一事务内全额解冻（金额 + 手续费回可用，txn type 23）**；Phase 7 种子单无冻结行时走旧路径只改状态（Test90 断言保持不变） |
| `POST /v1/admin/withdraw-orders/{id}/start-processing` | 进入处理中（须 `X-Admin-ID`） | — | `{stage:"processing"}`（状态 1 → 3，只改状态、不产生资金变动）；非已通过单 → 409 `INVALID_TRANSITION` |
| `POST /v1/admin/withdraw-orders/{id}/confirm-payout` | 人工打款确认成功（须 `X-Admin-ID`） | `{payout_proof_no（必填，银行/渠道回单号，≤256 字）, payout_at?（RFC3339，缺省为确认时刻）, payout_note?（≤500 字）}` | `{stage:"success"}`（状态 3 → 4；冻结扣减 txn type 24，见 §14.2；凭证回填后才置 4，数据库 CHECK 约束兜底）；无凭证 → 400；方式已解绑 → 400（不得向失效账户打款，见 §14.3）；非处理中单 → 409 |
| `POST /v1/admin/withdraw-orders/{id}/fail` | 渠道处理失败（须 `X-Admin-ID`） | `{failure_reason?（≤500 字）}` | `{stage:"failed"}`（状态 3 → 5，只改状态与失败原因、不产生资金变动，款仍在冻结中）；非处理中单 → 409 |
| `POST /v1/admin/withdraw-orders/{id}/refund` | 失败后退款（须 `X-Admin-ID`） | — | `{stage:"refunded"}`（状态 5 → 6；全额解冻 txn type 25）；非失败单 → 409 |
| `POST /v1/admin/withdraw-orders/{id}/auto-payout` | 自动打款骨架（须 `X-Admin-ID`） | — | 恒回 501 `CHANNEL_NOT_CONFIGURED`，绝不打款（见 §14.5） |
| `GET /v1/admin/withdraw-config` | 读提现手续费与限额配置（管理接口） | — | 见 §14.4 |
| `PUT /v1/admin/withdraw-config` | 写提现手续费与限额配置（管理接口，按字段部分更新；缺省字段保持原值，限额类字段显式 null 改为不限额；变更记审计 `admin_withdraw_config_update`） | 见 §14.4 | 同上；非法值 → 400 |

order 字段（用户视图）：`order_id, user_id, amount_cents, fee_cents, actual_cents, status（7 态）, withdraw_method_id, payout_proof_no?, payout_at?, failure_reason?, created_at, updated_at`。管理端视图另含 `order_type（恒 2）, channel_order_no, review_first_admin_id, review_second_admin_id, rejected_by_admin_id, review_note, reviewed_at, freeze_id`。

**状态机（仅 `order_type=2` 的行；与充值状态机共用 `status` 列但命名空间按 `order_type` 区分，查询须恒带 `order_type` 过滤）：**
`0 待审核 → 1 已通过 → 3 处理中 → 4 成功`；`0 待审核 → 2 已拒绝`；`3 处理中 → 5 失败 → 6 已退款`。
合法流转只有这 5 条；其余流转一律拒绝（Phase 9 新写入口回 409 `INVALID_TRANSITION`；
Phase 7 已有 approve/reject 入口保持原 400 口径，避免破坏既有断言）。
同一目标状态的重复操作不视为幂等重放（审核/流转类写入口无重放语义，重复调用按非法流转拒绝）。

**状态流转图（文字描述）：**

```
提交（冻结金额+手续费）
  └─> [0 待审核] ──双人复核通过──> [1 已通过] ──财务接单──> [3 处理中] ──人工打款确认──> [4 成功]（终态，冻结扣减）
       │                              │                        │
       │ 任一管理员拒绝                │                        └─渠道处理失败─> [5 失败] ──退款──> [6 已退款]（终态，全额解冻）
       └─> [2 已拒绝]（终态，全额解冻）
```

### 14.2 资金流转（与账务的对应关系）

- 提交冻结（txn type 22，`ref_type='withdraw'`，`ref_id=order_id`）：用户可用 DR / 用户冻结 CR
  （金额 + 手续费），同一事务内建 txn、分录 2 条、冻结行（`remaining = 金额 + 手续费`）、订单行（status 0）
  与审计行（`withdraw_submit`）。冻结行被篡改（`remaining ≠ 金额 + 手续费`）时后续解冻/扣减直接失败、整笔回滚。
- 审核拒绝解冻（txn type 23）：用户冻结 DR / 用户可用 CR（金额 + 手续费），同一事务内冻结行
  `remaining → 0`（status 1 已解冻）、订单 status → 2 与审计行。
- 人工打款确认成功（txn type 24）：用户冻结 DR / 平台结算 CR（金额 + 手续费），同一事务内冻结行
  `remaining → 0`（status 2 已扣款）、订单 status → 4（凭证回填）与审计行。
  平台结算户为账务对手方（出账现金侧）：按本系统“平台户余额 = 全部用户余额之和”不变量，
  用户冻结扣减后平台户余额同步减少金额 + 手续费；手续费的收入确认（平台实得手续费的会计归属）
  不在本账务口径内——订单行 `fee_cents` 即为手续费的唯一事实记录，未来需手续费收入报表时
  以订单行汇总为准，不得另起账本。
- 渠道处理失败（无 txn）：订单 status 3 → 5（`failure_reason` 回填）与审计行，无分录、余额与冻结不动。
- 失败后退款解冻（txn type 25）：用户冻结 DR / 用户可用 CR（金额 + 手续费），同一事务内冻结行
  `remaining → 0`（status 1 已解冻）、订单 status → 6 与审计行。
- 提交幂等三层：① 订单行 `UNIQUE (user_id, client_idempotency_key)`（空键除外）+ 先查后插；
  ② 提交幂等键 `wd-submit-<order_id>`（账务 txn 侧）；③ 同一用户提交由钱包行 `FOR UPDATE` 串行化，
  当日限额校验在同一事务内完成，并发提交不会双双通过同一额度。
- 对账：`POST /v1/admin/reconcile` 的逐户核对覆盖提现分录（Test123：多笔提交 + 拒绝 + 成功 +
  失败退款后 diff=0，全部 type 22–25 txn 分录合计均为 0）。

### 14.3 人工打款流程（无商户资质时的唯一打款路径）

1. 用户提交 → 管理端双人复核通过（状态 1，仅审核通过，未打款；页面与接口必须明示“未打款”）。
2. 财务人员在管理端将订单置为处理中（状态 3，表示已接单、尚未打款）。
3. 财务人员用**对公账户 / 企业网银手工转账**实际到账金额（`actual_cents`）到用户绑定的提现方式账户；
   **严禁个人号垫付为常规流程**（个人号垫付只可在极端应急下经负责人书面特批、事后补审计说明，
   不得作为日常打款通道；本接口与文档均不提供个人号垫付的快捷路径）。
4. 打款后财务人员在管理端回填凭证（银行/渠道回单号、打款时间、操作员自动记为确认管理员）并确认成功
   （状态 4，冻结扣减）。凭证缺失时确认被拒绝（400）；数据库 CHECK 约束兜底：成功单必须有回单号。
5. 打款确认前复核：提现方式必须仍为本人正常方式——用户在流程中解绑方式（计划 §9 验收场景 11 的变体）
   时，确认被拒绝（400，不得向失效账户打款）；操作员应将订单置失败（状态 5）再退款（状态 6），
   资金全额返回用户可用余额，由用户重绑方式后重新提交。
6. 日终对账：财务按银行回单逐笔核对当日成功单（订单号 / 回单号 / 实际到账金额三方一致），
   差异进 `wallet_reconcile` 流程；渠道对账任务在签约自动打款后与验签器同批落地（见 §14.5）。

### 14.4 手续费与限额配置（`fund_withdraw_config`，管理端读写）

| 字段 | 含义 | 默认值 |
|---|---|---|
| `fixed_fee_cents` | 固定费用（分，≥ 0） | 0 |
| `rate_bp` | 费率（万分比，0–10000；100 = 1%） | 0 |
| `min_amount_cents` | 单笔下限（分，null = 除 >0 外不限） | null（不限） |
| `single_max_cents` | 单笔上限（分，null = 不限额） | null（不限） |
| `daily_max_cents` | 单日金额上限（分，null = 不限额） | null（不限） |
| `daily_count_max` | 单日笔数上限（null = 不限额） | null（不限）；运营建议值 5 笔/日 |

手续费公式（向下取整）：`fee = fixed_fee_cents + floor(amount_cents × rate_bp / 10000)`；
实际到账金额 `actual = amount − fee`；`actual ≤ 0` 时拒绝提交（`FEE_EXCEEDS_AMOUNT`）。
频率限制统计口径（数据库当日）：同一用户当日已提交且未被拒绝的提现单（`status <> 2`），
含待审核/已通过/处理中/成功/失败/已退款；被拒绝的单不计入（款已全额解冻，不是一次真实出金尝试）。
提交时校验顺序中的限额段：下限 → 单笔上限 → 单日笔数 → 单日金额；任一超限均在任何资金变动前拒绝，
不产生订单行（与 Phase 7 转账单笔上限口径一致）。

### 14.5 自动打款签约前置清单（签约前不得启用自动打款）

以下全部完成前，`auto-payout` 入口保持 501，创建侧与人工流程不受影响：

- [ ] 商户资质：支付宝企业账户（或个体工商户）+ 支付宝开放平台应用（AppID）创建并通过审核；
  微信支付商户号（mchid）开通并完成实名/结算账户验证；银联入网（如走银行卡直连）。
  微信个人收款 ≠ 商户 API 提现，须按渠道官方能力设计（计划 §9.2）。
- [ ] 密钥与证书（只进生产 `fund.env` / 证书文件，0600 权限，绝不进 Git）：支付宝应用私钥 + 支付宝公钥
  （RSA2）、微信支付 APIv3 密钥 + 商户证书 + 平台证书（WECHATPAY2-SHA256-RSA2048）。
- [ ] 打款器落地：按官方规约实现自动打款调用（替换 501 骨架），先在官方沙箱跑通
  打款成功/打款失败/重复打款/金额不一致全量用例，再谈生产启用；重复打款必须以订单状态 CAS +
  渠道单号唯一索引双重拦截，绝不重复出账。
- [ ] 回调与对账：生产打款回调 URL（仅回环服务经网关暴露时，须限制来源 IP / 渠道签名双重校验）、
  日终渠道对账任务（渠道账单 vs `wallet_order` + 分录，差异进 `wallet_reconcile` 流程）。
- [ ] 官方文档（接入时以官方最新版为准）：支付宝开放平台文档 https://opendocs.alipay.com/open/ ；
  微信支付商户平台文档 https://pay.weixin.qq.com/doc/ 。

### 14.6 部署检查清单（Phase 9 部署时逐项打勾）

1. 备份旧二进制与 `fund.env`（只备份文件，不打印密钥值）。
2. 迁移：`fund-migrate up`（0001 → 0009；含 0009 提现字段与配置表；up 幂等，已有列/索引/约束跳过）。
3. **确认生产 `fund.env` 未开启 Mock 开关**（检查键名存在性，不输出值）：无 `FUND_MOCK_PAYMENT=1` / `true`
   （Mock 开关与提现无关，但生产部署一律复核，避免测试开关随二进制同批上线）。
4. 部署顺序铁律（先停旧进程、等端口释放、再换二进制、再起）：`systemctl stop fund-xianliao` →
   确认 18081 端口释放 → 替换 `fund-server` → `systemctl start fund-xianliao`。
5. 验证：`GET /healthz` 200；无鉴权调用户接口回 401；生产库 `wallet_order` 中 `order_type=2` 行数为 0
   且 `fund_withdraw_config` 默认行存在（只读计数，不写生产数据）；其余容器（openim/yichat/yudao）原样运行。

## 15. 手动充值（迁移 0010，App 回调充值展示的替代通道之一）

流程：钱包充值 → 展示后台配置的收款方式（微信/支付宝/银行卡）→ 用户选方式、输金额 →
展示该方式后台上传的收款二维码 → 用户线下付款 → 提交成功支付截图 → 等待审核 →
管理员双人复核通过才入账。回调接口（§13）本身不动，留给未来商户渠道。

订单口径：充值订单继续复用 `wallet_order`（`order_type=1`），新增 `recharge_kind`
区分三类：`callback`（Phase 8 既有回调单，历史行默认回填）、`manual`（手动单）、`gateway`
（网关单，见 §16）。`channel` CHECK 扩到 `manual`/`gateway`（仅订单来源标记，不代表渠道
验签方）。Phase 8 回调列表/详情端点（`/v1/recharges*`、`/v1/admin/recharges*`）本轮起只
服务 `kind='callback'` 的单；手动/网关单走本节与 §16 的通用端点（旧端点行为对回调单不变）。

状态机（手动单，按 kind 分命名空间，查询须恒带 kind 过滤）：
`10 待上传凭证 → 11 待审核 → 1 已入账`；`11 → 12 已拒绝`（拒绝不入账、不动余额）。
入账分录与 Phase 8 回调入账同模型：txn type 20（用户可用 CR / 平台结算 DR，
`ref_type='recharge'`，幂等键 `rch-manual-pay-<orderID>`）。

建单与凭证上传均不产生资金变动，故不设准入/交易密码门禁（同 §13.1 建单口径）；
入账的唯一路径是双人复核第二步的同一事务（订单行锁 + 状态 CAS + 入账 + 审计），
重复审批拒绝（已终态再审批 409；同人两次复核 400）。

### 15.1 收款方式管理（管理端，`X-Admin-Secret`；写操作另须 `X-Admin-ID`）

- `POST /v1/admin/payment-methods` `{type: wechat|alipay|bank, display_name(1–128), sort_order?}` →
  方式行（建时无二维码，须下一步上传）；`display_name` 为空、`type` 非法均 400。
- `POST /v1/admin/payment-methods/{id}/qr`（multipart 字段 `qr`）→ 设置/替换二维码；
  替换时旧文件行与磁盘文件一并清理；无管理密钥 401、无 `X-Admin-ID` 401。
- `PUT /v1/admin/payment-methods/{id}` `{display_name?, sort_order?, enabled?}`（部分更新；
  `enabled=false` = 停用，停用方式不再出现在用户列表、不可建单，已建订单不受影响）。
- `GET /v1/admin/payment-methods`（全部，含停用）与 `GET /v1/admin/payment-methods/{id}`。
- 二维码图片与截图走同一套上传机制（§15.4），不许用外链 URL 冒充上传。

### 15.2 用户侧手动充值（IM token 鉴权）

- `GET /v1/recharge/methods`：只返回启用项，含 `qr_url`（鉴权取用地址，见 §15.4）。
- `POST /v1/recharge/manual` `{method_id, amount_cents, idempotency_key?}` → 订单（状态 10）
  与方式展示名 + `qr_url`；停用方式 400 `PAYMENT_METHOD_NOT_AVAILABLE`、方式无二维码 400
  `PAYMENT_METHOD_NO_QR`、未知方式 404、金额 ≤ 0 或非整数分 400；同一幂等键重放返回同一单。
  建单时方式行加 `FOR UPDATE` 锁，停用操作与建单串行化。
- `POST /v1/recharge/{id}/proof`（multipart 字段 `proof`）：仅本人订单、仅状态 10/11；
  状态 11 重传替换旧凭证（旧文件行与磁盘文件清理后提交）；他人订单 404、已终态 409；
  上传后订单置 11；上传与审批竞态时以事务内重查为准（审批已终态则拒绝并删除新文件）。
- `GET /v1/recharge/orders?kind&status&limit&offset`（本人通用列表，见 §16.3）与
  `GET /v1/recharge/orders/{id}`（本人通用详情；手动单含方式展示名、`qr_url`、凭证信息、
  审核备注；详情为只读，不触发任何状态变更）。

### 15.3 审核入账（管理端）

- `GET /v1/admin/recharge/pending`（待审核手动单，状态 11，创建时间升序；可选 `user_id` 过滤）。
- `POST /v1/admin/recharge/{id}/approve`（双人复核，参照提现审批口径）：第一复核只记
  `review_first_admin_id` + 审计 `admin_manual_recharge_approve1`，余额不动；第二名不同管理员
  在同一事务内完成订单行锁重校验 + 入账 + 状态 → 1 + `paid_txn_id` 回填 + 审计
  `admin_manual_recharge_execute`；入账失败整事务回滚（第二复核不记录、订单仍为 11、可重试）。
- `POST /v1/admin/recharge/{id}/reject` `{review_note?(≤500)}`（单人即可，仅状态 11）：
  状态 → 12 + 拒绝人 + 备注 + 审计 `admin_manual_recharge_reject`，不入账、不动余额。
- 全部管理操作写 `wallet_audit_log`（建方式/改方式/传二维码/首复核/执行/拒绝/凭证上传均有审计行）。

### 15.4 截图与二维码存储（与 §15.1 共用）

- 仅 jpg/png（服务端嗅探前 512 字节，`http.DetectContentType`，客户端 Content-Type 与文件名
  一律忽略）、单张 ≤ 5MB（超限拒绝且不落盘）、随机服务端文件名（`file_id` + 按嗅探类型取的
  扩展名，文件名与路径不含用户可控内容）、只存本机磁盘（默认 `/opt/xianliao/fund-uploads`，
  环境变量 `FUND_UPLOAD_DIR` 可改；测试进程用临时目录）。
- 取用只走鉴权接口 `GET /v1/uploads/{file_id}`：管理员（`X-Admin-Secret`）恒可看；用户看
  二维码须该方式启用、看凭证须为上传本人；其余一律统一 404（不泄露存在性）；未鉴权 401；
  服务端开文件前校验路径落在上传目录内（前缀检查），防止目录穿越。

## 16. 网关支付（迁移 0010，与手动通道并存）

流程：用户输入金额 → 后端建网关支付单并返回网关收银台跳转地址 → 用户在网关完成付款后
回到 App → App 弹窗问“是否已经成功付款？”→ 用户点“是”只提示“请等待支付结果返回即可到账”
并轮询订单状态，绝不由这个按钮直接入账；用户点“付款遇到问题？”跳 App 内已有帮助与反馈入口
（专属客服聊天属 Phase 13 范围，本次不新建客服系统）。

安全红线（结构保证 + 测试断言）：用户确认“已付款”没有任何入账入口——状态查询接口
（§16.3）是只读的，唯一能改变余额的路径是网关服务端回调验签通过（§16.2）或手动审核
（§15.3，网关单不可走手动审核，kind 隔离）；验签失败、重复回调、过期订单回调一律拒绝入账并记审计。

### 16.1 网关适配器与配置开关

- 适配器接口 `GatewayAdapter`：`CreatePayment(ctx, orderID, userID, amountCents) → (cashierURL, gatewayRef, err)`；
  `VerifyCallback(r) → (*GatewayCallback, error)`（回调载荷含订单号、渠道单号、金额、状态 paid|failed、
  失败原因；验签失败返回 `errGatewayBadSignature`）。
- 生产默认不注册任何适配器（`Config.GatewayAdapter` 为空 = 通道关闭）：建单回 400
  `GATEWAY_NOT_CONFIGURED`（文案“网关支付暂未开通”）、回调端点回 501 `CHANNEL_NOT_CONFIGURED`，
  均不入账；**绝不生成假收银台、绝不伪造回调**。真实网关厂商与商户凭证未定，待定后再实现
  适配器并接配置开关（本轮只有接口 + 测试 stub，stub 收银台域名 `gateway.test` 仅测试库可见）。
- 建单 `POST /v1/recharge/gateway` `{amount_cents, idempotency_key?}` → 订单（状态 0 待支付、
  `cashier_url`、`gateway_ref`、`expires_at` = 建单 + 30 分钟）；金额 ≤ 0 或非整数分 400；
  同一幂等键重放返回同一单。建单不产生资金变动，不设准入/交易密码门禁（同 §15.2 口径）。

### 16.2 网关回调入账（唯一入账路径）

`POST /v1/payments/callback/gateway`（验签后处理，全部在订单行锁事务内）：

- 未知订单 404；同目标状态重放幂等返回原单（不重复入账、不重复审计）。
- 待支付单已过期（`now > expires_at`）：先关单为状态 4（审计 `gateway_recharge_expired`），
  再回 409 `GATEWAY_ORDER_EXPIRED`（paid/failed 回调同此处理，过期一律不入账）。
- 渠道单号不一致 400 `CHANNEL_ORDER_MISMATCH`（渠道单号部分唯一索引为第二道防线）；
  金额不一致回滚 + 独立审计 `gateway_callback_amount_mismatch` + 400 `CALLBACK_AMOUNT_MISMATCH`。
- 失败回调：状态 → 2 + 失败原因（≤500）+ 审计 `gateway_recharge_failed`，不入账。
- 支付回调：入账 type 20（幂等键 `gw-pay-<orderID>`）+ 状态 → 1 + 渠道单号与 `paid_txn_id`
  回填 + 审计 `gateway_recharge_paid`，同一事务；验签失败 401（`errGatewayBadSignature` →
  `SIGNATURE_INVALID`），订单与余额不动。
- 过期关单扫描：`POST /v1/admin/recharge/gateway/expire` `{limit?(默认 100，上限 500)}` →
  按创建时间升序关闭过期待支付单（逐单独立审计，返回 `closed_ids`）；未过期单不动。
  通用详情的 `expired` 计算字段只用于展示，**详情查询不懒关闭过期单**（与红包详情不触发
  退款的规则一致）。

### 16.3 订单状态查询（App 轮询用，只读）

- `GET /v1/recharge/orders?kind&status&limit&offset`（本人；`kind` = callback|manual|gateway，
  不传则三类全含；统一视图含订单号、用户、kind、渠道、金额/手续费/到账、状态、过期计算字段、
  方式引用、网关引用、入账 txn、失败原因、审核备注、时间）。
- `GET /v1/recharge/orders/{id}`（本人；按 kind 返回回调/手动/网关三种详情形态；网关详情含
  `expired` 计算字段、`cashier_url`、`gateway_ref`、`expires_at`）。
- 管理端：`GET /v1/admin/recharge/orders`（统一视图 + `user_id` 过滤）。
- 状态覆盖：网关单 0 待支付 / 1 已支付 / 2 失败 / 4 超时关闭；手动单 10/11/1/12（见 §15）；
  回调单 0/1/2/3（见 §13）。“是，已付款”按钮在 App 侧只应调用本节只读接口轮询。

### 16.4 部署检查清单（0010 部署时逐项打勾）

1. 备份旧二进制（`fund-server.bak-0010`）与 `fund.env`（只备份文件，不打印密钥值）。
2. 迁移：`fund-migrate up`（0001 → 0010；up 幂等，已有列/索引/约束跳过；**注意测试实例的
   `fund-test.env` 中 `FUND_ADMIN_DATABASE_URL` 指向生产库，迁移时务必确认目标库名，测试库
   迁移须用指向测试库的 ADMIN DSN，否则会把 DDL 打到生产库——0010 本身只做加表加字段，
   但目标库必须显式核对**）。
3. 确认生产 `fund.env` 未开启 Mock 开关（键名检查，不输出值）且未注册网关适配器（生产
   二进制默认无适配器，网关通道天然关闭）。
4. 部署顺序铁律（先停旧进程、等端口释放、再换二进制、再起）：`systemctl stop fund-xianliao` →
   确认 18081 端口释放 → 替换 `fund-server` → `systemctl start fund-xianliao`；测试实例先行
   验证（新二进制 + 测试库 + 测试上传目录，验证 healthz / 无鉴权 401 / 管理端新接口）。
5. 验证：`GET /healthz` 200；无鉴权调用户接口回 401；生产库计数部署前后一致（本轮：
   `wallet_account=2, wallet_txn=0, wallet_order=0`；account=2 的第二户为空钱包户，
   2026-11-02 09:20 +07 由用户侧 App 钱包页自动建户，余额 0、无流水，非本次部署产生）；
   其余容器（openim/yichat/yudao）原样运行；`GET /v1/admin/payment-methods` 与
   `GET /v1/admin/recharge/pending` 在生产返回空列表（`ok:true`）。

## 17. 网关充值通道配置（0012）与人工存款通道面板（0010 复用）

### 17.1 网关通道实体 `wallet_gateway_channel`

- 一行 = 一个厂商：`vendor VARCHAR(64) PRIMARY KEY`（须匹配 `^[a-z0-9_-]{1,64}$`）、
  `display_name`（1–128 字符）、`enabled BOOLEAN DEFAULT TRUE`、
  `params JSONB DEFAULT '{}'`（**只放非密钥参数**，如商户号、回调地址）、
  `created_by_admin_id`、`created_at`、`updated_at`。
- **密钥红线**：实体与响应里根本没有密钥字段；写入前对 params 键名做 fail-closed
  拒绝——小写后含 `secret`、`apikey`/`api_key`、`private`、`password`/`passwd`、
  `token`、`access_key`、`sign_key` 任一子串的键一律 400；代码里绝不打印 params；
  面板页明示“密钥由服务端环境变量配置，不在面板里填”。
- 授权：`GRANT SELECT, INSERT, UPDATE ON wallet_gateway_channel TO fund_app`
  （通道不删除，故不授 DELETE；0012 初版曾漏授 GRANT，致生产业务账号查表 500，
  已补——新迁移务必带 GRANT）。

### 17.2 网关通道管理接口（均在 `/v1/admin/` 下，走面板代理）

- `POST /v1/admin/gateway-channels`：新建 `{vendor, display_name, enabled?, params?}`，
  重复 vendor → 409 `GATEWAY_CHANNEL_EXISTS`；非法 vendor 或密钥键 → 400。
- `GET /v1/admin/gateway-channels`：列表，按 vendor 排序。
- `GET /v1/admin/gateway-channels/{vendor}`：单查，不存在 → 404 `GATEWAY_CHANNEL_NOT_FOUND`。
- `PUT /v1/admin/gateway-channels/{vendor}`：局部更新 `{display_name?, enabled?, params?}`，
  params 传了则整包替换，不合并。

### 17.3 网关建单门禁（开关语义）

- `POST /v1/recharge/gateway`（用户建单）可传可选 `vendor`：
  1. 传了 vendor 时先查通道开关：行不存在或 `enabled=false` 一律回
     `GATEWAY_NOT_CONFIGURED`（fail-closed）；
  2. 再查适配器：`gateway == nil` 一律回 `GATEWAY_NOT_CONFIGURED`，绝不生成假收银台。
- **边界（有意设计）**：未传 vendor 的老口径建单不受通道表约束（仅适配器门禁）。
  旧测试与旧 App 均不传 vendor，改成“无 vendor 也查任意启用行”会在单测子集运行时
  误伤回归；厂商接入时 App 必须传 vendor，否则不受通道开关约束。
- 回调接口不动：无适配器仍 501；已建订单的回调不因开关事后关闭而被截断。
- **开关有了 ≠ 能收款**：真实网关收款仍需用户定厂商 + 商户凭证 + 适配器实现，那是下一步。

### 17.4 人工存款通道面板（复用 0010 接口）

- 面板页（资金组 → 人工存款通道）调 0010 已有管理接口：
  `POST /v1/admin/payment-methods`（新建 `{type, display_name, sort_order}`）、
  `GET /v1/admin/payment-methods`（管理列表，含停用项）、
  `PUT /v1/admin/payment-methods/{id}`（局部更新，type 不可改）、
  `POST /v1/admin/payment-methods/{id}/qr`（multipart 字段 `qr` 的二维码上传）。
- 停用语义：`enabled=false` 后用户端 `GET /v1/recharge/methods`（`WHERE enabled=TRUE`）
  不再出现该方式；管理列表仍可见，便于重新启用。

### 17.5 0012 部署检查清单

1. 备份旧二进制（`fund-server.bak-0012`）与 `fund.env`（只备份文件，不打印密钥值）。
2. 迁移：`fund-migrate up`（0001 → 0012；up 幂等；新迁移必须带 fund_app GRANT）。
3. 部署顺序铁律（先停旧进程、等端口释放、再换二进制、再起）：`systemctl stop fund-xianliao`
   → 确认 18081 端口释放 → 替换 `fund-server` → `systemctl start fund-xianliao`；
   测试实例先行验证（新二进制 + 测试库 + 测试上传目录，验证 healthz / 管理端新接口）。
4. 验证：`GET /healthz` 200；生产库计数部署前后一致（本轮：`wallet_account=3,
   wallet_txn=0, wallet_order=0`，与部署前基线一致）；`GET /v1/admin/gateway-channels`
   与 `GET /v1/admin/payment-methods` 在生产返回空列表（`ok:true`）；其余容器原样运行。
