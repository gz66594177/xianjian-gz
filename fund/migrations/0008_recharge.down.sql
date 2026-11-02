-- 仙聊资金服务 Phase 8 迁移 0008 回滚（删充值专属字段与索引；执行前必须备份）
-- 注意：回滚会丢失已建充值单的渠道/支付 txn 关联信息；wallet_order 行本身保留（仅删列）。
BEGIN;
DROP INDEX IF EXISTS ix_order_recharge;
DROP INDEX IF EXISTS ux_order_user_client_idem;
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS ck_order_recharge_channel;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS client_idempotency_key;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS failure_reason;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS refund_txn_id;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS paid_txn_id;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS channel;
COMMIT;
