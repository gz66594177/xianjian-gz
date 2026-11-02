package fund

// Phase 4：群普通红包（定额均分）。
// 资金模型：发包时发送方全额可用 -> 冻结（与发送方同一事务内建包 + 预拆分）；领取时按份金额
// 发送方冻结 -> 领取方可用；过期时剩余金额发送方冻结 -> 发送方可用（退款）。全部走双重记账、
// 同一 DB 事务，流水 append-only。
// 均分规则（服务端预拆分，写进迁移与 API 文档）：base = total / count（整数除法），
// rem = total % count；前 rem 份各为 base+1，其余为 base；总和严格等于总金额，每份 ≥ 1 分
// （故建包要求 total >= count，否则拒绝）。领取按 seq 升序取最早未领份，客户端绝不能指定金额。
// 红包消息载体：IM 自定义消息体只放 packetId，绝不放金额；金额、状态、领取记录全部以服务端为准。
// 过期规则（统一规则，与好友红包一致）：创建后 24 小时未领完即过期；剩余退款由管理端扫描执行，
// 或领取时惰性触发；详情查询不触发退款（GET 不写库），但返回计算出的 expired 标记。
// 领取人归属边界：资金服务侧只校验“包内 receiver 集合”——发包时若传 member_ids，领取人必须
// 在集合内（落表 group_redpacket_member，发送方恒在集合内）；若不传，则为开放包，IM 群成员
// 关系核验由 Phase 10/11 的 chat/App 层在发包前调 OpenIM 群接口核验，资金服务不直读 IM 库表。
// 领取方不重复验交易密码，但须已完成准入；未完成只返回引导，包状态与领取记录不动、不扣款。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	GroupPacketActive   = 0 // 待领取（未领完）
	GroupPacketDone     = 1 // 已领完
	GroupPacketRefunded = 2 // 已过期退款（含部分领取后的剩余退款）
)

const (
	TxnGroupPacketSend   = 12 // 群发包（可用 -> 冻结）
	TxnGroupPacketClaim  = 13 // 群领取（发送方冻结 -> 领取方可用，按份金额）
	TxnGroupPacketRefund = 14 // 群过期退款（冻结 -> 发送方可用，按剩余金额）
)

const (
	maxGroupPacketCount   = 1000 // 单包最大份数
	maxGroupPacketMembers = 1000 // 包内 receiver 集合上限
)

var (
	errGroupPacketNotFound  = errors.New("group red packet not found")
	errGroupPacketForbidden = errors.New("group red packet does not belong to viewer")
	errGroupPacketFull      = errors.New("group red packet fully claimed")
	errGroupPacketExpired   = errors.New("group red packet expired")
	errGroupNotMember       = errors.New("claimer is not in group red packet receiver set")
)

type GroupPacket struct {
	PacketID          string     `json:"packet_id"`
	SenderID          string     `json:"sender_id"`
	GroupID           string     `json:"group_id"`
	TotalAmountCents  int64      `json:"total_amount_cents"`
	TotalCount        int        `json:"total_count"`
	ClaimedCount      int        `json:"claimed_count"`
	ClaimedAmountCents int64     `json:"claimed_amount_cents"`
	RemainingCount    int        `json:"remaining_count"`    // 计算字段
	RemainingAmountCents int64   `json:"remaining_amount_cents"` // 计算字段
	Greeting          string     `json:"greeting"`
	Status            int        `json:"status"` // 0 待领取 1 已领完 2 已过期退款
	Expired           bool       `json:"expired"` // 计算字段：待领取且已过 expires_at
	CreatedAt         time.Time  `json:"created_at"`
	ExpiresAt         time.Time  `json:"expires_at"`
	ClaimedAt         *time.Time `json:"claimed_at"`
	RefundedAt        *time.Time `json:"refunded_at"`
	freezeID          string     // 内部用，不序列化
}

type GroupShare struct {
	ShareID     string     `json:"share_id"`
	PacketID    string     `json:"packet_id"`
	Seq         int        `json:"seq"`
	AmountCents int64      `json:"amount_cents"`
	Status      int        `json:"status"` // 0 未领 1 已领
	ClaimerID   *string    `json:"claimer_id,omitempty"`
	ClaimedAt   *time.Time `json:"claimed_at,omitempty"`
}

type GroupClaim struct {
	ClaimID     string    `json:"claim_id"`
	PacketID    string    `json:"packet_id"`
	ClaimerID   string    `json:"claimer_id"`
	ShareID     string    `json:"share_id"`
	AmountCents int64     `json:"amount_cents"`
	ClaimedAt   time.Time `json:"claimed_at"`
}

