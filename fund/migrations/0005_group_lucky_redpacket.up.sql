-- 仙聊资金服务 Phase 5 迁移 0005：群拼手气红包（随机预拆分，仅动资金库）。
-- 过期规则（统一规则，与好友红包一致）：创建后 24 小时未领完即过期；剩余金额经过期扫描
-- （POST /v1/admin/group-lucky-redpackets/expire）或领取时惰性触发退款，幂等 CAS + 退款幂等键互斥。
-- packet 状态：0 待领取（未领完） 1 已领完 2 已过期退款（可能为部分领取后的剩余退款）。
-- 拼手气规则：建包时服务端用 crypto/rand 安全随机预拆分并落表（份金额只存在服务端，
-- 建包时即确定，事后可按份行核对总和；领取时绝不现算现分，客户端绝不能指定领取金额）。
-- 算法（二倍均值法，见 group_lucky_redpacket.go splitLucky）：对前 count-1 份，剩余金额 R、
-- 剩余份数 k 时，份额在 [1, min(2*floor(R/k)-1, R-k+1)] 内均匀随机；最后一份取剩余全部。
-- 硬约束：每份 ≥ 1 分；总和严格等于总金额；无负数；每份 ≤ 当时剩余金额且为其余份各留 ≥ 1 分
-- （故要求 total >= count，否则建包拒绝）。份序 seq 从 1 开始，领取按 seq 升序取最早未领份。
-- 手气最佳：金额最大的份；金额并列时最早领取者为手气最佳（领取时间升序、claim_id 升序决胜）。
-- 领取人归属边界（见 API 文档 §8）：资金服务侧只校验“包内 receiver 集合”——发包时若传
-- member_ids，则领取人必须在集合内（集合落表 group_lucky_redpacket_member）；若不传，则为开放包，
-- IM 群成员关系核验由 Phase 10/11 的 chat/App 层在发包前调 OpenIM 群接口核验，资金服务不直读 IM 库表。
-- 资金流转（与群普通红包同模型，表分离）：发包 txn type 15（发送方可用 DR / 发送方冻结 CR）；
-- 领取 txn type 16（发送方冻结 DR / 领取方可用 CR，按预拆分份金额）；过期退款 txn type 17
-- （发送方冻结 DR / 发送方可用 CR，按剩余金额）。领取幂等键 lky-claim-<packet_id>-<claimer_id>，
-- 退款幂等键 lky-refund-<packet_id>。ref_type='group_lucky_redpacket'。
BEGIN;

CREATE TABLE IF NOT EXISTS group_lucky_redpacket_packet (
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
    CONSTRAINT chk_lky_total_count CHECK (total_amount_cents >= total_count),
    CONSTRAINT chk_lky_claimed_count CHECK (claimed_count <= total_count),
    CONSTRAINT chk_lky_claimed_amount CHECK (claimed_amount_cents <= total_amount_cents)
);
CREATE INDEX IF NOT EXISTS ix_lky_packet_sender ON group_lucky_redpacket_packet (sender_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_lky_packet_group ON group_lucky_redpacket_packet (group_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ix_lky_packet_status_expires ON group_lucky_redpacket_packet (status, expires_at);
CREATE UNIQUE INDEX IF NOT EXISTS ux_lky_packet_sender_idem ON group_lucky_redpacket_packet (sender_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';

CREATE TABLE IF NOT EXISTS group_lucky_redpacket_share (
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
CREATE UNIQUE INDEX IF NOT EXISTS ux_lky_share_packet_seq ON group_lucky_redpacket_share (packet_id, seq);
CREATE INDEX IF NOT EXISTS ix_lky_share_packet_status ON group_lucky_redpacket_share (packet_id, status, seq);

CREATE TABLE IF NOT EXISTS group_lucky_redpacket_claim (
    claim_id     VARCHAR(128) PRIMARY KEY,
    packet_id    VARCHAR(128) NOT NULL,
    claimer_id   VARCHAR(128) NOT NULL,
    share_id     VARCHAR(128) NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    claimed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 每人限领一次（第二道防线；第一道为领取事务内的包行锁 + 领取前重复领取回查）。
CREATE UNIQUE INDEX IF NOT EXISTS ux_lky_claim_packet_claimer ON group_lucky_redpacket_claim (packet_id, claimer_id);
CREATE INDEX IF NOT EXISTS ix_lky_claim_claimer ON group_lucky_redpacket_claim (claimer_id, claimed_at DESC);

-- 包内 receiver 集合（发包时可选传入 member_ids；为空表示开放包，见文件头边界说明）。
CREATE TABLE IF NOT EXISTS group_lucky_redpacket_member (
    packet_id VARCHAR(128) NOT NULL,
    user_id   VARCHAR(128) NOT NULL,
    PRIMARY KEY (packet_id, user_id)
);

-- packet/share/claim/member 为业务单据（状态机流转需 UPDATE），不在 append-only 之列；
-- ledger / audit_log 仍禁 UPDATE/DELETE（见 0001）。
GRANT SELECT, INSERT, UPDATE ON group_lucky_redpacket_packet, group_lucky_redpacket_share, group_lucky_redpacket_claim, group_lucky_redpacket_member TO fund_app;

COMMIT;
