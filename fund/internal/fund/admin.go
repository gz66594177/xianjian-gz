package fund

// Phase 7：管理端资金中心 API。
// 全部接口走 X-Admin-Secret 鉴权；双人复核类（调整单 approve、提现订单 approve）另须 X-Admin-ID。
// 硬约束：
//   - 管理端绝不直接改余额：调整单执行走统一双重记账事务（txn type 19），与红包/转账同一模型。
//   - 管理端绝不看密码或哈希：交易密码管理查询的 SQL 不 SELECT password_hash 列，响应无哈希字段。
//   - 管理端不得修改用户提现绑定信息：提现账户管理端只提供 GET（列表/默认），无 PUT/DELETE/POST。
//   - 调整单只查不改：无修改接口；执行后不可撤销、不可拒绝，纠错另建反向调整单（须注明原单号，
//     且原单须已执行、方向相反）。
//   - 提现订单审核为脚手架（Phase 9 预留）：审核通过只改订单状态，不产生任何打款动作；
//     3 处理中 / 4 成功 / 5 失败 / 6 已退款为 Phase 9 流转，本阶段不提供写入口。
//   - 风控配置：单笔上限接入转账发起路径；单日上限本阶段只存配置、不接入执行（文档写清）。
//   - 敏感操作（调整单创建/复核/拒绝/执行、提现审核、限额变更）一律记 wallet_audit_log（append-only，
//     detail 已脱敏，不含卡号明文、密码、哈希）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const TxnAdminAdjustment = 19 // 管理员资金调整（ref_type='admin_adjustment', ref_id=adjustment_id）

const (
	AdjPending  = 0 // 待复核
	AdjExecuted = 1 // 已执行（终态，不可改、不可撤销）
	AdjRejected = 2 // 已拒绝（终态）

	AdjCredit = 1 // 增加用户可用余额
	AdjDebit  = 2 // 减少用户可用余额
)

const (
	WithdrawOrderPending    = 0 // 待审核
	WithdrawOrderApproved   = 1 // 已通过（仅改状态，无打款）
	WithdrawOrderRejected   = 2 // 已拒绝
	WithdrawOrderProcessing = 3 // 处理中（Phase 9）
	WithdrawOrderSuccess    = 4 // 成功（Phase 9 打款后）
	WithdrawOrderFailed     = 5 // 失败（Phase 9）
	WithdrawOrderRefunded   = 6 // 已退款（Phase 9）
)

var (
	errAdjustmentNotFound    = errors.New("adjustment not found")
	errAdjustmentNotPending  = errors.New("adjustment is not pending")
	errAdjustmentSameAdmin   = errors.New("second approval must come from a different admin")
	errWithdrawOrderNotFound = errors.New("withdraw order not found")
	errWithdrawOrderNotWdraw = errors.New("order is not a withdraw order")
	errWithdrawOrderNotPend  = errors.New("withdraw order is not pending review")
)

