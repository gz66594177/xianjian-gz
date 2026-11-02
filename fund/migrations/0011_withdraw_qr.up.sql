-- 仙聊资金服务迁移 0011：提现方式收款二维码（微信/支付宝二选一代替账号）+ 每种方式类型每账号最多绑一次（服务端兜底）。
-- 背景：此前提现方式只支持填账号（支付宝/微信账号、银行卡号），且同一类型可绑多条；
-- 本迁移后：微信/支付宝可传收款二维码文件替代账号，银行卡仍只填卡号；二维码文件走
-- fund_upload_file（kind='qr'）+ 磁盘存储，取用一律走鉴权接口 GET /v1/uploads/<file_id>，
-- 绝不做公开直链、不把磁盘路径暴露给客户端；二维码不代表商户 API 自动打款，只供人工打款时查看。
-- 本迁移只做加字段与放宽账号密文非空约束，不改现有数据；现有已绑定方式的账号密文不受影响。
BEGIN;

ALTER TABLE wallet_withdraw_method ADD COLUMN IF NOT EXISTS qr_file_id VARCHAR(128);

-- 二维码方式下账号为空，故账号密文列必须可空；已有行的密文值保持原样。
ALTER TABLE wallet_withdraw_method ALTER COLUMN account_no_enc DROP NOT NULL;

-- 同一用户同一类型最多一条正常方式的兜底（App 层先拦、服务端再拦；历史库若已有同类型多条，
-- 该唯一索引建不上，届时须先人工清理重复行再重跑——本迁移不删任何业务行）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_method_one_per_type
    ON wallet_withdraw_method (user_id, type) WHERE status = 0;

COMMIT;
