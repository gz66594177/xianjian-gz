-- Phase 2 回滚：仅删找回申请单表。执行前必须备份；回滚后旧申请单失效（须重新申请）。
DROP TABLE IF EXISTS wallet_tx_password_recovery;
