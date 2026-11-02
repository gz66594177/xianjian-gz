package fund

// 网关支付（与手动充值并存的第二条充值通道）。
// 流程（用户新定）：用户输入金额 -> 后端建网关支付单并返回网关收银台跳转地址 ->
// 用户在网关完成付款后回到 App -> App 弹窗问“是否已经成功付款？”->
// 用户点“是”只提示“请等待支付结果返回即可到账”并轮询订单状态，绝不由这个按钮直接入账；
// 用户点“付款遇到问题？”跳 App 内已有的帮助与反馈入口（专属客服聊天属 Phase 13，本次不新建客服系统）。
// 后端安全红线（全部写进测试）：
//   - “是，已付款”没有任何入账接口与之对应：状态查询（GET）只读，绝不改余额；
//   - 只有网关服务端回调验签通过才入账；验签失败、重复回调、过期订单回调一律拒绝入账并记审计；
//   - 网关厂商与商户凭证尚未定：本次只做适配器接口 + 配置开关，未配置真实网关时建单返回“暂未开通”，
//     绝不生成假收银台、绝不伪造回调。生产默认无适配器（Config.GatewayAdapter 为空即关闭）。
// 订单口径：复用 wallet_order（order_type=1，recharge_kind='gateway'，channel='gateway'）；
// 状态机（仅 gateway 行）：0 待支付 -> 1 已支付（仅回调）/ 2 失败；0 -> 4 超时关闭（过期回调拒绝入账并关单）。
// 入账分录：txn type 20（用户可用 CR / 平台结算 DR，ref_type='recharge'），与 Phase 8 回调入账同模型。
// 订单状态查询：通用接口 GET /v1/recharge/orders 与详情（见本文件末尾），App 轮询用只读接口。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	GatewayClosed = 4 // 超时关闭
	// 已支付复用 RechargePaid = 1；失败复用 RechargeFailed = 2。
	GatewayOrderTTL = 30 * time.Minute // 网关单支付有效期（建单时写入 expires_at，过期后回调拒绝入账）
)

var (
	errGatewayNotFound      = errors.New("gateway recharge order not found")
	errGatewayNotGateway    = errors.New("order is not a gateway recharge order")
	errGatewayExpired       = errors.New("gateway order expired")
	errGatewayBadSignature  = errors.New("gateway callback signature verification failed")
	errGatewayIdemConflict  = errors.New("idempotency key already used by another order kind")
	errGatewayAmountMismatch = errors.New("gateway callback amount does not match order amount")
	errGatewayChannelMismatch = errors.New("gateway order number does not match order")
	errGatewayInvalidTrans  = errors.New("illegal gateway order status transition")
)

// GatewayAdapter：网关厂商适配器接口。厂商与商户凭证未定前生产不注册任何实现
// （Config.GatewayAdapter 为空 = 通道关闭）；未来签约某厂商时实现本接口并注入，不动订单与记账代码。
type GatewayAdapter interface {
	// CreatePayment：向网关建支付单，返回收银台跳转地址与网关侧订单引用。
	CreatePayment(ctx context.Context, orderID, userID string, amountCents int64) (cashierURL, gatewayRef string, err error)
	// VerifyCallback：解析并验签网关服务端回调。验签失败返回 errGatewayBadSignature；
	// 报文无法解析返回其他 error（调用方按 400 处理）。
	VerifyCallback(r *http.Request) (*GatewayCallback, error)
}

// GatewayCallback：适配器验签通过后的回调载荷（只含记账必需字段，不含原始签名）。
type GatewayCallback struct {
	OrderID        string
	GatewayOrderNo string
	AmountCents    int64
	Status         string // paid | failed
	FailureReason  string
}

// ---- 网关订单视图 ----

