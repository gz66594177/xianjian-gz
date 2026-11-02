package fund

// 人工余额修改（单人直连入账，无双人复核流程）。
// 与 Phase 7 的 fund_admin_adjustment（双人复核：首复核不动账、第二复核入账）不同：
// 本功能按角色权限分配——面板侧用独立权限 xianliao:fund:adjust gating 提交按钮，
// 后端代理层对本接口的写操作要求该权限；有权限的管理员提交后在同一事务里直接入账，
// 不设待复核状态、不设拒绝流程；入账失败则整笔回滚，不留有效单据。
// 安全约束与 Phase 7 一致：整数分、减少不透支（用户可用与平台结算双侧校验）、
// 复式记账（用户可用 CR / 平台结算 DR 为增加，反向为减少；带符号合计为 0）、
// 流水不可删、审计日志 append-only、防重复提交（idempotency_key 唯一，重复提交返回原单）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	BalAdjExecuted = 1 // 已入账（本表唯一终态）
	BalAdjCredit   = 1 // 增加用户可用余额
	BalAdjDebit    = 2 // 减少用户可用余额
)

const (
	ReasonActivityReward = "activity_reward" // 活动奖励
	ReasonCompensation   = "compensation"   // 补偿赔付
	ReasonCorrection     = "correction"     // 账务纠错
	ReasonInternalTest   = "internal_test"  // 内部测试
	ReasonOther          = "other"          // 其他（备注必填）
)

var balanceAdjustReasons = map[string]bool{
	ReasonActivityReward: true,
	ReasonCompensation:   true,
	ReasonCorrection:     true,
	ReasonInternalTest:   true,
	ReasonOther:          true,
}

var errBalanceAdjustNotFound = errors.New("balance adjustment not found")

type BalanceAdjustment struct {
	AdjustmentID     string    `json:"adjustment_id"`
	UserID           string    `json:"user_id"`
	Direction        int       `json:"direction"` // 1 增加可用余额 2 减少可用余额
	AmountCents      int64     `json:"amount_cents"`
	ReasonCode       string    `json:"reason_code"`
	Note             string    `json:"note"`
	Status           int       `json:"status"` // 1 已入账
	CreatedByAdminID string    `json:"created_by_admin_id"`
	ExecutedTxnID    string    `json:"executed_txn_id"`
	IdempotencyKey   string    `json:"idempotency_key"`
	CreatedAt        time.Time `json:"created_at"`
	ExecutedAt       time.Time `json:"executed_at"`
}

const balanceAdjustmentColumns = `adjustment_id, user_id, direction, amount_cents, reason_code, note, status, created_by_admin_id, executed_txn_id, idempotency_key, created_at, executed_at`

func scanBalanceAdjustment(row interface{ Scan(...any) error }) (*BalanceAdjustment, error) {
	var a BalanceAdjustment
	var idem sql.NullString
	if err := row.Scan(&a.AdjustmentID, &a.UserID, &a.Direction, &a.AmountCents, &a.ReasonCode, &a.Note,
		&a.Status, &a.CreatedByAdminID, &a.ExecutedTxnID, &idem, &a.CreatedAt, &a.ExecutedAt); err != nil {
		return nil, err
	}
	if idem.Valid {
		a.IdempotencyKey = idem.String
	}
	return &a, nil
}

func (s *Server) getBalanceAdjustment(ctx context.Context, adjustmentID string) (*BalanceAdjustment, error) {
	a, err := scanBalanceAdjustment(s.db.QueryRowContext(ctx,
		`SELECT `+balanceAdjustmentColumns+` FROM fund_balance_adjustment WHERE adjustment_id=$1`, adjustmentID))
	if err == sql.ErrNoRows {
		return nil, errBalanceAdjustNotFound
	}
	return a, err
}

func (s *Server) getBalanceAdjustmentByKey(ctx context.Context, idemKey string) (*BalanceAdjustment, error) {
	a, err := scanBalanceAdjustment(s.db.QueryRowContext(ctx,
		`SELECT `+balanceAdjustmentColumns+` FROM fund_balance_adjustment WHERE idempotency_key=$1`, idemKey))
	if err == sql.ErrNoRows {
		return nil, errBalanceAdjustNotFound
	}
	return a, err
}

