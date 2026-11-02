-- 仙聊资金服务 Phase 9 迁移 0009 回滚（仅动资金库；执行前必须备份；回滚删提现配置表与扩展列）。
-- 注意：回滚会丢弃提现订单的冻结追溯与打款凭证字段值；已进入 3/4/5/6 状态的提现单
-- 在回滚后无法再用 Phase 9 接口流转——生产回滚前须先确认无处理中提现单。
BEGIN;

DROP INDEX IF EXISTS ix_order_withdraw_user;
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS ck_order_withdraw_payout;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS payout_note;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS payout_operator_id;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS payout_at;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS payout_proof_no;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS freeze_id;
DROP TABLE IF EXISTS fund_withdraw_config;

COMMIT;