type GatewayRechargeOrder struct {
	OrderID           string     `json:"order_id"`
	UserID            string     `json:"user_id"`
	OrderType         int        `json:"order_type"`
	RechargeKind      string     `json:"recharge_kind"`
	AmountCents       int64      `json:"amount_cents"`
	FeeCents          int64      `json:"fee_cents"`
	ActualCents       int64      `json:"actual_cents"`
	Status            int        `json:"status"`
	Expired           bool       `json:"expired"`
	Channel           string     `json:"channel"`
	GatewayRef        *string    `json:"gateway_ref"`
	CashierURL        *string    `json:"cashier_url"`
	ChannelOrderNo    *string    `json:"channel_order_no"`
	PaidTxnID        *string    `json:"paid_txn_id"`
	FailureReason     *string    `json:"failure_reason"`
	ExpiresAt         *time.Time `json:"expires_at"`
	ClientIdempotencyKey *string `json:"client_idempotency_key"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

const gatewayOrderColumns = `order_id, user_id, order_type, recharge_kind, amount_cents, fee_cents, actual_cents, status, channel, gateway_ref, cashier_url, channel_order_no, paid_txn_id, failure_reason, expires_at, client_idempotency_key, created_at, updated_at`

func scanGatewayOrder(row interface{ Scan(...any) error }) (*GatewayRechargeOrder, error) {
	var o GatewayRechargeOrder
	if err := row.Scan(&o.OrderID, &o.UserID, &o.OrderType, &o.RechargeKind, &o.AmountCents, &o.FeeCents, &o.ActualCents,
		&o.Status, &o.Channel, &o.GatewayRef, &o.CashierURL, &o.ChannelOrderNo, &o.PaidTxnID, &o.FailureReason,
		&o.ExpiresAt, &o.ClientIdempotencyKey, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	// expired 为计算字段（不落库、不触发关单；关单只走回调拒绝与管理端扫描）：
	// 待支付且已过 expires_at 即为过期态，App 轮询据此展示“已超时”，余额与订单状态均不动。
	o.Expired = o.Status == RechargePending && o.ExpiresAt != nil && time.Now().After(*o.ExpiresAt)
	return &o, nil
}

func (s *Server) getGatewayOrder(ctx context.Context, userID, orderID string) (*GatewayRechargeOrder, error) {
	o, err := scanGatewayOrder(s.db.QueryRowContext(ctx,
		`SELECT `+gatewayOrderColumns+` FROM wallet_order WHERE order_id=$1 AND user_id=$2 AND recharge_kind='gateway'`, orderID, userID))
	if err == sql.ErrNoRows {
		return nil, errGatewayNotFound
	}
	return o, err
}

func (s *Server) getGatewayOrderAdmin(ctx context.Context, orderID string) (*GatewayRechargeOrder, error) {
	o, err := scanGatewayOrder(s.db.QueryRowContext(ctx,
		`SELECT `+gatewayOrderColumns+` FROM wallet_order WHERE order_id=$1 AND recharge_kind='gateway'`, orderID))
	if err == sql.ErrNoRows {
		return nil, errGatewayNotFound
	}
	return o, err
}

// createGatewayRecharge：建网关单。门禁顺序（0012）：
//  1. 传了 vendor 时先查通道开关：行不存在或 enabled=false 直接关（fail-closed）；
//  2. 未配置适配器时直接返回“暂未开通”，绝不生成假收银台；
// 适配器建单失败时不落订单行；钱包不存在时同一事务内自动建钱包；幂等口径与手动单一致。
func (s *Server) createGatewayRecharge(ctx context.Context, userID string, amountCents int64, idemKey string, vendor string) (*GatewayRechargeOrder, error) {
	channelOn, err := s.gatewayChannelEnabled(ctx, vendor)
	if err != nil {
		return nil, err
	}
	if !channelOn {
		return nil, errGatewayNotConfigured
	}
	if s.gateway == nil {
		return nil, errGatewayNotConfigured
	}
	if amountCents <= 0 {
		return nil, errors.New("amount_cents must be > 0")
	}
	orderID := newID("gw_")
	cashierURL, gatewayRef, err := s.gateway.CreatePayment(ctx, orderID, userID, amountCents)
	if err != nil {
		return nil, fmt.Errorf("gateway create payment failed: %w", err)
	}
	if strings.TrimSpace(cashierURL) == "" {
		return nil, errors.New("gateway adapter returned empty cashier url")
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
		var existingID string
		err := tx.QueryRowContext(ctx,
			`SELECT order_id FROM wallet_order WHERE user_id=$1 AND client_idempotency_key=$2 AND recharge_kind='gateway'`, userID, idemKey).Scan(&existingID)
		if err == nil {
			_ = tx.Rollback()
			return s.getGatewayOrder(ctx, userID, existingID)
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	expiresAt := time.Now().Add(GatewayOrderTTL)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_order (order_id, user_id, order_type, recharge_kind, amount_cents, fee_cents, actual_cents, status, channel, gateway_ref, cashier_url, expires_at, client_idempotency_key)
		 VALUES ($1,$2,1,'gateway',$3,0,$3,0,'gateway',$4,$5,$6,$7)`,
		orderID, userID, amountCents, nullStr(gatewayRef), cashierURL, expiresAt, nullStr(idemKey)); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			existing, err2 := s.getGatewayOrderByIdem(ctx, userID, idemKey)
			if err2 != nil {
				return nil, errGatewayIdemConflict
			}
			return existing, nil
		}
		return nil, err
	}
	if err := insertAudit(ctx, tx, userID, userID, "gateway_recharge_create",
		fmt.Sprintf("gateway recharge %s %d cents ref %s", orderID, amountCents, gatewayRef), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getGatewayOrder(ctx, userID, orderID)
}

