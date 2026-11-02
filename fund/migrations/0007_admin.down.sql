-- 仙聊资金服务 Phase 7 迁移 0007 回滚（删表与提现审核字段；执行前必须备份）
BEGIN;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS reviewed_at;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS review_note;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS rejected_by_admin_id;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS review_second_admin_id;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS review_first_admin_id;
DROP TABLE IF EXISTS fund_risk_limit;
DROP TABLE IF EXISTS fund_admin_adjustment;
COMMIT;
