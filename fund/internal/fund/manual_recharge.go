package fund

// 手动充值（后台收款方式 + 二维码 + 支付截图 + 管理员双人复核入账）。
// 流程（用户新定，取代 App 内原回调充值展示；Phase 8 回调接口本身不动）：
//   点钱包充值 -> 选后台设置的收款方式（微信/支付宝/银行卡）-> 输入金额 -> 显示该方式后台上传的收款二维码
//   -> 用户线下付款后提交成功支付截图 -> 等待审核 -> 管理员双人复核通过才入账。
// 订单口径：复用 wallet_order（order_type=1，recharge_kind='manual'，channel='manual'）；
// 状态机（仅 manual 行）：10 待上传凭证 -> 11 待审核 -> 1 已入账；11 -> 12 已拒绝（拒绝不入账）。
// 入账分录：txn type 20（用户可用 CR / 平台结算 DR，ref_type='recharge'），与 Phase 8 回调入账同模型；
// 入账事务 = 订单行锁 + 账务 + 订单状态 + 审计同一事务，任一步失败整笔回滚（第二复核人不落单，可重试）。
// 建单与传凭证本身不产生资金变动，故不设准入/交易密码门禁（与 Phase 8 建单口径一致）；
// 真正的资金变动只发生在管理员双人复核通过的入账事务里。管理端前端 UI 本次不交付
// （admin-front 为 dist-only 无源码），管理员先用本文件 API 设置收款方式与审核。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ManualAwaitingProof = 10 // 待上传凭证
	ManualPendingReview = 11 // 待审核
	ManualRejected      = 12 // 已拒绝（不入账）
	// 已入账复用 RechargePaid = 1（与回调单“已支付”同义：钱已进可用余额）。

	RechargeKindCallback = "callback"
	RechargeKindManual   = "manual"
	RechargeKindGateway  = "gateway"
)

var (
	errManualNotFound       = errors.New("manual recharge order not found")
	errManualNotManual      = errors.New("order is not a manual recharge order")
	errManualNotPending     = errors.New("manual recharge order is not pending review")
	errManualProofState     = errors.New("proof can only be uploaded while awaiting proof or pending review")
	errPayMethodNotFound    = errors.New("payment method not found")
	errPayMethodDisabled    = errors.New("payment method is disabled")
	errPayMethodNoQR        = errors.New("payment method has no QR code uploaded")
	errPayMethodBadType     = errors.New("payment method type must be wechat, alipay or bank")
	errManualIdemConflict   = errors.New("idempotency key already used by another order kind")
	errGatewayNotConfigured = errors.New("gateway payment is not available yet")
)

// ---- 收款方式 ----

type PaymentMethod struct {
	MethodID          string    `json:"method_id"`
	Type              string    `json:"type"`
	DisplayName       string    `json:"display_name"`
	QRFileID          *string   `json:"qr_file_id"`
	QRURL             *string   `json:"qr_url"`
	Enabled           bool      `json:"enabled"`
	SortOrder         int       `json:"sort_order"`
	CreatedByAdminID  *string   `json:"created_by_admin_id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func qrURL(qrFileID *string) *string {
	if qrFileID == nil || *qrFileID == "" {
		return nil
	}
	u := "/v1/uploads/" + *qrFileID
	return &u
}

const payMethodColumns = `method_id, type, display_name, qr_file_id, enabled, sort_order, created_by_admin_id, created_at, updated_at`

func scanPayMethod(row interface{ Scan(...any) error }) (*PaymentMethod, error) {
	var m PaymentMethod
	if err := row.Scan(&m.MethodID, &m.Type, &m.DisplayName, &m.QRFileID, &m.Enabled, &m.SortOrder,
		&m.CreatedByAdminID, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return nil, err
	}
	m.QRURL = qrURL(m.QRFileID)
	return &m, nil
}

func validPayMethodType(t string) bool {
	return t == "wechat" || t == "alipay" || t == "bank"
}

func (s *Server) createPayMethod(ctx context.Context, adminID, typ, displayName string, sortOrder int) (*PaymentMethod, error) {
	if !validPayMethodType(typ) {
		return nil, errPayMethodBadType
	}
	if strings.TrimSpace(displayName) == "" || utf8.RuneCountInString(displayName) > 128 {
		return nil, errors.New("display_name must be 1..128 chars")
	}
	id := newID("pm_")
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO wallet_payment_method (method_id, type, display_name, enabled, sort_order, created_by_admin_id) VALUES ($1,$2,$3,TRUE,$4,$5)`,
		id, typ, displayName, sortOrder, adminID); err != nil {
		return nil, err
	}
	s.auditStandalone(ctx, "", adminID, "admin_payment_method_create", "payment method "+id+" type="+typ, nil)
	return s.getPayMethodAdmin(ctx, id)
}