func (s *Server) getGatewayOrderByIdem(ctx context.Context, userID, idemKey string) (*GatewayRechargeOrder, error) {
	o, err := scanGatewayOrder(s.db.QueryRowContext(ctx,
		`SELECT `+gatewayOrderColumns+` FROM wallet_order WHERE user_id=$1 AND client_idempotency_key=$2 AND recharge_kind='gateway'`, userID, idemKey))
	if err == sql.ErrNoRows {
		return nil, errGatewayNotFound
	}
	return o, err
}

// applyGatewayCallback：网关回调落账（验签已通过）。订单行 FOR UPDATE 串行化并发回调；
// 同一目标状态的重复回调视为幂等重放（返回原订单，不重复入账）；过期、金额不一致、
// 单号不一致、非法流转时主事务回滚（订单与余额不动），其中过期关单与金额不一致另记审计。
func (s *Server) applyGatewayCallback(ctx context.Context, cb *GatewayCallback) (*GatewayRechargeOrder, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var o GatewayRechargeOrder
	err = tx.QueryRowContext(ctx,
		`SELECT `+gatewayOrderColumns+` FROM wallet_order WHERE order_id=$1 AND recharge_kind='gateway' FOR UPDATE`, cb.OrderID).
		Scan(&o.OrderID, &o.UserID, &o.OrderType, &o.RechargeKind, &o.AmountCents, &o.FeeCents, &o.ActualCents,
			&o.Status, &o.Channel, &o.GatewayRef, &o.CashierURL, &o.ChannelOrderNo, &o.PaidTxnID, &o.FailureReason,
			&o.ExpiresAt, &o.ClientIdempotencyKey, &o.CreatedAt, &o.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, errGatewayNotFound
	}
	if err != nil {
		return nil, err
	}
	target := -1
	switch cb.Status {
	case "paid":
		target = RechargePaid
	case "failed":
		target = RechargeFailed
	default:
		return nil, errors.New("gateway callback status must be paid or failed")
	}
	// 幂等重放：目标状态与现状相同 → 返回原订单，不重复入账、不重复写审计。
	if o.Status == target {
		_ = tx.Rollback()
		return s.getGatewayOrderAdmin(ctx, o.OrderID)
	}
	// 过期：待支付且已过 expires_at 的订单，任何回调都拒绝入账并关单（状态 4）+ 记审计。
	if o.Status == RechargePending && o.ExpiresAt != nil && time.Now().After(*o.ExpiresAt) {
		if _, err := tx.ExecContext(ctx,
			`UPDATE wallet_order SET status=4, updated_at=now() WHERE order_id=$1 AND recharge_kind='gateway' AND status=0`, o.OrderID); err != nil {
			return nil, err
		}
		if err := insertAudit(ctx, tx, o.UserID, "gateway", "gateway_recharge_expired",
			fmt.Sprintf("gateway recharge %s expired, callback refused", o.OrderID), "", ""); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, errGatewayExpired
	}
	if o.Status != RechargePending {
		return nil, errGatewayInvalidTrans
	}
	if o.ChannelOrderNo != nil && *o.ChannelOrderNo != "" && *o.ChannelOrderNo != cb.GatewayOrderNo {
		return nil, errGatewayChannelMismatch
	}
	if cb.AmountCents != o.AmountCents {
		_ = tx.Rollback()
		s.auditStandalone(ctx, o.UserID, "gateway", "gateway_callback_amount_mismatch",
			fmt.Sprintf("gateway recharge %s callback amount %d != order amount %d", o.OrderID, cb.AmountCents, o.AmountCents), nil)
		return nil, errGatewayAmountMismatch
	}
	if target == RechargeFailed {
		reason := cb.FailureReason
		if utf8.RuneCountInString(reason) > 500 {
			reason = string([]rune(reason)[:500])
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE wallet_order SET status=2, channel_order_no=COALESCE(channel_order_no,$1), failure_reason=$2, updated_at=now() WHERE order_id=$3 AND recharge_kind='gateway' AND status=0`,
			nullStr(cb.GatewayOrderNo), nullStr(reason), o.OrderID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, errGatewayInvalidTrans
		}
		if err := insertAudit(ctx, tx, o.UserID, "gateway", "gateway_recharge_failed",
			fmt.Sprintf("gateway recharge %s failed gateway_order_no=%s", o.OrderID, cb.GatewayOrderNo), "", ""); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return s.getGatewayOrderAdmin(ctx, o.OrderID)
	}
	txnID, err := postRechargeCreditTx(ctx, tx, o.UserID, o.AmountCents, TxnRecharge, "gw-pay-"+o.OrderID, o.OrderID)
	if err != nil {
		return nil, err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE wallet_order SET status=1, channel_order_no=$1, paid_txn_id=$2, updated_at=now() WHERE order_id=$3 AND recharge_kind='gateway' AND status=0`,
		cb.GatewayOrderNo, txnID, o.OrderID)
	if err != nil {
		if isUniqueViolation(err) {
			_ = tx.Rollback()
			return nil, errGatewayChannelMismatch
		}
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, errGatewayInvalidTrans
	}
	if err := insertAudit(ctx, tx, o.UserID, "gateway", "gateway_recharge_paid",
		fmt.Sprintf("gateway recharge %s txn %s %d cents", o.OrderID, txnID, o.AmountCents), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if isUniqueViolation(err) {
			return nil, errGatewayChannelMismatch
		}
		return nil, err
	}
	return s.getGatewayOrderAdmin(ctx, o.OrderID)
}

// expireGatewayOrders：管理端扫描关闭过期待支付网关单（status 0 -> 4，逐单记审计）。
func (s *Server) expireGatewayOrders(ctx context.Context, adminID string, limit int) ([]string, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT order_id, user_id FROM wallet_order WHERE recharge_kind='gateway' AND status=0 AND expires_at < now() ORDER BY expires_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	type item struct{ id, uid string }
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.uid); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, it)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	closed := []string{}
	for _, it := range items {
		res, err := s.db.ExecContext(ctx,
			`UPDATE wallet_order SET status=4, updated_at=now() WHERE order_id=$1 AND recharge_kind='gateway' AND status=0`, it.id)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			closed = append(closed, it.id)
			s.auditStandalone(ctx, it.uid, adminID, "gateway_recharge_expired", "gateway recharge expired order_id="+it.id, nil)
		}
	}
	return closed, nil
}

