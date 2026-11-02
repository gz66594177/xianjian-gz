-- 0013：邮箱注册用户手机号绑定（格式 + 服务端唯一）+ 提现实名一致性（姓名基准
-- 由最早绑定的正常方式决定，见 methods.go，不新增姓名表）。
-- 手机号明文绝不落库：phone_enc 为 AES 密文，phone_hash = SHA256(area|phone)
-- 供跨账号唯一约束；bound_at 为绑定时间。手机号一旦绑定不得改（接口层拦）。
CREATE TABLE IF NOT EXISTS user_phone_binding (
    user_id    VARCHAR(128) PRIMARY KEY,
    area_code  VARCHAR(8) NOT NULL,
    phone_enc  TEXT NOT NULL,
    phone_hash CHAR(64) NOT NULL UNIQUE,
    bound_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

GRANT SELECT, INSERT ON user_phone_binding TO fund_app;
