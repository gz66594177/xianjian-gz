package fund

// Phase 3：好友红包（单聊红包）。
// 资金模型：发包时发送方可用 -> 冻结（与发送方同一事务内建包）；领取时发送方冻结 -> 领取方可用；
// 过期时发送方冻结 -> 发送方可用（退款）。全部走双重记账、同一 DB 事务，流水 append-only。
// 红包消息载体：IM 自定义消息体只放 packetId，绝不放金额；金额、状态、领取记录全部以服务端为准，
// 客户端绝不能指定领取金额。App 层接入见 API 文档（Phase 10/11 落地，服务端 API 已按可直接调用方式设计）。
// 过期规则（统一规则）：创建后 24 小时未领取即过期；过期退款由管理端扫描执行，或领取时惰性触发；
// 详情查询不触发退款（GET 不写库），但返回计算出的 expired 标记供客户端展示。

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
	PacketActive   = 0 // 待领取
	PacketClaimed  = 1 // 已领取
	PacketRefunded = 2 // 已过期退款
)

const (
	TxnPacketSend   = 9  // 发包（可用 -> 冻结）
	TxnPacketClaim  = 10 // 领取（发送方冻结 -> 领取方可用）
	TxnPacketRefund = 11 // 过期退款（冻结 -> 发送方可用）
)

const packetExpiry = 24 * time.Hour

var (
	errPacketNotFound  = errors.New("red packet not found")
	errPacketForbidden = errors.New("red packet does not belong to viewer")
	errPacketClaimed   = errors.New("red packet already claimed")
	errPacketExpired   = errors.New("red packet expired")
)

// admissionFailure：准入未完成（发送/领取的引导语义）：调用方须把 guidance 原样返回客户端，
// 绝不扣款、不改包状态、不判成功。
type admissionFailure struct{ guidance string }

func (e *admissionFailure) Error() string { return "finance admission not satisfied: " + e.guidance }

type Packet struct {
	PacketID    string     `json:"packet_id"`
	SenderID    string     `json:"sender_id"`
	ReceiverID  string     `json:"receiver_id"`
	AmountCents int64      `json:"amount_cents"`
	Greeting    string     `json:"greeting"`
	Status      int        `json:"status"`  // 0 待领取 1 已领取 2 已过期退款
	Expired     bool       `json:"expired"` // 计算字段：待领取且已过 expires_at
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ClaimedAt   *time.Time `json:"claimed_at"`
	RefundedAt  *time.Time `json:"refunded_at"`
	freezeID    string     // 内部用，不序列化
}

type Claim struct {
	ClaimID     string    `json:"claim_id"`
	PacketID    string    `json:"packet_id"`
	ClaimerID   string    `json:"claimer_id"`
	AmountCents int64     `json:"amount_cents"`
	ClaimedAt   time.Time `json:"claimed_at"`
}

func scanPacket(row interface {
	Scan(...any) error
}) (*Packet, error) {
	var p Packet
	if err := row.Scan(&p.PacketID, &p.SenderID, &p.ReceiverID, &p.AmountCents, &p.Greeting, &p.Status,
		&p.CreatedAt, &p.ExpiresAt, &p.ClaimedAt, &p.RefundedAt, &p.freezeID); err != nil {
		return nil, err
	}
	if p.Status == PacketActive && time.Now().After(p.ExpiresAt) {
		p.Expired = true
	}
	return &p, nil
}

const packetColumns = `packet_id, sender_id, receiver_id, amount_cents, greeting, status, created_at, expires_at, claimed_at, refunded_at, freeze_id`

func loadPacketTx(ctx context.Context, tx *sql.Tx, packetID string, forUpdate bool) (*Packet, error) {
	q := `SELECT ` + packetColumns + ` FROM redpacket_packet WHERE packet_id=$1`
	if forUpdate {
		q += ` FOR UPDATE`
	}
	row := tx.QueryRowContext(ctx, q, packetID)
	p, err := scanPacket(row)
	if err == sql.ErrNoRows {
		return nil, errPacketNotFound
	}
	return p, err
}

