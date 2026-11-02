-- 仙聊资金服务 Phase 8 迁移 0008（仅动资金库 xianliao_fund，须以管理员账号执行）
-- 充值订单复用 wallet_order（order_type=1）：本迁移只给该表加充值专属字段，不另建账本、
-- 不改动 OpenIM 聊天业务库。金额全部 BIGINT 分；ledger / audit_log 仍 append-only。
-- 充值状态机（仅 order_type=1 的行）：0 待支付 1 已支付 2 已失败 3 已退款；
-- 与提现 7 态（order_type=2）共用 status 列但状态命名空间按 order_type 区分，查询须恒带 order_type 过滤。
-- 渠道字段：'mock'（测试/开发模式，见 FUND_MOCK_PAYMENT 开关）/ 'alipay' / 'wechat'；
-- 真实渠道未签约前服务端只接受 mock 创建（见 recharge.go），alipay/wechat 行现阶段只存在于文档与验签骨架，
-- 绝不由本阶段代码写入真实渠道订单。
BEGIN;

ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS channel VARCHAR(32)
    CHECK (channel IS NULL OR channel IN ('mock', 'alipay', 'wechat'));
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS paid_txn_id VARCHAR(128);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS refund_txn_id VARCHAR(128);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS failure_reason VARCHAR(500);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS client_idempotency_key VARCHAR(256);

-- 充值行必须有渠道（历史行 order_type<>1 允许 channel 为空，避免回填误标提现单为 mock）。
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS ck_order_recharge_channel;
ALTER TABLE wallet_order ADD CONSTRAINT ck_order_recharge_channel
    CHECK (order_type <> 1 OR channel IS NOT NULL);

-- 创建幂等：同一用户同一客户端幂等键只建一单（空键除外；与 wallet_txn 的 partial unique 口径一致）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_order_user_client_idem ON wallet_order (user_id, client_idempotency_key)
    WHERE client_idempotency_key IS NOT NULL AND client_idempotency_key <> '';
-- 充值管理查询：按类型 + 渠道 + 时间倒序。
CREATE INDEX IF NOT EXISTS ix_order_recharge ON wallet_order (order_type, channel, created_at DESC);

-- 业务账号权限与 0001 口径一致（wallet_order 已授权 SELECT/INSERT/UPDATE，无需重复 GRANT）。

COMMIT;
