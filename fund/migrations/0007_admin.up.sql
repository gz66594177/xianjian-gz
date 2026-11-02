-- 仙聊资金服务 Phase 7 迁移 0007（仅动资金库 xianliao_fund，须以管理员账号执行）
-- 新增：管理员资金调整单（双人复核）+ 风控限额配置表 + wallet_order 提现审核字段扩展。
-- 原则不变：金额全部 BIGINT 分；ledger / audit_log 仍 append-only；管理端绝不直接改余额
-- （调整单执行走统一双重记账事务）；调整单只查不改，执行后不可撤销，纠错另建反向单。
-- 提现审核脚手架（Phase 9 预留）：审核通过只改订单状态，绝不产生打款动作。
BEGIN;

-- 管理员资金调整单：任何管理员余额调整必须先建单；全部调整单统一双人复核
-- （两名不同管理员先后 approve 后才执行；不设单人直通阈值，避免单人调账口子）。
-- status: 0 待复核 1 已执行 2 已拒绝；direction: 1 增加用户可用余额 2 减少用户可用余额。
CREATE TABLE IF NOT EXISTS fund_admin_adjustment (
    adjustment_id          VARCHAR(128) PRIMARY KEY,
    user_id                VARCHAR(128) NOT NULL,
    direction              SMALLINT NOT NULL CHECK (direction IN (1, 2)),
    amount_cents           BIGINT NOT NULL CHECK (amount_cents > 0),
    reason                 VARCHAR(500) NOT NULL,
    status                 SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2)),
    created_by_admin_id    VARCHAR(128) NOT NULL,
    first_admin_id         VARCHAR(128),
    second_admin_id        VARCHAR(128),
    rejected_by_admin_id   VARCHAR(128),
    original_adjustment_id VARCHAR(128),
    executed_txn_id        VARCHAR(128),
    executed_at            TIMESTAMPTZ,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (first_admin_id IS NULL OR first_admin_id <> second_admin_id),
    CHECK (status <> 1 OR executed_txn_id IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS ix_adjustment_user ON fund_admin_adjustment (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_adjustment_status ON fund_admin_adjustment (status, created_at DESC);

-- 风控限额配置：单行 key='default'；NULL = 不限额（默认不限额）。
-- 本阶段仅转账发起路径接入单笔上限校验；单日上限与红包路径限额接入留 Phase 8/9 统一做，
-- 配置字段先行存在、文档写清未接入前不得宣称已生效。
CREATE TABLE IF NOT EXISTS fund_risk_limit (
    config_key       VARCHAR(64) PRIMARY KEY,
    single_max_cents BIGINT CHECK (single_max_cents IS NULL OR single_max_cents >= 0),
    daily_max_cents  BIGINT CHECK (daily_max_cents IS NULL OR daily_max_cents >= 0),
    updated_by       VARCHAR(128),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO fund_risk_limit (config_key, single_max_cents, daily_max_cents)
VALUES ('default', NULL, NULL)
ON CONFLICT (config_key) DO NOTHING;

-- wallet_order 提现审核字段（最小扩展；Phase 9 打款前不得用本表字段之外的口径）：
-- 提现 7 态：0 待审核 1 已通过 2 已拒绝 3 处理中 4 成功 5 失败 6 已退款。
-- 本阶段管理端仅允许 0 -> 1（双人复核）与 0 -> 2（任一管理员拒绝）；3/4/5/6 为 Phase 9 流转，
-- 本阶段不提供写入口；审核通过只改订单状态，不产生任何打款动作。
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS review_first_admin_id VARCHAR(128);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS review_second_admin_id VARCHAR(128);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS rejected_by_admin_id VARCHAR(128);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS review_note VARCHAR(500);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS reviewed_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS ix_order_type_status ON wallet_order (order_type, status, created_at DESC);

-- 业务账号权限（新表与 0001 口径一致；wallet_order 已在 0001 授权）
GRANT SELECT, INSERT, UPDATE ON fund_admin_adjustment, fund_risk_limit TO fund_app;

COMMIT;
