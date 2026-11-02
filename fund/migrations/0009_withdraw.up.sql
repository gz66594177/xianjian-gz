-- 仙聊资金服务 Phase 9 迁移 0009（仅动资金库 xianliao_fund，须以管理员账号执行）
-- 提现 7 态全流程：复用 wallet_order（order_type=2），不另建订单表；本迁移只做最小扩展。
-- 订单状态机（仅 order_type=2 的行）：0 待审核 -> 1 已通过 -> 3 处理中 -> 4 成功；
-- 0 待审核 -> 2 已拒绝（解冻）；3 处理中 -> 5 失败 -> 6 已退款（解冻）。
-- 合法流转只有上述 5 条；其余流转一律拒绝（审核双人复核 0->1 / 拒绝 0->2 沿用 Phase 7）。
-- 金额全部 BIGINT 分；ledger / audit_log 仍 append-only；订单为业务单据（状态机需 UPDATE），不在 append-only 之列。
-- 人工打款为无商户资质时的唯一打款路径：财务用对公账户/企业网银手工转账后，
-- 管理端回填打款凭证（payout_proof_no / payout_at / payout_operator_id）并确认成功；
-- 严禁个人号垫付为常规流程。自动打款渠道只留接口骨架（501），不连真实渠道端点、不写真实商户密钥。
BEGIN;

-- 提现订单扩展字段：冻结追溯 + 人工打款凭证。
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS freeze_id VARCHAR(128);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS payout_proof_no VARCHAR(256);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS payout_at TIMESTAMPTZ;
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS payout_operator_id VARCHAR(128);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS payout_note VARCHAR(500);

-- 提现成功后必填打款凭证（历史 Phase 7 种子单无冻结、状态恒为 0/1/2，不受此约束影响，
-- 约束只对 status=4 的行生效；人工打款确认接口保证回填后才置 4）。
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS ck_order_withdraw_payout;
ALTER TABLE wallet_order ADD CONSTRAINT ck_order_withdraw_payout
    CHECK (order_type <> 2 OR status <> 4 OR payout_proof_no IS NOT NULL);

-- 提现手续费与限额配置：单行 key='default'。固定费用 + 费率万分比（默认均为 0）；
-- 费率按向下取整计算：fee = fixed_fee_cents + floor(amount_cents * rate_bp / 10000)。
-- 限额默认不限额（NULL）：min_amount_cents（>0 才生效）/ single_max_cents / daily_max_cents（>=0）/
-- daily_count_max（>0，单日提交笔数上限，统计口径见 API 文档 §14：当日已提交且未被拒绝的提现单）。
-- 运营建议值（文档值，非代码硬编码默认值）：单日笔数上限 5 笔；单笔/单日金额上限由运营按风控定。
CREATE TABLE IF NOT EXISTS fund_withdraw_config (
    config_key       VARCHAR(64) PRIMARY KEY,
    fixed_fee_cents  BIGINT NOT NULL DEFAULT 0 CHECK (fixed_fee_cents >= 0),
    rate_bp          BIGINT NOT NULL DEFAULT 0 CHECK (rate_bp >= 0 AND rate_bp <= 10000),
    min_amount_cents BIGINT CHECK (min_amount_cents IS NULL OR min_amount_cents > 0),
    single_max_cents BIGINT CHECK (single_max_cents IS NULL OR single_max_cents >= 0),
    daily_max_cents  BIGINT CHECK (daily_max_cents IS NULL OR daily_max_cents >= 0),
    daily_count_max  INT CHECK (daily_count_max IS NULL OR daily_count_max > 0),
    updated_by       VARCHAR(128),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO fund_withdraw_config (config_key, fixed_fee_cents, rate_bp)
VALUES ('default', 0, 0)
ON CONFLICT (config_key) DO NOTHING;

-- 提现用户查询：按用户 + 时间倒序（管理端已有 ix_order_type_status 覆盖类型/状态查询）。
CREATE INDEX IF NOT EXISTS ix_order_withdraw_user ON wallet_order (user_id, created_at DESC);

-- 业务账号权限与 0001 口径一致（wallet_order 已授权 SELECT/INSERT/UPDATE，无需重复 GRANT）。
GRANT SELECT, INSERT, UPDATE ON fund_withdraw_config TO fund_app;

COMMIT;
