package fund

// Phase 6：用户之间余额转账（即时到账）。
// 资金模型：同一 DB 事务、同一 txn_id（txn type 18，ref_type='transfer', ref_id=order_id）下两条分录
// （转出方可用 DR / 转入方可用 CR，金额相等，带符号合计为 0）；无冻结行、无过期、无领取环节。
// 模式定稿：全阶段只用一套资金状态机——即时到账，与红包即时结算模型一致；待确认模式不做
// （待确认模式须另起冻结 + 过期 + 领取/退款状态机，会使同一资金服务内并存两套状态机与两套争议
// 路径，Phase 6 不引入该复杂度；未来如需待确认转账，须另立阶段并扩展订单状态，不得在本阶段
// 代码里留半截待确认分支）。
// 订单状态：1 成功（唯一由成功转账写入的状态；订单行与账务 txn、分录在同一 DB 事务内落库）；
// 2 失败（仅结算失败审计行：余额不足 / 收款方钱包冻结 / 注销时，HTTP 层在主事务回滚后另起短事务
// 写入一行 status=2、failure_code 非空、txn_id 为空的审计行；失败行不落账——无 wallet_txn、无分录、
// 双方余额不动；失败行不带幂等键，同一幂等键事后补足余额重发仍可成功，成功幂等只匹配 status=1 的行）。
// 下列失败不产生订单行（请求本身无效，非一次结算尝试）：参数错误、 自转、平台结算户为收款方、
// 鉴权失败、转出方准入未完成、交易密码错误——在任何资金变动发生前即被拒绝，错误响应本身就是记录。
// 门禁：转出方须已完成准入（financeStatus == ok，否则只返回引导、不扣款）+ 验交易密码；
// 收款方为被动收款：钱包不存在时在同一事务内自动建钱包即可入账，不设准入关——转账由转出方发起，
// 收款方准入在其下次主动使用资金时 enforcement；与红包领取须准入的区别：红包领取是领取方的主动
// 资金操作（领取即确认收款并可随即使用），故领取时查准入；转账入账是被动记账，不查收款方准入。
// 收款方钱包冻结/注销时整笔拒绝（不落账）。转出方与收款方 user_id 取自 IM token：转出方即 token
// 用户；请求体 user_id 不一致由 decodeAndCheckUser 统一 403；收款方为平台结算户时 403。
// 纠错：绝不直接改余额、不 UPDATE 订单行改状态；异常纠错只走已有冲正（POST /v1/ledger/reverse，
// txn type 8，全部分录取反生成新 txn；接收方余额不足时冲正整体失败、原转账不动）。冲正不改写原
// 订单行（订单为历史事实），冲正链路凭原 txn_id 在管理端详情接口查到。
// 并发：涉及账户按 user_id 排序加锁防死锁 + version 条件更新（第二道防线）+ 余额校验；
// 转出方 Redis 辅助锁（fund:user:<sender>）仅协调，锁被持有回 429，绝不以 Redis 计数为准出账。
// 幂等：成功订单 UNIQUE (sender_id, idempotency_key)（空键除外）；同一幂等键重复提交返回原订单
// （先查后插 + 唯一冲突回查），不重复扣款；并发重复同样安全。

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
	TransferSuccess = 1 // 成功（唯一由成功转账写入的状态）
	TransferFailed  = 2 // 失败（仅结算失败审计行，不落账）
)

const (
	FailureInsufficientBalance = "INSUFFICIENT_BALANCE"
	FailureReceiverFrozen      = "RECEIVER_FROZEN"
	FailureReceiverClosed      = "RECEIVER_CLOSED"
)

var (
	errTransferNotFound       = errors.New("transfer order not found")
	errTransferForbidden      = errors.New("transfer order does not belong to viewer")
	errTransferSelf           = errors.New("cannot transfer to self")
	errTransferPlatform       = errors.New("platform settlement account is not operable via API")
	errTransferInsufficient   = errors.New("insufficient available balance")
	errTransferReceiverFrozen = errors.New("receiver wallet frozen")
	errTransferReceiverClosed = errors.New("receiver wallet closed")
)

