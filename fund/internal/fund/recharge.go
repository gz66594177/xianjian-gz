package fund

// Phase 8：人民币充值。CNY、整数分、双重记账、流水 append-only 不变。
// 资金模型：充值订单复用 wallet_order（order_type=1，迁移 0008 加充值专属字段）；
// 入账只走可信支付回调——客户端创建订单、页面操作、交易密码均不能直接增加余额
// （计划 §9.5：不得用虚假支付成功页面增加余额）。
// 充值状态机（仅 order_type=1）：0 待支付 1 已支付 2 已失败 3 已退款；合法流转只有
// 待支付 -> 已支付、待支付 -> 已失败、已支付 -> 已退款；其余流转一律拒绝，
// 同一目标状态的重复回调视为幂等重放（返回原订单，不重复入账）。
// 入账分录（txn type 20，ref_type='recharge'）：用户可用 CR / 平台结算户 DR，同一事务；
// 退款分录（txn type 21，仅 mock 渠道演练）：用户可用 DR / 平台结算户 CR，同一事务，
// 余额不足时整笔回滚、订单与余额不动（另起短事务记审计，见下）。
// 回调处理同一事务：订单行锁 + 状态 CAS + 账务入账 + 审计；金额不一致的回调拒绝入账并记审计
// （订单不动、资金不动）；渠道订单号唯一索引是回调幂等的第二道防线。
// 渠道：创建时 channel 只接受 mock（真实渠道未签约）；alipay/wechat 的回调入口恒回 501
// （验签骨架未配置），绝不入账。Mock 回调入口只在 FUND_MOCK_PAYMENT=1 时注册，
// 生产 fund.env 必须不开启该开关（部署检查清单见 API 文档 §13）。
// 充值创建不设准入/交易密码门禁：建单本身不产生资金变动（仅 status 0 的 pending 行），
// 真正的资金变动只发生在渠道验签通过的回调事务里；用户钱包不存在时建单事务内自动建钱包。

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
	OrderTypeRecharge = 1
	OrderTypeWithdraw = 2

	RechargePending  = 0 // 待支付
	RechargePaid     = 1 // 已支付
	RechargeFailed   = 2 // 已失败
	RechargeRefunded = 3 // 已退款
	// TxnRecharge = 20（充值入账）与 TxnRechargeRefund = 21（充值退款）定义见 fund.go txn 常量区。
)

const (
	ChannelMock   = "mock"
	ChannelAlipay = "alipay"
	ChannelWechat = "wechat"
)

// RechargeTiers：固定金额档位（分，CNY）——客户端展示用推荐档位；服务端接受任意 >0 整数分
// （档位金额与自定义金额走同一创建接口，档位只是约定值，不做白名单硬限制，避免档位调整改代码）。
var RechargeTiers = []int64{100, 500, 1000, 5000, 10000, 30000, 50000, 100000}

