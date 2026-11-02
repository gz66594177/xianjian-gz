package fund

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
)

type Account struct {
	UserID       string    `json:"user_id"`
	BalanceCents int64     `json:"balance_cents"`
	FrozenCents  int64     `json:"frozen_cents"`
	Status       int       `json:"status"`
	Version      int64     `json:"version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type LedgerEntry struct {
	EntryID           int64     `json:"entry_id"`
	TxnID             string    `json:"txn_id"`
	UserID            string    `json:"user_id"`
	AccountType       int       `json:"account_type"`
	Direction         int       `json:"direction"`
	AmountCents       int64     `json:"amount_cents"`
	BalanceAfterCents int64     `json:"balance_after_cents"`
	FrozenAfterCents  int64     `json:"frozen_after_cents"`
	OppositeUserID    *string   `json:"opposite_user_id"`
	OppositeAccount   *string   `json:"opposite_account"`
	CreatedAt         time.Time `json:"created_at"`
}

type Txn struct {
	TxnID          string    `json:"txn_id"`
	UserID         string    `json:"user_id"`
	Type           int       `json:"type"`
	RefType        *string   `json:"ref_type"`
	RefID          *string   `json:"ref_id"`
	Status         int       `json:"status"`
	IdempotencyKey *string   `json:"idempotency_key"`
	CreatedAt      time.Time `json:"created_at"`
}

// signed：贷(CR)为正、借(DR)为负。同一 txn 下全部带符号分录合计必须为 0（借贷相等，与余额增减方向无关）。
// deltaFor：余额增减方向按账户性质而定——用户户（对平台为负债）：贷增借减；
// 平台结算户（平台持有的清算资金，资产性质）：借增贷减。如此平台户余额恒为“已净流入的清算资金”，
// 收入时平台户余额增加而非变负，CHECK >= 0 在全部正常业务下均成立。
func signed(direction int, amount int64) int64 {
	if direction == DirCredit {
		return amount
	}
	return -amount
}

func deltaFor(userID string, direction int, amount int64) int64 {
	if userID == PlatformUserID {
		return -signed(direction, amount)
	}
	return signed(direction, amount)
}

func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return err != nil && strings.Contains(err.Error(), "duplicate key")
}

type postingEntry struct {
	userID      string
	accountType int
	direction   int
	amount      int64
	oppositeUID string
	oppositeAcc string
}

type accountState struct {
	balance int64
	frozen  int64
	version int64
	status  int
}

// postLedger 是统一账务核心：同一 DB 事务内完成行锁、幂等、双重记账校验、余额条件更新。
// entries 至少 2 条；调用方须保证 entries 已覆盖借贷双方。
// 返回已创建的 txn；若幂等键命中已存在交易，则直接返回原交易（不重复记账）。
func (s *Server) postLedger(ctx context.Context, userID string, txnType int, refType, refID, idemKey string, entries []postingEntry, auditAction, auditDetail string) (*Txn, error) {
	if len(entries) < 2 {
		return nil, errors.New("double-entry requires at least 2 entries")
	}
	var sum int64
	for _, e := range entries {
		if e.amount <= 0 {
			return nil, errors.New("ledger amount must be > 0")
		}
		sum += signed(e.direction, e.amount)
	}
	if sum != 0 {
		return nil, fmt.Errorf("double-entry sum must be 0, got %d", sum)
	}

	// 涉及账户按 user_id 排序加锁，避免死锁。
	usersSet := map[string]bool{}
	for _, e := range entries {
		usersSet[e.userID] = true
	}
	users := make([]string, 0, len(usersSet))
	for u := range usersSet {
		users = append(users, u)
	}
	sort.Strings(users)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// 幂等：先查已存在交易。
	if idemKey != "" {
		var existing Txn
		err := tx.QueryRowContext(ctx,
			`SELECT txn_id, user_id, type, ref_type, ref_id, status, idempotency_key, created_at FROM wallet_txn WHERE user_id=$1 AND idempotency_key=$2`,
			userID, idemKey).Scan(&existing.TxnID, &existing.UserID, &existing.Type, &existing.RefType, &existing.RefID, &existing.Status, &existing.IdempotencyKey, &existing.CreatedAt)
		if err == nil {
			_ = tx.Rollback()
			return &existing, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}

	// SELECT ... FOR UPDATE 行锁。
	states := map[string]*accountState{}
	for _, u := range users {
		var st accountState
		err := tx.QueryRowContext(ctx,
			`SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&st.balance, &st.frozen, &st.version, &st.status)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("wallet not found: %s", u)
		}
		if err != nil {
			return nil, err
		}
		if st.status != 0 {
			return nil, fmt.Errorf("wallet not normal: %s status=%d", u, st.status)
		}
		states[u] = &st
	}

	// 逐条应用分录并校验余额不为负。
	type applied struct {
		e            postingEntry
		balanceAfter int64
		frozenAfter  int64
	}
	appliedList := make([]applied, 0, len(entries))
	for _, e := range entries {
		st := states[e.userID]
		if e.accountType == AcctAvailable {
			st.balance += deltaFor(e.userID, e.direction, e.amount)
			if st.balance < 0 {
				return nil, fmt.Errorf("insufficient available balance for %s", e.userID)
			}
		} else if e.accountType == AcctFrozen {
			st.frozen += deltaFor(e.userID, e.direction, e.amount)
			if st.frozen < 0 {
				return nil, fmt.Errorf("insufficient frozen balance for %s", e.userID)
			}
		} else {
			return nil, fmt.Errorf("unknown account_type %d", e.accountType)
		}
		appliedList = append(appliedList, applied{e: e, balanceAfter: st.balance, frozenAfter: st.frozen})
	}

	txnID := newID("txn_")
	_, err = tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,$4,$5,1,$6)`,
		txnID, userID, txnType, nullStr(refType), nullStr(refID), nullStr(idemKey))
	if err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			// 并发重复请求：回滚后返回原交易。
			_ = tx.Rollback()
			var existing Txn
			err2 := s.db.QueryRowContext(ctx,
				`SELECT txn_id, user_id, type, ref_type, ref_id, status, idempotency_key, created_at FROM wallet_txn WHERE user_id=$1 AND idempotency_key=$2`,
				userID, idemKey).Scan(&existing.TxnID, &existing.UserID, &existing.Type, &existing.RefType, &existing.RefID, &existing.Status, &existing.IdempotencyKey, &existing.CreatedAt)
			if err2 != nil {
				return nil, err2
			}
			return &existing, nil
		}
		return nil, err
	}

	for _, a := range appliedList {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, a.e.userID, a.e.accountType, a.e.direction, a.e.amount, a.balanceAfter, a.frozenAfter, nullStr(a.e.oppositeUID), nullStr(a.e.oppositeAcc))
		if err != nil {
			return nil, err
		}
	}
	// 余额更新带 version 条件（FOR UPDATE 已串行化，version 条件为第二道防线）。
	for u, st := range states {
		res, err := tx.ExecContext(ctx,
			`UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`,
			st.balance, st.frozen, u, st.version)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return nil, fmt.Errorf("optimistic lock failed for %s", u)
		}
	}
	if auditAction != "" {
		if err := insertAudit(ctx, tx, userID, userID, auditAction, auditDetail, "", ""); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			var existing Txn
			err2 := s.db.QueryRowContext(ctx,
				`SELECT txn_id, user_id, type, ref_type, ref_id, status, idempotency_key, created_at FROM wallet_txn WHERE user_id=$1 AND idempotency_key=$2`,
				userID, idemKey).Scan(&existing.TxnID, &existing.UserID, &existing.Type, &existing.RefType, &existing.RefID, &existing.Status, &existing.IdempotencyKey, &existing.CreatedAt)
			if err2 == nil {
				return &existing, nil
			}
		}
		return nil, err
	}
	return &Txn{TxnID: txnID, UserID: userID, Type: txnType, Status: 1}, nil
}

func (s *Server) getAccount(ctx context.Context, userID string) (*Account, error) {
	var a Account
	err := s.db.QueryRowContext(ctx,
		`SELECT user_id, balance_cents, frozen_cents, status, version, created_at, updated_at FROM wallet_account WHERE user_id=$1`, userID).
		Scan(&a.UserID, &a.BalanceCents, &a.FrozenCents, &a.Status, &a.Version, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Server) createWallet(ctx context.Context, userID string) (*Account, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wallet_account (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID)
	if err != nil {
		return nil, err
	}
	return s.getAccount(ctx, userID)
}

// withUserLock：Redis 辅助锁（仅协调）；锁被持有回 429，Redis 不可用则降级继续。
func (s *Server) withUserLock(ctx context.Context, userID string, fn func() error) error {
	lock, _, busy := s.tryLock(ctx, "fund:user:"+userID)
	if busy {
		return errBusy
	}
	if lock != nil {
		defer lock.unlock(ctx)
	}
	return fn()
}

var errBusy = errors.New("system busy, please retry")

// ---- 账务操作（Phase 1 内部 primitives；真实充值/提现/红包/转账在后续阶段接交易密码与渠道，见阶段边界） ----

func (s *Server) opIncome(ctx context.Context, userID string, amount int64, refType, refID, idem string) (*Txn, error) {
	return s.postLedger(ctx, userID, TxnIncome, refType, refID, idem, []postingEntry{
		{userID: userID, accountType: AcctAvailable, direction: DirCredit, amount: amount, oppositeUID: PlatformUserID, oppositeAcc: "settlement"},
		{userID: PlatformUserID, accountType: AcctAvailable, direction: DirDebit, amount: amount, oppositeUID: userID, oppositeAcc: "available"},
	}, "ledger_income", fmt.Sprintf("income %d cents", amount))
}

func (s *Server) opExpense(ctx context.Context, userID string, amount int64, refType, refID, idem string) (*Txn, error) {
	return s.postLedger(ctx, userID, TxnExpense, refType, refID, idem, []postingEntry{
		{userID: userID, accountType: AcctAvailable, direction: DirDebit, amount: amount, oppositeUID: PlatformUserID, oppositeAcc: "settlement"},
		{userID: PlatformUserID, accountType: AcctAvailable, direction: DirCredit, amount: amount, oppositeUID: userID, oppositeAcc: "available"},
	}, "ledger_expense", fmt.Sprintf("expense %d cents", amount))
}

func (s *Server) opRefund(ctx context.Context, userID string, amount int64, refType, refID, idem string) (*Txn, error) {
	// 退款与收入同向（平台 -> 用户），以不同 txn type 区分业务语义。
	return s.postLedger(ctx, userID, TxnRefund, refType, refID, idem, []postingEntry{
		{userID: userID, accountType: AcctAvailable, direction: DirCredit, amount: amount, oppositeUID: PlatformUserID, oppositeAcc: "settlement"},
		{userID: PlatformUserID, accountType: AcctAvailable, direction: DirDebit, amount: amount, oppositeUID: userID, oppositeAcc: "available"},
	}, "ledger_refund", fmt.Sprintf("refund %d cents", amount))
}

// 冻结：可用 -> 冻结（同一用户两条分录），并在同一事务内建 wallet_freeze 行。
// 幂等命中时直接返回原交易与已建的 freeze 行，不重复记账、不重复建行。
func (s *Server) opFreeze(ctx context.Context, userID string, amount int64, freezeID, refType, refID, idem string) (*Txn, string, error) {
	if amount <= 0 {
		return nil, "", errors.New("amount must be > 0")
	}
	if freezeID == "" {
		freezeID = newID("frz_")
	}
	entries := []postingEntry{
		{userID: userID, accountType: AcctAvailable, direction: DirDebit, amount: amount, oppositeUID: userID, oppositeAcc: "frozen"},
		{userID: userID, accountType: AcctFrozen, direction: DirCredit, amount: amount, oppositeUID: userID, oppositeAcc: "available"},
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	if idem != "" {
		var existing Txn
		err := tx.QueryRowContext(ctx, `SELECT txn_id, user_id, type, ref_type, ref_id, status, idempotency_key, created_at FROM wallet_txn WHERE user_id=$1 AND idempotency_key=$2`, userID, idem).
			Scan(&existing.TxnID, &existing.UserID, &existing.Type, &existing.RefType, &existing.RefID, &existing.Status, &existing.IdempotencyKey, &existing.CreatedAt)
		if err == nil {
			var fid string
			_ = tx.QueryRowContext(ctx, `SELECT freeze_id FROM wallet_freeze WHERE txn_id=$1`, existing.TxnID).Scan(&fid)
			_ = tx.Rollback()
			if fid == "" {
				fid = freezeID
			}
			return &existing, fid, nil
		}
		if err != sql.ErrNoRows {
			return nil, "", err
		}
	}
	var st accountState
	err = tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, userID).
		Scan(&st.balance, &st.frozen, &st.version, &st.status)
	if err != nil {
		return nil, "", err
	}
	if st.status != 0 {
		return nil, "", fmt.Errorf("wallet not normal: %s", userID)
	}
	// 应用分录并校验
	bal, frz := st.balance, st.frozen
	type ap struct {
		e            postingEntry
		balanceAfter int64
		frozenAfter  int64
	}
	var aps []ap
	for _, e := range entries {
		if e.accountType == AcctAvailable {
			bal += deltaFor(e.userID, e.direction, e.amount)
			if bal < 0 {
				return nil, "", fmt.Errorf("insufficient available balance for %s", userID)
			}
		} else {
			frz += deltaFor(e.userID, e.direction, e.amount)
			if frz < 0 {
				return nil, "", fmt.Errorf("insufficient frozen balance for %s", userID)
			}
		}
		aps = append(aps, ap{e: e, balanceAfter: bal, frozenAfter: frz})
	}
	txnID := newID("txn_")
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,$4,$5,1,$6)`, txnID, userID, TxnFreeze, nullStr(refType), nullStr(refID), nullStr(idem)); err != nil {
		return nil, "", err
	}
	for _, a := range aps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, txnID, a.e.userID, a.e.accountType, a.e.direction, a.e.amount, a.balanceAfter, a.frozenAfter, nullStr(a.e.oppositeUID), nullStr(a.e.oppositeAcc)); err != nil {
			return nil, "", err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, bal, frz, userID, st.version)
	if err != nil {
		return nil, "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, "", fmt.Errorf("optimistic lock failed for %s", userID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_freeze (freeze_id, user_id, txn_id, amount_cents, remaining_cents, status) VALUES ($1,$2,$3,$4,$4,0)`, freezeID, userID, txnID, amount); err != nil {
		return nil, "", err
	}
	if err := insertAudit(ctx, tx, userID, userID, "ledger_freeze", fmt.Sprintf("freeze %d cents", amount), "", ""); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", err
	}
	return &Txn{TxnID: txnID, UserID: userID, Type: TxnFreeze, Status: 1}, freezeID, nil
}

