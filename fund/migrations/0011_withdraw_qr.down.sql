-- 0011 回滚：删二维码列；账号密文列仅在无空值时恢复 NOT NULL（已有二维码方式行时不强制恢复，
-- 避免回滚删业务数据；回滚后二维码方式行将失去二维码引用，须人工处理）。
BEGIN;

ALTER TABLE wallet_withdraw_method DROP COLUMN IF EXISTS qr_file_id;
DROP INDEX IF EXISTS ux_method_one_per_type;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM wallet_withdraw_method WHERE account_no_enc IS NULL) THEN
        ALTER TABLE wallet_withdraw_method ALTER COLUMN account_no_enc SET NOT NULL;
    END IF;
END $$;

COMMIT;