// rowQuerier：*sql.Tx 与 *sql.DB 均满足；幂等回查在事务内走 tx，并发冲突回查走 s.db。
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func loadPacketBySenderIdem(ctx context.Context, q rowQuerier, senderID, idemKey string) (*Packet, error) {
	row := q.QueryRowContext(ctx, `SELECT `+packetColumns+` FROM redpacket_packet WHERE sender_id=$1 AND idempotency_key=$2`, senderID, idemKey)
	p, err := scanPacket(row)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return p, err
}

func loadClaimTx(ctx context.Context, tx *sql.Tx, packetID string) (*Claim, error) {
	var c Claim
	err := tx.QueryRowContext(ctx, `SELECT claim_id, packet_id, claimer_id, amount_cents, claimed_at FROM redpacket_claim WHERE packet_id=$1`, packetID).
		Scan(&c.ClaimID, &c.PacketID, &c.ClaimerID, &c.AmountCents, &c.ClaimedAt)
	if err == sql.ErrNoRows {
		return nil, errPacketNotFound
	}
	return &c, err
}

// sendPacket：发包。发送方须先通过准入与交易密码（HTTP 层守卫，本函数只做资金与建包）。
// 同一事务：幂等回查 -> 发送方行锁 -> 记账（可用 DR / 冻结 CR）-> 冻结行 -> 包行 -> 审计。
// 并发重复发送：幂等键命中（wallet_txn 唯一索引或包唯一索引）时回滚并返回原包，不重复扣款。
func (s *Server) sendPacket(ctx context.Context, senderID, receiverID string, amount int64, greeting, idemKey string) (*Packet, error) {
	if !validUserID(receiverID) {
		return nil, errors.New("invalid receiver_id")
	}
	if receiverID == senderID {
		return nil, errors.New("cannot send red packet to self")
	}
	if amount <= 0 {
		return nil, errors.New("amount_cents must be > 0")
	}
	if utf8.RuneCountInString(greeting) > 200 {
		return nil, errors.New("greeting too long, max 200 chars")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if idemKey != "" {
		if existing, err := loadPacketBySenderIdem(ctx, tx, senderID, idemKey); err == nil {
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
	balAfter, frzAfter := st.balance-amount, st.frozen+amount
	if balAfter < 0 {
		return nil, fmt.Errorf("insufficient available balance for %s", senderID)
	}

	txnID := newID("txn_")
	freezeID := newID("frz_")
	packetID := newID("rp_")
	now := time.Now()
	expires := now.Add(packetExpiry)

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'redpacket',$4,1,$5)`,
		txnID, senderID, TxnPacketSend, packetID, nullStr(idemKey)); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			existing, err2 := loadPacketBySenderIdem(ctx, s.db, senderID, idemKey)
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
			txnID, senderID, lr.acctType, lr.dir, amount, balAfter, frzAfter, lr.oppUID, lr.oppAcc); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_freeze (freeze_id, user_id, txn_id, amount_cents, remaining_cents, status) VALUES ($1,$2,$3,$4,$4,0)`, freezeID, senderID, txnID, amount); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO redpacket_packet (packet_id, sender_id, receiver_id, amount_cents, greeting, status, freeze_id, send_txn_id, idempotency_key, expires_at)
		 VALUES ($1,$2,$3,$4,$5,0,$6,$7,$8,$9)`,
		packetID, senderID, receiverID, amount, greeting, freezeID, txnID, nullStr(idemKey), expires); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			existing, err2 := loadPacketBySenderIdem(ctx, s.db, senderID, idemKey)
			if err2 != nil {
				return nil, err2
			}
			return existing, nil
		}
		return nil, err
	}
	if err := insertAudit(ctx, tx, senderID, senderID, "redpacket_send", fmt.Sprintf("packet %s %d cents", packetID, amount), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Packet{PacketID: packetID, SenderID: senderID, ReceiverID: receiverID, AmountCents: amount, Greeting: greeting, Status: PacketActive, CreatedAt: now, ExpiresAt: expires}, nil
}

// refundPacketTx：在调用方事务内执行过期退款（调用方须已对包行加锁且确认 status=0）。
// 冻结行 remaining 必须等于包金额（部分动用即为异常，直接失败、整笔回滚）。
func (s *Server) refundPacketTx(ctx context.Context, tx *sql.Tx, pkt *Packet) (*Txn, error) {
	var fUser string
	var remaining int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id, remaining_cents FROM wallet_freeze WHERE freeze_id=$1 FOR UPDATE`, pkt.freezeID).
		Scan(&fUser, &remaining); err != nil {
		return nil, err
	}
	if fUser != pkt.SenderID {
		return nil, errors.New("freeze does not belong to packet sender")
	}
	if remaining != pkt.AmountCents {
		return nil, fmt.Errorf("freeze remaining mismatch for packet %s: remaining=%d amount=%d", pkt.PacketID, remaining, pkt.AmountCents)
	}
	var st accountState
	if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, pkt.SenderID).
		Scan(&st.balance, &st.frozen, &st.version, &st.status); err != nil {
		return nil, err
	}
	if st.status != 0 {
		return nil, fmt.Errorf("wallet not normal: %s", pkt.SenderID)
	}
	balAfter, frzAfter := st.balance+pkt.AmountCents, st.frozen-pkt.AmountCents
	if frzAfter < 0 {
		return nil, fmt.Errorf("insufficient frozen balance for %s", pkt.SenderID)
	}
	txnID := newID("txn_")
	now := time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'redpacket',$4,1,$5)`,
		txnID, pkt.SenderID, TxnPacketRefund, pkt.PacketID, "rp-refund-"+pkt.PacketID); err != nil {
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
			txnID, pkt.SenderID, lr.acctType, lr.dir, pkt.AmountCents, balAfter, frzAfter, lr.oppUID, lr.oppAcc); err != nil {
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
	if _, err := tx.ExecContext(ctx, `UPDATE redpacket_packet SET status=2, refund_txn_id=$1, refunded_at=$2, updated_at=now() WHERE packet_id=$3 AND status=0`, txnID, now, pkt.PacketID); err != nil {
		return nil, err
	}
	if err := insertAudit(ctx, tx, pkt.SenderID, pkt.SenderID, "redpacket_refund", fmt.Sprintf("packet %s %d cents expired refund", pkt.PacketID, pkt.AmountCents), "", ""); err != nil {
		return nil, err
	}
	return &Txn{TxnID: txnID, UserID: pkt.SenderID, Type: TxnPacketRefund, Status: 1}, nil
}

// claimPacket：领取。同一事务：包行锁 -> 状态 CAS -> 过期惰性退款 -> 归属校验 -> 准入校验
// -> 冻结行锁 -> 双方账户锁（按 user_id 排序）-> 记账（发送方冻结 DR / 领取方可用 CR）-> 领取行 -> 包状态。
// 同一领取人重复领取返回原领取记录（幂等）；他人领取已领包返回 ALREADY_CLAIMED；并发领取由包行锁串行化。
func (s *Server) claimPacket(ctx context.Context, claimerID, packetID string) (*Claim, *Packet, error) {
	if _, err := s.createWallet(ctx, claimerID); err != nil {
		return nil, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()

	pkt, err := loadPacketTx(ctx, tx, packetID, true)
	if err != nil {
		return nil, nil, err
	}
	switch pkt.Status {
	case PacketClaimed:
		cl, err := loadClaimTx(ctx, tx, packetID)
		if err == nil && cl.ClaimerID == claimerID {
			_ = tx.Rollback()
			return cl, pkt, nil
		}
		return nil, nil, errPacketClaimed
	case PacketRefunded:
		return nil, nil, errPacketExpired
	}
	if time.Now().After(pkt.ExpiresAt) {
		// 领取时惰性过期：先退款（发送方受益，不受领取人准入影响），再报过期。
		if _, err := s.refundPacketTx(ctx, tx, pkt); err != nil {
			return nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		pkt.Status = PacketRefunded
		now := time.Now()
		pkt.RefundedAt = &now
		pkt.Expired = false
		return nil, pkt, errPacketExpired
	}
	if claimerID != pkt.ReceiverID {
		return nil, nil, errPacketForbidden
	}
	// 领包不重复验交易密码，但须已完成准入；未完成只返回引导，包状态不动、不扣款。
	st, err := s.financeStatus(ctx, claimerID)
	if err != nil {
		return nil, nil, err
	}
	if st.FinanceAccountStatus != "ok" {
		return nil, nil, &admissionFailure{guidance: st.Guidance}
	}

	var fUser string
	var remaining int64
	if err := tx.QueryRowContext(ctx, `SELECT user_id, remaining_cents FROM wallet_freeze WHERE freeze_id=$1 FOR UPDATE`, pkt.freezeID).
		Scan(&fUser, &remaining); err != nil {
		return nil, nil, err
	}
	if fUser != pkt.SenderID {
		return nil, nil, errors.New("freeze does not belong to packet sender")
	}
	if remaining != pkt.AmountCents {
		return nil, nil, fmt.Errorf("freeze remaining mismatch for packet %s: remaining=%d amount=%d", pkt.PacketID, remaining, pkt.AmountCents)
	}

	users := []string{pkt.SenderID, claimerID}
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
	senderFrozenAfter := senderSt.frozen - pkt.AmountCents
	claimerBalAfter := claimerSt.balance + pkt.AmountCents
	if senderFrozenAfter < 0 {
		return nil, nil, fmt.Errorf("insufficient frozen balance for %s", pkt.SenderID)
	}

	txnID := newID("txn_")
	claimID := newID("rpc_")
	now := time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'redpacket',$4,1,$5)`,
		txnID, claimerID, TxnPacketClaim, packetID, "rp-claim-"+packetID); err != nil {
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
			txnID, e.uid, e.acctType, e.dir, pkt.AmountCents, e.balAfter, e.frzAfter, e.oppUID, e.oppAcc); err != nil {
			return nil, nil, err
		}
	}
	acctUpdates := []struct {
		uid string
		bal int64
		frz int64
		ver int64
	}{
		{pkt.SenderID, senderSt.balance, senderFrozenAfter, senderSt.version},
		{claimerID, claimerBalAfter, claimerSt.frozen, claimerSt.version},
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
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_freeze SET remaining_cents=0, status=2, updated_at=now() WHERE freeze_id=$1`, pkt.freezeID); err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO redpacket_claim (claim_id, packet_id, claimer_id, amount_cents) VALUES ($1,$2,$3,$4)`, claimID, packetID, claimerID, pkt.AmountCents); err != nil {
		if isUniqueViolation(err) {
			return nil, nil, errPacketClaimed
		}
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE redpacket_packet SET status=1, claim_txn_id=$1, claimed_at=$2, updated_at=now() WHERE packet_id=$3 AND status=0`, txnID, now, packetID); err != nil {
		return nil, nil, err
	}
	if err := insertAudit(ctx, tx, claimerID, claimerID, "redpacket_claim", fmt.Sprintf("packet %s %d cents", packetID, pkt.AmountCents), "", ""); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	pkt.Status = PacketClaimed
	pkt.ClaimedAt = &now
	pkt.Expired = false
	return &Claim{ClaimID: claimID, PacketID: packetID, ClaimerID: claimerID, AmountCents: pkt.AmountCents, ClaimedAt: now}, pkt, nil
}

// expirePacket：单个包过期处理（管理端扫描用）。未到期或已终态时不改动、返回当前包与 expired=false。
func (s *Server) expirePacket(ctx context.Context, packetID string) (*Packet, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	pkt, err := loadPacketTx(ctx, tx, packetID, true)
	if err != nil {
		return nil, false, err
	}
	if pkt.Status != PacketActive {
		_ = tx.Rollback()
		return pkt, false, nil
	}
	if !time.Now().After(pkt.ExpiresAt) {
		_ = tx.Rollback()
		return pkt, false, nil
	}
	if _, err := s.refundPacketTx(ctx, tx, pkt); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	pkt.Status = PacketRefunded
	now := time.Now()
	pkt.RefundedAt = &now
	pkt.Expired = false
	return pkt, true, nil
}

// expireDuePackets：批量过期扫描（每次最多 limit 个，按 expires_at 升序）。单个包失败不阻塞其余包。
func (s *Server) expireDuePackets(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT packet_id FROM redpacket_packet WHERE status=0 AND expires_at <= now() ORDER BY expires_at LIMIT $1`, limit)
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
		if _, ok, err := s.expirePacket(ctx, id); err == nil && ok {
			expired = append(expired, id)
		}
	}
	return expired, nil
}