// splitAverage：均分预拆分。base = total/count，rem = total%count；前 rem 份 base+1，其余 base。
// 调用方须保证 total >= count > 0，否则每份无法 ≥ 1 分。
func splitAverage(total int64, count int) []int64 {
	base := total / int64(count)
	rem := int(total % int64(count))
	out := make([]int64, count)
	for i := range out {
		out[i] = base
		if i < rem {
			out[i] = base + 1
		}
	}
	return out
}

func validGroupID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

const groupPacketColumns = `packet_id, sender_id, group_id, total_amount_cents, total_count, claimed_count, claimed_amount_cents, greeting, status, created_at, expires_at, claimed_at, refunded_at, freeze_id`

func scanGroupPacket(row interface {
	Scan(...any) error
}) (*GroupPacket, error) {
	var p GroupPacket
	if err := row.Scan(&p.PacketID, &p.SenderID, &p.GroupID, &p.TotalAmountCents, &p.TotalCount, &p.ClaimedCount,
		&p.ClaimedAmountCents, &p.Greeting, &p.Status, &p.CreatedAt, &p.ExpiresAt, &p.ClaimedAt, &p.RefundedAt, &p.freezeID); err != nil {
		return nil, err
	}
	p.RemainingCount = p.TotalCount - p.ClaimedCount
	p.RemainingAmountCents = p.TotalAmountCents - p.ClaimedAmountCents
	if p.Status == GroupPacketActive && time.Now().After(p.ExpiresAt) {
		p.Expired = true
	}
	return &p, nil
}

