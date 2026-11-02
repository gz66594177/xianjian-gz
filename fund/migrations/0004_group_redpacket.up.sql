-- 仙聊资金服务 Phase 4 迁移 0004：群普通红包（定额均分，仅动资金库）。
-- 过期规则（统一规则，与好友红包一致）：创建后 24 小时未领完即过期；剩余金额经过期扫描
-- （POST /v1/admin/group-redpackets/expire）或领取时惰性触发退款，幂等 CAS + 退款幂等键互斥。
-- packet 状态：0 待领取（未领完） 1 已领完 2 已过期退款（可能为部分领取后的剩余退款）。
-- 均分规则：建包时服务端预拆分。base = total / count（整数除法），rem = total % count；
-- 前 rem 份各为 base+1，其余为 base；总和严格等于总金额，每份 ≥ 1 分（故要求 total >= count，
-- 否则建包拒绝）。份序 seq 从 1 开始，领取按 seq 升序取最早未领份。
-- 领取人归属边界（见 API 文档 §7）：资金服务侧只校验“包内 receiver 集合”——发包时若传
-- member_ids，则领取人必须在集合内（集合落表 group_redpacket_member）；若不传，则为开放包，
-- IM 群成员关系核验由 Phase 10/11 的 chat/App 层在发包前调 OpenIM 群接口核验，资金服务不直读 IM 库表。
-- 资金流转（与好友红包同模型）：发包 txn type 12（发送方可用 DR / 发送方冻结 CR）；
-- 领取 txn type 13（发送方冻结 DR / 领取方可用 CR，按份金额）；过期退款 txn type 14
-- （发送方冻结 DR / 发送方可用 CR，按剩余金额）。领取幂等键 grp-claim-<packet_id>-<claimer_id>，
-- 退款幂等键 grp-refund-<packet_id>。
BEGIN;

CREATE TABLE IF NOT EXISTS group_redpacket_packet (
    packet_id           VARCHAR(128) PRIMARY KEY,
    sender_id           VARCHAR(128) NOT NULL,
    group_id            VARCHAR(128) NOT NULL,
    total_amount_cents  BIGINT NOT NULL CHECK (total_amount_cents > 0),
    total_count         INT NOT NULL CHECK (total_count > 0),
    claimed_count       INT NOT NULL DEFAULT 0 CHECK (claimed_count >= 0),
    claimed_amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (claimed_amount_cents >= 0),
    greeting            VARCHAR(200) NOT NULL DEFAULT '',
    status              SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1, 2)),
    freeze_id           VARCHAR(128) NOT NULL,
    send_txn_id         VARCHAR(128) NOT NULL,
    refund_txn_id       VARCHAR(128),
    idempotency_key     VARCHAR(256),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at          TIMESTAMPTZ NOT NULL,
    claimed_at          TIMESTAMPTZ,
    refunded_at         TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_grp_total_count CHECK (total_amount_cents >= total_count),
    CONSTRAINT chk_grp_claimed_count CHECK (claimed_count <= total_count),
    CONSTRAINT chk_grp_claimed_amount CHECK (claimed_amount_cents <= total_amount_cents)
);
CREATE INDEX IF NOT EXISTS ix_grp_packet_sender ON group_redpacket_packet (sender_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_grp_packet_group ON group_redpacket_packet (group_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_grp_packet_status_expires ON group_redpacket_packet (status, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS ux_grp_packet_sender_idem ON group_redpacket_packet (sender_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';

CREATE TABLE IF NOT EXISTS group_redpacket_share (
    share_id     VARCHAR(128) PRIMARY KEY,
    packet_id    VARCHAR(128) NOT NULL,
    seq          INT NOT NULL CHECK (seq > 0),
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    status       SMALLINT NOT NULL DEFAULT 0 CHECK (status IN (0, 1)),
    claimer_id   VARCHAR(128),
    claim_id     VARCHAR(128),
    claimed_at   TIMESTAMPTZ
);
-- 同一包内份序唯一（预拆分份数 = total_count 的第一道约束；第二道为包行锁 + claimed_count CAS）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_grp_share_packet_seq ON group_redpacket_share (packet_id, seq);
CREATE INDEX IF NOT EXISTS ix_grp_share_packet_status ON group_redpacket_share (packet_id, status, seq);

CREATE TABLE IF NOT EXISTS group_redpacket_claim (
    claim_id     VARCHAR(128) PRIMARY KEY,
    packet_id    VARCHAR(128) NOT NULL,
    claimer_id   VARCHAR(128) NOT NULL,
    share_id     VARCHAR(128) NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    claimed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 每人限领一次（第二道防线；第一道为领取事务内的包行锁 + 领取前重复领取回查）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_grp_claim_packet_claimer ON group_redpacket_claim (packet_id, claimer_id);
CREATE INDEX IF NOT EXISTS ix_grp_claim_claimer ON group_redpacket_claim (claimer_id, claimed_at DESC);

-- 包内 receiver 集合（发包时可选传入 member_ids；为空表示开放包，见文件头边界说明）。
CREATE TABLE IF NOT EXISTS group_redpacket_member (
    packet_id VARCHAR(128) NOT NULL,
    user_id   VARCHAR(128) NOT NULL,
    PRIMARY KEY (packet_id, user_id)
);

-- packet/share/claim/member 为业务单据（状态机流转需 UPDATE），不在 append-only 之列；
-- ledger / audit_log 仍禁 UPDATE/DELETE（见 0001）。
GRANT SELECT, INSERT, UPDATE ON group_redpacket_packet, group_redpacket_share, group_redpacket_claim, group_redpacket_member TO fund_app;

COMMIT;
