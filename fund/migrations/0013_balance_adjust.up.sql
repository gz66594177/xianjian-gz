-- 0013: 人工余额修改（单人直连入账，无双人复核流程）。
-- 新表 fund_balance_adjustment：只存单人提交后直接入账的调整单；
-- 入账与建单在同一事务内完成，失败则整笔回滚、不留有效单据。
-- 与 Phase 7 的 fund_admin_adjustment（双人复核）互不干扰：旧表旧接口原样保留，
-- 面板人工调账页只用本表接口。
CREATE TABLE IF NOT EXISTS fund_balance_adjustment (
    adjustment_id       VARCHAR(128) PRIMARY KEY,
    user_id             VARCHAR(128) NOT NULL,
    direction           SMALLINT NOT NULL CHECK (direction IN (1, 2)),
    amount_cents        BIGINT NOT NULL CHECK (amount_cents > 0),
    reason_code         VARCHAR(32) NOT NULL CHECK (reason_code IN ('activity_reward', 'compensation', 'correction', 'internal_test', 'other')),
    note                VARCHAR(500) NOT NULL DEFAULT '',
    status              SMALLINT NOT NULL DEFAULT 1 CHECK (status = 1),
    created_by_admin_id VARCHAR(128) NOT NULL,
    executed_txn_id     VARCHAR(128) NOT NULL,
    idempotency_key     VARCHAR(128),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    executed_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (reason_code <> 'other' OR note <> '')
);
CREATE UNIQUE INDEX IF NOT EXISTS ix_baladj_idem ON fund_balance_adjustment (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS ix_baladj_user ON fund_balance_adjustment (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_baladj_reason ON fund_balance_adjustment (reason_code, created_at DESC);
-- 业务进程以 fund_app 运行（0012 曾漏授致 500 的教训）：新表必须显式授权。
GRANT SELECT, INSERT, UPDATE ON fund_balance_adjustment TO fund_app;