type RechargeOrder struct {
	OrderID             string    `json:"order_id"`
	UserID              string    `json:"user_id"`
	OrderType           int       `json:"order_type"`
	AmountCents         int64     `json:"amount_cents"`
	FeeCents            int64     `json:"fee_cents"`
	ActualCents         int64     `json:"actual_cents"`
	Status              int       `json:"status"`
	Channel             string    `json:"channel"`
	ChannelOrderNo      *string   `json:"channel_order_no"`
	PaidTxnID          *string   `json:"paid_txn_id"`
	RefundTxnID        *string   `json:"refund_txn_id"`
	FailureReason       *string   `json:"failure_reason"`
	ClientIdempotencyKey *string  `json:"client_idempotency_key"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

const rechargeColumns = `order_id, user_id, order_type, amount_cents, fee_cents, actual_cents, status, channel, channel_order_no, paid_txn_id, refund_txn_id, failure_reason, client_idempotency_key, created_at, updated_at`

func scanRechargeOrder(row interface{ Scan(...any) error }) (*RechargeOrder, error) {
	var o RechargeOrder
	if err := row.Scan(&o.OrderID, &o.UserID, &o.OrderType, &o.AmountCents, &o.FeeCents, &o.ActualCents, &o.Status,
		&o.Channel, &o.ChannelOrderNo, &o.PaidTxnID, &o.RefundTxnID, &o.FailureReason, &o.ClientIdempotencyKey,
		&o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

var (
	errRechargeNotFound      = errors.New("recharge order not found")
	errRechargeNotRecharge   = errors.New("order is not a recharge order")
	errRechargeAmountMismatch = errors.New("callback amount does not match order amount")
	errRechargeInvalidTransition = errors.New("illegal recharge status transition")
	errRechargeChannelMismatch = errors.New("callback channel order number does not match order")
	errRechargeChannelConflict = errors.New("channel order number already claimed by another order")
	errRechargeWalletAbnormal = errors.New("wallet not normal, recharge cannot settle")
	errRechargeRefundInsufficient = errors.New("insufficient balance for recharge refund")
	errRechargeChannelUnavailable = errors.New("payment channel not available: real channels require merchant signup")
)

// createRecharge：建单（同一事务内自动建钱包 + 幂等）。fee 现为 0（渠道手续费政策待签约后定，
// 到时以配置/渠道费率落地，不得在回调里临时改 actual）；actual = amount。
func (s *Server) createRecharge(ctx context.Context, userID string, amountCents int64, channel, idemKey string) (*RechargeOrder, error) {
	if amountCents <= 0 {
		return nil, errors.New("amount_cents must be > 0")
	}
	if channel == "" {
		channel = ChannelMock
	}
	if channel != ChannelMock {
		return nil, errRechargeChannelUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_account (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return nil, err
	}
	if idemKey != "" {
		var existing RechargeOrder
		err := tx.QueryRowContext(ctx, `SELECT `+rechargeColumns+` FROM wallet_order WHERE user_id=$1 AND order_type=1 AND client_idempotency_key=$2`, userID, idemKey).
			Scan(&existing.OrderID, &existing.UserID, &existing.OrderType, &existing.AmountCents, &existing.FeeCents, &existing.ActualCents, &existing.Status,
				&existing.Channel, &existing.ChannelOrderNo, &existing.PaidTxnID, &existing.RefundTxnID, &existing.FailureReason, &existing.ClientIdempotencyKey,
				&existing.CreatedAt, &existing.UpdatedAt)
		if err == nil {
			_ = tx.Rollback()
			return &existing, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	orderID := newID("rch_")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_order (order_id, user_id, order_type, amount_cents, fee_cents, actual_cents, status, channel, client_idempotency_key)
		 VALUES ($1,$2,1,$3,0,$3,0,$4,$5)`,
		orderID, userID, amountCents, channel, nullStr(idemKey)); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			var existing RechargeOrder
			err2 := s.db.QueryRowContext(ctx, `SELECT `+rechargeColumns+` FROM wallet_order WHERE user_id=$1 AND order_type=1 AND client_idempotency_key=$2`, userID, idemKey).
				Scan(&existing.OrderID, &existing.UserID, &existing.OrderType, &existing.AmountCents, &existing.FeeCents, &existing.ActualCents, &existing.Status,
					&existing.Channel, &existing.ChannelOrderNo, &existing.PaidTxnID, &existing.RefundTxnID, &existing.FailureReason, &existing.ClientIdempotencyKey,
					&existing.CreatedAt, &existing.UpdatedAt)
			if err2 != nil {
				return nil, err2
			}
			return &existing, nil
		}
		return nil, err
	}
	if err := insertAudit(ctx, tx, userID, userID, "recharge_create", fmt.Sprintf("recharge order %s amount %d cents channel %s", orderID, amountCents, channel), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getRecharge(ctx, userID, orderID)
}

func (s *Server) getRecharge(ctx context.Context, userID, orderID string) (*RechargeOrder, error) {
	o, err := scanRechargeOrder(s.db.QueryRowContext(ctx,
		`SELECT `+rechargeColumns+` FROM wallet_order WHERE order_id=$1 AND user_id=$2 AND order_type=1 AND recharge_kind='callback'`, orderID, userID))
	if err == sql.ErrNoRows {
		return nil, errRechargeNotFound
	}
	return o, err
}

