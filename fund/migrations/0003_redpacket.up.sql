-- 仙聊资金服务 Phase 3 迁移 0003：好友红包（单聊红包，仅动资金库）。
-- 过期规则（统一规则）：创建后 24 小时未领取即过期；过期退款由过期扫描（POST /v1/admin/redpackets/expire）
-- 执行，或领取时惰性触发；过期后红包状态只允许从 0（待领取）经退款事务变为 2（已过期退款）。
-- packet 状态：0 待领取 1 已领取 2 已过期退款。
-- 红包金额只存在服务端：IM 自定义消息体只放 packetId，绝不放金额；客户端不得指定领取金额。
BEGIN;

CREATE TABLE IF NOT EXISTS redpacket_packet (
    packet_id       VARCHAR(128) PRIMARY KEY,
    sender_id       VARCHAR(128) NOT NULL,
    receiver_id     VARCHAR(128) NOT NULL,
    amount_cents    BIGINT NOT NULL CHECK (amount_cents > 0),
    greeting        VARCHAR(200) NOT NULL DEFAULT '',
    status          SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2)),
    freeze_id       VARCHAR(128) NOT NULL,
    send_txn_id     VARCHAR(128) NOT NULL,
    claim_txn_id    VARCHAR(128),
    refund_txn_id   VARCHAR(128),
    idempotency_key VARCHAR(256),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    claimed_at      TIMESTAMPTZ,
    refunded_at     TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_packet_sender_receiver CHECK (sender_id <> receiver_id)
);
CREATE INDEX IF NOT EXISTS ix_packet_sender ON redpacket_packet (sender_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_packet_receiver ON redpacket_packet (receiver_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_packet_status_expires ON redpacket_packet (status, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS ux_packet_sender_idem ON redpacket_packet (sender_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';

CREATE TABLE IF NOT EXISTS redpacket_claim (
    claim_id     VARCHAR(128) PRIMARY KEY,
    packet_id    VARCHAR(128) NOT NULL,
    claimer_id   VARCHAR(128) NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    claimed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 好友红包一人一包：同一 packet 只允许一条领取记录（第二道防线；第一道为领取事务内的 packet 行锁 + 状态 CAS）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_claim_packet ON redpacket_claim (packet_id);
CREATE INDEX IF NOT EXISTS ix_claim_claimer ON redpacket_claim (claimer_id, claimed_at DESC);

-- packet/claim 为业务单据（状态机流转需 UPDATE），不在 append-only 之列；
-- ledger / audit_log 仍禁 UPDATE/DELETE（见 0001）。
GRANT SELECT, INSERT, UPDATE ON redpacket_packet, redpacket_claim TO fund_app;

COMMIT;
