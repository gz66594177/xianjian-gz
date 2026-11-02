-- Phase 2：交易密码安全找回申请单（双人复核 + 用户本人重置）。
-- 状态：0 待复核 1 双人通过 2 已使用 3 已拒绝 4 已过期。
-- 身份核验材料（实名、后 4 位）不落库：核验时实时比对已绑方式的解密账号，申请单只存流程状态。
-- 本表为流程表（非资金流水），业务账号可 UPDATE 状态机字段；密码与哈希绝不出现在此表。
CREATE TABLE IF NOT EXISTS wallet_tx_password_recovery (
    request_id      VARCHAR(128) PRIMARY KEY,
    user_id         VARCHAR(128) NOT NULL,
    status          SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2, 3, 4)),
    first_admin_id  VARCHAR(128),
    second_admin_id VARCHAR(128),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_recovery_user_created ON wallet_tx_password_recovery (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_recovery_status ON wallet_tx_password_recovery (status);