func (s *Server) getRechargeAdmin(ctx context.Context, orderID string) (*RechargeOrder, error) {
	var orderType int
	var kind string
	if err := s.db.QueryRowContext(ctx, `SELECT order_type, recharge_kind FROM wallet_order WHERE order_id=$1`, orderID).Scan(&orderType, &kind); err == sql.ErrNoRows {
		return nil, errRechargeNotFound
	} else if err != nil {
		return nil, err
	} else if orderType != OrderTypeRecharge {
		return nil, errRechargeNotRecharge
	} else if kind != "callback" {
		// 0010：回调端点只服务回调单；手动/网关单走通用 /v1/recharge/orders 详情。
		return nil, errRechargeNotFound
	}
	return scanRechargeOrder(s.db.QueryRowContext(ctx,
		`SELECT `+rechargeColumns+` FROM wallet_order WHERE order_id=$1 AND order_type=1 AND recharge_kind='callback'`, orderID))
}

func (s *Server) listRecharges(ctx context.Context, userID string, status int, channel string, limit, offset int) ([]RechargeOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+rechargeColumns+` FROM wallet_order WHERE user_id=$1 AND order_type=1 AND recharge_kind='callback' AND ($2<0 OR status=$2) AND ($3='' OR channel=$3) ORDER BY created_at DESC LIMIT $4 OFFSET $5`,
		userID, status, channel, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RechargeOrder{}
	for rows.Next() {
		o, err := scanRechargeOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (s *Server) listRechargesAdmin(ctx context.Context, userID string, status int, channel string, limit, offset int) ([]RechargeOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+rechargeColumns+` FROM wallet_order WHERE order_type=1 AND recharge_kind='callback' AND ($1='' OR user_id=$1) AND ($2<0 OR status=$2) AND ($3='' OR channel=$3) ORDER BY created_at DESC LIMIT $4 OFFSET $5`,
		userID, status, channel, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RechargeOrder{}
	for rows.Next() {
		o, err := scanRechargeOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// ---- 回调处理（核心，同一 DB 事务） ----

type callbackReq struct {
	OrderID        string `json:"order_id"`
	ChannelOrderNo string `json:"channel_order_no"`
	AmountCents    int64  `json:"amount_cents"`
	Status         string `json:"status"` // paid | failed | refunded
	Signature      string `json:"signature"`
	FailureReason  string `json:"failure_reason"`
}

// applyRechargeCallback：验签已通过后的回调落账。订单行 FOR UPDATE 串行化并发回调；
// 状态 CAS + 账务 + 审计同一事务；金额不一致、渠道单号不一致、非法流转、钱包异常、
// 退款余额不足时主事务整体回滚（订单与余额不动），其中金额不一致与退款余额不足另起短事务记审计
// （主事务回滚后审计仍需留痕，复用 transfer 失败审计行的口径：审计行不落账、只留痕）。
func (s *Server) applyRechargeCallback(ctx context.Context, req callbackReq) (*RechargeOrder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var o RechargeOrder
	err = tx.QueryRowContext(ctx, `SELECT `+rechargeColumns+` FROM wallet_order WHERE order_id=$1 FOR UPDATE`, req.OrderID).
		Scan(&o.OrderID, &o.UserID, &o.OrderType, &o.AmountCents, &o.FeeCents, &o.ActualCents, &o.Status,
			&o.Channel, &o.ChannelOrderNo, &o.PaidTxnID, &o.RefundTxnID, &o.FailureReason, &o.ClientIdempotencyKey,
			&o.CreatedAt, &o.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, errRechargeNotFound
	}
	if err != nil {
		return nil, err
	}
	if o.OrderType != OrderTypeRecharge {
		return nil, errRechargeNotRecharge
	}
	// 渠道单号一致性：订单已有单号时回调单号必须相同，否则拒绝（防串单）。
	if o.ChannelOrderNo != nil && *o.ChannelOrderNo != "" && *o.ChannelOrderNo != req.ChannelOrderNo {
		return nil, errRechargeChannelMismatch
	}
	// 金额一致性：回调金额必须等于订单金额，否则拒绝入账并记审计（订单不动、资金不动）。
	if req.AmountCents != o.AmountCents {
		_ = tx.Rollback()
		s.auditStandalone(ctx, o.UserID, "channel:"+ChannelMock, "recharge_callback_amount_mismatch",
			fmt.Sprintf("recharge order %s callback amount %d != order amount %d", o.OrderID, req.AmountCents, o.AmountCents), nil)
		return nil, errRechargeAmountMismatch
	}

	target := -1
	switch req.Status {
	case "paid":
		target = RechargePaid
	case "failed":
		target = RechargeFailed
	case "refunded":
		target = RechargeRefunded
	default:
		return nil, errors.New("callback status must be paid, failed or refunded")
	}
	// 幂等重放：回调目标状态与订单现状相同 → 返回原订单，不重复入账、不重复写审计。
	if o.Status == target {
		_ = tx.Rollback()
		return &o, nil
	}
	legal := (o.Status == RechargePending && (target == RechargePaid || target == RechargeFailed)) ||
		(o.Status == RechargePaid && target == RechargeRefunded)
	if !legal {
		return nil, errRechargeInvalidTransition
	}

	if target == RechargeFailed {
		reason := req.FailureReason
		if utf8.RuneCountInString(reason) > 500 {
			reason = string([]rune(reason)[:500])
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE wallet_order SET status=2, channel_order_no=COALESCE(channel_order_no,$1), failure_reason=$2, updated_at=now() WHERE order_id=$3 AND status=0`,
			nullStr(req.ChannelOrderNo), nullStr(reason), o.OrderID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, errRechargeInvalidTransition
		}
		if err := insertAudit(ctx, tx, o.UserID, "channel:"+ChannelMock, "recharge_failed", fmt.Sprintf("recharge order %s failed channel_order_no=%s", o.OrderID, req.ChannelOrderNo), "", ""); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.getRechargeAdmin(ctx, o.OrderID)
	}

	// 入账 / 退款：账务分录 + 余额 + 订单状态同一事务。
	var txnType int
	var entries []postingEntry
	var auditAction string
	if target == RechargePaid {
		txnType = TxnRecharge
		entries = []postingEntry{
			{userID: o.UserID, accountType: AcctAvailable, direction: DirCredit, amount: o.AmountCents, oppositeUID: PlatformUserID, oppositeAcc: "settlement"},
			{userID: PlatformUserID, accountType: AcctAvailable, direction: DirDebit, amount: o.AmountCents, oppositeUID: o.UserID, oppositeAcc: "available"},
		}
		auditAction = "recharge_paid"
	} else {
		txnType = TxnRechargeRefund
		entries = []postingEntry{
			{userID: o.UserID, accountType: AcctAvailable, direction: DirDebit, amount: o.AmountCents, oppositeUID: PlatformUserID, oppositeAcc: "settlement"},
			{userID: PlatformUserID, accountType: AcctAvailable, direction: DirCredit, amount: o.AmountCents, oppositeUID: o.UserID, oppositeAcc: "available"},
		}
		auditAction = "recharge_refunded"
	}
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
	states := map[string]*accountState{}
	for _, u := range users {
		var st accountState
		err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&st.balance, &st.frozen, &st.version, &st.status)
		if err != nil {
			return nil, err
		}
		if st.status != 0 {
			_ = tx.Rollback()
			s.auditStandalone(ctx, o.UserID, "channel:"+ChannelMock, "recharge_callback_wallet_abnormal",
				fmt.Sprintf("recharge order %s wallet %s status=%d", o.OrderID, u, st.status), nil)
			return nil, errRechargeWalletAbnormal
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
		st.balance += deltaFor(e.userID, e.direction, e.amount)
		if st.balance < 0 {
			// 退款时用户余额不足：整笔回滚、订单与余额不动，另起短事务记审计。
			_ = tx.Rollback()
			if target == RechargeRefunded {
				s.auditStandalone(ctx, o.UserID, "channel:"+ChannelMock, "recharge_refund_failed_insufficient",
					fmt.Sprintf("recharge order %s refund %d cents insufficient", o.OrderID, o.AmountCents), nil)
				return nil, errRechargeRefundInsufficient
			}
			return nil, fmt.Errorf("insufficient available balance for %s", e.userID)
		}
		appliedList = append(appliedList, applied{e: e, balanceAfter: st.balance, frozenAfter: st.frozen})
	}
	txnID := newID("txn_")
	idem := "rch-pay-" + o.OrderID
	if target == RechargeRefunded {
		idem = "rch-refund-" + o.OrderID
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'recharge',$4,1,$5)`,
		txnID, o.UserID, txnType, o.OrderID, idem); err != nil {
		if isUniqueViolation(err) {
			// 并发回调已入账：回滚后返回现状订单（幂等收敛）。
			_ = tx.Rollback()
			return s.getRechargeAdmin(ctx, o.OrderID)
		}
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
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, fmt.Errorf("optimistic lock failed for %s", u)
		}
	}
	var orderSQL string
	var orderArgs []any
	if target == RechargePaid {
		orderSQL = `UPDATE wallet_order SET status=1, channel_order_no=$1, paid_txn_id=$2, updated_at=now() WHERE order_id=$3 AND status=0`
		orderArgs = []any{req.ChannelOrderNo, txnID, o.OrderID}
	} else {
		orderSQL = `UPDATE wallet_order SET status=3, refund_txn_id=$1, updated_at=now() WHERE order_id=$2 AND status=1`
		orderArgs = []any{txnID, o.OrderID}
	}
	res, err := tx.ExecContext(ctx, orderSQL, orderArgs...)
	if err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback()
			return nil, errRechargeChannelConflict
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errRechargeInvalidTransition
	}
	if err := insertAudit(ctx, tx, o.UserID, "channel:"+ChannelMock, auditAction, fmt.Sprintf("recharge order %s txn %s %d cents", o.OrderID, txnID, o.AmountCents), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			return nil, errRechargeChannelConflict
		}
		return nil, err
	}
	return s.getRechargeAdmin(ctx, o.OrderID)
}

// ---- HTTP ----

type rechargeCreateReq struct {
	AmountCents    int64  `json:"amount_cents"`
	Channel        string `json:"channel"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) hCreateRecharge(w http.ResponseWriter, r *http.Request) {
	var req rechargeCreateReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if req.AmountCents <= 0 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "amount_cents must be > 0")
		return
	}
	ord, err := s.createRecharge(r.Context(), uid, req.AmountCents, req.Channel, req.IdempotencyKey)
	if err != nil {
		if errors.Is(err, errRechargeChannelUnavailable) {
			fail(w, http.StatusBadRequest, "CHANNEL_NOT_AVAILABLE", err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, ord)
}

func rechargeIDFromPath(r *http.Request, prefix string) string {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) == 1 && parts[0] != "" {
		return parts[0]
	}
	return ""
}

func (s *Server) hGetRecharge(w http.ResponseWriter, r *http.Request) {
	id := rechargeIDFromPath(r, "/v1/recharges/")
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	if id == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown recharge path")
		return
	}
	ord, err := s.getRecharge(r.Context(), uid, id)
	if err != nil {
		fail(w, http.StatusNotFound, "RECHARGE_NOT_FOUND", "recharge order not found")
		return
	}
	ok(w, ord)
}

func parseStatusFilter(r *http.Request) int {
	v := r.URL.Query().Get("status")
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 3 {
		return -2 // 调用方据此回 400
	}
	return n
}

func (s *Server) hListRecharges(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	status := parseStatusFilter(r)
	if status == -2 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "status filter must be 0..3")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.listRecharges(r.Context(), uid, status, r.URL.Query().Get("channel"), limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) hAdminListRecharges(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	status := parseStatusFilter(r)
	if status == -2 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "status filter must be 0..3")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.listRechargesAdmin(r.Context(), r.URL.Query().Get("user_id"), status, r.URL.Query().Get("channel"), limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) hAdminGetRecharge(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	id := rechargeIDFromPath(r, "/v1/admin/recharges/")
	if id == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown recharge path")
		return
	}
	ord, err := s.getRechargeAdmin(r.Context(), id)
	if err != nil {
		if errors.Is(err, errRechargeNotRecharge) {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "order is not a recharge order")
			return
		}
		fail(w, http.StatusNotFound, "RECHARGE_NOT_FOUND", "recharge order not found")
		return
	}
	ok(w, ord)
}

// hMockCallback：mock 渠道回调入口。仅在 FUND_MOCK_PAYMENT=1 时注册（见 Handler）；
// 验签失败 401（不透露订单是否存在）；签名原文与密钥绝不进日志。
func (s *Server) hMockCallback(w http.ResponseWriter, r *http.Request) {
	v, ok := s.verifiers[ChannelMock]
	if !ok {
		fail(w, http.StatusNotFound, "NOT_FOUND", "mock payment callback is not enabled")
		return
	}
	var req callbackReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.OrderID == "" || req.ChannelOrderNo == "" || req.AmountCents <= 0 || req.Signature == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "order_id, channel_order_no, amount_cents>0 and signature are required")
		return
	}
	if req.Status != "paid" && req.Status != "failed" && req.Status != "refunded" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "status must be paid, failed or refunded")
		return
	}
	if err := v.VerifySignature(ChannelMock, MockCallbackPayload(req.OrderID, req.ChannelOrderNo, req.AmountCents, req.Status), req.Signature); err != nil {
		s.log.Printf("mock callback signature invalid order=%s", req.OrderID)
		fail(w, http.StatusUnauthorized, "SIGNATURE_INVALID", "callback signature verification failed")
		return
	}
	s.callbackResult(w, r, req)
}

func (s *Server) callbackResult(w http.ResponseWriter, r *http.Request, req callbackReq) {
	ord, err := s.applyRechargeCallback(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, errRechargeNotFound):
			fail(w, http.StatusNotFound, "RECHARGE_NOT_FOUND", "recharge order not found")
		case errors.Is(err, errRechargeNotRecharge):
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "order is not a recharge order")
		case errors.Is(err, errRechargeAmountMismatch):
			fail(w, http.StatusBadRequest, "CALLBACK_AMOUNT_MISMATCH", err.Error())
		case errors.Is(err, errRechargeChannelMismatch):
			fail(w, http.StatusBadRequest, "CHANNEL_ORDER_MISMATCH", err.Error())
		case errors.Is(err, errRechargeChannelConflict):
			fail(w, http.StatusConflict, "CHANNEL_ORDER_CONFLICT", err.Error())
		case errors.Is(err, errRechargeInvalidTransition):
			fail(w, http.StatusConflict, "INVALID_TRANSITION", err.Error())
		case errors.Is(err, errRechargeWalletAbnormal):
			fail(w, http.StatusConflict, "WALLET_ABNORMAL", err.Error())
		case errors.Is(err, errRechargeRefundInsufficient):
			fail(w, http.StatusConflict, "REFUND_INSUFFICIENT", err.Error())
		default:
			fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		}
		return
	}
	ok(w, ord)
}

// hPaymentCallbackFallback：非 mock 渠道回调入口。真实渠道验签器未配置（商户未签约）时恒回 501，
// 绝不入账；未知渠道与 mock 开关关闭时的 mock 路径回 404（mock 路径在开关开启时由精确路由优先处理）。
func (s *Server) hPaymentCallbackFallback(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/payments/callback/")
	channel := strings.Split(rest, "/")[0]
	if channel == ChannelAlipay || channel == ChannelWechat {
		fail(w, http.StatusNotImplemented, "CHANNEL_NOT_CONFIGURED", "payment channel verifier not configured: merchant signup pending")
		return
	}
	fail(w, http.StatusNotFound, "NOT_FOUND", "unknown payment callback channel")
}
