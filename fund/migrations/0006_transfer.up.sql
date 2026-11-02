-- 仙聊资金服务 Phase 6 迁移 0006：用户之间余额转账（即时到账，仅动资金库）。
-- 模式定稿（只用一套资金状态机，不混用两种模式）：转账为即时到账（instant），与红包即时结算
-- 模型一致；待确认/待领取模式不做——待确认模式须另起冻结 + 过期 + 领取/退款状态机，会使同一资金
-- 服务内并存两套状态机与两套争议路径，Phase 6 不引入该复杂度；未来如需待确认转账，须另立阶段
-- 并以本表 status 扩展新状态，不得在本阶段代码里留半截待确认分支。
-- 订单状态：1 成功（唯一由成功转账写入的状态；订单行与账务 txn、分录在同一 DB 事务内落库）；
-- 2 失败（仅结算失败审计行：余额不足 / 收款方钱包冻结 / 注销时，HTTP 层在主事务回滚后另起短事务
-- 写入一行 status=2、failure_code 非空、txn_id 为空的审计行；失败行不落账——无 wallet_txn、无分录、
-- 双方余额不动；失败行不带幂等键，同一幂等键事后补足余额重发仍可成功，成功幂等只匹配 status=1 的行）。
-- 下列失败不产生订单行（请求本身无效，非一次结算尝试）：参数错误（金额 ≤ 0、附言超长、收款方非法）、
-- 自转、平台结算户为收款方、鉴权失败、转出方准入未完成、交易密码错误。以上失败的共同点是：
-- 在任何资金变动发生前即被拒绝，错误响应本身就是完整记录，无需订单行。
-- 纠错：绝不直接改余额、不 UPDATE 订单行改状态；异常纠错只走已有冲正（POST /v1/ledger/reverse，
-- txn type 8，全部分录取反生成新 txn；接收方余额不足时冲正整体失败、原转账不动）。冲正不改写原
-- 订单行（订单为历史事实），冲正链路凭原 txn_id 在管理端详情接口查到。
-- 资金流转（txn type 18，ref_type='transfer', ref_id=order_id）：同一 DB 事务、同一 txn_id 下两条分录
-- （转出方可用 DR / 转入方可用 CR，金额相等，带符号合计为 0）；无冻结行、无过期、无领取环节。
-- 收款方被动收款：钱包不存在时在同一事务内自动建钱包（INSERT ... ON CONFLICT DO NOTHING 后行锁）
-- 即可入账，不设准入关——转账由转出方发起，收款方准入在其下次主动使用资金时 enforcement；
-- 与红包领取须准入的区别：红包领取是领取方的主动资金操作（领取即确认收款并可随即使用），故领取
-- 时查准入；转账入账是被动记账，不查收款方准入。收款方钱包冻结/注销时整笔拒绝（不落账）。
-- 并发：涉及账户按 user_id 排序加锁防死锁 + version 条件更新（第二道防线）+ 余额校验；
-- 转出方 Redis 辅助锁（fund:user:<sender>，与红包同一机制）仅协调，锁被持有回 429。
-- 幂等：成功订单 UNIQUE (sender_id, idempotency_key)（空键除外，partial index）；同一幂等键重复
-- 提交返回原订单（先查后插 + 唯一冲突回查），不重复扣款；并发重复同样安全。
BEGIN;

CREATE TABLE IF NOT EXISTS transfer_order (
    order_id        VARCHAR(128) PRIMARY KEY,
    sender_id       VARCHAR(128) NOT NULL,
    receiver_id     VARCHAR(128) NOT NULL,
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    remark          VARCHAR(200) NOT NULL DEFAULT '',
    status          SMALLINT NOT NULL CHECK (status IN (1, 2)),
    failure_code    VARCHAR(64),
    txn_id          VARCHAR(128),
    idempotency_key VARCHAR(256),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_transfer_self CHECK (sender_id <> receiver_id),
    CONSTRAINT chk_transfer_failed_no_txn CHECK (status <> 2 OR txn_id IS NULL),
    CONSTRAINT chk_transfer_success_has_txn CHECK (status <> 1 OR txn_id IS NOT NULL),
    CONSTRAINT chk_transfer_failure_code CHECK ((status = 1 AND failure_code IS NULL) OR (status = 2 AND failure_code IS NOT NULL))
);
CREATE INDEX IF NOT EXISTS ix_transfer_sender ON transfer_order (sender_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_transfer_receiver ON transfer_order (receiver_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ux_transfer_sender_idem ON transfer_order (sender_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> '' AND status = 1;

-- 订单为业务单据（状态机流转与失败审计行需 INSERT），不在 append-only 之列；
-- ledger / audit_log 仍禁 UPDATE/DELETE（见 0001）。
GRANT SELECT, INSERT, UPDATE ON transfer_order TO fund_app;

COMMIT;
