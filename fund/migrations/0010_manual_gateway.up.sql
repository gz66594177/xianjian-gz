-- 仙聊资金服务迁移 0010：手动充值 + 网关支付（仅动资金库 xianliao_fund，须以管理员账号执行）
-- 背景：App 内原回调充值展示被两条新通道取代（回调接口本身不动、留给未来商户渠道）：
--   ① 手动充值：后台配置收款方式（微信/支付宝/银行卡）+ 二维码，用户线下付款后传支付截图，管理员双人复核通过才入账；
--   ② 网关支付：后端建单返回网关收银台跳转地址，用户在网关付款后 App 只轮询状态，入账只走网关服务端回调验签。
-- 订单口径：充值订单继续复用 wallet_order（order_type=1），新增 recharge_kind 区分三类：
--   'callback'（Phase 8 既有回调单，历史行默认回填该值）、'manual'（手动单）、'gateway'（网关单）。
--   channel 列 CHECK 扩到 manual/gateway（仍要求 order_type=1 的行 channel 非空；回调单的 mock/alipay/wechat
--   语义不变，manual/gateway 只是订单来源标记，不代表渠道验签方）。
-- 状态机（按 recharge_kind 分命名空间，查询须恒带 kind 过滤）：
--   manual：10 待上传凭证 -> 11 待审核 -> 1 已入账；11 -> 12 已拒绝（拒绝不入账）。
--   gateway：0 待支付 -> 1 已支付（仅回调验签通过）/ 2 失败；0 -> 4 超时关闭（过期回调拒绝入账并关单）。
--   callback：0/1/2/3 不变（见 0008）。
-- 入账分录：手动审核通过与网关回调入账均走 txn type 20（用户可用 CR / 平台结算 DR，ref_type='recharge'），
-- 与 Phase 8 回调入账同模型；截图与二维码走 fund_upload_file + 磁盘存储，绝不做公开直链。
-- 金额全部 BIGINT 分；ledger / audit_log 仍 append-only；本迁移只做加表加字段，不改现有数据。
BEGIN;

-- wallet_order 扩展：kind 区分 + 手动方式引用 + 网关引用/收银台地址/过期时间。
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS recharge_kind VARCHAR(16) NOT NULL DEFAULT 'callback'
    CHECK (recharge_kind IN ('callback', 'manual', 'gateway'));
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS payment_method_id VARCHAR(128);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS gateway_ref VARCHAR(256);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS cashier_url VARCHAR(1024);
ALTER TABLE wallet_order ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;

-- channel 取值扩到 manual/gateway（0008 的列级 CHECK 名为 wallet_order_channel_check，见实测）。
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS wallet_order_channel_check;
ALTER TABLE wallet_order ADD CONSTRAINT wallet_order_channel_check
    CHECK (channel IS NULL OR channel IN ('mock', 'alipay', 'wechat', 'manual', 'gateway'));

-- 手动单必须引用收款方式；网关单必须有过期时间（建单时服务端写入，App 轮询据此展示过期态）。
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS ck_order_manual_method;
ALTER TABLE wallet_order ADD CONSTRAINT ck_order_manual_method
    CHECK (recharge_kind <> 'manual' OR payment_method_id IS NOT NULL);
ALTER TABLE wallet_order DROP CONSTRAINT IF EXISTS ck_order_gateway_expiry;
ALTER TABLE wallet_order ADD CONSTRAINT ck_order_gateway_expiry
    CHECK (recharge_kind <> 'gateway' OR expires_at IS NOT NULL);

CREATE INDEX IF NOT EXISTS ix_order_kind_status ON wallet_order (recharge_kind, status, created_at DESC);

-- 后台收款方式：类型 wechat/alipay/bank、展示名、二维码文件引用、启用开关、排序。
-- 二维码不存外链 URL：只存 fund_upload_file 的 file_id，取用走鉴权接口。
CREATE TABLE IF NOT EXISTS wallet_payment_method (
    method_id           VARCHAR(128) PRIMARY KEY,
    type                VARCHAR(32) NOT NULL CHECK (type IN ('wechat', 'alipay', 'bank')),
    display_name        VARCHAR(128) NOT NULL CHECK (display_name <> ''),
    qr_file_id          VARCHAR(128),
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order          INT NOT NULL DEFAULT 0,
    created_by_admin_id VARCHAR(128),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_paymethod_enabled_sort ON wallet_payment_method (enabled, sort_order, created_at);

-- 上传文件：截图与二维码共用。storage_path 由服务端生成（随机文件名），绝不含用户可控内容；
-- 取用一律走鉴权接口（本人或管理员；启用中收款方式的二维码可供已鉴权用户取用），绝不做公开直链。
CREATE TABLE IF NOT EXISTS fund_upload_file (
    file_id      VARCHAR(128) PRIMARY KEY,
    kind         VARCHAR(32) NOT NULL CHECK (kind IN ('qr', 'proof')),
    content_type VARCHAR(64) NOT NULL,
    size_bytes   BIGINT NOT NULL CHECK (size_bytes > 0),
    storage_path VARCHAR(512) NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 手动充值凭证：一单最多一条有效凭证（order_id 唯一）；待上传/待审核态可重传（替换旧文件）。
CREATE TABLE IF NOT EXISTS recharge_proof (
    proof_id            VARCHAR(128) PRIMARY KEY,
    order_id            VARCHAR(128) NOT NULL UNIQUE,
    file_id             VARCHAR(128) NOT NULL,
    uploaded_by_user_id VARCHAR(128) NOT NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS ix_proof_user ON recharge_proof (uploaded_by_user_id, created_at DESC);

-- 业务账号权限与 0001 口径一致（wallet_order 已授权 SELECT/INSERT/UPDATE；新表按需授权，
-- 凭证替换需 DELETE 旧文件行，故 fund_upload_file 与 recharge_proof 另授 DELETE）。
GRANT SELECT, INSERT, UPDATE ON wallet_payment_method TO fund_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON fund_upload_file, recharge_proof TO fund_app;

COMMIT;