// getPacket：详情（发送方或领取方可见，第三方 403）。GET 不触发退款，只返回计算出的 expired 标记。
func (s *Server) getPacket(ctx context.Context, viewerID, packetID string) (*Packet, *Claim, error) {
	var p Packet
	err := s.db.QueryRowContext(ctx, `SELECT `+packetColumns+` FROM redpacket_packet WHERE packet_id=$1`, packetID).
		Scan(&p.PacketID, &p.SenderID, &p.ReceiverID, &p.AmountCents, &p.Greeting, &p.Status, &p.CreatedAt, &p.ExpiresAt, &p.ClaimedAt, &p.RefundedAt, &p.freezeID)
	if err == sql.ErrNoRows {
		return nil, nil, errPacketNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if p.Status == PacketActive && time.Now().After(p.ExpiresAt) {
		p.Expired = true
	}
	if viewerID != p.SenderID && viewerID != p.ReceiverID {
		return nil, nil, errPacketForbidden
	}
	var claim *Claim
	if p.Status == PacketClaimed {
		var c Claim
		if err := s.db.QueryRowContext(ctx, `SELECT claim_id, packet_id, claimer_id, amount_cents, claimed_at FROM redpacket_claim WHERE packet_id=$1`, packetID).
			Scan(&c.ClaimID, &c.PacketID, &c.ClaimerID, &c.AmountCents, &c.ClaimedAt); err == nil {
			claim = &c
		}
	}
	return &p, claim, nil
}

// listPackets：发出（sent）或收到（received）列表，按创建时间倒序。
func (s *Server) listPackets(ctx context.Context, userID, role string, limit, offset int) ([]Packet, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var col string
	switch role {
	case "sent":
		col = "sender_id"
	case "received":
		col = "receiver_id"
	default:
		return nil, errors.New("role must be sent or received")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+packetColumns+` FROM redpacket_packet WHERE `+col+`=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Packet{}
	for rows.Next() {
		var p Packet
		if err := rows.Scan(&p.PacketID, &p.SenderID, &p.ReceiverID, &p.AmountCents, &p.Greeting, &p.Status, &p.CreatedAt, &p.ExpiresAt, &p.ClaimedAt, &p.RefundedAt, &p.freezeID); err != nil {
			return nil, err
		}
		if p.Status == PacketActive && time.Now().After(p.ExpiresAt) {
			p.Expired = true
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- HTTP ----

func packetIDFromPath(r *http.Request) (string, string) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/redpackets/")
	parts := strings.Split(rest, "/")
	if len(parts) == 1 {
		return parts[0], ""
	}
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func packetErr(w http.ResponseWriter, err error) bool {
	var adm *admissionFailure
	switch {
	case errors.Is(err, errPacketNotFound):
		fail(w, http.StatusNotFound, "PACKET_NOT_FOUND", err.Error())
	case errors.Is(err, errPacketForbidden):
		fail(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, errPacketClaimed):
		fail(w, http.StatusConflict, "PACKET_ALREADY_CLAIMED", err.Error())
	case errors.Is(err, errPacketExpired):
		fail(w, http.StatusConflict, "PACKET_EXPIRED", "red packet expired; amount refunded to sender")
	case errors.As(err, &adm):
		fail(w, http.StatusForbidden, "ADMISSION_NOT_SATISFIED", adm.guidance)
	case errors.Is(err, errBusy):
		fail(w, http.StatusTooManyRequests, "SYSTEM_BUSY", "system busy, please retry")
	default:
		msg := err.Error()
		if strings.Contains(msg, "insufficient") || strings.Contains(msg, "not found") || strings.Contains(msg, "not normal") || strings.Contains(msg, "does not belong") || strings.Contains(msg, "remaining mismatch") {
			fail(w, http.StatusBadRequest, "BUSINESS_ERROR", msg)
			return true
		}
		if strings.Contains(msg, "invalid") || strings.Contains(msg, "must be") || strings.Contains(msg, "too long") || strings.Contains(msg, "to self") {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", msg)
			return true
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", msg)
	}
	return true
}

type packetSendReq struct {
	ReceiverID     string `json:"receiver_id"`
	AmountCents    int64  `json:"amount_cents"`
	Greeting       string `json:"greeting"`
	IdempotencyKey string `json:"idempotency_key"`
	TxPassword     string `json:"tx_password"`
}

func (s *Server) hSendPacket(w http.ResponseWriter, r *http.Request) {
	var req packetSendReq
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
	var pkt *Packet
	err = s.withUserLock(r.Context(), uid, func() error {
		var err error
		pkt, err = s.sendPacket(r.Context(), uid, req.ReceiverID, req.AmountCents, req.Greeting, req.IdempotencyKey)
		return err
	})
	if err != nil {
		packetErr(w, err)
		return
	}
	ok(w, pkt)
}

func (s *Server) hClaimPacket(w http.ResponseWriter, r *http.Request) {
	id, suffix := packetIDFromPath(r)
	if id == "" || suffix != "claim" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown red packet path")
		return
	}
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	var claim *Claim
	var pkt *Packet
	err := s.withUserLock(r.Context(), uid, func() error {
		var err error
		claim, pkt, err = s.claimPacket(r.Context(), uid, id)
		return err
	})
	if err != nil {
		packetErr(w, err)
		return
	}
	ok(w, map[string]any{"claim": claim, "packet": pkt})
}

func (s *Server) hPacketSubGet(w http.ResponseWriter, r *http.Request) {
	id, suffix := packetIDFromPath(r)
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	if id == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown red packet path")
		return
	}
	switch suffix {
	case "":
		pkt, claim, err := s.getPacket(r.Context(), uid, id)
		if err != nil {
			packetErr(w, err)
			return
		}
		ok(w, map[string]any{"packet": pkt, "claim": claim})
	case "claims":
		pkt, claim, err := s.getPacket(r.Context(), uid, id)
		if err != nil {
			packetErr(w, err)
			return
		}
		_ = pkt
		out := []Claim{}
		if claim != nil {
			out = append(out, *claim)
		}
		ok(w, out)
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown red packet path")
	}
}

func (s *Server) hListPackets(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	role := r.URL.Query().Get("role")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.listPackets(r.Context(), uid, role, limit, offset)
	if err != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	ok(w, list)
}

type expireReq struct {
	PacketID string `json:"packet_id"`
	Limit    int    `json:"limit"`
}

func (s *Server) hExpirePackets(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	var req expireReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.PacketID != "" {
		pkt, expired, err := s.expirePacket(r.Context(), req.PacketID)
		if err != nil {
			packetErr(w, err)
			return
		}
		expiredIDs := []string{}
		if expired {
			expiredIDs = append(expiredIDs, pkt.PacketID)
		}
		ok(w, map[string]any{"expired_ids": expiredIDs, "packet": pkt})
		return
	}
	ids, err := s.expireDuePackets(r.Context(), req.Limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, map[string]any{"expired_ids": ids})
}