// ---- 通用充值订单查询（App 轮询与账单筛选共用；只读，绝不改余额与订单状态） ----
// 列表为统一视图（含 kind 区分）；详情按 kind 返回各自的完整结构（手动单含方式与凭证、
// 网关单含过期计算字段、回调单沿用 Phase 8 结构）。

type RechargeOrderView struct {
	OrderID            string `json:"order_id"`
	UserID             string `json:"user_id"`
	RechargeKind       string `json:"recharge_kind"`
	Channel            string `json:"channel"`
	AmountCents        int64  `json:"amount_cents"`
	FeeCents           int64  `json:"fee_cents"`
	ActualCents        int64  `json:"actual_cents"`
	Status             int    `json:"status"`
	Expired            bool   `json:"expired"`
	PaymentMethodID    *string `json:"payment_method_id"`
	GatewayRef         *string `json:"gateway_ref"`
	PaidTxnID          *string `json:"paid_txn_id"`
	FailureReason      *string `json:"failure_reason"`
	ReviewNote         *string `json:"review_note"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func scanRechargeOrderView(row interface{ Scan(...any) error }) (*RechargeOrderView, error) {
	var o RechargeOrderView
	var expiresAt *time.Time
	if err := row.Scan(&o.OrderID, &o.UserID, &o.RechargeKind, &o.Channel, &o.AmountCents, &o.FeeCents, &o.ActualCents,
		&o.Status, &o.PaymentMethodID, &o.GatewayRef, &o.PaidTxnID, &o.FailureReason, &o.ReviewNote, &expiresAt,
		&o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	o.Expired = o.RechargeKind == RechargeKindGateway && o.Status == RechargePending && expiresAt != nil && time.Now().After(*expiresAt)
	return &o, nil
}

const rechargeOrderViewSelect = `
	SELECT order_id, user_id, recharge_kind, channel, amount_cents, fee_cents, actual_cents, status,
		payment_method_id, gateway_ref, paid_txn_id, failure_reason, review_note, expires_at, created_at, updated_at
	FROM wallet_order WHERE order_type=1`

func (s *Server) listRechargeOrders(ctx context.Context, userID, kind string, status, limit, offset int) ([]RechargeOrderView, error) {
	if kind != "" && kind != RechargeKindCallback && kind != RechargeKindManual && kind != RechargeKindGateway {
		return nil, errors.New("kind must be callback, manual or gateway")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		rechargeOrderViewSelect+` AND user_id=$1 AND ($2='' OR recharge_kind=$2) AND ($3<0 OR status=$3) ORDER BY created_at DESC LIMIT $4 OFFSET $5`,
		userID, kind, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RechargeOrderView{}
	for rows.Next() {
		o, err := scanRechargeOrderView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (s *Server) listRechargeOrdersAdmin(ctx context.Context, userID, kind string, status, limit, offset int) ([]RechargeOrderView, error) {
	if kind != "" && kind != RechargeKindCallback && kind != RechargeKindManual && kind != RechargeKindGateway {
		return nil, errors.New("kind must be callback, manual or gateway")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		rechargeOrderViewSelect+` AND ($1='' OR user_id=$1) AND ($2='' OR recharge_kind=$2) AND ($3<0 OR status=$3) ORDER BY created_at DESC LIMIT $4 OFFSET $5`,
		userID, kind, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RechargeOrderView{}
	for rows.Next() {
		o, err := scanRechargeOrderView(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// ---- HTTP ----

func gatewayErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errGatewayNotFound) || errors.Is(err, errGatewayNotGateway):
		fail(w, http.StatusNotFound, "GATEWAY_RECHARGE_NOT_FOUND", "gateway recharge order not found")
	case errors.Is(err, errGatewayNotConfigured):
		fail(w, http.StatusBadRequest, "GATEWAY_NOT_CONFIGURED", "gateway payment is not available yet")
	case errors.Is(err, errGatewayExpired):
		fail(w, http.StatusConflict, "GATEWAY_ORDER_EXPIRED", err.Error())
	case errors.Is(err, errGatewayAmountMismatch):
		fail(w, http.StatusBadRequest, "CALLBACK_AMOUNT_MISMATCH", err.Error())
	case errors.Is(err, errGatewayChannelMismatch):
		fail(w, http.StatusConflict, "CHANNEL_ORDER_MISMATCH", err.Error())
	case errors.Is(err, errGatewayInvalidTrans):
		fail(w, http.StatusConflict, "INVALID_TRANSITION", err.Error())
	case errors.Is(err, errGatewayIdemConflict):
		fail(w, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", err.Error())
	case errors.Is(err, errGatewayBadSignature):
		fail(w, http.StatusUnauthorized, "SIGNATURE_INVALID", err.Error())
	default:
		msg := err.Error()
		if strings.Contains(msg, "must be") || strings.Contains(msg, "required") || strings.Contains(msg, "invalid") || strings.Contains(msg, "kind must be") {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", msg)
			return
		}
		if strings.Contains(msg, "gateway create payment failed") {
			fail(w, http.StatusBadGateway, "GATEWAY_ERROR", msg)
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", msg)
	}
}

type gatewayCreateReq struct {
	AmountCents    int64  `json:"amount_cents"`
	IdempotencyKey string `json:"idempotency_key"`
	// Vendor：厂商标识（可选；传了才受 0012 通道开关约束，见 gatewayChannelEnabled）。
	Vendor string `json:"vendor"`
}

func (s *Server) hCreateGatewayRecharge(w http.ResponseWriter, r *http.Request) {
	var req gatewayCreateReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if req.AmountCents <= 0 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "amount_cents must be > 0")
		return
	}
	ord, err := s.createGatewayRecharge(r.Context(), uid, req.AmountCents, req.IdempotencyKey, req.Vendor)
	if err != nil {
		gatewayErr(w, err)
		return
	}
	ok(w, ord)
}

func (s *Server) hGatewayCallback(w http.ResponseWriter, r *http.Request) {
	if s.gateway == nil {
		// 未配置真实网关：恒回 501，绝不入账（与 Phase 8 真实渠道回调 501 同口径）。
		fail(w, http.StatusNotImplemented, "CHANNEL_NOT_CONFIGURED", "gateway payment channel not configured")
		return
	}
	cb, err := s.gateway.VerifyCallback(r)
	if err != nil {
		if errors.Is(err, errGatewayBadSignature) {
			s.log.Printf("gateway callback signature invalid")
			fail(w, http.StatusUnauthorized, "SIGNATURE_INVALID", "callback signature verification failed")
			return
		}
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid gateway callback body")
		return
	}
	if cb.OrderID == "" || cb.GatewayOrderNo == "" || cb.AmountCents <= 0 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "order_id, gateway_order_no and amount_cents>0 are required")
		return
	}
	ord, err := s.applyGatewayCallback(r.Context(), cb)
	if err != nil {
		gatewayErr(w, err)
		return
	}
	ok(w, ord)
}

func (s *Server) hExpireGatewayOrders(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	closed, err := s.expireGatewayOrders(r.Context(), s.adminOperator(r), limit)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, map[string]any{"closed_ids": closed})
}

func parseIntFilter(r *http.Request, key string) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return -2
	}
	return n
}

func (s *Server) hListRechargeOrders(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	status := parseIntFilter(r, "status")
	if status == -2 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "status must be an integer")
		return
	}
	limit, offset := pageParams(r)
	list, err := s.listRechargeOrders(r.Context(), uid, r.URL.Query().Get("kind"), status, limit, offset)
	if err != nil {
		gatewayErr(w, err)
		return
	}
	ok(w, list)
}

func (s *Server) hGetRechargeOrder(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/recharge/orders/")
	if id == "" || strings.Contains(id, "/") {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown recharge order path")
		return
	}
	var kind string
	if err := s.db.QueryRowContext(r.Context(),
		`SELECT recharge_kind FROM wallet_order WHERE order_id=$1 AND user_id=$2 AND order_type=1`, id, uid).Scan(&kind); err != nil {
		if err == sql.ErrNoRows {
			fail(w, http.StatusNotFound, "RECHARGE_NOT_FOUND", "recharge order not found")
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	switch kind {
	case RechargeKindManual:
		o, err := s.getManualOrder(r.Context(), uid, id)
		if err != nil {
			manualErr(w, err)
			return
		}
		ok(w, o)
	case RechargeKindGateway:
		o, err := s.getGatewayOrder(r.Context(), uid, id)
		if err != nil {
			gatewayErr(w, err)
			return
		}
		ok(w, o)
	default:
		o, err := s.getRecharge(r.Context(), uid, id)
		if err != nil {
			fail(w, http.StatusNotFound, "RECHARGE_NOT_FOUND", "recharge order not found")
			return
		}
		ok(w, o)
	}
}

func (s *Server) hAdminListRechargeOrders(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	status := parseIntFilter(r, "status")
	if status == -2 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "status must be an integer")
		return
	}
	limit, offset := pageParams(r)
	list, err := s.listRechargeOrdersAdmin(r.Context(), r.URL.Query().Get("user_id"), r.URL.Query().Get("kind"), status, limit, offset)
	if err != nil {
		gatewayErr(w, err)
		return
	}
	ok(w, list)
}