type AdminAdjustment struct {
	AdjustmentID         string     `json:"adjustment_id"`
	UserID               string     `json:"user_id"`
	Direction            int        `json:"direction"` // 1 增加可用余额 2 减少可用余额
	AmountCents          int64      `json:"amount_cents"`
	Reason               string     `json:"reason"`
	Status               int        `json:"status"` // 0 待复核 1 已执行 2 已拒绝
	CreatedByAdminID     string     `json:"created_by_admin_id"`
	FirstAdminID         *string    `json:"first_admin_id"`
	SecondAdminID        *string    `json:"second_admin_id"`
	RejectedByAdminID    *string    `json:"rejected_by_admin_id"`
	OriginalAdjustmentID *string    `json:"original_adjustment_id"`
	ExecutedTxnID        *string    `json:"executed_txn_id"`
	ExecutedAt           *time.Time `json:"executed_at"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

const adminAdjustmentColumns = `adjustment_id, user_id, direction, amount_cents, reason, status, created_by_admin_id, first_admin_id, second_admin_id, rejected_by_admin_id, original_adjustment_id, executed_txn_id, executed_at, created_at, updated_at`

func scanAdminAdjustment(row interface{ Scan(...any) error }) (*AdminAdjustment, error) {
	var a AdminAdjustment
	if err := row.Scan(&a.AdjustmentID, &a.UserID, &a.Direction, &a.AmountCents, &a.Reason, &a.Status,
		&a.CreatedByAdminID, &a.FirstAdminID, &a.SecondAdminID, &a.RejectedByAdminID, &a.OriginalAdjustmentID,
		&a.ExecutedTxnID, &a.ExecutedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// AdminTxPasswordStatus：管理端交易密码状态。注意：本结构与查询均不含哈希；
// 查询 SQL 绝不 SELECT password_hash 列（测试断言响应体不含 password_hash 与 $2a$）。
type AdminTxPasswordStatus struct {
	UserID        string     `json:"user_id"`
	HasPassword   bool       `json:"has_password"`
	ErrorCount    int        `json:"error_count"`
	Locked        bool       `json:"locked"`
	LockedUntil   *time.Time `json:"locked_until"`
	LastChangedAt *time.Time `json:"last_changed_at"`
}

func (s *Server) adminTxPasswordStatus(ctx context.Context, userID string) (*AdminTxPasswordStatus, error) {
	var st AdminTxPasswordStatus
	st.UserID = userID
	var lockedUntil, lastChanged sql.NullTime
	var errCount int
	err := s.db.QueryRowContext(ctx,
		`SELECT error_count, locked_until, last_changed_at FROM wallet_tx_password WHERE user_id=$1`, userID).
		Scan(&errCount, &lockedUntil, &lastChanged)
	if err == sql.ErrNoRows {
		return &st, nil
	}
	if err != nil {
		return nil, err
	}
	st.HasPassword = true
	st.ErrorCount = errCount
	if lockedUntil.Valid {
		t := lockedUntil.Time
		st.LockedUntil = &t
		st.Locked = time.Now().Before(t)
	}
	if lastChanged.Valid {
		t := lastChanged.Time
		st.LastChangedAt = &t
	}
	return &st, nil
}

func (s *Server) adminDefaultMethod(ctx context.Context, userID string) (*WithdrawMethod, error) {
	var m WithdrawMethod
	var enc string
	err := s.db.QueryRowContext(ctx,
		`SELECT method_id, user_id, type, real_name, account_no_enc, bank_name, bank_branch, is_default, status, bind_time
		 FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 AND is_default ORDER BY bind_time LIMIT 1`, userID).
		Scan(&m.MethodID, &m.UserID, &m.Type, &m.RealName, &enc, &m.BankName, &m.BankBranch, &m.IsDefault, &m.Status, &m.BindTime)
	if err != nil {
		return nil, err
	}
	m.AccountNo = maskFromEnc(s, enc)
	return &m, nil
}

func (s *Server) adminListRecoveries(ctx context.Context, userID string, status int) ([]RecoveryRequest, error) {
	q := `SELECT request_id, user_id, status, first_admin_id, second_admin_id, created_at, expires_at FROM wallet_tx_password_recovery WHERE ($1='' OR user_id=$1) AND ($2<0 OR status=$2) ORDER BY created_at DESC LIMIT 200`
	rows, err := s.db.QueryContext(ctx, q, userID, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecoveryRequest{}
	for rows.Next() {
		var r RecoveryRequest
		if err := rows.Scan(&r.RequestID, &r.UserID, &r.Status, &r.FirstAdminID, &r.SecondAdminID, &r.CreatedAt, &r.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- 管理员资金调整单 ----

func (s *Server) createAdjustment(ctx context.Context, adminID, userID string, direction int, amount int64, reason, originalID string) (*AdminAdjustment, error) {
	if !validUserID(userID) {
		return nil, errors.New("invalid user_id")
	}
	if userID == PlatformUserID {
		return nil, errors.New("platform settlement account is not operable via API")
	}
	if direction != AdjCredit && direction != AdjDebit {
		return nil, errors.New("direction must be 1 (credit) or 2 (debit)")
	}
	if amount <= 0 {
		return nil, errors.New("amount_cents must be > 0")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || utf8.RuneCountInString(reason) > 500 {
		return nil, errors.New("reason is required and must be <= 500 chars")
	}
	if _, err := s.getAccount(ctx, userID); err == sql.ErrNoRows {
		return nil, errors.New("wallet not found; create it first")
	} else if err != nil {
		return nil, err
	}
	if originalID != "" {
		orig, err := s.getAdjustment(ctx, originalID)
		if err != nil {
			return nil, errors.New("original adjustment not found: " + originalID)
		}
		if orig.Status != AdjExecuted {
			return nil, errors.New("original adjustment must be executed before a reversal adjustment can reference it")
		}
		if orig.Direction == direction {
			return nil, errors.New("reversal adjustment direction must be opposite to the original adjustment")
		}
	}
	adjID := newID("adj_")
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO fund_admin_adjustment (adjustment_id, user_id, direction, amount_cents, reason, status, created_by_admin_id, original_adjustment_id)
		 VALUES ($1,$2,$3,$4,$5,0,$6,$7)`,
		adjID, userID, direction, amount, reason, adminID, nullStr(originalID))
	if err != nil {
		return nil, err
	}
	s.auditStandalone(ctx, userID, adminID, "admin_adjustment_create",
		fmt.Sprintf("adjustment_id=%s direction=%d amount=%d", adjID, direction, amount), nil)
	return s.getAdjustment(ctx, adjID)
}

