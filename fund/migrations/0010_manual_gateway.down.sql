-- 迁移 0010 回滚（仅测试/演练用；生产执行前须先确认无 manual/gateway 订单与新表数据需要保留）。
BEGIN;

DROP TABLE IF EXISTS recharge_proof;
DROP TABLE IF EXISTS fund_upload_file;
DROP TABLE IF EXISTS wallet_payment_method;
DROP INDEX IF EXISTS ix_order_kind_status;

ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS ck_order_gateway_expiry;
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS ck_order_manual_method;
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS wallet_order_channel_check;
ALTER TABLE wallet_order ADD CONSTRAINT wallet_order_channel_check
    CHECK (channel IS NULL OR channel IN ('mock', 'alipay', 'wechat'));
ALTER TABLE wallet_order DROP COLUMN IF EXISTS expires_at;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS cashier_url;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS gateway_ref;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS payment_method_id;
ALTER TABLE wallet_order DROP COLUMN IF EXISTS recharge_kind;

COMMIT;