// isSettlementFailure：须写入失败审计行的结算失败（余额不足 / 收款方钱包异常）。
func isSettlementFailure(err error) (string, bool) {
	switch {
	case errors.Is(err, errTransferInsufficient):
		return FailureInsufficientBalance, true
	case errors.Is(err, errTransferReceiverFrozen):
		return FailureReceiverFrozen, true
	case errors.Is(err, errTransferReceiverClosed):
		return FailureReceiverClosed, true
	default:
		return "", false
	}
}

type TransferOrder struct {
	OrderID        string    `json:"order_id"`
	SenderID       string    `json:"sender_id"`
	ReceiverID     string    `json:"receiver_id"`
	AmountCents    int64     `json:"amount_cents"`
	Remark         string    `json:"remark"`
	Status         int       `json:"status"` // 1 成功 2 失败（失败行仅审计，不落账）
	FailureCode    *string   `json:"failure_code,omitempty"`
	TxnID          *string   `json:"txn_id,omitempty"`
	IdempotencyKey *string   `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

const transferOrderColumns = `order_id, sender_id, receiver_id, amount_cents, remark, status, failure_code, txn_id, idempotency_key, created_at, updated_at`

func scanTransferOrder(row interface{ Scan(...any) error }) (*TransferOrder, error) {
	var o TransferOrder
	if err := row.Scan(&o.OrderID, &o.SenderID, &o.ReceiverID, &o.AmountCents, &o.Remark, &o.Status,
		&o.FailureCode, &o.TxnID, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

func loadTransferBySenderIdem(ctx context.Context, q rowQuerier, senderID, idemKey string) (*TransferOrder, error) {
	row := q.QueryRowContext(ctx, `SELECT `+transferOrderColumns+` FROM transfer_order WHERE sender_id=$1 AND idempotency_key=$2 AND status=1`, senderID, idemKey)
	o, err := scanTransferOrder(row)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return o, err
}

// sendTransfer：即时转账。同一事务：成功幂等回查 -> 收款方自动建钱包（同一事务内）->
// 双方行锁（按 user_id 排序）-> 收款方状态校验 -> 转出方余额校验 -> 记账（转出可用 DR /
// 转入可用 CR）-> 双方余额 CAS 更新 -> 订单行 -> 审计。任一步失败整笔回滚（无 txn、无分录、
// 无订单成功行、双方余额不动）；唯一冲突时回滚并回查返回原订单。
func (s *Server) sendTransfer(ctx context.Context, senderID, receiverID string, amount int64, remark, idemKey string) (*TransferOrder, error) {
	if !validUserID(receiverID) {
		return nil, errors.New("invalid receiver_id")
	}
	if receiverID == PlatformUserID {
		return nil, errTransferPlatform
	}
	if receiverID == senderID {
		return nil, errTransferSelf
	}
	if amount <= 0 {
		return nil, errors.New("amount_cents must be > 0")
	}
	if utf8.RuneCountInString(remark) > 200 {
		return nil, errors.New("remark too long, max 200 chars")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if idemKey != "" {
		if existing, err := loadTransferBySenderIdem(ctx, tx, senderID, idemKey); err == nil {
			_ = tx.Rollback()
			return existing, nil
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}

	// 收款方被动收款：钱包不存在时在同一事务内自动建钱包（事务回滚时该行一并消失，
	// 不会因后续结算失败而残留空钱包）；建钱包本身不是资金变动。
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_account (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, receiverID); err != nil {
		return nil, err
	}

	users := []string{senderID, receiverID}
	sort.Strings(users)
	states := map[string]*accountState{}
	for _, u := range users {
		var st accountState
		if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&st.balance, &st.frozen, &st.version, &st.status); err != nil {
			if err == sql.ErrNoRows {
				return nil, fmt.Errorf("wallet not found: %s", u)
			}
			return nil, err
		}
		states[u] = &st
	}
	senderSt, receiverSt := states[senderID], states[receiverID]
	if senderSt.status != 0 {
		return nil, fmt.Errorf("wallet not normal: %s", senderID)
	}
	switch receiverSt.status {
	case 1:
		return nil, errTransferReceiverFrozen
	case 2:
		return nil, errTransferReceiverClosed
	}
	senderBalAfter := senderSt.balance - amount
	if senderBalAfter < 0 {
		return nil, errTransferInsufficient
	}
	receiverBalAfter := receiverSt.balance + amount

	txnID := newID("txn_")
	orderID := newID("trf_")
	now := time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'transfer',$4,1,$5)`,
		txnID, senderID, TxnTransfer, orderID, "trf-"+orderID); err != nil {
		return nil, err
	}
	type le struct {
		uid      string
		dir      int
		balAfter int64
		frzAfter int64
		oppUID   string
	}
	entries := []le{
		{senderID, DirDebit, senderBalAfter, senderSt.frozen, receiverID},
		{receiverID, DirCredit, receiverBalAfter, receiverSt.frozen, senderID},
	}
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'available')`,
			txnID, e.uid, AcctAvailable, e.dir, amount, e.balAfter, e.frzAfter, e.oppUID); err != nil {
			return nil, err
		}
	}
	for _, u := range users {
		st := states[u]
		newBal := senderBalAfter
		if u == receiverID {
			newBal = receiverBalAfter
		}
		res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, newBal, st.frozen, u, st.version)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, fmt.Errorf("optimistic lock failed for %s", u)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO transfer_order (order_id, sender_id, receiver_id, amount_cents, remark, status, txn_id, idempotency_key) VALUES ($1,$2,$3,$4,$5,1,$6,$7)`,
		orderID, senderID, receiverID, amount, remark, txnID, nullStr(idemKey)); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			existing, err2 := loadTransferBySenderIdem(ctx, s.db, senderID, idemKey)
			if err2 != nil {
				return nil, err2
			}
			return existing, nil
		}
		return nil, err
	}
	if err := insertAudit(ctx, tx, senderID, senderID, "transfer_send", fmt.Sprintf("transfer %s %d cents to %s", orderID, amount, receiverID), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			existing, err2 := loadTransferBySenderIdem(ctx, s.db, senderID, idemKey)
			if err2 == nil {
				return existing, nil
			}
		}
		return nil, err
	}
	txnIDCopy := txnID
	idemCopy := (*string)(nil)
	if idemKey != "" {
		v := idemKey
		idemCopy = &v
	}
	return &TransferOrder{OrderID: orderID, SenderID: senderID, ReceiverID: receiverID, AmountCents: amount,
		Remark: remark, Status: TransferSuccess, TxnID: &txnIDCopy, IdempotencyKey: idemCopy, CreatedAt: now, UpdatedAt: now}, nil
}