func (s *Server) getAdjustment(ctx context.Context, adjustmentID string) (*AdminAdjustment, error) {
	a, err := scanAdminAdjustment(s.db.QueryRowContext(ctx,
		`SELECT `+adminAdjustmentColumns+` FROM fund_admin_adjustment WHERE adjustment_id=$1`, adjustmentID))
	if err == sql.ErrNoRows {
		return nil, errAdjustmentNotFound
	}
	return a, err
}

func (s *Server) listAdjustments(ctx context.Context, userID string, status, limit, offset int) ([]AdminAdjustment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+adminAdjustmentColumns+` FROM fund_admin_adjustment WHERE ($1='' OR user_id=$1) AND ($2<0 OR status=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		userID, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminAdjustment{}
	for rows.Next() {
		a, err := scanAdminAdjustment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// approveAdjustment：双人复核。第一人记录 first_admin_id，单据仍待复核、余额不动；
// 第二名不同管理员触发执行：第二复核与记账在同一事务内完成（行锁 + 状态重查），
// 记账失败（如减额超余额）时整笔回滚，第二复核人不落单，单据仍为“待第二复核”可重试。
func (s *Server) approveAdjustment(ctx context.Context, adjustmentID, adminID string) (string, error) {
	rec, err := s.getAdjustment(ctx, adjustmentID)
	if err != nil {
		return "", err
	}
	if rec.Status != AdjPending {
		return "", errAdjustmentNotPending
	}
	if rec.FirstAdminID == nil {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE fund_admin_adjustment SET first_admin_id=$1, updated_at=now() WHERE adjustment_id=$2 AND status=0 AND first_admin_id IS NULL`,
			adminID, adjustmentID); err != nil {
			return "", err
		}
		s.auditStandalone(ctx, rec.UserID, adminID, "admin_adjustment_approve",
			"adjustment approve1 adjustment_id="+adjustmentID, nil)
		return "awaiting_second_approval", nil
	}
	if *rec.FirstAdminID == adminID {
		return "", errAdjustmentSameAdmin
	}
	if err := s.executeAdjustment(ctx, adjustmentID, adminID); err != nil {
		return "", err
	}
	return "executed", nil
}

