-- 0012 网关充值通道配置（面板可配开关；密钥绝不进本表）。
-- 一行 = 一个厂商：vendor 为厂商标识（主键），display_name 为面板展示名，
-- enabled 为通道开关，params 只放非密钥参数（如商户号、回调地址等公开标识）。
-- 密钥（API Key/私钥/签名密钥）一律只走服务端环境变量，绝不写入本表、
-- 绝不经管理接口返回、绝不进浏览器与日志；params 若含密钥类键名会被接口层拒绝。
CREATE TABLE IF NOT EXISTS wallet_gateway_channel (
    vendor              VARCHAR(64) PRIMARY KEY,
    display_name        VARCHAR(128) NOT NULL CHECK (display_name <> ''),
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    params              JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_by_admin_id VARCHAR(128),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 业务账号授权（与 0001/0010 惯例一致；通道不删除，故不授 DELETE）。
GRANT SELECT, INSERT, UPDATE ON wallet_gateway_channel TO fund_app;