func loadGroupPacketTx(ctx context.Context, tx *sql.Tx, packetID string, forUpdate bool) (*GroupPacket, error) {
	q := `SELECT ` + groupPacketColumns + ` FROM group_redpacket_packet WHERE packet_id=$1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	row := tx.QueryRowContext(ctx, q, packetID)
	p, err := scanGroupPacket(row)
	if err == sql.ErrNoRows {
		return nil, errGroupPacketNotFound
	}
	return p, err
}

func loadGroupPacketBySenderIdem(ctx context.Context, q rowQuerier, senderID, idemKey string) (*GroupPacket, error) {
	row := q.QueryRowContext(ctx, `SELECT `+groupPacketColumns+` FROM group_redpacket_packet WHERE sender_id=$1 AND idempotency_key=$2`, senderID, idemKey)
	p, err := scanGroupPacket(row)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return p, err
}

func loadGroupClaimTx(ctx context.Context, tx *sql.Tx, packetID, claimerID string) (*GroupClaim, error) {
	var c GroupClaim
	err := tx.QueryRowContext(ctx, `SELECT claim_id, packet_id, claimer_id, share_id, amount_cents, claimed_at FROM group_redpacket_claim WHERE packet_id=$1 AND claimer_id=$2`, packetID, claimerID).
		Scan(&c.ClaimID, &c.PacketID, &c.ClaimerID, &c.ShareID, &c.AmountCents, &c.ClaimedAt)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return &c, err
}

// sendGroupPacket：群发包。发送方须先通过准入与交易密码（HTTP 层守卫，本函数只做资金与建包）。
// 同一事务：幂等回查 -> 发送方行锁 -> 记账（可用 DR / 冻结 CR）-> 冻结行 -> 包行 -> 预拆分份行
// -> receiver 集合行 -> 审计。并发重复发送：幂等键命中时回滚并返回原包，不重复扣款。
func (s *Server) sendGroupPacket(ctx context.Context, senderID, groupID string, total int64, count int, greeting, idemKey string, memberIDs []string) (*GroupPacket, error) {
	if !validGroupID(groupID) {
		return nil, errors.New("invalid group_id")
	}
	if total <= 0 {
		return nil, errors.New("total_amount_cents must be > 0")
	}
	if count <= 0 || count > maxGroupPacketCount {
		return nil, fmt.Errorf("total_count must be 1..%d", maxGroupPacketCount)
	}
	if total < int64(count) {
		return nil, errors.New("total_amount_cents must be >= total_count so every share is at least 1 cent")
	}
	if utf8.RuneCountInString(greeting) > 200 {
		return nil, errors.New("greeting too long, max 200 chars")
	}
	// receiver 集合：去重 + 上限 + 发送方恒在集合内（发送方可领自己的群包，与微信群包一致）。
	members := []string{}
	if memberIDs != nil {
		seen := map[string]bool{}
		for _, m := range memberIDs {
			if !validUserID(m) {
				return nil, fmt.Errorf("invalid member_id: %s", m)
			}
			if !seen[m] {
				seen[m] = true
				members = append(members, m)
			}
		}
		if !seen[senderID] {
			members = append(members, senderID)
		}
		if len(members) > maxGroupPacketMembers {
			return nil, fmt.Errorf("member_ids too many, max %d", maxGroupPacketMembers)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if idemKey != "" {
		if existing, err := loadGroupPacketBySenderIdem(ctx, tx, senderID, idemKey); err == nil {
			_ = tx.Rollback()
			return existing, nil
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}

	var st accountState
	if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, senderID).
		Scan(&st.balance, &st.frozen, &st.version, &st.status); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("wallet not found: %s", senderID)
		}
		return nil, err
	}
	if st.status != 0 {
		return nil, fmt.Errorf("wallet not normal: %s", senderID)
	}
	balAfter, frzAfter := st.balance-total, st.frozen+total
	if balAfter < 0 {
		return nil, fmt.Errorf("insufficient available balance for %s", senderID)
	}

	txnID := newID("txn_")
	freezeID := newID("frz_")
	packetID := newID("grp_")
	now := time.Now()
	expires := now.Add(packetExpiry)
	shares := splitAverage(total, count)

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'group_redpacket',$4,1,$5)`,
		txnID, senderID, TxnGroupPacketSend, packetID, nullStr(idemKey)); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			existing, err2 := loadGroupPacketBySenderIdem(ctx, s.db, senderID, idemKey)
			if err2 != nil {
				return nil, err2
			}
			return existing, nil
		}
		return nil, err
	}
	ledgerRows := []struct {
		acctType int
		dir      int
		oppUID   string
		oppAcc   string
	}{
		{AcctAvailable, DirDebit, senderID, "frozen"},
		{AcctFrozen, DirCredit, senderID, "available"},
	}
	for _, lr := range ledgerRows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, senderID, lr.acctType, lr.dir, total, balAfter, frzAfter, lr.oppUID, lr.oppAcc); err != nil {
			return nil, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, balAfter, frzAfter, senderID, st.version)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("optimistic lock failed for %s", senderID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_freeze (freeze_id, user_id, txn_id, amount_cents, remaining_cents, status) VALUES ($1,$2,$3,$4,$4,0)`, freezeID, senderID, txnID, total); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO group_redpacket_packet (packet_id, sender_id, group_id, total_amount_cents, total_count, greeting, status, freeze_id, send_txn_id, idempotency_key, expires_at)
		 VALUES ($1,$2,$3,$4,$5,$6,0,$7,$8,$9,$10)`,
		packetID, senderID, groupID, total, count, greeting, freezeID, txnID, nullStr(idemKey), expires); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			existing, err2 := loadGroupPacketBySenderIdem(ctx, s.db, senderID, idemKey)
			if err2 != nil {
				return nil, err2
			}
			return existing, nil
		}
		return nil, err
	}
	for i, amt := range shares {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO group_redpacket_share (share_id, packet_id, seq, amount_cents, status) VALUES ($1,$2,$3,$4,0)`,
			newID("grs_"), packetID, i+1, amt); err != nil {
			return nil, err
		}
	}
	for _, m := range members {
		if _, err := tx.ExecContext(ctx, `INSERT INTO group_redpacket_member (packet_id, user_id) VALUES ($1,$2)`, packetID, m); err != nil {
			return nil, err
		}
	}
	if err := insertAudit(ctx, tx, senderID, senderID, "group_redpacket_send", fmt.Sprintf("group packet %s %d cents x %d", packetID, total, count), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &GroupPacket{PacketID: packetID, SenderID: senderID, GroupID: groupID, TotalAmountCents: total, TotalCount: count,
		Greeting: greeting, Status: GroupPacketActive, RemainingCount: count, RemainingAmountCents: total, CreatedAt: now, ExpiresAt: expires}, nil
}

// refundGroupPacketTx：在调用方事务内执行群包过期退款（调用方须已对包行加锁且确认 status=0）。
// 退剩余金额 = total - claimed_amount；冻结行 remaining 必须等于该值，否则直接失败、整笔回滚。
func (s *Server) refundGroupPacketTx(ctx context.Context, tx *sql.Tx, pkt *GroupPacket) (*Txn, error) {
	remaining := pkt.TotalAmountCents - pkt.ClaimedAmountCents
	if remaining <= 0 {
		return nil, fmt.Errorf("nothing to refund for group packet %s", pkt.PacketID)
	}
	var fUser string
	var frzRemaining int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id, remaining_cents FROM wallet_freeze WHERE freeze_id=$1 FOR UPDATE`, pkt.freezeID).
		Scan(&fUser, &frzRemaining); err != nil {
		return nil, err
	}
	if fUser != pkt.SenderID {
		return nil, errors.New("freeze does not belong to group packet sender")
	}
	if frzRemaining != remaining {
		return nil, fmt.Errorf("freeze remaining mismatch for group packet %s: remaining=%d expected=%d", pkt.PacketID, frzRemaining, remaining)
	}
	var st accountState
	if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, pkt.SenderID).
		Scan(&st.balance, &st.frozen, &st.version, &st.status); err != nil {
		return nil, err
	}
	if st.status != 0 {
		return nil, fmt.Errorf("wallet not normal: %s", pkt.SenderID)
	}
	balAfter, frzAfter := st.balance+remaining, st.frozen-remaining
	if frzAfter < 0 {
		return nil, fmt.Errorf("insufficient frozen balance for %s", pkt.SenderID)
	}
	txnID := newID("txn_")
	now := time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'group_redpacket',$4,1,$5)`,
		txnID, pkt.SenderID, TxnGroupPacketRefund, pkt.PacketID, "grp-refund-"+pkt.PacketID); err != nil {
		return nil, err
	}
	ledgerRows := []struct {
		acctType int
		dir      int
		oppUID   string
		oppAcc   string
	}{
		{AcctFrozen, DirDebit, pkt.SenderID, "available"},
		{AcctAvailable, DirCredit, pkt.SenderID, "frozen"},
	}
	for _, lr := range ledgerRows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, pkt.SenderID, lr.acctType, lr.dir, remaining, balAfter, frzAfter, lr.oppUID, lr.oppAcc); err != nil {
			return nil, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, balAfter, frzAfter, pkt.SenderID, st.version)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("optimistic lock failed for %s", pkt.SenderID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_freeze SET remaining_cents=0, status=1, updated_at=now() WHERE freeze_id=$1`, pkt.freezeID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE group_redpacket_packet SET status=2, refund_txn_id=$1, refunded_at=$2, updated_at=now() WHERE packet_id=$3 AND status=0`, txnID, now, pkt.PacketID); err != nil {
		return nil, err
	}
	if err := insertAudit(ctx, tx, pkt.SenderID, pkt.SenderID, "group_redpacket_refund", fmt.Sprintf("group packet %s %d cents expired refund", pkt.PacketID, remaining), "", ""); err != nil {
		return nil, err
	}
	return &Txn{TxnID: txnID, UserID: pkt.SenderID, Type: TxnGroupPacketRefund, Status: 1}, nil
}

// claimGroupPacket：群领取。同一事务：包行锁 -> 已领幂等回查 -> 终态/过期处理（过期先惰性退款）
// -> receiver 集合校验 -> 准入校验（不重复验交易密码）-> 冻结行锁 -> 取最早未领份（行锁）
// -> 双方账户锁（按 user_id 排序）-> 记账（发送方冻结 DR / 领取方可用 CR，按份金额）-> 份状态 + 领取行
// -> 包进度 CAS。并发领取由包行锁串行化，恰好 total_count 条领取行、金额总和等于已领总额。
func (s *Server) claimGroupPacket(ctx context.Context, claimerID, packetID string) (*GroupClaim, *GroupPacket, error) {
	if _, err := s.createWallet(ctx, claimerID); err != nil {
		return nil, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	pkt, err := loadGroupPacketTx(ctx, tx, packetID, true)
	if err != nil {
		return nil, nil, err
	}
	// 已领幂等回查优先：同一领取人重领返回原记录（即使包已领完或已过期退款，领取记录不动）。
	if existing, err := loadGroupClaimTx(ctx, tx, packetID, claimerID); err == nil {
		_ = tx.Rollback()
		return existing, pkt, nil
	} else if err != sql.ErrNoRows {
		return nil, nil, err
	}
	switch pkt.Status {
	case GroupPacketDone:
		return nil, nil, errGroupPacketFull
	case GroupPacketRefunded:
		return nil, nil, errGroupPacketExpired
	}
	if time.Now().After(pkt.ExpiresAt) {
		// 领取时惰性过期：先退剩余（发送方受益，不受领取人准入影响），再报过期。
		if _, err := s.refundGroupPacketTx(ctx, tx, pkt); err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		pkt.Status = GroupPacketRefunded
		now := time.Now()
		pkt.RefundedAt = &now
		pkt.Expired = false
		return nil, pkt, errGroupPacketExpired
	}
	// receiver 集合校验：集合非空时领取人必须在集合内；开放包（集合为空）跳过本校验。
	var memberCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_redpacket_member WHERE packet_id=$1`, packetID).Scan(&memberCount); err != nil {
		return nil, nil, err
	}
	if memberCount > 0 {
		var inSet int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_redpacket_member WHERE packet_id=$1 AND user_id=$2`, packetID, claimerID).Scan(&inSet); err != nil {
			return nil, nil, err
		}
		if inSet == 0 {
			return nil, nil, errGroupNotMember
		}
	}
	// 领包不重复验交易密码，但须已完成准入；未完成只返回引导，包状态不动、不扣款。
	st, err := s.financeStatus(ctx, claimerID)
	if err != nil {
		return nil, nil, err
	}
	if st.FinanceAccountStatus != "ok" {
		return nil, nil, &admissionFailure{guidance: st.Guidance}
	}

	expectedRemaining := pkt.TotalAmountCents - pkt.ClaimedAmountCents
	var fUser string
	var frzRemaining int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id, remaining_cents FROM wallet_freeze WHERE freeze_id=$1 FOR UPDATE`, pkt.freezeID).
		Scan(&fUser, &frzRemaining); err != nil {
		return nil, nil, err
	}
	if fUser != pkt.SenderID {
		return nil, nil, errors.New("freeze does not belong to group packet sender")
	}
	if frzRemaining != expectedRemaining {
		return nil, nil, fmt.Errorf("freeze remaining mismatch for group packet %s: remaining=%d expected=%d", pkt.PacketID, frzRemaining, expectedRemaining)
	}

	// 取最早未领份（行锁；包行锁已串行化并发领取，本锁防份行竞态）。
	var shareID string
	var shareSeq int
	var shareAmount int64
	if err := tx.QueryRowContext(ctx, `SELECT share_id, seq, amount_cents FROM group_redpacket_share WHERE packet_id=$1 AND status=0 ORDER BY seq LIMIT 1 FOR UPDATE`, packetID).
		Scan(&shareID, &shareSeq, &shareAmount); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, fmt.Errorf("group packet %s has no unclaimed share but status is active", packetID)
		}
		return nil, nil, err
	}
	_ = shareSeq

	// 发送方可领自己的群包（与微信群包一致）：同人时只锁一次行、只做一次账户更新
	// （balance += share、frozen -= share），否则两次乐观锁更新必有一方撞 version。
	users := []string{pkt.SenderID}
	if claimerID != pkt.SenderID {
		users = append(users, claimerID)
	}
	sort.Strings(users)
	states := map[string]*accountState{}
	for _, u := range users {
		var as accountState
		if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&as.balance, &as.frozen, &as.version, &as.status); err != nil {
			return nil, nil, err
		}
		if as.status != 0 {
			return nil, nil, fmt.Errorf("wallet not normal: %s", u)
		}
		states[u] = &as
	}
	senderSt, claimerSt := states[pkt.SenderID], states[claimerID]
	senderFrozenAfter := senderSt.frozen - shareAmount
	claimerBalAfter := claimerSt.balance + shareAmount
	if senderFrozenAfter < 0 {
		return nil, nil, fmt.Errorf("insufficient frozen balance for %s", pkt.SenderID)
	}

	txnID := newID("txn_")
	claimID := newID("grc_")
	now := time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'group_redpacket',$4,1,$5)`,
		txnID, claimerID, TxnGroupPacketClaim, packetID, "grp-claim-"+packetID+"-"+claimerID); err != nil {
		if isUniqueViolation(err) {
			return nil, nil, errGroupPacketFull
		}
		return nil, nil, err
	}
	type le struct {
		uid      string
		acctType int
		dir      int
		balAfter int64
		frzAfter int64
		oppUID   string
		oppAcc   string
	}
	entries := []le{
		{pkt.SenderID, AcctFrozen, DirDebit, senderSt.balance, senderFrozenAfter, claimerID, "available"},
		{claimerID, AcctAvailable, DirCredit, claimerBalAfter, claimerSt.frozen, pkt.SenderID, "frozen"},
	}
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, e.uid, e.acctType, e.dir, shareAmount, e.balAfter, e.frzAfter, e.oppUID, e.oppAcc); err != nil {
			return nil, nil, err
		}
	}
	acctUpdates := []struct {
		uid string
		bal int64
		frz int64
		ver int64
	}{}
	if pkt.SenderID == claimerID {
		acctUpdates = append(acctUpdates, struct {
			uid string
			bal int64
			frz int64
			ver int64
		}{pkt.SenderID, claimerBalAfter, senderFrozenAfter, senderSt.version})
	} else {
		acctUpdates = append(acctUpdates, struct {
			uid string
			bal int64
			frz int64
			ver int64
		}{pkt.SenderID, senderSt.balance, senderFrozenAfter, senderSt.version},
			struct {
				uid string
				bal int64
				frz int64
				ver int64
			}{claimerID, claimerBalAfter, claimerSt.frozen, claimerSt.version})
	}
	for _, au := range acctUpdates {
		res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, au.bal, au.frz, au.uid, au.ver)
		if err != nil {
			return nil, nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, nil, fmt.Errorf("optimistic lock failed for %s", au.uid)
		}
	}
	newClaimedCount := pkt.ClaimedCount + 1
	newClaimedAmount := pkt.ClaimedAmountCents + shareAmount
	newFreezeRemaining := frzRemaining - shareAmount
	newFreezeStatus := 0
	newPacketStatus := GroupPacketActive
	var claimedAt any
	if newClaimedCount == pkt.TotalCount {
		newFreezeStatus = 2 // 全部领完：冻结行已扣款完毕
		newPacketStatus = GroupPacketDone
		claimedAt = now
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_freeze SET remaining_cents=$1, status=$2, updated_at=now() WHERE freeze_id=$3`, newFreezeRemaining, newFreezeStatus, pkt.freezeID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE group_redpacket_share SET status=1, claimer_id=$1, claim_id=$2, claimed_at=$3 WHERE share_id=$4 AND status=0`, claimerID, claimID, now, shareID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO group_redpacket_claim (claim_id, packet_id, claimer_id, share_id, amount_cents) VALUES ($1,$2,$3,$4,$5)`, claimID, packetID, claimerID, shareID, shareAmount); err != nil {
		if isUniqueViolation(err) {
			return nil, nil, errGroupPacketFull
		}
		return nil, nil, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE group_redpacket_packet SET claimed_count=$1, claimed_amount_cents=$2, status=$3, claimed_at=$4, updated_at=now() WHERE packet_id=$5 AND status=0 AND claimed_count=$6`,
		newClaimedCount, newClaimedAmount, newPacketStatus, claimedAt, packetID, pkt.ClaimedCount)
	if err != nil {
		return nil, nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil, fmt.Errorf("group packet progress CAS failed for %s", packetID)
	}
	if err := insertAudit(ctx, tx, claimerID, claimerID, "group_redpacket_claim", fmt.Sprintf("group packet %s %d cents", packetID, shareAmount), "", ""); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	pkt.ClaimedCount = newClaimedCount
	pkt.ClaimedAmountCents = newClaimedAmount
	pkt.RemainingCount = pkt.TotalCount - newClaimedCount
	pkt.RemainingAmountCents = pkt.TotalAmountCents - newClaimedAmount
	pkt.Status = newPacketStatus
	if newPacketStatus == GroupPacketDone {
		pkt.ClaimedAt = &now
	}
	pkt.Expired = false
	return &GroupClaim{ClaimID: claimID, PacketID: packetID, ClaimerID: claimerID, ShareID: shareID, AmountCents: shareAmount, ClaimedAt: now}, pkt, nil
}

// expireGroupPacket：单个群包过期处理（管理端扫描用）。未到期或已终态时不改动、返回当前包与 expired=false。
func (s *Server) expireGroupPacket(ctx context.Context, packetID string) (*GroupPacket, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	pkt, err := loadGroupPacketTx(ctx, tx, packetID, true)
	if err != nil {
		return nil, false, err
	}
	if pkt.Status != GroupPacketActive {
		_ = tx.Rollback()
		return pkt, false, nil
	}
	if !time.Now().After(pkt.ExpiresAt) {
		_ = tx.Rollback()
		return pkt, false, nil
	}
	if _, err := s.refundGroupPacketTx(ctx, tx, pkt); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	pkt.Status = GroupPacketRefunded
	now := time.Now()
	pkt.RefundedAt = &now
	pkt.Expired = false
	pkt.RemainingAmountCents = 0
	pkt.RemainingCount = pkt.TotalCount - pkt.ClaimedCount
	return pkt, true, nil
}

// expireDueGroupPackets：批量过期扫描（每次最多 limit 个，按 expires_at 升序）。单个包失败不阻塞其余包。
func (s *Server) expireDueGroupPackets(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT packet_id FROM group_redpacket_packet WHERE status=0 AND expires_at <= now() ORDER BY expires_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	expired := []string{}
	for _, id := range ids {
		if _, ok, err := s.expireGroupPacket(ctx, id); err == nil && ok {
			expired = append(expired, id)
		}
	}
	return expired, nil
}

// groupPacketVisible：详情/领取记录可见性。发送方恒可见；receiver 集合非空时仅集合内成员与
// 已领取人可见；开放包（集合为空）时任意已鉴权用户可见（包 ID 本身即为群内广播的凭证）。
func (s *Server) groupPacketVisible(ctx context.Context, viewerID string, pkt *GroupPacket) (bool, error) {
	if viewerID == pkt.SenderID {
		return true, nil
	}
	var memberCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_redpacket_member WHERE packet_id=$1`, pkt.PacketID).Scan(&memberCount); err != nil {
		return false, err
	}
	if memberCount == 0 {
		return true, nil
	}
	var inSet int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_redpacket_member WHERE packet_id=$1 AND user_id=$2`, pkt.PacketID, viewerID).Scan(&inSet); err != nil {
		return false, err
	}
	if inSet > 0 {
		return true, nil
	}
	var claimed int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM group_redpacket_claim WHERE packet_id=$1 AND claimer_id=$2`, pkt.PacketID, viewerID).Scan(&claimed); err != nil {
		return false, err
	}
	return claimed > 0, nil
}