// executeAdjustment：第二复核 + 双重记账执行（同一事务）。
// 增加：用户可用 CR / 平台结算 DR（与收入同向）；减少：用户可用 DR / 平台结算 CR（与支出同向）。
func (s *Server) executeAdjustment(ctx context.Context, adjustmentID, adminID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var userID string
	var direction int
	var amount int64
	var status int
	var firstAdmin sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id, direction, amount_cents, status, first_admin_id FROM fund_admin_adjustment WHERE adjustment_id=$1 FOR UPDATE`,
		adjustmentID).Scan(&userID, &direction, &amount, &status, &firstAdmin); err != nil {
		if err == sql.ErrNoRows {
			return errAdjustmentNotFound
		}
		return err
	}
	if status != AdjPending || !firstAdmin.Valid || firstAdmin.String == adminID {
		if status != AdjPending {
			return errAdjustmentNotPending
		}
		return errAdjustmentSameAdmin
	}
	// 涉及账户按 user_id 排序加锁防死锁（与 postLedger 同一约定）。
	users := []string{userID, PlatformUserID}
	if users[0] > users[1] {
		users[0], users[1] = users[1], users[0]
	}
	states := map[string]*accountState{}
	for _, u := range users {
		var st accountState
		if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&st.balance, &st.frozen, &st.version, &st.status); err != nil {
			return err
		}
		if st.status != 0 {
			return fmt.Errorf("wallet not normal: %s status=%d", u, st.status)
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
	if direction == AdjCredit {
		entries = []le{
			{userID, DirCredit, userSt.balance + amount, userSt.frozen, PlatformUserID, "settlement"},
			{PlatformUserID, DirDebit, platSt.balance + amount, platSt.frozen, userID, "available"},
		}
		userSt.balance += amount
		platSt.balance += amount
	} else {
		if userSt.balance-amount < 0 {
			return fmt.Errorf("insufficient available balance for %s", userID)
		}
		entries = []le{
			{userID, DirDebit, userSt.balance - amount, userSt.frozen, PlatformUserID, "settlement"},
			{PlatformUserID, DirCredit, platSt.balance - amount, platSt.frozen, userID, "available"},
		}
		// 平台户余额按资产性质借增贷减：支出同向时平台贷记使余额减少；平台余额恒 >= 0，
		// 若平台侧不足则整笔回滚（调整单仍待第二复核），绝不产生半笔账。
		if platSt.balance-amount < 0 {
			return fmt.Errorf("insufficient platform settlement balance")
		}
		userSt.balance -= amount
		platSt.balance -= amount
	}
	// 带符号合计校验（贷+ / 借- 必须为 0）。
	var sum int64
	for _, e := range entries {
		sum += signed(e.dir, amount)
	}
	if sum != 0 {
		return fmt.Errorf("double-entry sum must be 0, got %d", sum)
	}
	txnID := newID("txn_")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'admin_adjustment',$4,1,$5)`,
		txnID, userID, TxnAdminAdjustment, adjustmentID, "adj-"+adjustmentID); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, e.uid, AcctAvailable, e.dir, amount, e.balAfter, e.frzAfter, e.oppUID, e.oppAcc); err != nil {
			return err
		}
	}
	for _, u := range users {
		st := states[u]
		res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, st.balance, st.frozen, u, st.version)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("optimistic lock failed for %s", u)
		}
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE fund_admin_adjustment SET status=1, second_admin_id=$1, executed_txn_id=$2, executed_at=now(), updated_at=now() WHERE adjustment_id=$3 AND status=0`,
		adminID, txnID, adjustmentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errAdjustmentNotPending
	}
	if err := insertAudit(ctx, tx, userID, adminID, "admin_adjustment_execute",
		fmt.Sprintf("adjustment_id=%s direction=%d amount=%d txn_id=%s", adjustmentID, direction, amount, txnID), "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) rejectAdjustment(ctx context.Context, adjustmentID, adminID string) error {
	rec, err := s.getAdjustment(ctx, adjustmentID)
	if err != nil {
		return err
	}
	if rec.Status != AdjPending {
		return errAdjustmentNotPending
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE fund_admin_adjustment SET status=2, rejected_by_admin_id=$1, updated_at=now() WHERE adjustment_id=$2 AND status=0`,
		adminID, adjustmentID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errAdjustmentNotPending
	}
	s.auditStandalone(ctx, rec.UserID, adminID, "admin_adjustment_reject", "adjustment rejected adjustment_id="+adjustmentID, nil)
	return nil
}

// ---- 提现订单审核脚手架（Phase 9 预留；审核通过只改订单状态，绝无打款动作） ----

type WithdrawOrderView struct {
	OrderID             string     `json:"order_id"`
	UserID              string     `json:"user_id"`
	OrderType           int        `json:"order_type"`
	AmountCents         int64      `json:"amount_cents"`
	FeeCents            int64      `json:"fee_cents"`
	ActualCents         int64      `json:"actual_cents"`
	Status              int        `json:"status"` // 提现 7 态：0 待审核 1 已通过 2 已拒绝 3 处理中 4 成功 5 失败 6 已退款
	WithdrawMethodID    *string    `json:"withdraw_method_id"`
	ChannelOrderNo      *string    `json:"channel_order_no"`
	ReviewFirstAdminID  *string    `json:"review_first_admin_id"`
	ReviewSecondAdminID *string    `json:"review_second_admin_id"`
	RejectedByAdminID   *string    `json:"rejected_by_admin_id"`
	ReviewNote          *string    `json:"review_note"`
	ReviewedAt          *time.Time `json:"reviewed_at"`
	FreezeID            *string    `json:"freeze_id,omitempty"`          // Phase 9：提交冻结行追溯（Phase 7 种子单为空）
	FailureReason       *string    `json:"failure_reason,omitempty"`     // Phase 9：处理失败原因（充值失败原因复用同列）
	PayoutProofNo       *string    `json:"payout_proof_no,omitempty"`    // Phase 9：人工打款凭证（银行/渠道回单号）
	PayoutAt            *time.Time `json:"payout_at,omitempty"`          // Phase 9：打款时间
	PayoutOperatorID    *string    `json:"payout_operator_id,omitempty"` // Phase 9：打款确认操作员
	PayoutNote          *string    `json:"payout_note,omitempty"`        // Phase 9：打款备注（≤500 字）
	CreatedAt           time.Time  `json:"created_at"`
	UpdatedAt           time.Time  `json:"updated_at"`
}

const withdrawOrderColumns = `order_id, user_id, order_type, amount_cents, fee_cents, actual_cents, status, withdraw_method_id, channel_order_no, review_first_admin_id, review_second_admin_id, rejected_by_admin_id, review_note, reviewed_at, freeze_id, failure_reason, payout_proof_no, payout_at, payout_operator_id, payout_note, created_at, updated_at`

