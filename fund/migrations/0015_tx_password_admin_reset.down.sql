-- 0015 回滚：删发起人/理由列（找回记录本身保留）。
ALTER TABLE wallet_tx_password_recovery DROP COLUMN IF EXISTS reason;
ALTER TABLE wallet_tx_password_recovery DROP COLUMN IF EXISTS initiated_by_admin_id;