// getGroupPacket：详情（GET 不触发退款，只返回计算出的 expired 标记）。
func (s *Server) getGroupPacket(ctx context.Context, viewerID, packetID string) (*GroupPacket, error) {
	var p GroupPacket
	err := s.db.QueryRowContext(ctx, `SELECT `+groupPacketColumns+` FROM group_redpacket_packet WHERE packet_id=$1`, packetID).
		Scan(&p.PacketID, &p.SenderID, &p.GroupID, &p.TotalAmountCents, &p.TotalCount, &p.ClaimedCount, &p.ClaimedAmountCents, &p.Greeting, &p.Status, &p.CreatedAt, &p.ExpiresAt, &p.ClaimedAt, &p.RefundedAt, &p.freezeID)
	if err == sql.ErrNoRows {
		return nil, errGroupPacketNotFound
	}
	if err != nil {
		return nil, err
	}
	p.RemainingCount = p.TotalCount - p.ClaimedCount
	p.RemainingAmountCents = p.TotalAmountCents - p.ClaimedAmountCents
	if p.Status == GroupPacketActive && time.Now().After(p.ExpiresAt) {
		p.Expired = true
	}
	vis, err := s.groupPacketVisible(ctx, viewerID, &p)
	if err != nil {
		return nil, err
	}
	if !vis {
		return nil, errGroupPacketForbidden
	}
	return &p, nil
}