func scanWithdrawOrder(row interface{ Scan(...any) error }) (*WithdrawOrderView, error) {
	var o WithdrawOrderView
	if err := row.Scan(&o.OrderID, &o.UserID, &o.OrderType, &o.AmountCents, &o.FeeCents, &o.ActualCents, &o.Status,
		&o.WithdrawMethodID, &o.ChannelOrderNo, &o.ReviewFirstAdminID, &o.ReviewSecondAdminID, &o.RejectedByAdminID,
		&o.ReviewNote, &o.ReviewedAt, &o.FreezeID, &o.FailureReason, &o.PayoutProofNo, &o.PayoutAt, &o.PayoutOperatorID,
		&o.PayoutNote, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

func (s *Server) getWithdrawOrder(ctx context.Context, orderID string) (*WithdrawOrderView, error) {
	o, err := scanWithdrawOrder(s.db.QueryRowContext(ctx,
		`SELECT `+withdrawOrderColumns+` FROM wallet_order WHERE order_id=$1`, orderID))
	if err == sql.ErrNoRows {
		return nil, errWithdrawOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	if o.OrderType != 2 {
		return nil, errWithdrawOrderNotWdraw
	}
	return o, nil
}

func (s *Server) listWithdrawOrders(ctx context.Context, userID string, status, limit, offset int) ([]WithdrawOrderView, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+withdrawOrderColumns+` FROM wallet_order WHERE order_type=2 AND ($1='' OR user_id=$1) AND ($2<0 OR status=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		userID, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WithdrawOrderView{}
	for rows.Next() {
		o, err := scanWithdrawOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (s *Server) approveWithdrawOrder(ctx context.Context, orderID, adminID string) (string, error) {
	o, err := s.getWithdrawOrder(ctx, orderID)
	if err != nil {
		return "", err
	}
	if o.Status != WithdrawOrderPending {
		return "", errWithdrawOrderNotPend
	}
	if o.ReviewFirstAdminID == nil {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE wallet_order SET review_first_admin_id=$1, updated_at=now() WHERE order_id=$2 AND status=0 AND review_first_admin_id IS NULL`,
			adminID, orderID); err != nil {
			return "", err
		}
		s.auditStandalone(ctx, o.UserID, adminID, "admin_withdraw_review_approve", "withdraw approve1 order_id="+orderID, nil)
		return "awaiting_second_approval", nil
	}
	if *o.ReviewFirstAdminID == adminID {
		return "", errAdjustmentSameAdmin
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE wallet_order SET review_second_admin_id=$1, status=1, reviewed_at=now(), updated_at=now() WHERE order_id=$2 AND status=0`,
		adminID, orderID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", errWithdrawOrderNotPend
	}
	s.auditStandalone(ctx, o.UserID, adminID, "admin_withdraw_review_approve", "withdraw approve2 order_id="+orderID, nil)
	return "approved", nil
}

func (s *Server) rejectWithdrawOrder(ctx context.Context, orderID, adminID, note string) error {
	o, err := s.getWithdrawOrder(ctx, orderID)
	if err != nil {
		return err
	}
	if o.Status != WithdrawOrderPending {
		return errWithdrawOrderNotPend
	}
	if utf8.RuneCountInString(note) > 500 {
		return errors.New("review_note must be <= 500 chars")
	}
	// Phase 9：真实提现单带冻结行，拒绝必须在同一事务内全额解冻（金额 + 手续费回可用）；
	// Phase 7 种子单无冻结行，走旧路径只改状态（Test90 断言：审核前后余额与 txn 数均不变）。
	if o.FreezeID != nil {
		return s.rejectWithdrawWithUnfreeze(ctx, orderID, adminID, note)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE wallet_order SET status=2, rejected_by_admin_id=$1, review_note=$2, reviewed_at=now(), updated_at=now() WHERE order_id=$3 AND status=0`,
		adminID, nullStr(note), orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errWithdrawOrderNotPend
	}
	s.auditStandalone(ctx, o.UserID, adminID, "admin_withdraw_review_reject", "withdraw rejected order_id="+orderID, nil)
	return nil
}

// ---- 风控限额配置 ----

type RiskLimits struct {
	SingleMaxCents *int64    `json:"single_max_cents"` // nil = 不限额
	DailyMaxCents  *int64    `json:"daily_max_cents"`  // nil = 不限额；本阶段仅存配置、不接入执行
	UpdatedBy      *string   `json:"updated_by"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (s *Server) getRiskLimits(ctx context.Context) (*RiskLimits, error) {
	var r RiskLimits
	var single, daily sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT single_max_cents, daily_max_cents, updated_by, updated_at FROM fund_risk_limit WHERE config_key='default'`).Scan(&single, &daily, &r.UpdatedBy, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return &RiskLimits{UpdatedAt: time.Now()}, nil
	}
	if err != nil {
		return nil, err
	}
	if single.Valid {
		v := single.Int64
		r.SingleMaxCents = &v
	}
	if daily.Valid {
		v := daily.Int64
		r.DailyMaxCents = &v
	}
	return &r, nil
}

