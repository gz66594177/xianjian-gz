-- 仙聊资金服务 Phase 1 迁移 0001（仅动资金库 xianliao_fund，须以管理员账号执行）
-- 金额全部 BIGINT 分（int64），禁浮点。ledger / audit_log 由业务账号权限强制 append-only（见本文件末尾 GRANT/REVOKE）。
BEGIN;

CREATE TABLE IF NOT EXISTS wallet_account (
    user_id        VARCHAR(128) PRIMARY KEY,
    balance_cents  BIGINT NOT NULL DEFAULT 0 CHECK (balance_cents >= 0),
    frozen_cents   BIGINT NOT NULL DEFAULT 0 CHECK (frozen_cents >= 0),
    status         SMALLINT NOT NULL DEFAULT 0, -- 0 正常 1 整户冻结 2 注销
    version        BIGINT NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- txn type: 1 收入 2 支出 3 冻结 4 解冻 5 冻结扣款 6 冻结退款 7 退款 8 冲正
-- status: 0 待处理 1 成功 2 失败（Phase 1 同步成功才落库，失败不建头）
CREATE TABLE IF NOT EXISTS wallet_txn (
    txn_id          VARCHAR(128) PRIMARY KEY,
    user_id         VARCHAR(128) NOT NULL,
    type            SMALLINT NOT NULL,
    ref_type        VARCHAR(64),
    ref_id          VARCHAR(128),
    status          SMALLINT NOT NULL DEFAULT 1,
    idempotency_key VARCHAR(256),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_txn_user_idem ON wallet_txn (user_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';
CREATE INDEX IF NOT EXISTS ix_txn_user_created ON wallet_txn (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_txn_ref ON wallet_txn (ref_type, ref_id);

-- ledger: account_type 1 可用 2 冻结；direction 1 借(DR, 负) 2 贷(CR, 正)；同一 txn 下带符号合计必须为 0（应用层校验 + 对账校验）
CREATE TABLE IF NOT EXISTS wallet_ledger (
    entry_id            BIGSERIAL PRIMARY KEY,
    txn_id              VARCHAR(128) NOT NULL,
    user_id             VARCHAR(128) NOT NULL,
    account_type        SMALLINT NOT NULL,
    direction           SMALLINT NOT NULL,
    amount_cents        BIGINT NOT NULL CHECK (amount_cents > 0),
    balance_after_cents BIGINT NOT NULL,
    frozen_after_cents  BIGINT NOT NULL,
    opposite_user_id    VARCHAR(128),
    opposite_account    VARCHAR(128),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
    -- 注意：本表无 updated_at，业务账号无 UPDATE/DELETE 权限
);
CREATE INDEX IF NOT EXISTS ix_ledger_txn ON wallet_ledger (txn_id);
CREATE INDEX IF NOT EXISTS ix_ledger_user_created ON wallet_ledger (user_id, created_at DESC);

-- order_type: 1 充值 2 提现 3 红包 4 转账；status: 充值/提现 7 态见 API 文档（Phase 1 仅作结构预留，不产生真实充值提现订单）
CREATE TABLE IF NOT EXISTS wallet_order (
    order_id          VARCHAR(128) PRIMARY KEY,
    user_id           VARCHAR(128) NOT NULL,
    order_type        SMALLINT NOT NULL,
    amount_cents      BIGINT NOT NULL CHECK (amount_cents > 0),
    fee_cents         BIGINT NOT NULL DEFAULT 0 CHECK (fee_cents >= 0),
    actual_cents      BIGINT NOT NULL CHECK (actual_cents >= 0),
    status            SMALLINT NOT NULL DEFAULT 0,
    withdraw_method_id VARCHAR(128),
    channel_order_no  VARCHAR(128),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_order_user ON wallet_order (user_id, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS ux_order_channel ON wallet_order (channel_order_no) WHERE channel_order_no IS NOT NULL AND channel_order_no <> '';

-- freeze status: 0 冻结中 1 已解冻 2 已扣款（部分操作后 remaining>0 仍为 0）
CREATE TABLE IF NOT EXISTS wallet_freeze (
    freeze_id       VARCHAR(128) PRIMARY KEY,
    user_id         VARCHAR(128) NOT NULL,
    txn_id          VARCHAR(128),
    order_id        VARCHAR(128),
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    remaining_cents BIGINT NOT NULL CHECK (remaining_cents >= 0),
    status          SMALLINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_freeze_user ON wallet_freeze (user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS wallet_tx_password (
    user_id         VARCHAR(128) PRIMARY KEY,
    password_hash   VARCHAR(256) NOT NULL,
    error_count     INT NOT NULL DEFAULT 0 CHECK (error_count >= 0),
    locked_until    TIMESTAMPTZ,
    last_changed_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- method type: 1 支付宝 2 微信 3 银行卡；status: 0 正常 1 已解绑
CREATE TABLE IF NOT EXISTS wallet_withdraw_method (
    method_id      VARCHAR(128) PRIMARY KEY,
    user_id        VARCHAR(128) NOT NULL,
    type           SMALLINT NOT NULL,
    real_name      VARCHAR(128) NOT NULL,
    account_no_enc TEXT NOT NULL,
    bank_name      VARCHAR(128),
    bank_branch    VARCHAR(256),
    is_default     BOOLEAN NOT NULL DEFAULT FALSE,
    status         SMALLINT NOT NULL DEFAULT 0,
    bind_time      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_method_user_type ON wallet_withdraw_method (user_id, type);
CREATE UNIQUE INDEX IF NOT EXISTS ux_method_one_default ON wallet_withdraw_method (user_id) WHERE is_default AND status = 0;

CREATE TABLE IF NOT EXISTS wallet_audit_log (
    log_id        BIGSERIAL PRIMARY KEY,
    user_id       VARCHAR(128) NOT NULL,
    operator_id   VARCHAR(128),
    action        VARCHAR(128) NOT NULL,
    detail_masked TEXT,
    ip            VARCHAR(64),
    device        VARCHAR(256),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
    -- 无 updated_at，业务账号无 UPDATE/DELETE 权限
);
CREATE INDEX IF NOT EXISTS ix_audit_user_created ON wallet_audit_log (user_id, created_at DESC);

-- reconcile status: 0 平账 1 差异待处理 2 已处理
CREATE TABLE IF NOT EXISTS wallet_reconcile (
    reconcile_id   VARCHAR(128) PRIMARY KEY,
    reconcile_date DATE NOT NULL,
    channel        VARCHAR(64),
    expected_cents BIGINT NOT NULL,
    actual_cents   BIGINT NOT NULL,
    diff_cents     BIGINT NOT NULL,
    status         SMALLINT NOT NULL DEFAULT 0,
    handler_id     VARCHAR(128),
    handled_at     TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_reconcile_date ON wallet_reconcile (reconcile_date DESC);

-- 平台结算户（非真实用户，API 层禁止查询/操作）
INSERT INTO wallet_account (user_id, balance_cents, frozen_cents, status, version)
VALUES ('platform:settlement', 0, 0, 0, 0)
ON CONFLICT (user_id) DO NOTHING;

-- 业务账号权限（幂等）：先建角色（无口令，口令由 migrate 程序另行设置），再授权，最后对 append-only 表显式收回 UPDATE/DELETE
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'fund_app') THEN
    CREATE ROLE fund_app LOGIN;
  END IF;
END $$;
-- CONNECT 默认授予 PUBLIC，fund_app 登录后可连接；此处不以函数表达式授权（GRANT ON DATABASE 不接受表达式）。
GRANT USAGE ON SCHEMA public TO fund_app;
GRANT SELECT, INSERT, UPDATE ON wallet_account, wallet_txn, wallet_order, wallet_freeze, wallet_tx_password, wallet_withdraw_method, wallet_reconcile TO fund_app;
GRANT SELECT, INSERT ON wallet_ledger, wallet_audit_log TO fund_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO fund_app;
REVOKE UPDATE, DELETE ON wallet_ledger, wallet_audit_log FROM fund_app;

COMMIT;