// listGroupClaims：领取列表（按领取时间升序，即份序对应的领取顺序近似；份序见 share）。
func (s *Server) listGroupClaims(ctx context.Context, viewerID, packetID string) ([]GroupClaim, error) {
	pkt, err := s.getGroupPacket(ctx, viewerID, packetID)
	if err != nil {
		return nil, err
	}
	_ = pkt
	rows, err := s.db.QueryContext(ctx, `SELECT claim_id, packet_id, claimer_id, share_id, amount_cents, claimed_at FROM group_redpacket_claim WHERE packet_id=$1 ORDER BY claimed_at, claim_id`, packetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupClaim{}
	for rows.Next() {
		var c GroupClaim
		if err := rows.Scan(&c.ClaimID, &c.PacketID, &c.ClaimerID, &c.ShareID, &c.AmountCents, &c.ClaimedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// listGroupPackets：发出（sent）或收到（received，已领取过的群包）列表，按创建时间倒序。
func (s *Server) listGroupPackets(ctx context.Context, userID, role string, limit, offset int) ([]GroupPacket, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var q string
	switch role {
	case "sent":
		q = `SELECT ` + groupPacketColumns + ` FROM group_redpacket_packet WHERE sender_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`
	case "received":
		q = `SELECT ` + groupPacketColumns + ` FROM group_redpacket_packet p WHERE EXISTS (SELECT 1 FROM group_redpacket_claim c WHERE c.packet_id=p.packet_id AND c.claimer_id=$1) ORDER BY p.created_at DESC LIMIT $2 OFFSET $3`
	default:
		return nil, errors.New("role must be sent or received")
	}
	rows, err := s.db.QueryContext(ctx, q, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GroupPacket{}
	for rows.Next() {
		var p GroupPacket
		if err := rows.Scan(&p.PacketID, &p.SenderID, &p.GroupID, &p.TotalAmountCents, &p.TotalCount, &p.ClaimedCount,
			&p.ClaimedAmountCents, &p.Greeting, &p.Status, &p.CreatedAt, &p.ExpiresAt, &p.ClaimedAt, &p.RefundedAt, &p.freezeID); err != nil {
			return nil, err
		}
		p.RemainingCount = p.TotalCount - p.ClaimedCount
		p.RemainingAmountCents = p.TotalAmountCents - p.ClaimedAmountCents
		if p.Status == GroupPacketActive && time.Now().After(p.ExpiresAt) {
			p.Expired = true
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- HTTP ----

func groupPacketIDFromPath(r *http.Request) (string, string) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/group-redpackets/")
	parts := strings.Split(rest, "/")
	if len(parts) == 1 {
		return parts[0], ""
	}
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func groupPacketErr(w http.ResponseWriter, err error) bool {
	var adm *admissionFailure
	switch {
	case errors.Is(err, errGroupPacketNotFound):
		fail(w, http.StatusNotFound, "GROUP_PACKET_NOT_FOUND", err.Error())
	case errors.Is(err, errGroupPacketForbidden):
		fail(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, errGroupNotMember):
		fail(w, http.StatusForbidden, "NOT_GROUP_MEMBER", err.Error())
	case errors.Is(err, errGroupPacketFull):
		fail(w, http.StatusConflict, "GROUP_PACKET_FULL", err.Error())
	case errors.Is(err, errGroupPacketExpired):
		fail(w, http.StatusConflict, "GROUP_PACKET_EXPIRED", "group red packet expired; remaining amount refunded to sender")
	case errors.As(err, &adm):
		fail(w, http.StatusForbidden, "ADMISSION_NOT_SATISFIED", adm.guidance)
	case errors.Is(err, errBusy):
		fail(w, http.StatusTooManyRequests, "SYSTEM_BUSY", "system busy, please retry")
	default:
		msg := err.Error()
		if strings.Contains(msg, "insufficient") || strings.Contains(msg, "not found") || strings.Contains(msg, "not normal") || strings.Contains(msg, "does not belong") || strings.Contains(msg, "remaining mismatch") || strings.Contains(msg, "no unclaimed share") || strings.Contains(msg, "CAS failed") {
			fail(w, http.StatusBadRequest, "BUSINESS_ERROR", msg)
			return true
		}
		if strings.Contains(msg, "invalid") || strings.Contains(msg, "must be") || strings.Contains(msg, "too long") || strings.Contains(msg, "too many") {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", msg)
			return true
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", msg)
	}
	return true
}

type groupPacketSendReq struct {
	GroupID        string   `json:"group_id"`
	TotalAmountCents int64  `json:"total_amount_cents"`
	TotalCount     int      `json:"total_count"`
	Greeting       string   `json:"greeting"`
	MemberIDs      []string `json:"member_ids"`
	IdempotencyKey string   `json:"idempotency_key"`
	TxPassword     string   `json:"tx_password"`
}

func (s *Server) hSendGroupPacket(w http.ResponseWriter, r *http.Request) {
	var req groupPacketSendReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	// 守卫顺序：准入引导优先（未完成时只返回引导、绝不扣款），再验交易密码。
	st, err := s.financeStatus(r.Context(), uid)
	if err != nil {
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, "WALLET_NOT_FOUND", "wallet not found; create it first")
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if st.FinanceAccountStatus != "ok" {
		fail(w, http.StatusForbidden, "ADMISSION_NOT_SATISFIED", st.Guidance)
		return
	}
	if err := s.requireTxPassword(r.Context(), uid, req.TxPassword); err != nil {
		fail(w, http.StatusForbidden, "TX_PASSWORD_ERROR", err.Error())
		return
	}
	var pkt *GroupPacket
	err = s.withUserLock(r.Context(), uid, func() error {
		var err error
		pkt, err = s.sendGroupPacket(r.Context(), uid, req.GroupID, req.TotalAmountCents, req.TotalCount, req.Greeting, req.IdempotencyKey, req.MemberIDs)
		return err
	})
	if err != nil {
		groupPacketErr(w, err)
		return
	}
	ok(w, pkt)
}

func (s *Server) hClaimGroupPacket(w http.ResponseWriter, r *http.Request) {
	id, suffix := groupPacketIDFromPath(r)
	if id == "" || suffix != "claim" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown group red packet path")
		return
	}
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	var claim *GroupClaim
	var pkt *GroupPacket
	err := s.withUserLock(r.Context(), uid, func() error {
		var err error
		claim, pkt, err = s.claimGroupPacket(r.Context(), uid, id)
		return err
	})
	if err != nil {
		groupPacketErr(w, err)
		return
	}
	ok(w, map[string]any{"claim": claim, "packet": pkt})
}

func (s *Server) hGroupPacketSubGet(w http.ResponseWriter, r *http.Request) {
	id, suffix := groupPacketIDFromPath(r)
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	if id == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown group red packet path")
		return
	}
	switch suffix {
	case "":
		pkt, err := s.getGroupPacket(r.Context(), uid, id)
		if err != nil {
			groupPacketErr(w, err)
			return
		}
		ok(w, pkt)
	case "claims":
		claims, err := s.listGroupClaims(r.Context(), uid, id)
		if err != nil {
			groupPacketErr(w, err)
			return
		}
		ok(w, claims)
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown group red packet path")
	}
}

func (s *Server) hListGroupPackets(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	role := r.URL.Query().Get("role")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.listGroupPackets(r.Context(), uid, role, limit, offset)
	if err != nil {
		groupPacketErr(w, err)
		return
	}
	ok(w, list)
}

func (s *Server) hExpireGroupPackets(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	var req expireReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.PacketID != "" {
		pkt, expired, err := s.expireGroupPacket(r.Context(), req.PacketID)
		if err != nil {
			groupPacketErr(w, err)
			return
		}
		expiredIDs := []string{}
		if expired {
			expiredIDs = append(expiredIDs, pkt.PacketID)
		}
		ok(w, map[string]any{"expired_ids": expiredIDs, "packet": pkt})
		return
	}
	ids, err := s.expireDueGroupPackets(r.Context(), req.Limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, map[string]any{"expired_ids": ids})
}