// recordFailedTransfer：结算失败审计行（主事务回滚后另起短事务写入；写入失败只记日志，
// 绝不改变已返回给客户端的失败结果）。失败行 status=2、failure_code 非空、txn_id 为空、
// 不带幂等键（同一幂等键事后重发不受失败行阻挡）。
func (s *Server) recordFailedTransfer(ctx context.Context, senderID, receiverID string, amount int64, remark, failureCode string) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO transfer_order (order_id, sender_id, receiver_id, amount_cents, remark, status, failure_code) VALUES ($1,$2,$3,$4,$5,2,$6)`,
		newID("trf_"), senderID, receiverID, amount, remark, failureCode)
	if err != nil {
		s.log.Printf("failed transfer audit insert failed sender=%s failure=%s err=%v", senderID, failureCode, err)
	}
}

// getTransfer：订单详情（仅转出方/收款方可见，第三方 403）。
func (s *Server) getTransfer(ctx context.Context, viewerID, orderID string) (*TransferOrder, error) {
	var o TransferOrder
	err := s.db.QueryRowContext(ctx, `SELECT `+transferOrderColumns+` FROM transfer_order WHERE order_id=$1`, orderID).
		Scan(&o.OrderID, &o.SenderID, &o.ReceiverID, &o.AmountCents, &o.Remark, &o.Status,
			&o.FailureCode, &o.TxnID, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, errTransferNotFound
	}
	if err != nil {
		return nil, err
	}
	if viewerID != o.SenderID && viewerID != o.ReceiverID {
		return nil, errTransferForbidden
	}
	return &o, nil
}

// listTransfers：发出（sent）或收到（received）列表，按创建时间倒序。
// 注意：失败审计行（status=2）同样出现在列表中（status 字段可区分），以便管理端与用户
// 核对失败尝试；成功幂等只匹配 status=1 的行，失败行不阻挡重发。
func (s *Server) listTransfers(ctx context.Context, userID, role string, limit, offset int) ([]TransferOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var q string
	switch role {
	case "sent":
		q = `SELECT ` + transferOrderColumns + ` FROM transfer_order WHERE sender_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`
	case "received":
		q = `SELECT ` + transferOrderColumns + ` FROM transfer_order WHERE receiver_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`
	default:
		return nil, errors.New("role must be sent or received")
	}
	rows, err := s.db.QueryContext(ctx, q, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TransferOrder{}
	for rows.Next() {
		var o TransferOrder
		if err := rows.Scan(&o.OrderID, &o.SenderID, &o.ReceiverID, &o.AmountCents, &o.Remark, &o.Status,
			&o.FailureCode, &o.TxnID, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// adminTransferDetail：管理端全链路查询（订单 + 账务 txn + 该 txn 的全部分录，含双方）。
// 成功订单的分录恰好 2 条（转出 DR / 转入 CR）；失败审计行无 txn、无分录。
type adminTransferDetail struct {
	Order   *TransferOrder `json:"order"`
	Txn     *Txn           `json:"txn,omitempty"`
	Entries []LedgerEntry  `json:"entries"`
}

func (s *Server) adminTransferDetail(ctx context.Context, orderID string) (*adminTransferDetail, error) {
	var o TransferOrder
	err := s.db.QueryRowContext(ctx, `SELECT `+transferOrderColumns+` FROM transfer_order WHERE order_id=$1`, orderID).
		Scan(&o.OrderID, &o.SenderID, &o.ReceiverID, &o.AmountCents, &o.Remark, &o.Status,
			&o.FailureCode, &o.TxnID, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, errTransferNotFound
	}
	if err != nil {
		return nil, err
	}
	d := &adminTransferDetail{Order: &o, Entries: []LedgerEntry{}}
	if o.TxnID == nil {
		return d, nil
	}
	var txn Txn
	if err := s.db.QueryRowContext(ctx, `SELECT txn_id, user_id, type, ref_type, ref_id, status, idempotency_key, created_at FROM wallet_txn WHERE txn_id=$1`, *o.TxnID).
		Scan(&txn.TxnID, &txn.UserID, &txn.Type, &txn.RefType, &txn.RefID, &txn.Status, &txn.IdempotencyKey, &txn.CreatedAt); err != nil {
		return nil, err
	}
	d.Txn = &txn
	rows, err := s.db.QueryContext(ctx,
		`SELECT entry_id, txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account, created_at
		 FROM wallet_ledger WHERE txn_id=$1 ORDER BY entry_id`, *o.TxnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.EntryID, &e.TxnID, &e.UserID, &e.AccountType, &e.Direction, &e.AmountCents, &e.BalanceAfterCents, &e.FrozenAfterCents, &e.OppositeUserID, &e.OppositeAccount, &e.CreatedAt); err != nil {
			return nil, err
		}
		d.Entries = append(d.Entries, e)
	}
	return d, rows.Err()
}

// adminListTransfers：管理端订单列表（sender_id / receiver_id 可选过滤，均为空时为全量倒序）。
func (s *Server) adminListTransfers(ctx context.Context, senderID, receiverID string, limit, offset int) ([]TransferOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	q := `SELECT ` + transferOrderColumns + ` FROM transfer_order WHERE ($1='' OR sender_id=$1) AND ($2='' OR receiver_id=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`
	rows, err := s.db.QueryContext(ctx, q, senderID, receiverID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TransferOrder{}
	for rows.Next() {
		var o TransferOrder
		if err := rows.Scan(&o.OrderID, &o.SenderID, &o.ReceiverID, &o.AmountCents, &o.Remark, &o.Status,
			&o.FailureCode, &o.TxnID, &o.IdempotencyKey, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---- HTTP ----

func transferIDFromPath(r *http.Request, prefix string) string {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) >= 1 && parts[0] != "" {
		return parts[0]
	}
	return ""
}

func transferErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTransferNotFound):
		fail(w, http.StatusNotFound, "TRANSFER_NOT_FOUND", err.Error())
	case errors.Is(err, errTransferForbidden):
		fail(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, errTransferSelf):
		fail(w, http.StatusBadRequest, "TRANSFER_SELF", err.Error())
	case errors.Is(err, errTransferPlatform):
		fail(w, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, errTransferInsufficient):
		fail(w, http.StatusBadRequest, "INSUFFICIENT_BALANCE", err.Error())
	case errors.Is(err, errTransferReceiverFrozen):
		fail(w, http.StatusBadRequest, "RECEIVER_WALLET_ABNORMAL", "receiver wallet frozen")
	case errors.Is(err, errTransferReceiverClosed):
		fail(w, http.StatusBadRequest, "RECEIVER_WALLET_ABNORMAL", "receiver wallet closed")
	case errors.Is(err, errBusy):
		fail(w, http.StatusTooManyRequests, "SYSTEM_BUSY", "system busy, please retry")
	default:
		msg := err.Error()
		if strings.Contains(msg, "insufficient") || strings.Contains(msg, "not found") || strings.Contains(msg, "not normal") {
			fail(w, http.StatusBadRequest, "BUSINESS_ERROR", msg)
			return
		}
		if strings.Contains(msg, "invalid") || strings.Contains(msg, "must be") || strings.Contains(msg, "too long") || strings.Contains(msg, "role must be") {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", msg)
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", msg)
	}
}

type transferSendReq struct {
	ReceiverID     string `json:"receiver_id"`
	AmountCents    int64  `json:"amount_cents"`
	Remark         string `json:"remark"`
	IdempotencyKey string `json:"idempotency_key"`
	TxPassword     string `json:"tx_password"`
}

func (s *Server) hSendTransfer(w http.ResponseWriter, r *http.Request) {
	var req transferSendReq
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
	// Phase 7 风控：单笔上限校验（默认不限额；超限在任何资金变动前拒绝，不产生订单行，
	// 与参数错误同级；红包路径的限额接入留 Phase 8/9 统一做，文档写清）。
	if lim, err := s.getSingleLimit(r.Context()); err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	} else if lim != nil && req.AmountCents > *lim {
		fail(w, http.StatusBadRequest, "AMOUNT_LIMIT_EXCEEDED", "amount exceeds single transaction limit")
		return
	}
	var ord *TransferOrder
	err = s.withUserLock(r.Context(), uid, func() error {
		var err error
		ord, err = s.sendTransfer(r.Context(), uid, req.ReceiverID, req.AmountCents, req.Remark, req.IdempotencyKey)
		return err
	})
	if err != nil {
		// 结算失败写入失败审计行（不落账；写入失败不改变已确定的失败结果）。
		if code, ok := isSettlementFailure(err); ok {
			s.recordFailedTransfer(r.Context(), uid, req.ReceiverID, req.AmountCents, req.Remark, code)
		}
		transferErr(w, err)
		return
	}
	ok(w, ord)
}

func (s *Server) hGetTransfer(w http.ResponseWriter, r *http.Request) {
	id := transferIDFromPath(r, "/v1/transfers/")
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	if id == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown transfer path")
		return
	}
	ord, err := s.getTransfer(r.Context(), uid, id)
	if err != nil {
		transferErr(w, err)
		return
	}
	ok(w, ord)
}

func (s *Server) hListTransfers(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	role := r.URL.Query().Get("role")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.listTransfers(r.Context(), uid, role, limit, offset)
	if err != nil {
		transferErr(w, err)
		return
	}
	ok(w, list)
}

func (s *Server) hAdminGetTransfer(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	id := transferIDFromPath(r, "/v1/admin/transfers/")
	if id == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown transfer path")
		return
	}
	d, err := s.adminTransferDetail(r.Context(), id)
	if err != nil {
		transferErr(w, err)
		return
	}
	ok(w, d)
}

func (s *Server) hAdminListTransfers(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	list, err := s.adminListTransfers(r.Context(), r.URL.Query().Get("sender_id"), r.URL.Query().Get("receiver_id"), limit, offset)
	if err != nil {
		transferErr(w, err)
		return
	}
	ok(w, list)
}