func (s *Server) listBalanceAdjustments(ctx context.Context, userID string, status, limit, offset int) ([]BalanceAdjustment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+balanceAdjustmentColumns+` FROM fund_balance_adjustment WHERE ($1='' OR user_id=$1) AND ($2<0 OR status=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		userID, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BalanceAdjustment{}
	for rows.Next() {
		a, err := scanBalanceAdjustment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// createBalanceAdjustment：单人提交、同一事务内建单 + 双重记账入账。
// 幂等：idempotency_key 命中时直接返回原单，不重复记账；并发重复提交撞唯一索引时回查原单返回。
func (s *Server) createBalanceAdjustment(ctx context.Context, adminID, userID string, direction int, amount int64, reasonCode, note, idemKey string) (*BalanceAdjustment, error) {
	if !validUserID(userID) {
		return nil, errors.New("invalid user_id")
	}
	if userID == PlatformUserID {
		return nil, errors.New("platform settlement account is not operable via API")
	}
	if direction != BalAdjCredit && direction != BalAdjDebit {
		return nil, errors.New("direction must be 1 (credit) or 2 (debit)")
	}
	if amount <= 0 {
		return nil, errors.New("amount_cents must be > 0")
	}
	if !balanceAdjustReasons[reasonCode] {
		return nil, errors.New("invalid reason_code")
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 500 {
		return nil, errors.New("note must be <= 500 chars")
	}
	if reasonCode == ReasonOther && note == "" {
		return nil, errors.New("note is required when reason_code is other")
	}
	if _, err := s.getAccount(ctx, userID); err == sql.ErrNoRows {
		return nil, errors.New("wallet not found; create it first")
	} else if err != nil {
		return nil, err
	}
	if idemKey != "" {
		if existing, err := s.getBalanceAdjustmentByKey(ctx, idemKey); err == nil {
			return existing, nil
		} else if err != errBalanceAdjustNotFound {
			return nil, err
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	adjID := newID("baladj_")
	txnID := newID("txn_")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO fund_balance_adjustment (adjustment_id, user_id, direction, amount_cents, reason_code, note, status, created_by_admin_id, executed_txn_id, idempotency_key)
		 VALUES ($1,$2,$3,$4,$5,$6,1,$7,$8,$9)`,
		adjID, userID, direction, amount, reasonCode, note, adminID, txnID, nullStr(idemKey)); err != nil {
		_ = tx.Rollback()
		if isUniqueViolation(err) && idemKey != "" {
			if existing, err2 := s.getBalanceAdjustmentByKey(ctx, idemKey); err2 == nil {
				return existing, nil
			}
		}
		return nil, err
	}
	// 涉及账户按 user_id 排序加锁防死锁（与 Phase 7 执行同一约定）。
	users := []string{userID, PlatformUserID}
	if users[0] > users[1] {
		users[0], users[1] = users[1], users[0]
	}
	states := map[string]*accountState{}
	for _, u := range users {
		var st accountState
		if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&st.balance, &st.frozen, &st.version, &st.status); err != nil {
			return nil, err
		}
		if st.status != 0 {
			return nil, fmt.Errorf("wallet not normal: %s status=%d", u, st.status)
		}
		states[u] = &st
	}
	type le struct {
		uid      string
		dir      int
		balAfter int64
		frzAfter int64
		oppUID   string
		oppAcc   string
	}
	var entries []le
	userSt, platSt := states[userID], states[PlatformUserID]
	if direction == BalAdjCredit {
		entries = []le{
			{userID, DirCredit, userSt.balance + amount, userSt.frozen, PlatformUserID, "settlement"},
			{PlatformUserID, DirDebit, platSt.balance + amount, platSt.frozen, userID, "available"},
		}
		userSt.balance += amount
		platSt.balance += amount
	} else {
		if userSt.balance-amount < 0 {
			return nil, fmt.Errorf("insufficient available balance for %s", userID)
		}
		entries = []le{
			{userID, DirDebit, userSt.balance - amount, userSt.frozen, PlatformUserID, "settlement"},
			{PlatformUserID, DirCredit, platSt.balance - amount, platSt.frozen, userID, "available"},
		}
		// 平台侧与 Phase 7 执行同模型：支出同向时平台贷记使余额减少；平台余额恒 >= 0，
		// 若平台侧不足则整笔回滚，不留有效单据。
		if platSt.balance-amount < 0 {
			return nil, fmt.Errorf("insufficient platform settlement balance")
		}
		userSt.balance -= amount
		platSt.balance -= amount
	}
	var sum int64
	for _, e := range entries {
		sum += signed(e.dir, amount)
	}
	if sum != 0 {
		return nil, fmt.Errorf("double-entry sum must be 0, got %d", sum)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'balance_adjustment',$4,1,$5)`,
		txnID, userID, TxnAdminAdjustment, adjID, "baladj-"+adjID); err != nil {
		return nil, err
	}
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, e.uid, AcctAvailable, e.dir, amount, e.balAfter, e.frzAfter, e.oppUID, e.oppAcc); err != nil {
			return nil, err
		}
	}
	for _, u := range users {
		st := states[u]
		res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, st.balance, st.frozen, u, st.version)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, fmt.Errorf("optimistic lock failed for %s", u)
		}
	}
	if err := insertAudit(ctx, tx, userID, adminID, "balance_adjustment",
		fmt.Sprintf("adjustment_id=%s direction=%d amount=%d reason=%s operator=%s note=%s", adjID, direction, amount, reasonCode, adminID, note), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getBalanceAdjustment(ctx, adjID)
}

// ---- HTTP ----

type balanceAdjustCreateReq struct {
	UserID         string `json:"user_id"`
	Direction      int    `json:"direction"`
	AmountCents    int64  `json:"amount_cents"`
	ReasonCode     string `json:"reason_code"`
	Note           string `json:"note"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) hAdminCreateBalanceAdjustment(w http.ResponseWriter, r *http.Request) {
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	var req balanceAdjustCreateReq
	if !decodeBody(w, r, &req) {
		return
	}
	adj, err := s.createBalanceAdjustment(r.Context(), adminID, req.UserID, req.Direction, req.AmountCents, req.ReasonCode, req.Note, req.IdempotencyKey)
	if err != nil {
		adminErr(w, err)
		return
	}
	ok(w, adj)
}

func (s *Server) hAdminListBalanceAdjustments(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	limit, offset := adminPageParams(r)
	list, err := s.listBalanceAdjustments(r.Context(), r.URL.Query().Get("user_id"), adminStatusParam(r), limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) hAdminGetBalanceAdjustment(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/admin/balance-adjustments/")
	if id == "" || strings.Contains(id, "/") {
		fail(w, http.StatusNotFound, "NOT_FOUND", "adjustment_id is required")
		return
	}
	adj, err := s.getBalanceAdjustment(r.Context(), id)
	if err != nil {
		adminErr(w, err)
		return
	}
	ok(w, adj)
}

// adminStatusParam / adminPageParams 复用 admin.go 的管理端分页参数解析。