// freezeMove：解冻 / 冻结扣款 / 冻结退款的通用实现（共享冻结行余额校验）。
func (s *Server) freezeMove(ctx context.Context, userID, freezeID string, amount int64, txnType int, auditAction string) (*Txn, error) {
	// 先在独立事务里校验并扣减 freeze remaining，再记账；若记账失败，remaining 回滚需同一事务——
	// 因此本函数改为：记账事务内先锁 freeze 行。实现方式：先取锁，再在 postLedger 事务外做 remaining 扣减会产生
	// “记账失败但 remaining 已扣”的窗口，故这里采用两段式但用同一 DB 连接事务重写：
	return s.freezeMoveTx(ctx, userID, freezeID, amount, txnType, auditAction)
}

func (s *Server) freezeMoveTx(ctx context.Context, userID, freezeID string, amount int64, txnType int, auditAction string) (*Txn, error) {
	// 构造分录
	var entries []postingEntry
	switch txnType {
	case TxnUnfreeze, TxnFreezeRefund: // 冻结 -> 可用
		entries = []postingEntry{
			{userID: userID, accountType: AcctFrozen, direction: DirDebit, amount: amount, oppositeUID: userID, oppositeAcc: "available"},
			{userID: userID, accountType: AcctAvailable, direction: DirCredit, amount: amount, oppositeUID: userID, oppositeAcc: "frozen"},
		}
	case TxnFreezeCharge: // 冻结 -> 平台结算
		entries = []postingEntry{
			{userID: userID, accountType: AcctFrozen, direction: DirDebit, amount: amount, oppositeUID: PlatformUserID, oppositeAcc: "settlement"},
			{userID: PlatformUserID, accountType: AcctAvailable, direction: DirCredit, amount: amount, oppositeUID: userID, oppositeAcc: "frozen"},
		}
	default:
		return nil, fmt.Errorf("unsupported freeze move type %d", txnType)
	}
	// 为保证 freeze remaining 与记账原子性，复用 postLedger 的事务模式：先在同一事务里锁 freeze 行并扣 remaining，
	// 再复用 postLedger 逻辑会开第二事务——为避免窗口，这里直接调用带 freeze 校验的内联版本：
	txn, err := s.postLedgerWithFreeze(ctx, userID, freezeID, amount, txnType, entries, auditAction)
	return txn, err
}

