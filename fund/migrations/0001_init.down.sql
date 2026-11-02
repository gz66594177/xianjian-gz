-- 仙聊资金服务 Phase 1 迁移 0001 回滚（仅动资金库；回滚会删除全部资金表数据，生产执行前必须备份）
-- 注意：不删除 fund_app 角色（角色为库级对象、且重上迁移会复用）；只删表，up 可重建并重设权限与平台户。
BEGIN;
DROP TABLE IF EXISTS wallet_reconcile;
DROP TABLE IF EXISTS wallet_audit_log;
DROP TABLE IF EXISTS wallet_withdraw_method;
DROP TABLE IF EXISTS wallet_tx_password;
DROP TABLE IF EXISTS wallet_freeze;
DROP TABLE IF EXISTS wallet_order;
DROP TABLE IF EXISTS wallet_ledger;
DROP TABLE IF EXISTS wallet_txn;
DROP TABLE IF EXISTS wallet_account;
COMMIT;