func (s *Server) updateRiskLimits(ctx context.Context, adminID string, single, daily *int64) (*RiskLimits, error) {
	if single != nil && *single < 0 {
		return nil, errors.New("single_max_cents must be >= 0 or null for unlimited")
	}
	if daily != nil && *daily < 0 {
		return nil, errors.New("daily_max_cents must be >= 0 or null for unlimited")
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO fund_risk_limit (config_key, single_max_cents, daily_max_cents, updated_by) VALUES ('default',$1,$2,$3)
		 ON CONFLICT (config_key) DO UPDATE SET single_max_cents=EXCLUDED.single_max_cents, daily_max_cents=EXCLUDED.daily_max_cents, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		nullInt64(single), nullInt64(daily), adminID); err != nil {
		return nil, err
	}
	singleStr, dailyStr := "unlimited", "unlimited"
	if single != nil {
		singleStr = fmt.Sprintf("%d", *single)
	}
	if daily != nil {
		dailyStr = fmt.Sprintf("%d", *daily)
	}
	s.auditStandalone(ctx, "", adminID, "admin_risk_limit_update",
		"risk limits single_max="+singleStr+" daily_max="+dailyStr, nil)
	return s.getRiskLimits(ctx)
}

func nullInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// getSingleLimit：转账发起路径的单笔上限；nil = 不限额；配置行缺失同样视为不限额。
func (s *Server) getSingleLimit(ctx context.Context) (*int64, error) {
	r, err := s.getRiskLimits(ctx)
	if err != nil {
		return nil, err
	}
	return r.SingleMaxCents, nil
}

// ---- 审计日志管理查询（只读；detail 写入时已脱敏） ----

type AuditLogEntry struct {
	LogID        int64     `json:"log_id"`
	UserID       string    `json:"user_id"`
	OperatorID   *string   `json:"operator_id"`
	Action       string    `json:"action"`
	DetailMasked *string   `json:"detail_masked"`
	IP           *string   `json:"ip"`
	CreatedAt    time.Time `json:"created_at"`
}

