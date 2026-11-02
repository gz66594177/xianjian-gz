-- 0015：交易密码管理员发起重置：记录发起人与理由。
-- 审批语义不变：记录仍为待复核（status=0），须两名不同管理员先后 approve 后，
-- 用户本人才可用现有 /v1/tx-password/recover/reset 自设新密码；发起人不占审批位，
-- 发起本身不计入审批，first_admin_id/second_admin_id 只由 approve 写入。
-- 密码与哈希绝不出现在此表；reason 只在管理端可见，用户侧列表结构不变、不返回 reason。
ALTER TABLE wallet_tx_password_recovery ADD COLUMN IF NOT EXISTS initiated_by_admin_id VARCHAR(128);
ALTER TABLE wallet_tx_password_recovery ADD COLUMN IF NOT EXISTS reason TEXT;
CREATE INDEX IF NOT EXISTS ix_recovery_initiated_by ON wallet_tx_password_recovery (initiated_by_admin_id);

GRANT SELECT, INSERT, UPDATE ON wallet_tx_password_recovery TO fund_app;