// postLedgerWithFreeze：postLedger 的扩展版，同一事务内先锁 freeze 行、校验 remaining、扣减并更新状态，再记账。
func (s *Server) postLedgerWithFreeze(ctx context.Context, userID, freezeID string, amount int64, txnType int, entries []postingEntry, auditAction string) (*Txn, error) {
	var sum int64
	for _, e := range entries {
		sum += signed(e.direction, e.amount)
	}
	if sum != 0 {
		return nil, fmt.Errorf("double-entry sum must be 0, got %d", sum)
	}
	usersSet := map[string]bool{}
	for _, e := range entries {
		usersSet[e.userID] = true
	}
	users := make([]string, 0, len(usersSet))
	for u := range usersSet {
		users = append(users, u)
	}
	sort.Strings(users)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var remaining int64
	var fUser string
	err = tx.QueryRowContext(ctx, `SELECT user_id, remaining_cents FROM wallet_freeze WHERE freeze_id=$1 FOR UPDATE`, freezeID).
		Scan(&fUser, &remaining)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("freeze not found: %s", freezeID)
	}
	if err != nil {
		return nil, err
	}
	if fUser != userID {
		return nil, errors.New("freeze does not belong to user")
	}
	if amount > remaining {
		return nil, fmt.Errorf("freeze remaining insufficient: remaining=%d amount=%d", remaining, amount)
	}
	newRemaining := remaining - amount
	newStatus := 0
	if newRemaining == 0 {
		if txnType == TxnFreezeCharge {
			newStatus = 2
		} else {
			newStatus = 1
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_freeze SET remaining_cents=$1, status=$2, updated_at=now() WHERE freeze_id=$3`, newRemaining, newStatus, freezeID); err != nil {
		return nil, err
	}

	states := map[string]*accountState{}
	for _, u := range users {
		var st accountState
		err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&st.balance, &st.frozen, &st.version, &st.status)
		if err != nil {
			return nil, err
		}
		if st.status != 0 {
			return nil, fmt.Errorf("wallet not normal: %s", u)
		}
		states[u] = &st
	}
	type applied struct {
		e            postingEntry
		balanceAfter int64
		frozenAfter  int64
	}
	var appliedList []applied
	for _, e := range entries {
		st := states[e.userID]
		if e.accountType == AcctAvailable {
			st.balance += deltaFor(e.userID, e.direction, e.amount)
			if st.balance < 0 {
				return nil, fmt.Errorf("insufficient available balance for %s", e.userID)
			}
		} else {
			st.frozen += deltaFor(e.userID, e.direction, e.amount)
			if st.frozen < 0 {
				return nil, fmt.Errorf("insufficient frozen balance for %s", e.userID)
			}
		}
		appliedList = append(appliedList, applied{e: e, balanceAfter: st.balance, frozenAfter: st.frozen})
	}
	txnID := newID("txn_")
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_txn (txn_id, user_id, type, status) VALUES ($1,$2,$3,1)`, txnID, userID, txnType); err != nil {
		return nil, err
	}
	for _, a := range appliedList {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, a.e.userID, a.e.accountType, a.e.direction, a.e.amount, a.balanceAfter, a.frozenAfter, nullStr(a.e.oppositeUID), nullStr(a.e.oppositeAcc)); err != nil {
			return nil, err
		}
	}
	for u, st := range states {
		res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, st.balance, st.frozen, u, st.version)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return nil, fmt.Errorf("optimistic lock failed for %s", u)
		}
	}
	if err := insertAudit(ctx, tx, userID, userID, auditAction, fmt.Sprintf("freeze %s %d cents", freezeID, amount), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &Txn{TxnID: txnID, UserID: userID, Type: txnType, Status: 1}, nil
}