func (s *Server) adminListAuditLogs(ctx context.Context, userID, action string, limit, offset int) ([]AuditLogEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT log_id, user_id, operator_id, action, detail_masked, ip, created_at FROM wallet_audit_log WHERE ($1='' OR user_id=$1) AND ($2='' OR action=$2) ORDER BY log_id DESC LIMIT $3 OFFSET $4`,
		userID, action, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditLogEntry{}
	for rows.Next() {
		var e AuditLogEntry
		if err := rows.Scan(&e.LogID, &e.UserID, &e.OperatorID, &e.Action, &e.DetailMasked, &e.IP, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- HTTP ----

func adminPageParams(r *http.Request) (limit, offset int) {
	limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ = strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func adminStatusParam(r *http.Request) int {
	v := r.URL.Query().Get("status")
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -1
	}
	return n
}

func adminPathParts(r *http.Request, prefix string) []string {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return nil
	}
	return strings.Split(rest, "/")
}

func adminErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAdjustmentNotFound) || errors.Is(err, errBalanceAdjustNotFound) || errors.Is(err, errWithdrawOrderNotFound):
		fail(w, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "NOT_FOUND", "resource not found")
	case errors.Is(err, errAdjustmentNotPending) || errors.Is(err, errWithdrawOrderNotPend) || errors.Is(err, errAdjustmentSameAdmin):
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
	case errors.Is(err, errWithdrawOrderNotWdraw):
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
	default:
		msg := err.Error()
		if strings.Contains(msg, "not found") || strings.Contains(msg, "not normal") || strings.Contains(msg, "insufficient") {
			fail(w, http.StatusBadRequest, "BUSINESS_ERROR", msg)
			return
		}
		if strings.Contains(msg, "invalid") || strings.Contains(msg, "must be") || strings.Contains(msg, "required") || strings.Contains(msg, "not operable") || strings.Contains(msg, "opposite") || strings.Contains(msg, "already") || strings.Contains(msg, "executed") {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", msg)
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", msg)
	}
}

// hAdminWallet：GET /v1/admin/wallets/{user_id} | /{user_id}/ledger | /{user_id}/finance-status
func (s *Server) hAdminWallet(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	parts := adminPathParts(r, "/v1/admin/wallets/")
	if len(parts) == 0 {
		fail(w, http.StatusNotFound, "NOT_FOUND", "user_id is required")
		return
	}
	uid := parts[0]
	if uid == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "user_id is required")
		return
	}
	if len(parts) == 1 {
		acct, err := s.getAccount(r.Context(), uid)
		if err != nil {
			adminErr(w, err)
			return
		}
		ok(w, acct)
		return
	}
	if len(parts) == 2 && parts[1] == "ledger" {
		if _, err := s.getAccount(r.Context(), uid); err != nil {
			adminErr(w, err)
			return
		}
		limit, offset := adminPageParams(r)
		rows, err := s.db.QueryContext(r.Context(),
			`SELECT entry_id, txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account, created_at
			 FROM wallet_ledger WHERE user_id=$1 ORDER BY entry_id DESC LIMIT $2 OFFSET $3`, uid, limit, offset)
		if err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		defer rows.Close()
		out := []LedgerEntry{}
		for rows.Next() {
			var e LedgerEntry
			if err := rows.Scan(&e.EntryID, &e.TxnID, &e.UserID, &e.AccountType, &e.Direction, &e.AmountCents, &e.BalanceAfterCents, &e.FrozenAfterCents, &e.OppositeUserID, &e.OppositeAccount, &e.CreatedAt); err != nil {
				fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
				return
			}
			out = append(out, e)
		}
		ok(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "finance-status" {
		st, err := s.financeStatus(r.Context(), uid)
		if err != nil {
			adminErr(w, err)
			return
		}
		ok(w, st)
		return
	}
	fail(w, http.StatusNotFound, "NOT_FOUND", "unknown admin wallet path")
}

// hAdminWithdrawMethods：GET /v1/admin/withdraw-methods?user_id=（只查；管理端无修改入口）
func (s *Server) hAdminWithdrawMethods(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	uid := r.URL.Query().Get("user_id")
	if uid == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "user_id query param is required")
		return
	}
	methods, err := s.listMethods(r.Context(), uid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, methods)
}

// hAdminDefaultMethod：GET /v1/admin/withdraw-methods/default?user_id=
func (s *Server) hAdminDefaultMethod(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	uid := r.URL.Query().Get("user_id")
	if uid == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "user_id query param is required")
		return
	}
	m, err := s.adminDefaultMethod(r.Context(), uid)
	if err != nil {
		adminErr(w, err)
		return
	}
	ok(w, m)
}

// hAdminTxPasswordStatus：GET /v1/admin/tx-password/status?user_id=（无哈希字段）
func (s *Server) hAdminTxPasswordStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	uid := r.URL.Query().Get("user_id")
	if uid == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "user_id query param is required")
		return
	}
	st, err := s.adminTxPasswordStatus(r.Context(), uid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, st)
}

// hAdminListRecoveries：GET /v1/admin/tx-password/recover?user_id&status
func (s *Server) hAdminListRecoveries(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	list, err := s.adminListRecoveries(r.Context(), r.URL.Query().Get("user_id"), adminStatusParam(r))
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

type adjustmentCreateReq struct {
	UserID               string `json:"user_id"`
	Direction            int    `json:"direction"`
	AmountCents          int64  `json:"amount_cents"`
	Reason               string `json:"reason"`
	OriginalAdjustmentID string `json:"original_adjustment_id"`
}

func (s *Server) hAdminCreateAdjustment(w http.ResponseWriter, r *http.Request) {
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	var req adjustmentCreateReq
	if !decodeBody(w, r, &req) {
		return
	}
	adj, err := s.createAdjustment(r.Context(), adminID, req.UserID, req.Direction, req.AmountCents, req.Reason, req.OriginalAdjustmentID)
	if err != nil {
		adminErr(w, err)
		return
	}
	ok(w, adj)
}

func (s *Server) hAdminAdjustmentsSub(w http.ResponseWriter, r *http.Request) {
	parts := adminPathParts(r, "/v1/admin/adjustments/")
	if len(parts) == 0 {
		fail(w, http.StatusNotFound, "NOT_FOUND", "adjustment_id is required")
		return
	}
	id := parts[0]
	if r.Method == http.MethodGet && len(parts) == 1 {
		if !s.authAdmin(r) {
			fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
			return
		}
		adj, err := s.getAdjustment(r.Context(), id)
		if err != nil {
			adminErr(w, err)
			return
		}
		ok(w, adj)
		return
	}
	if r.Method == http.MethodPost && len(parts) == 2 && (parts[1] == "approve" || parts[1] == "reject") {
		adminID, okAuth := s.authAdminID(r)
		if !okAuth {
			fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
			return
		}
		if parts[1] == "approve" {
			stage, err := s.approveAdjustment(r.Context(), id, adminID)
			if err != nil {
				adminErr(w, err)
				return
			}
			ok(w, map[string]string{"stage": stage})
			return
		}
		if err := s.rejectAdjustment(r.Context(), id, adminID); err != nil {
			adminErr(w, err)
			return
		}
		ok(w, map[string]bool{"rejected": true})
		return
	}
	fail(w, http.StatusNotFound, "NOT_FOUND", "unknown adjustment path")
}

func (s *Server) hAdminListAdjustments(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	limit, offset := adminPageParams(r)
	list, err := s.listAdjustments(r.Context(), r.URL.Query().Get("user_id"), adminStatusParam(r), limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) hAdminListWithdrawOrders(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	limit, offset := adminPageParams(r)
	list, err := s.listWithdrawOrders(r.Context(), r.URL.Query().Get("user_id"), adminStatusParam(r), limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

type withdrawReviewReq struct {
	Note string `json:"note"`
}

func (s *Server) hAdminWithdrawOrdersSub(w http.ResponseWriter, r *http.Request) {
	parts := adminPathParts(r, "/v1/admin/withdraw-orders/")
	if len(parts) == 0 {
		fail(w, http.StatusNotFound, "NOT_FOUND", "order_id is required")
		return
	}
	id := parts[0]
	if r.Method == http.MethodGet && len(parts) == 1 {
		if !s.authAdmin(r) {
			fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
			return
		}
		o, err := s.getWithdrawOrder(r.Context(), id)
		if err != nil {
			adminErr(w, err)
			return
		}
		ok(w, o)
		return
	}
	// Phase 9 新增流转：start-processing / confirm-payout / fail / refund / auto-payout（骨架 501）。
	if r.Method == http.MethodPost && len(parts) == 2 && (parts[1] == "start-processing" || parts[1] == "confirm-payout" || parts[1] == "fail" || parts[1] == "refund" || parts[1] == "auto-payout") {
		s.hAdminWithdrawFlow(w, r, id, parts[1])
		return
	}
	if r.Method == http.MethodPost && len(parts) == 2 && (parts[1] == "approve" || parts[1] == "reject") {
		adminID, okAuth := s.authAdminID(r)
		if !okAuth {
			fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
			return
		}
		if parts[1] == "approve" {
			stage, err := s.approveWithdrawOrder(r.Context(), id, adminID)
			if err != nil {
				adminErr(w, err)
				return
			}
			ok(w, map[string]string{"stage": stage})
			return
		}
		var req withdrawReviewReq
		body, _ := readAll(r)
		if len(body) > 0 {
			var raw map[string]any
			if err := json.Unmarshal(body, &raw); err != nil {
				fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
				return
			}
			if v, ok := raw["note"].(string); ok {
				req.Note = v
			}
		}
		if err := s.rejectWithdrawOrder(r.Context(), id, adminID, req.Note); err != nil {
			adminErr(w, err)
			return
		}
		ok(w, map[string]bool{"rejected": true})
		return
	}
	fail(w, http.StatusNotFound, "NOT_FOUND", "unknown withdraw order path")
}

func (s *Server) hAdminRiskLimits(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	if r.Method == http.MethodGet {
		lim, err := s.getRiskLimits(r.Context())
		if err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		ok(w, lim)
		return
	}
	// PUT：管理端读写限额配置（变更记审计）。按字段部分更新：请求体中缺省的字段保持原值，
	// 显式 null 表示改为不限额；全量覆盖语义会误删另一项，故不用 DisallowUnknownFields 的整单替换。
	cur, err := s.getRiskLimits(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	body, _ := readAll(r)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
		return
	}
	single, daily := cur.SingleMaxCents, cur.DailyMaxCents
	if v, ok := raw["single_max_cents"]; ok {
		var p *int64
		if err := json.Unmarshal(v, &p); err != nil {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid single_max_cents")
			return
		}
		single = p
	}
	if v, ok := raw["daily_max_cents"]; ok {
		var p *int64
		if err := json.Unmarshal(v, &p); err != nil {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid daily_max_cents")
			return
		}
		daily = p
	}
	lim, err := s.updateRiskLimits(r.Context(), s.adminOperator(r), single, daily)
	if err != nil {
		adminErr(w, err)
		return
	}
	ok(w, lim)
}

func (s *Server) hAdminAuditLogs(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	limit, offset := adminPageParams(r)
	list, err := s.adminListAuditLogs(r.Context(), r.URL.Query().Get("user_id"), r.URL.Query().Get("action"), limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}