func (s *Server) getPayMethodAdmin(ctx context.Context, id string) (*PaymentMethod, error) {
	m, err := scanPayMethod(s.db.QueryRowContext(ctx,
		`SELECT `+payMethodColumns+` FROM wallet_payment_method WHERE method_id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, errPayMethodNotFound
	}
	return m, err
}

func (s *Server) listPayMethodsAdmin(ctx context.Context) ([]PaymentMethod, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+payMethodColumns+` FROM wallet_payment_method ORDER BY sort_order ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PaymentMethod{}
	for rows.Next() {
		m, err := scanPayMethod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *Server) listPayMethodsUser(ctx context.Context) ([]PaymentMethod, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+payMethodColumns+` FROM wallet_payment_method WHERE enabled=TRUE ORDER BY sort_order ASC, created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PaymentMethod{}
	for rows.Next() {
		m, err := scanPayMethod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *Server) updatePayMethod(ctx context.Context, adminID, id string, displayName *string, sortOrder *int, enabled *bool) (*PaymentMethod, error) {
	if _, err := s.getPayMethodAdmin(ctx, id); err != nil {
		return nil, err
	}
	if displayName != nil && (strings.TrimSpace(*displayName) == "" || utf8.RuneCountInString(*displayName) > 128) {
		return nil, errors.New("display_name must be 1..128 chars")
	}
	setParts := []string{}
	args := []any{}
	if displayName != nil {
		setParts = append(setParts, fmt.Sprintf("display_name=$%d", len(args)+1))
		args = append(args, *displayName)
	}
	if sortOrder != nil {
		setParts = append(setParts, fmt.Sprintf("sort_order=$%d", len(args)+1))
		args = append(args, *sortOrder)
	}
	if enabled != nil {
		setParts = append(setParts, fmt.Sprintf("enabled=$%d", len(args)+1))
		args = append(args, *enabled)
	}
	if len(setParts) == 0 {
		return s.getPayMethodAdmin(ctx, id)
	}
	setParts = append(setParts, "updated_at=now()")
	args = append(args, id)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE wallet_payment_method SET `+strings.Join(setParts, ", ")+` WHERE method_id=$`+strconv.Itoa(len(args)), args...); err != nil {
		return nil, err
	}
	s.auditStandalone(ctx, "", adminID, "admin_payment_method_update", "payment method "+id, nil)
	return s.getPayMethodAdmin(ctx, id)
}

// uploadPayMethodQR：管理端上传收款二维码（multipart 字段 qr）。替换旧二维码时旧文件行与磁盘文件一并清理。
func (s *Server) uploadPayMethodQR(ctx context.Context, adminID, methodID string, fh *multipart.FileHeader) (*PaymentMethod, error) {
	m, err := s.getPayMethodAdmin(ctx, methodID)
	if err != nil {
		return nil, err
	}
	fileID, _, _, _, err := s.saveUpload(ctx, uploadKindQR, fh)
	if err != nil {
		return nil, err
	}
	oldQR := m.QRFileID
	if _, err := s.db.ExecContext(ctx,
		`UPDATE wallet_payment_method SET qr_file_id=$1, updated_at=now() WHERE method_id=$2`, fileID, methodID); err != nil {
		s.deleteUploadFile(ctx, fileID)
		return nil, err
	}
	s.auditStandalone(ctx, "", adminID, "admin_payment_method_qr_upload", "payment method "+methodID+" qr uploaded", nil)
	if oldQR != nil && *oldQR != "" && *oldQR != fileID {
		s.deleteUploadFile(ctx, *oldQR)
	}
	return s.getPayMethodAdmin(ctx, methodID)
}

// ---- 手动充值订单视图 ----

type ManualRechargeOrder struct {
	OrderID            string     `json:"order_id"`
	UserID             string     `json:"user_id"`
	OrderType          int        `json:"order_type"`
	RechargeKind       string     `json:"recharge_kind"`
	AmountCents        int64      `json:"amount_cents"`
	FeeCents           int64      `json:"fee_cents"`
	ActualCents        int64      `json:"actual_cents"`
	Status             int        `json:"status"`
	Channel            string     `json:"channel"`
	PaymentMethodID    string     `json:"payment_method_id"`
	MethodType         string     `json:"method_type"`
	MethodDisplayName  string     `json:"method_display_name"`
	QRURL              *string    `json:"qr_url"`
	ProofID            *string    `json:"proof_id"`
	ProofFileID        *string    `json:"proof_file_id"`
	ProofURL           *string    `json:"proof_url"`
	ProofUploadedAt    *time.Time `json:"proof_uploaded_at"`
	ReviewFirstAdminID *string    `json:"review_first_admin_id"`
	ReviewSecondAdminID *string   `json:"review_second_admin_id"`
	RejectedByAdminID  *string    `json:"rejected_by_admin_id"`
	ReviewNote         *string    `json:"review_note"`
	ReviewedAt         *time.Time `json:"reviewed_at"`
	PaidTxnID          *string    `json:"paid_txn_id"`
	ClientIdempotencyKey *string  `json:"client_idempotency_key"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

const manualOrderSelect = `
	SELECT o.order_id, o.user_id, o.order_type, o.recharge_kind, o.amount_cents, o.fee_cents, o.actual_cents,
		o.status, o.channel, o.payment_method_id, COALESCE(pm.type,''), COALESCE(pm.display_name,''), pm.qr_file_id,
		pr.proof_id, pr.file_id, pr.created_at,
		o.review_first_admin_id, o.review_second_admin_id, o.rejected_by_admin_id, o.review_note, o.reviewed_at,
		o.paid_txn_id, o.client_idempotency_key, o.created_at, o.updated_at
	FROM wallet_order o
	LEFT JOIN wallet_payment_method pm ON pm.method_id = o.payment_method_id
	LEFT JOIN recharge_proof pr ON pr.order_id = o.order_id`

func scanManualOrder(row interface{ Scan(...any) error }) (*ManualRechargeOrder, error) {
	var o ManualRechargeOrder
	var pmID, qrFileID, proofID, proofFileID *string
	var proofAt *time.Time
	if err := row.Scan(&o.OrderID, &o.UserID, &o.OrderType, &o.RechargeKind, &o.AmountCents, &o.FeeCents, &o.ActualCents,
		&o.Status, &o.Channel, &pmID, &o.MethodType, &o.MethodDisplayName, &qrFileID,
		&proofID, &proofFileID, &proofAt,
		&o.ReviewFirstAdminID, &o.ReviewSecondAdminID, &o.RejectedByAdminID, &o.ReviewNote, &o.ReviewedAt,
		&o.PaidTxnID, &o.ClientIdempotencyKey, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	if pmID != nil {
		o.PaymentMethodID = *pmID
	}
	o.QRURL = qrURL(qrFileID)
	o.ProofID = proofID
	o.ProofFileID = proofFileID
	o.ProofUploadedAt = proofAt
	if proofFileID != nil {
		u := "/v1/uploads/" + *proofFileID
		o.ProofURL = &u
	}
	return &o, nil
}

func (s *Server) getManualOrder(ctx context.Context, userID, orderID string) (*ManualRechargeOrder, error) {
	o, err := scanManualOrder(s.db.QueryRowContext(ctx,
		manualOrderSelect+` WHERE o.order_id=$1 AND o.user_id=$2 AND o.recharge_kind='manual'`, orderID, userID))
	if err == sql.ErrNoRows {
		return nil, errManualNotFound
	}
	return o, err
}

func (s *Server) getManualOrderAdmin(ctx context.Context, orderID string) (*ManualRechargeOrder, error) {
	o, err := scanManualOrder(s.db.QueryRowContext(ctx,
		manualOrderSelect+` WHERE o.order_id=$1 AND o.recharge_kind='manual'`, orderID))
	if err == sql.ErrNoRows {
		return nil, errManualNotFound
	}
	return o, err
}

// createManualRecharge：建手动单。方式行 FOR UPDATE（停用与建单串行化）；方式停用/无二维码均拒绝；
// 金额必须 >0（JSON 非整数分在解码层即 400）；钱包不存在时同一事务内自动建钱包；
// 同一用户同一幂等键（manual 口径）重复创建返回原单。
func (s *Server) createManualRecharge(ctx context.Context, userID, methodID string, amountCents int64, idemKey string) (*ManualRechargeOrder, error) {
	if amountCents <= 0 {
		return nil, errors.New("amount_cents must be > 0")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var enabled bool
	var qrFileID *string
	if err := tx.QueryRowContext(ctx,
		`SELECT enabled, qr_file_id FROM wallet_payment_method WHERE method_id=$1 FOR UPDATE`, methodID).
		Scan(&enabled, &qrFileID); err != nil {
		if err == sql.ErrNoRows {
			return nil, errPayMethodNotFound
		}
		return nil, err
	}
	if !enabled {
		return nil, errPayMethodDisabled
	}
	if qrFileID == nil || *qrFileID == "" {
		return nil, errPayMethodNoQR
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_account (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return nil, err
	}
	if idemKey != "" {
		var existingID string
		err := tx.QueryRowContext(ctx,
			`SELECT order_id FROM wallet_order WHERE user_id=$1 AND client_idempotency_key=$2 AND recharge_kind='manual'`, userID, idemKey).Scan(&existingID)
		if err == nil {
			_ = tx.Rollback()
			return s.getManualOrder(ctx, userID, existingID)
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}
	orderID := newID("mr_")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_order (order_id, user_id, order_type, recharge_kind, amount_cents, fee_cents, actual_cents, status, channel, payment_method_id, client_idempotency_key)
		 VALUES ($1,$2,1,'manual',$3,0,$3,10,'manual',$4,$5)`,
		orderID, userID, amountCents, methodID, nullStr(idemKey)); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			existing, err2 := s.getManualOrderByIdem(ctx, userID, idemKey)
			if err2 != nil {
				return nil, errManualIdemConflict
			}
			return existing, nil
		}
		return nil, err
	}
	if err := insertAudit(ctx, tx, userID, userID, "manual_recharge_create",
		fmt.Sprintf("manual recharge %s %d cents method %s", orderID, amountCents, methodID), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getManualOrder(ctx, userID, orderID)
}