// opReverse：冲正——把原 txn 的全部用户分录取反（方向互换），生成新 txn；平台对手方分录同样取反，保证合计为 0。
func (s *Server) opReverse(ctx context.Context, userID, originalTxnID, idem string) (*Txn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT user_id, account_type, direction, amount_cents, opposite_user_id, opposite_account FROM wallet_ledger WHERE txn_id=$1 ORDER BY entry_id`, originalTxnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []postingEntry
	origUser := ""
	for rows.Next() {
		var e postingEntry
		var oppUID, oppAcc sql.NullString
		if err := rows.Scan(&e.userID, &e.accountType, &e.direction, &e.amount, &oppUID, &oppAcc); err != nil {
			return nil, err
		}
		if origUser == "" {
			// 原 txn 归属用户以 txn 头为准，下面校验
		}
		// 取反方向
		if e.direction == DirCredit {
			e.direction = DirDebit
		} else {
			e.direction = DirCredit
		}
		if oppUID.Valid {
			e.oppositeUID = oppUID.String
		}
		if oppAcc.Valid {
			e.oppositeAcc = oppAcc.String
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(entries) < 2 {
		return nil, fmt.Errorf("original txn not found or incomplete: %s", originalTxnID)
	}
	// 校验原 txn 确实归属该用户
	var txnUser string
	if err := s.db.QueryRowContext(ctx, `SELECT user_id FROM wallet_txn WHERE txn_id=$1`, originalTxnID).Scan(&txnUser); err != nil {
		return nil, err
	}
	if txnUser != userID {
		return nil, errors.New("original txn does not belong to user")
	}
	_ = origUser
	return s.postLedger(ctx, userID, TxnReversal, "txn", originalTxnID, idem, entries, "ledger_reversal", "reverse txn "+originalTxnID)
}