func (s *Server) getManualOrderByIdem(ctx context.Context, userID, idemKey string) (*ManualRechargeOrder, error) {
	o, err := scanManualOrder(s.db.QueryRowContext(ctx,
		manualOrderSelect+` WHERE o.user_id=$1 AND o.client_idempotency_key=$2 AND o.recharge_kind='manual'`, userID, idemKey))
	if err == sql.ErrNoRows {
		return nil, errManualNotFound
	}
	return o, err
}

func (s *Server) listManualOrders(ctx context.Context, userID, status int, limit, offset int) ([]ManualRechargeOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		manualOrderSelect+` WHERE o.user_id=$1 AND o.recharge_kind='manual' AND ($2<0 OR o.status=$2) ORDER BY o.created_at DESC LIMIT $3 OFFSET $4`,
		userID, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManualRechargeOrder{}
	for rows.Next() {
		o, err := scanManualOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

func (s *Server) listManualPendingAdmin(ctx context.Context, userID string, limit, offset int) ([]ManualRechargeOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		manualOrderSelect+` WHERE o.recharge_kind='manual' AND o.status=11 AND ($1='' OR o.user_id=$1) ORDER BY o.created_at ASC LIMIT $2 OFFSET $3`,
		userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ManualRechargeOrder{}
	for rows.Next() {
		o, err := scanManualOrder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// ---- 统一入账记账（调用方事务内）：用户可用 CR / 平台结算 DR，txn type 20，ref_type='recharge'。
// 与 Phase 8 回调入账同模型；本函数不提交事务，订单状态与审计由调用方在同一事务内完成。
func postRechargeCreditTx(ctx context.Context, tx *sql.Tx, userID string, amountCents int64, txnType int, idemKey, refID string) (string, error) {
	entries := []postingEntry{
		{userID: userID, accountType: AcctAvailable, direction: DirCredit, amount: amountCents, oppositeUID: PlatformUserID, oppositeAcc: "settlement"},
		{userID: PlatformUserID, accountType: AcctAvailable, direction: DirDebit, amount: amountCents, oppositeUID: userID, oppositeAcc: "available"},
	}
	var sum int64
	for _, e := range entries {
		sum += signed(e.direction, e.amount)
	}
	if sum != 0 {
		return "", fmt.Errorf("double-entry sum must be 0, got %d", sum)
	}
	users := []string{userID, PlatformUserID}
	// 排序加锁防死锁（与 Phase 8 同口径；平台户 user_id 含冒号，排序后仍在用户户之后）。
	if users[0] > users[1] {
		users[0], users[1] = users[1], users[0]
	}
	states := map[string]*accountState{}
	for _, u := range users {
		var st accountState
		err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&st.balance, &st.frozen, &st.version, &st.status)
		if err != nil {
			return "", err
		}
		if st.status != 0 {
			return "", fmt.Errorf("wallet not normal: %s", u)
		}
		states[u] = &st
	}
	type applied struct {
		e            postingEntry
		balanceAfter int64
		frozenAfter  int64
	}
	appliedList := []applied{}
	for _, e := range entries {
		st := states[e.userID]
		st.balance += deltaFor(e.userID, e.direction, e.amount)
		if st.balance < 0 {
			return "", fmt.Errorf("insufficient available balance for %s", e.userID)
		}
		appliedList = append(appliedList, applied{e: e, balanceAfter: st.balance, frozenAfter: st.frozen})
	}
	txnID := newID("txn_")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'recharge',$4,1,$5)`,
		txnID, userID, txnType, refID, idemKey); err != nil {
		return "", err
	}
	for _, a := range appliedList {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, a.e.userID, a.e.accountType, a.e.direction, a.e.amount, a.balanceAfter, a.frozenAfter, nullStr(a.e.oppositeUID), nullStr(a.e.oppositeAcc)); err != nil {
			return "", err
		}
	}
	for u, st := range states {
		res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, st.balance, st.frozen, u, st.version)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return "", fmt.Errorf("optimistic lock failed for %s", u)
		}
	}
	return txnID, nil
}

// approveManualRecharge：双人复核。第一人只记 review_first_admin_id（余额不动）；
// 第二名不同管理员在同一事务内入账 + 置状态 1 + 记审计；同一人两次、非待审核单、
// 入账失败时整笔回滚（第二复核人不落单，单据仍为待审核、可重试或拒绝）。
func (s *Server) approveManualRecharge(ctx context.Context, orderID, adminID string) (string, error) {
	o, err := s.getManualOrderAdmin(ctx, orderID)
	if err != nil {
		return "", err
	}
	if o.Status != ManualPendingReview {
		return "", errManualNotPending
	}
	if o.ReviewFirstAdminID == nil {
		res, err := s.db.ExecContext(ctx,
			`UPDATE wallet_order SET review_first_admin_id=$1, updated_at=now() WHERE order_id=$2 AND recharge_kind='manual' AND status=11 AND review_first_admin_id IS NULL`,
			adminID, orderID)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			// 并发下首审已被他人抢占：重读后按第二复核路径继续（首审人非本人时可执行）。
			o2, err := s.getManualOrderAdmin(ctx, orderID)
			if err != nil {
				return "", err
			}
			if o2.Status != ManualPendingReview {
				return "", errManualNotPending
			}
			if o2.ReviewFirstAdminID != nil && *o2.ReviewFirstAdminID == adminID {
				return "", errAdjustmentSameAdmin
			}
			o = o2
		} else {
			s.auditStandalone(ctx, o.UserID, adminID, "admin_manual_recharge_approve1", "manual recharge approve1 order_id="+orderID, nil)
			return "awaiting_second_approval", nil
		}
	}
	if o.ReviewFirstAdminID != nil && *o.ReviewFirstAdminID == adminID {
		return "", errAdjustmentSameAdmin
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var userID string
	var amount int64
	var status int
	var first *string
	err = tx.QueryRowContext(ctx,
		`SELECT user_id, amount_cents, status, review_first_admin_id FROM wallet_order WHERE order_id=$1 AND recharge_kind='manual' FOR UPDATE`,
		orderID).Scan(&userID, &amount, &status, &first)
	if err == sql.ErrNoRows {
		return "", errManualNotFound
	}
	if err != nil {
		return "", err
	}
	if status != ManualPendingReview {
		return "", errManualNotPending
	}
	if first == nil || *first == adminID {
		return "", errAdjustmentSameAdmin
	}
	txnID, err := postRechargeCreditTx(ctx, tx, userID, amount, TxnRecharge, "rch-manual-pay-"+orderID, orderID)
	if err != nil {
		return "", err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE wallet_order SET status=1, review_second_admin_id=$1, paid_txn_id=$2, reviewed_at=now(), updated_at=now() WHERE order_id=$3 AND recharge_kind='manual' AND status=11`,
		adminID, txnID, orderID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", errManualNotPending
	}
	if err := insertAudit(ctx, tx, userID, adminID, "admin_manual_recharge_execute",
		fmt.Sprintf("manual recharge approved order_id=%s txn=%s %d cents", orderID, txnID, amount), "", ""); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return "approved", nil
}

// rejectManualRecharge：任一管理员可拒绝（仅待审核单）。只改状态 + 拒绝原因，不入账、不动余额。
func (s *Server) rejectManualRecharge(ctx context.Context, orderID, adminID, note string) error {
	if utf8.RuneCountInString(note) > 500 {
		return errors.New("review_note must be <= 500 chars")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE wallet_order SET status=12, rejected_by_admin_id=$1, review_note=$2, reviewed_at=now(), updated_at=now() WHERE order_id=$3 AND recharge_kind='manual' AND status=11`,
		adminID, nullStr(note), orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		// 区分不存在与非待审核态。
		if _, err := s.getManualOrderAdmin(ctx, orderID); err != nil {
			return err
		}
		return errManualNotPending
	}
	o, _ := s.getManualOrderAdmin(ctx, orderID)
	uid := ""
	if o != nil {
		uid = o.UserID
	}
	s.auditStandalone(ctx, uid, adminID, "admin_manual_recharge_reject", "manual recharge rejected order_id="+orderID, nil)
	return nil
}

// uploadManualProof：用户上传支付截图。仅本人订单、仅待上传（10）/待审核（11）态；
// 11 态重传替换旧凭证（旧文件行与磁盘文件在提交后清理）；上传后订单置 11 待审核。
// 竞态防护：落盘后在事务内重查订单状态（FOR UPDATE），审批已开始（状态已变）时拒绝并清理新文件。
func (s *Server) uploadManualProof(ctx context.Context, userID, orderID string, fh *multipart.FileHeader) (*ManualRechargeOrder, error) {
	o, err := s.getManualOrder(ctx, userID, orderID)
	if err != nil {
		return nil, err
	}
	if o.Status != ManualAwaitingProof && o.Status != ManualPendingReview {
		return nil, errManualProofState
	}
	fileID, _, _, _, err := s.saveUpload(ctx, uploadKindProof, fh)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.deleteUploadFile(ctx, fileID)
		return nil, err
	}
	defer tx.Rollback()
	var status int
	if err := tx.QueryRowContext(ctx,
		`SELECT status FROM wallet_order WHERE order_id=$1 AND user_id=$2 AND recharge_kind='manual' FOR UPDATE`, orderID, userID).
		Scan(&status); err != nil {
		s.deleteUploadFile(ctx, fileID)
		if err == sql.ErrNoRows {
			return nil, errManualNotFound
		}
		return nil, err
	}
	if status != ManualAwaitingProof && status != ManualPendingReview {
		s.deleteUploadFile(ctx, fileID)
		return nil, errManualProofState
	}
	var oldFileID *string
	var existingProofID *string
	err = tx.QueryRowContext(ctx, `SELECT proof_id, file_id FROM recharge_proof WHERE order_id=$1`, orderID).Scan(&existingProofID, &oldFileID)
	if err != nil && err != sql.ErrNoRows {
		s.deleteUploadFile(ctx, fileID)
		return nil, err
	}
	if existingProofID != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE recharge_proof SET file_id=$1, uploaded_by_user_id=$2, updated_at=now() WHERE order_id=$3`, fileID, userID, orderID); err != nil {
			s.deleteUploadFile(ctx, fileID)
			return nil, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO recharge_proof (proof_id, order_id, file_id, uploaded_by_user_id) VALUES ($1,$2,$3,$4)`,
			newID("prf_"), orderID, fileID, userID); err != nil {
			s.deleteUploadFile(ctx, fileID)
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE wallet_order SET status=11, updated_at=now() WHERE order_id=$1 AND recharge_kind='manual'`, orderID); err != nil {
		s.deleteUploadFile(ctx, fileID)
		return nil, err
	}
	if err := insertAudit(ctx, tx, userID, userID, "manual_recharge_proof_upload",
		fmt.Sprintf("manual recharge %s proof uploaded", orderID), "", ""); err != nil {
		s.deleteUploadFile(ctx, fileID)
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		s.deleteUploadFile(ctx, fileID)
		return nil, err
	}
	if oldFileID != nil && *oldFileID != "" && *oldFileID != fileID {
		s.deleteUploadFile(ctx, *oldFileID)
	}
	return s.getManualOrder(ctx, userID, orderID)
}

// ---- HTTP ----

func manualErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errManualNotFound) || errors.Is(err, errManualNotManual):
		fail(w, http.StatusNotFound, "MANUAL_RECHARGE_NOT_FOUND", "manual recharge order not found")
	case errors.Is(err, errPayMethodNotFound):
		fail(w, http.StatusNotFound, "PAYMENT_METHOD_NOT_FOUND", err.Error())
	case errors.Is(err, errPayMethodDisabled):
		fail(w, http.StatusBadRequest, "PAYMENT_METHOD_NOT_AVAILABLE", err.Error())
	case errors.Is(err, errPayMethodNoQR):
		fail(w, http.StatusBadRequest, "PAYMENT_METHOD_NO_QR", err.Error())
	case errors.Is(err, errPayMethodBadType):
		fail(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	case errors.Is(err, errManualNotPending) || errors.Is(err, errManualProofState):
		fail(w, http.StatusConflict, "INVALID_TRANSITION", err.Error())
	case errors.Is(err, errAdjustmentSameAdmin):
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
	case errors.Is(err, errManualIdemConflict):
		fail(w, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", err.Error())
	case errors.Is(err, errUploadTooLarge):
		fail(w, http.StatusBadRequest, "UPLOAD_TOO_LARGE", err.Error())
	case errors.Is(err, errUploadBadType):
		fail(w, http.StatusBadRequest, "UPLOAD_BAD_TYPE", err.Error())
	default:
		msg := err.Error()
		if strings.Contains(msg, "must be") || strings.Contains(msg, "required") || strings.Contains(msg, "invalid") {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", msg)
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", msg)
	}
}

type payMethodCreateReq struct {
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
	SortOrder   int    `json:"sort_order"`
}

func (s *Server) hAdminCreatePayMethod(w http.ResponseWriter, r *http.Request) {
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	var req payMethodCreateReq
	if !decodeBody(w, r, &req) {
		return
	}
	m, err := s.createPayMethod(r.Context(), adminID, req.Type, req.DisplayName, req.SortOrder)
	if err != nil {
		manualErr(w, err)
		return
	}
	ok(w, m)
}

func (s *Server) hAdminListPayMethods(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	list, err := s.listPayMethodsAdmin(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

func payMethodIDFromPath(r *http.Request) (string, string) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/admin/payment-methods/")
	parts := strings.Split(rest, "/")
	if len(parts) == 1 {
		return parts[0], ""
	}
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func (s *Server) hAdminPayMethodSub(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	id, suffix := payMethodIDFromPath(r)
	if id == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown payment method path")
		return
	}
	switch {
	case r.Method == http.MethodGet && suffix == "":
		m, err := s.getPayMethodAdmin(r.Context(), id)
		if err != nil {
			manualErr(w, err)
			return
		}
		ok(w, m)
	case r.Method == http.MethodPost && suffix == "qr":
		adminID, okAuth := s.authAdminID(r)
		if !okAuth {
			fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
			return
		}
		fh, okForm := parseUploadForm(w, r, "qr")
		if !okForm {
			return
		}
		m, err := s.uploadPayMethodQR(r.Context(), adminID, id, fh)
		if err != nil {
			manualErr(w, err)
			return
		}
		ok(w, m)
	case r.Method == http.MethodPut && suffix == "":
		var raw map[string]any
		body, _ := readAll(r)
		if len(body) > 0 {
			if err := json.Unmarshal(body, &raw); err != nil {
				fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
				return
			}
		}
		var displayName *string
		var sortOrder *int
		var enabled *bool
		if v, ok := raw["display_name"].(string); ok {
			displayName = &v
		}
		if v, ok := raw["sort_order"].(float64); ok {
			n := int(v)
			sortOrder = &n
		}
		if v, ok := raw["enabled"].(bool); ok {
			enabled = &v
		}
		m, err := s.updatePayMethod(r.Context(), s.adminOperator(r), id, displayName, sortOrder, enabled)
		if err != nil {
			manualErr(w, err)
			return
		}
		ok(w, m)
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown payment method path")
	}
}

// ---- 用户侧：方式列表 / 建单 / 传凭证 / 订单查询 ----

func (s *Server) hListPayMethodsUser(w http.ResponseWriter, r *http.Request) {
	if _, okAuth := s.userCtx(w, r); !okAuth {
		return
	}
	list, err := s.listPayMethodsUser(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

type manualCreateReq struct {
	MethodID       string `json:"method_id"`
	AmountCents    int64  `json:"amount_cents"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) hCreateManualRecharge(w http.ResponseWriter, r *http.Request) {
	var req manualCreateReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if req.MethodID == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "method_id is required")
		return
	}
	if req.AmountCents <= 0 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "amount_cents must be > 0")
		return
	}
	ord, err := s.createManualRecharge(r.Context(), uid, req.MethodID, req.AmountCents, req.IdempotencyKey)
	if err != nil {
		manualErr(w, err)
		return
	}
	ok(w, ord)
}

func (s *Server) hUploadManualProof(w http.ResponseWriter, r *http.Request) {
	// POST /v1/recharge/<id>/proof（单数 recharge 前缀，与复数 /v1/recharges/ 回调单路径区分）。
	rest := strings.TrimPrefix(r.URL.Path, "/v1/recharge/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "proof" || parts[0] == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown recharge path")
		return
	}
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	fh, okForm := parseUploadForm(w, r, "proof")
	if !okForm {
		return
	}
	ord, err := s.uploadManualProof(r.Context(), uid, parts[0], fh)
	if err != nil {
		manualErr(w, err)
		return
	}
	ok(w, ord)
}

func (s *Server) hListManualPendingAdmin(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	limit, offset := pageParams(r)
	list, err := s.listManualPendingAdmin(r.Context(), r.URL.Query().Get("user_id"), limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

type manualReviewReq struct {
	ReviewNote string `json:"review_note"`
}

func (s *Server) hAdminManualReview(w http.ResponseWriter, r *http.Request) {
	// POST /v1/admin/recharge/<id>/approve | /reject
	rest := strings.TrimPrefix(r.URL.Path, "/v1/admin/recharge/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || (parts[1] != "approve" && parts[1] != "reject") {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown manual recharge review path")
		return
	}
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	if parts[1] == "approve" {
		stage, err := s.approveManualRecharge(r.Context(), parts[0], adminID)
		if err != nil {
			manualErr(w, err)
			return
		}
		ok(w, map[string]string{"stage": stage})
		return
	}
	var req manualReviewReq
	body, _ := readAll(r)
	if len(body) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
			return
		}
		if v, ok := raw["review_note"].(string); ok {
			req.ReviewNote = v
		}
	}
	if err := s.rejectManualRecharge(r.Context(), parts[0], adminID, req.ReviewNote); err != nil {
		manualErr(w, err)
		return
	}
	ok(w, map[string]bool{"rejected": true})
}
