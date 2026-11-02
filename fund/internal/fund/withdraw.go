package fund

// Phase 9：人民币提现（7 态订单全流程）。
// 资金模型（CNY、整数分、双重记账、流水 append-only，与 Phase 1–8 同口径）：
//   提交：用户可用 DR / 用户冻结 CR（金额 + 手续费，同一事务建 wallet_freeze 行与订单行，状态 0 待审核）。
//   审核拒绝（0 -> 2）：用户冻结 DR / 用户可用 CR（金额 + 手续费，全额解冻，同一事务改状态）。
//   审核通过（0 -> 1，双人复核，沿用 Phase 7）：只改订单状态，不产生任何资金变动、不打款。
//   处理中（1 -> 3）：只改订单状态（财务已接单、尚未打款），不产生资金变动。
//   人工打款确认（3 -> 4）：用户冻结 DR / 平台结算 CR（金额 + 手续费，冻结扣减，分录带符号合计为 0；
//     平台结算户为账务对手方：出账现金侧；手续费的收入确认不在本账务口径内，见 API 文档 §14 说明）。
//   渠道处理失败（3 -> 5）：只改订单状态与失败原因，不产生资金变动（款仍在冻结中）。
//   失败后退款（5 -> 6）：用户冻结 DR / 用户可用 CR（金额 + 手续费，全额解冻，同一事务改状态）。
// 合法流转只有：0->1、0->2、1->3、3->4、3->5、5->6；其余流转一律拒绝（新写入口回 409 INVALID_TRANSITION；
// Phase 7 已有 approve/reject 入口保持原 400 口径，避免破坏 Test90 的既有断言）。
// 人工打款流程（无商户资质时的唯一打款路径，文档与接口注释中明确写出）：管理端审核通过后，
// 财务人员用对公账户/企业网银手工转账，打款后在管理端回填凭证（银行/渠道回单号、打款时间、操作员）
// 并确认成功；严禁个人号垫付为常规流程。自动打款渠道只留接口骨架（恒回 501，见 hWithdrawAutoPayout），
// 不连真实渠道端点、不写真实商户密钥，签约后按 Phase 8 验签器模式接入（签约前置清单见 API 文档 §14.5）。
// 守卫顺序（提交时）：准入引导优先（非 ok 只返回引导、不扣款、不建单）-> 验交易密码 ->
// 提现方式归属（必须是本人正常方式，否则 404，不透露他人方式存在性）-> 金额上下限与频率限制 ->
// 余额校验（可用 >= 金额 + 手续费）。任一步失败均不产生资金变动、不建成功订单行。
// 手续费：fee = fixed_fee_cents + floor(amount_cents * rate_bp / 10000)，配置见 fund_withdraw_config
// （管理端读写，默认固定 0、费率 0）；actual = amount - fee；actual <= 0 时拒绝提交。
// 频率限制统计口径（数据库当日）：同一用户当日已提交且未被拒绝的提现单（status <> 2），
// 含待审核/已通过/处理中/成功/失败/已退款；被拒绝的单不计入（款已全额解冻，不是一次真实出金尝试）。
// 幂等：同一用户同一 client_idempotency_key 只建一单（空键除外；先查后插 + 唯一冲突回查）；
// 被拒绝订单的幂等键同样被占用——被拒后重提须换新幂等键（与 Phase 8 充值创建口径一致）。

import (
	"context"
	"database/sql"
	"encoding/json"
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
	WithdrawPending    = 0 // 待审核
	WithdrawApproved   = 1 // 已通过（仅审核通过，未打款）
	WithdrawRejected   = 2 // 已拒绝（已全额解冻）
	WithdrawProcessing = 3 // 处理中（财务已接单，尚未打款）
	WithdrawSuccess    = 4 // 成功（人工打款确认，冻结已扣减）
	WithdrawFailed     = 5 // 失败（渠道处理失败，款仍在冻结中）
	WithdrawRefunded   = 6 // 已退款（失败后全额解冻）
)

var (
	errWithdrawNotFound         = errors.New("withdraw order not found")
	errWithdrawForbidden        = errors.New("withdraw order does not belong to viewer")
	errWithdrawMethodNotFound   = errors.New("withdraw method not found")
	errWithdrawMethodInactive   = errors.New("withdraw method is no longer active")
	errWithdrawInsufficient     = errors.New("insufficient available balance for withdrawal")
	errWithdrawInvalidTrans     = errors.New("invalid withdraw order transition")
	errWithdrawFreezeMismatch   = errors.New("withdraw freeze remaining does not match order total")
	errWithdrawFeeExceedsAmount = errors.New("fee exceeds or equals withdrawal amount")
)

// ---- 手续费与限额配置（fund_withdraw_config，单行 key='default'） ----

type WithdrawConfig struct {
	FixedFeeCents  int64     `json:"fixed_fee_cents"`  // 固定费用（分，默认 0）
	RateBp         int64     `json:"rate_bp"`          // 费率（万分比，默认 0；100 = 1%）
	MinAmountCents *int64    `json:"min_amount_cents"` // 单笔下限（分，null = 除 >0 外不限）
	SingleMaxCents *int64    `json:"single_max_cents"` // 单笔上限（分，null = 不限额）
	DailyMaxCents  *int64    `json:"daily_max_cents"`  // 单日金额上限（分，null = 不限额）
	DailyCountMax  *int      `json:"daily_count_max"`  // 单日笔数上限（null = 不限额；运营建议值 5 笔）
	UpdatedBy      *string   `json:"updated_by"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func scanWithdrawConfig(row interface{ Scan(...any) error }) (*WithdrawConfig, error) {
	var c WithdrawConfig
	if err := row.Scan(&c.FixedFeeCents, &c.RateBp, &c.MinAmountCents, &c.SingleMaxCents,
		&c.DailyMaxCents, &c.DailyCountMax, &c.UpdatedBy, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *Server) getWithdrawConfig(ctx context.Context) (*WithdrawConfig, error) {
	return scanWithdrawConfig(s.db.QueryRowContext(ctx,
		`SELECT fixed_fee_cents, rate_bp, min_amount_cents, single_max_cents, daily_max_cents, daily_count_max, updated_by, updated_at FROM fund_withdraw_config WHERE config_key='default'`))
}

// calcWithdrawFee：固定费用 + 费率向下取整。rate_bp 已由配置 CHECK 限定在 [0,10000]。
func calcWithdrawFee(cfg *WithdrawConfig, amountCents int64) int64 {
	return cfg.FixedFeeCents + amountCents*cfg.RateBp/10000
}

// updateWithdrawConfig：按字段部分更新（调用方传入 nil 表示该字段保持原值；
// 限额类字段传显式 null 时由 HTTP 层转为 nil 指针的“清为空”语义，见 hAdminWithdrawConfig）。
// fee 类字段（fixed/rate）不接受 null（缺省 = 保持原值）。
func (s *Server) updateWithdrawConfig(ctx context.Context, adminID string, fixed, rate *int64, min, single, daily *int64, count *int, clearMin, clearSingle, clearDaily, clearCount bool) (*WithdrawConfig, error) {
	cur, err := s.getWithdrawConfig(ctx)
	if err != nil {
		return nil, err
	}
	if fixed != nil {
		if *fixed < 0 {
			return nil, errors.New("fixed_fee_cents must be >= 0")
		}
		cur.FixedFeeCents = *fixed
	}
	if rate != nil {
		if *rate < 0 || *rate > 10000 {
			return nil, errors.New("rate_bp must be in [0, 10000]")
		}
		cur.RateBp = *rate
	}
	if clearMin {
		cur.MinAmountCents = nil
	} else if min != nil {
		if *min <= 0 {
			return nil, errors.New("min_amount_cents must be > 0 or null for unlimited")
		}
		cur.MinAmountCents = min
	}
	if clearSingle {
		cur.SingleMaxCents = nil
	} else if single != nil {
		if *single < 0 {
			return nil, errors.New("single_max_cents must be >= 0 or null for unlimited")
		}
		cur.SingleMaxCents = single
	}
	if clearDaily {
		cur.DailyMaxCents = nil
	} else if daily != nil {
		if *daily < 0 {
			return nil, errors.New("daily_max_cents must be >= 0 or null for unlimited")
		}
		cur.DailyMaxCents = daily
	}
	if clearCount {
		cur.DailyCountMax = nil
	} else if count != nil {
		if *count <= 0 {
			return nil, errors.New("daily_count_max must be > 0 or null for unlimited")
		}
		cur.DailyCountMax = count
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO fund_withdraw_config (config_key, fixed_fee_cents, rate_bp, min_amount_cents, single_max_cents, daily_max_cents, daily_count_max, updated_by) VALUES ('default',$1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (config_key) DO UPDATE SET fixed_fee_cents=EXCLUDED.fixed_fee_cents, rate_bp=EXCLUDED.rate_bp, min_amount_cents=EXCLUDED.min_amount_cents, single_max_cents=EXCLUDED.single_max_cents, daily_max_cents=EXCLUDED.daily_max_cents, daily_count_max=EXCLUDED.daily_count_max, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		cur.FixedFeeCents, cur.RateBp, nullInt64(cur.MinAmountCents), nullInt64(cur.SingleMaxCents),
		nullInt64(cur.DailyMaxCents), nullInt(count), adminID); err != nil {
		return nil, err
	}
	s.auditStandalone(ctx, "", adminID, "admin_withdraw_config_update",
		fmt.Sprintf("withdraw config fixed=%d rate_bp=%d", cur.FixedFeeCents, cur.RateBp), nil)
	return s.getWithdrawConfig(ctx)
}

func nullInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

// ---- 订单读写 ----

// WithdrawOrder：用户侧提现订单视图（管理端用 WithdrawOrderView，含审核与打款字段）。
type WithdrawOrder struct {
	OrderID          string     `json:"order_id"`
	UserID           string     `json:"user_id"`
	AmountCents      int64      `json:"amount_cents"`
	FeeCents         int64      `json:"fee_cents"`
	ActualCents      int64      `json:"actual_cents"`
	Status           int        `json:"status"`
	WithdrawMethodID *string    `json:"withdraw_method_id"`
	PayoutProofNo    *string    `json:"payout_proof_no,omitempty"`
	PayoutAt         *time.Time `json:"payout_at,omitempty"`
	FailureReason    *string    `json:"failure_reason,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

const withdrawOrderUserColumns = `order_id, user_id, amount_cents, fee_cents, actual_cents, status, withdraw_method_id, payout_proof_no, payout_at, failure_reason, created_at, updated_at`

func scanWithdrawOrderUser(row interface{ Scan(...any) error }) (*WithdrawOrder, error) {
	var o WithdrawOrder
	if err := row.Scan(&o.OrderID, &o.UserID, &o.AmountCents, &o.FeeCents, &o.ActualCents, &o.Status,
		&o.WithdrawMethodID, &o.PayoutProofNo, &o.PayoutAt, &o.FailureReason, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	return &o, nil
}

func loadWithdrawByUserIdem(ctx context.Context, q rowQuerier, userID, idemKey string) (*WithdrawOrder, error) {
	row := q.QueryRowContext(ctx, `SELECT `+withdrawOrderUserColumns+` FROM wallet_order WHERE user_id=$1 AND client_idempotency_key=$2 AND order_type=2`, userID, idemKey)
	o, err := scanWithdrawOrderUser(row)
	if err == sql.ErrNoRows {
		return nil, sql.ErrNoRows
	}
	return o, err
}

// submitWithdraw：提交提现。同一事务：幂等回查 -> 方式归属校验（行锁）-> 钱包行锁 ->
// 钱包状态与余额校验 -> 当日限额校验（钱包行锁已串行化同一用户提交）-> 冻结记账 ->
// 冻结行 + 订单行 + 审计。任一步失败整笔回滚（无 txn、无分录、无订单行、余额不动）。
func (s *Server) submitWithdraw(ctx context.Context, userID, methodID string, amountCents int64, idemKey string, cfg *WithdrawConfig) (*WithdrawOrder, error) {
	if !validUserID(methodID) {
		return nil, errors.New("invalid withdraw_method_id")
	}
	if amountCents <= 0 {
		return nil, errors.New("amount_cents must be > 0")
	}
	fee := calcWithdrawFee(cfg, amountCents)
	actual := amountCents - fee
	if actual <= 0 {
		return nil, errWithdrawFeeExceedsAmount
	}
	if cfg.MinAmountCents != nil && amountCents < *cfg.MinAmountCents {
		return nil, fmt.Errorf("amount below minimum %d", *cfg.MinAmountCents)
	}
	if cfg.SingleMaxCents != nil && amountCents > *cfg.SingleMaxCents {
		return nil, errors.New("amount exceeds single transaction limit")
	}
	total := amountCents + fee

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if idemKey != "" {
		if existing, err := loadWithdrawByUserIdem(ctx, tx, userID, idemKey); err == nil {
			_ = tx.Rollback()
			return existing, nil
		} else if err != sql.ErrNoRows {
			return nil, err
		}
	}

	// 方式归属：必须是本人正常方式；不存在或非本人或已解绑一律视为不存在（不透露他人方式存在性）。
	var methodRow string
	if err := tx.QueryRowContext(ctx, `SELECT method_id FROM wallet_withdraw_method WHERE method_id=$1 AND user_id=$2 AND status=0 FOR UPDATE`, methodID, userID).Scan(&methodRow); err != nil {
		if err == sql.ErrNoRows {
			return nil, errWithdrawMethodNotFound
		}
		return nil, err
	}

	// 钱包不存在时在同一事务内自动建钱包（事务回滚时该行一并消失；准入检查在 HTTP 层已先行，
	// 正常流程下钱包必然存在，此处仅为防御性兜底）。
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_account (user_id) VALUES ($1) ON CONFLICT (user_id) DO NOTHING`, userID); err != nil {
		return nil, err
	}
	var st accountState
	if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, userID).
		Scan(&st.balance, &st.frozen, &st.version, &st.status); err != nil {
		return nil, err
	}
	if st.status != 0 {
		return nil, fmt.Errorf("wallet not normal: %s", userID)
	}
	balAfter, frzAfter := st.balance-total, st.frozen+total
	if balAfter < 0 {
		return nil, errWithdrawInsufficient
	}

	// 当日限额：钱包行锁已串行化同一用户提交，并发提交不会双双通过同一额度。
	if cfg.DailyMaxCents != nil || cfg.DailyCountMax != nil {
		var cnt int
		var sum int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*), COALESCE(SUM(amount_cents),0) FROM wallet_order WHERE user_id=$1 AND order_type=2 AND status<>2 AND created_at >= CURRENT_DATE`,
			userID).Scan(&cnt, &sum); err != nil {
			return nil, err
		}
		if cfg.DailyCountMax != nil && cnt+1 > *cfg.DailyCountMax {
			return nil, errors.New("daily withdrawal count limit exceeded")
		}
		if cfg.DailyMaxCents != nil && sum+amountCents > *cfg.DailyMaxCents {
			return nil, errors.New("daily withdrawal amount limit exceeded")
		}
	}

	txnID := newID("txn_")
	freezeID := newID("frz_")
	orderID := newID("wd_")
	now := time.Now()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'withdraw',$4,1,$5)`,
		txnID, userID, TxnWithdrawSubmit, orderID, "wd-submit-"+orderID); err != nil {
		return nil, err
	}
	type le struct {
		acctType int
		dir      int
		oppAcc   string
	}
	for _, e := range []le{{AcctAvailable, DirDebit, "frozen"}, {AcctFrozen, DirCredit, "available"}} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, userID, e.acctType, e.dir, total, balAfter, frzAfter, userID, e.oppAcc); err != nil {
			return nil, err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`, balAfter, frzAfter, userID, st.version)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, fmt.Errorf("optimistic lock failed for %s", userID)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO wallet_freeze (freeze_id, user_id, txn_id, order_id, amount_cents, remaining_cents, status) VALUES ($1,$2,$3,$4,$5,$5,0)`, freezeID, userID, txnID, orderID, total); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_order (order_id, user_id, order_type, amount_cents, fee_cents, actual_cents, status, withdraw_method_id, freeze_id, client_idempotency_key) VALUES ($1,$2,2,$3,$4,$5,0,$6,$7,$8)`,
		orderID, userID, amountCents, fee, actual, methodID, freezeID, nullStr(idemKey)); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			_ = tx.Rollback()
			existing, err2 := loadWithdrawByUserIdem(ctx, s.db, userID, idemKey)
			if err2 != nil {
				return nil, err2
			}
			return existing, nil
		}
		return nil, err
	}
	if err := insertAudit(ctx, tx, userID, userID, "withdraw_submit", fmt.Sprintf("withdraw %s %d cents fee %d", orderID, amountCents, fee), "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if idemKey != "" && isUniqueViolation(err) {
			existing, err2 := loadWithdrawByUserIdem(ctx, s.db, userID, idemKey)
			if err2 == nil {
				return existing, nil
			}
		}
		return nil, err
	}
	mID := methodID
	return &WithdrawOrder{OrderID: orderID, UserID: userID, AmountCents: amountCents, FeeCents: fee,
		ActualCents: actual, Status: WithdrawPending, WithdrawMethodID: &mID, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *Server) getWithdraw(ctx context.Context, viewerID, orderID string) (*WithdrawOrder, error) {
	o, err := scanWithdrawOrderUser(s.db.QueryRowContext(ctx,
		`SELECT `+withdrawOrderUserColumns+` FROM wallet_order WHERE order_id=$1 AND order_type=2`, orderID))
	if err == sql.ErrNoRows {
		return nil, errWithdrawNotFound
	}
	if err != nil {
		return nil, err
	}
	if o.UserID != viewerID {
		return nil, errWithdrawForbidden
	}
	return o, nil
}

func (s *Server) listWithdraws(ctx context.Context, userID string, status, limit, offset int) ([]WithdrawOrder, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+withdrawOrderUserColumns+` FROM wallet_order WHERE user_id=$1 AND order_type=2 AND ($2<0 OR status=$2) ORDER BY created_at DESC LIMIT $3 OFFSET $4`,
		userID, status, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WithdrawOrder{}
	for rows.Next() {
		o, err := scanWithdrawOrderUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// ---- 管理端流转（全部在 DB 事务内完成订单状态 + 账务 + 冻结行 + 审计；任一步失败整笔回滚） ----

type withdrawTxnState struct {
	order     *WithdrawOrderView
	freezeID  string
	remaining int64
	userBal   int64
	userFrz   int64
	userVer   int64
	platBal   int64
	platFrz   int64
	platVer   int64
}

// loadWithdrawForUpdate：订单行 + 冻结行 + 用户钱包行 + 平台钱包行全部 FOR UPDATE。
// 锁顺序：订单 -> 冻结 -> 钱包（按 user_id 排序，用户与平台），与 Phase 7 调整单执行口径一致。
func (s *Server) loadWithdrawForUpdate(ctx context.Context, tx *sql.Tx, orderID string) (*withdrawTxnState, error) {
	o, err := scanWithdrawOrder(tx.QueryRowContext(ctx,
		`SELECT `+withdrawOrderColumns+` FROM wallet_order WHERE order_id=$1 AND order_type=2 FOR UPDATE`, orderID))
	if err == sql.ErrNoRows {
		return nil, errWithdrawNotFound
	}
	if err != nil {
		return nil, err
	}
	if o.FreezeID == nil {
		return nil, errors.New("withdraw order has no freeze record")
	}
	var remaining int64
	var freezeUser string
	if err := tx.QueryRowContext(ctx, `SELECT user_id, remaining_cents FROM wallet_freeze WHERE freeze_id=$1 FOR UPDATE`, *o.FreezeID).
		Scan(&freezeUser, &remaining); err != nil {
		return nil, err
	}
	if freezeUser != o.UserID {
		return nil, errors.New("withdraw freeze owner mismatch")
	}
	users := []string{o.UserID, PlatformUserID}
	sort.Strings(users)
	states := map[string]*accountState{}
	for _, u := range users {
		var st accountState
		if err := tx.QueryRowContext(ctx, `SELECT balance_cents, frozen_cents, version, status FROM wallet_account WHERE user_id=$1 FOR UPDATE`, u).
			Scan(&st.balance, &st.frozen, &st.version, &st.status); err != nil {
			return nil, err
		}
		states[u] = &st
	}
	us, ps := states[o.UserID], states[PlatformUserID]
	return &withdrawTxnState{order: o, freezeID: *o.FreezeID, remaining: remaining,
		userBal: us.balance, userFrz: us.frozen, userVer: us.version,
		platBal: ps.balance, platFrz: ps.frozen, platVer: ps.version}, nil
}

// moveFrozen：冻结金额变动的通用记账。toAvailable=true 时为解冻（冻结 DR / 可用 CR）；
// false 时为扣减（冻结 DR / 平台结算 CR）。total 必须等于订单金额 + 手续费，且冻结剩余必须等于 total，
// 否则整笔失败（冻结行被篡改的防护，与红包 Test34/47/64 同口径）。
func (s *Server) moveFrozen(ctx context.Context, tx *sql.Tx, ws *withdrawTxnState, txnType int, idemSuffix string, toAvailable bool) (string, error) {
	total := ws.order.AmountCents + ws.order.FeeCents
	if ws.remaining != total {
		return "", errWithdrawFreezeMismatch
	}
	if ws.userFrz < total {
		return "", errWithdrawFreezeMismatch
	}
	txnID := newID("txn_")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_txn (txn_id, user_id, type, ref_type, ref_id, status, idempotency_key) VALUES ($1,$2,$3,'withdraw',$4,1,$5)`,
		txnID, ws.order.UserID, txnType, ws.order.OrderID, idemSuffix+ws.order.OrderID); err != nil {
		return "", err
	}
	userBalAfter, userFrzAfter := ws.userBal, ws.userFrz-total
	platBalAfter := ws.platBal
	var entries []struct {
		uid    string
		acct   int
		dir    int
		amount int64
		bal    int64
		frz    int64
		oppUID string
		oppAcc string
	}
	if toAvailable {
		userBalAfter = ws.userBal + total
		entries = []struct {
			uid    string
			acct   int
			dir    int
			amount int64
			bal    int64
			frz    int64
			oppUID string
			oppAcc string
		}{
			{ws.order.UserID, AcctFrozen, DirDebit, total, userBalAfter, userFrzAfter, ws.order.UserID, "available"},
			{ws.order.UserID, AcctAvailable, DirCredit, total, userBalAfter, userFrzAfter, ws.order.UserID, "frozen"},
		}
	} else {
		// 平台结算户为资产性质：贷方减少其余额（deltaFor 取反），与 Phase 7 减额调整同向。
		platBalAfter = ws.platBal - total
		if platBalAfter < 0 {
			return "", fmt.Errorf("platform settlement balance insufficient")
		}
		entries = []struct {
			uid    string
			acct   int
			dir    int
			amount int64
			bal    int64
			frz    int64
			oppUID string
			oppAcc string
		}{
			{ws.order.UserID, AcctFrozen, DirDebit, total, userBalAfter, userFrzAfter, PlatformUserID, "settlement"},
			{PlatformUserID, AcctAvailable, DirCredit, total, platBalAfter, ws.platFrz, ws.order.UserID, "frozen"},
		}
	}
	for _, e := range entries {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO wallet_ledger (txn_id, user_id, account_type, direction, amount_cents, balance_after_cents, frozen_after_cents, opposite_user_id, opposite_account)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			txnID, e.uid, e.acct, e.dir, e.amount, e.bal, e.frz, e.oppUID, e.oppAcc); err != nil {
			return "", err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`,
		userBalAfter, userFrzAfter, ws.order.UserID, ws.userVer)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", fmt.Errorf("optimistic lock failed for %s", ws.order.UserID)
	}
	if !toAvailable {
		res, err := tx.ExecContext(ctx, `UPDATE wallet_account SET balance_cents=$1, frozen_cents=$2, version=version+1, updated_at=now() WHERE user_id=$3 AND version=$4`,
			platBalAfter, ws.platFrz, PlatformUserID, ws.platVer)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return "", fmt.Errorf("optimistic lock failed for %s", PlatformUserID)
		}
	}
	// 冻结行终态：解冻 status=1，扣减 status=2（与 wallet_freeze 既有语义一致）。
	frzStatus := 1
	if !toAvailable {
		frzStatus = 2
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_freeze SET remaining_cents=0, status=$1, updated_at=now() WHERE freeze_id=$2`, frzStatus, ws.freezeID); err != nil {
		return "", err
	}
	return txnID, nil
}

// rejectWithdrawWithUnfreeze：审核拒绝（0 -> 2）带全额解冻。Phase 7 种子单无 freeze_id 时
// 不走本函数（admin.go 的旧路径只改状态，Test90 断言保持不变）。
func (s *Server) rejectWithdrawWithUnfreeze(ctx context.Context, orderID, adminID, note string) error {
	if utf8.RuneCountInString(note) > 500 {
		return errors.New("review_note must be <= 500 chars")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ws, err := s.loadWithdrawForUpdate(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if ws.order.Status != WithdrawPending {
		return errWithdrawOrderNotPend
	}
	if _, err := s.moveFrozen(ctx, tx, ws, TxnWithdrawReject, "wd-reject-", true); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE wallet_order SET status=2, rejected_by_admin_id=$1, review_note=$2, reviewed_at=now(), updated_at=now() WHERE order_id=$3 AND status=0`,
		adminID, nullStr(note), orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errWithdrawOrderNotPend
	}
	if err := insertAudit(ctx, tx, ws.order.UserID, adminID, "admin_withdraw_review_reject", "withdraw rejected order_id="+orderID, "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

// startWithdrawProcessing：审核通过（1）-> 处理中（3）。只改状态，不产生资金变动。
func (s *Server) startWithdrawProcessing(ctx context.Context, orderID, adminID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE wallet_order SET status=3, updated_at=now() WHERE order_id=$1 AND order_type=2 AND status=1`, orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errWithdrawInvalidTrans
	}
	o, err := s.getWithdrawOrder(ctx, orderID)
	if err != nil {
		return err
	}
	s.auditStandalone(ctx, o.UserID, adminID, "admin_withdraw_processing", "withdraw processing order_id="+orderID, nil)
	return nil
}

// confirmWithdrawPayout：人工打款确认（3 -> 4）。要求回填打款凭证：银行/渠道回单号必填
// （≤256 字符）、打款时间缺省为确认时刻、操作员为确认管理员；严禁个人号垫付为常规流程
// （见 API 文档 §14.3：财务须用对公账户/企业网银手工转账后回填）。
func (s *Server) confirmWithdrawPayout(ctx context.Context, orderID, adminID, proofNo string, payoutAt *time.Time, note string) error {
	if strings.TrimSpace(proofNo) == "" {
		return errors.New("payout_proof_no is required")
	}
	if utf8.RuneCountInString(proofNo) > 256 {
		return errors.New("payout_proof_no too long, max 256 chars")
	}
	if utf8.RuneCountInString(note) > 500 {
		return errors.New("payout_note must be <= 500 chars")
	}
	if payoutAt == nil {
		now := time.Now()
		payoutAt = &now
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ws, err := s.loadWithdrawForUpdate(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if ws.order.Status != WithdrawProcessing {
		return errWithdrawInvalidTrans
	}
	// 打款前复核方式仍有效：用户在提现流程中解绑方式（计划 §9 验收场景 11 的变体）时，
	// 不得向已失效账户打款；操作员应走失败（3 -> 5）再退款（5 -> 6）逃生通道。
	if ws.order.WithdrawMethodID != nil {
		var mStatus int
		if err := tx.QueryRowContext(ctx, `SELECT status FROM wallet_withdraw_method WHERE method_id=$1 AND user_id=$2`, *ws.order.WithdrawMethodID, ws.order.UserID).Scan(&mStatus); err != nil {
			if err == sql.ErrNoRows {
				return errWithdrawMethodInactive
			}
			return err
		}
		if mStatus != 0 {
			return errWithdrawMethodInactive
		}
	}
	if _, err := s.moveFrozen(ctx, tx, ws, TxnWithdrawPayout, "wd-payout-", false); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE wallet_order SET status=4, payout_proof_no=$1, payout_at=$2, payout_operator_id=$3, payout_note=$4, updated_at=now() WHERE order_id=$5 AND status=3`,
		proofNo, *payoutAt, adminID, nullStr(note), orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errWithdrawInvalidTrans
	}
	if err := insertAudit(ctx, tx, ws.order.UserID, adminID, "admin_withdraw_payout", "withdraw payout order_id="+orderID, "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

// failWithdrawPayout：渠道处理失败（3 -> 5）。只改状态与失败原因，不产生资金变动（款仍在冻结中）。
func (s *Server) failWithdrawPayout(ctx context.Context, orderID, adminID, reason string) error {
	if utf8.RuneCountInString(reason) > 500 {
		return errors.New("failure_reason must be <= 500 chars")
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE wallet_order SET status=5, failure_reason=$1, updated_at=now() WHERE order_id=$2 AND order_type=2 AND status=3`,
		nullStr(reason), orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errWithdrawInvalidTrans
	}
	o, err := s.getWithdrawOrder(ctx, orderID)
	if err != nil {
		return err
	}
	s.auditStandalone(ctx, o.UserID, adminID, "admin_withdraw_failed", "withdraw failed order_id="+orderID, nil)
	return nil
}

// refundWithdraw：失败后退款（5 -> 6），全额解冻。
func (s *Server) refundWithdraw(ctx context.Context, orderID, adminID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ws, err := s.loadWithdrawForUpdate(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if ws.order.Status != WithdrawFailed {
		return errWithdrawInvalidTrans
	}
	if _, err := s.moveFrozen(ctx, tx, ws, TxnWithdrawRefund, "wd-refund-", true); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE wallet_order SET status=6, updated_at=now() WHERE order_id=$1 AND status=5`, orderID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errWithdrawInvalidTrans
	}
	if err := insertAudit(ctx, tx, ws.order.UserID, adminID, "admin_withdraw_refunded", "withdraw refunded order_id="+orderID, "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- HTTP 层 ----

type withdrawSubmitReq struct {
	WithdrawMethodID string `json:"withdraw_method_id"`
	AmountCents      int64  `json:"amount_cents"`
	TxPassword       string `json:"tx_password"`
	IdempotencyKey   string `json:"idempotency_key"`
}

func withdrawErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWithdrawNotFound) || errors.Is(err, errWithdrawForbidden):
		fail(w, http.StatusNotFound, "WITHDRAW_NOT_FOUND", "withdraw order not found")
	case errors.Is(err, errWithdrawMethodNotFound):
		fail(w, http.StatusNotFound, "WITHDRAW_METHOD_NOT_FOUND", err.Error())
	case errors.Is(err, errWithdrawMethodInactive):
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
	case errors.Is(err, errWithdrawInsufficient):
		fail(w, http.StatusBadRequest, "INSUFFICIENT_BALANCE", err.Error())
	case errors.Is(err, errWithdrawFeeExceedsAmount):
		fail(w, http.StatusBadRequest, "FEE_EXCEEDS_AMOUNT", err.Error())
	case errors.Is(err, errWithdrawInvalidTrans):
		fail(w, http.StatusConflict, "INVALID_TRANSITION", err.Error())
	case errors.Is(err, errBusy):
		fail(w, http.StatusTooManyRequests, "SYSTEM_BUSY", "system busy, please retry")
	default:
		msg := err.Error()
		if strings.Contains(msg, "exceeds single") {
			fail(w, http.StatusBadRequest, "AMOUNT_LIMIT_EXCEEDED", msg)
			return
		}
		if strings.Contains(msg, "limit exceeded") || strings.Contains(msg, "below minimum") {
			fail(w, http.StatusBadRequest, "AMOUNT_LIMIT_EXCEEDED", msg)
			return
		}
		if strings.Contains(msg, "invalid") || strings.Contains(msg, "must be") || strings.Contains(msg, "required") || strings.Contains(msg, "too long") || strings.Contains(msg, "not normal") || strings.Contains(msg, "mismatch") || strings.Contains(msg, "insufficient") {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", msg)
			return
		}
		fail(w, http.StatusInternalServerError, "INTERNAL", msg)
	}
}

func (s *Server) hSubmitWithdraw(w http.ResponseWriter, r *http.Request) {
	var req withdrawSubmitReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	// 守卫顺序：准入引导优先（未完成时只返回引导、绝不扣款、不建单），再验交易密码。
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
	cfg, err := s.getWithdrawConfig(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if req.AmountCents <= 0 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "amount_cents must be > 0")
		return
	}
	var ord *WithdrawOrder
	err = s.withUserLock(r.Context(), uid, func() error {
		var err error
		ord, err = s.submitWithdraw(r.Context(), uid, req.WithdrawMethodID, req.AmountCents, req.IdempotencyKey, cfg)
		return err
	})
	if err != nil {
		withdrawErr(w, err)
		return
	}
	ok(w, ord)
}

func withdrawIDFromPath(r *http.Request, prefix string) string {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.Split(rest, "/")
	if len(parts) >= 1 && parts[0] != "" {
		return parts[0]
	}
	return ""
}

func (s *Server) hGetWithdraw(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	o, err := s.getWithdraw(r.Context(), uid, withdrawIDFromPath(r, "/v1/withdrawals/"))
	if err != nil {
		withdrawErr(w, err)
		return
	}
	ok(w, o)
}

func (s *Server) hListWithdraws(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	status := -1
	if v := r.URL.Query().Get("status"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 6 {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid status, must be 0-6")
			return
		}
		status = n
	}
	limit, offset := pageParams(r)
	list, err := s.listWithdraws(r.Context(), uid, status, limit, offset)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

func pageParams(r *http.Request) (int, int) {
	limit, offset := 50, 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}
	return limit, offset
}

// hWithdrawQuote：手续费试算（展示用，无资金变动，不设准入/交易密码门禁；
// 试算结果不作为提交依据，提交时服务端按当时配置重算并校验）。
func (s *Server) hWithdrawQuote(w http.ResponseWriter, r *http.Request) {
	_, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	amount, err := strconv.ParseInt(r.URL.Query().Get("amount_cents"), 10, 64)
	if err != nil || amount <= 0 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "amount_cents must be > 0")
		return
	}
	cfg, err := s.getWithdrawConfig(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	fee := calcWithdrawFee(cfg, amount)
	ok(w, map[string]any{
		"amount_cents": amount, "fee_cents": fee, "actual_cents": amount - fee,
		"min_amount_cents": cfg.MinAmountCents, "single_max_cents": cfg.SingleMaxCents,
		"daily_max_cents": cfg.DailyMaxCents, "daily_count_max": cfg.DailyCountMax,
	})
}

func (s *Server) hAdminWithdrawConfig(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	if r.Method == http.MethodGet {
		cfg, err := s.getWithdrawConfig(r.Context())
		if err != nil {
			fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		ok(w, cfg)
		return
	}
	// PUT：按字段部分更新；缺省字段保持原值，限额类字段显式 null 改为不限额（与 Phase 7 限额配置同口径）。
	body, _ := readAll(r)
	var raw map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &raw); err != nil {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
			return
		}
	}
	var fixed, rate, min, single, daily *int64
	var count *int
	var clearMin, clearSingle, clearDaily, clearCount bool
	parseI64 := func(key string, dst **int64, clear *bool) bool {
		v, ok := raw[key]
		if !ok {
			return true
		}
		if string(v) == "null" {
			*clear = true
			return true
		}
		var n int64
		if err := json.Unmarshal(v, &n); err != nil {
			return false
		}
		*dst = &n
		return true
	}
	var dummyClear bool // fixed/rate 不接受 null，null 由下方显式检查拒绝，不借用限额 clear 标志
	if !parseI64("fixed_fee_cents", &fixed, &dummyClear) || !parseI64("rate_bp", &rate, &dummyClear) ||
		!parseI64("min_amount_cents", &min, &clearMin) || !parseI64("single_max_cents", &single, &clearSingle) ||
		!parseI64("daily_max_cents", &daily, &clearDaily) {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid withdraw config value")
		return
	}
	if v, ok := raw["daily_count_max"]; ok {
		if string(v) == "null" {
			clearCount = true
		} else {
			var n int
			if err := json.Unmarshal(v, &n); err != nil {
				fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid daily_count_max")
				return
			}
			count = &n
		}
	}
	// fixed/rate 不接受显式 null（视为无效输入）。
	if v, ok := raw["fixed_fee_cents"]; ok && string(v) == "null" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "fixed_fee_cents must not be null")
		return
	}
	if v, ok := raw["rate_bp"]; ok && string(v) == "null" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "rate_bp must not be null")
		return
	}
	cfg, err := s.updateWithdrawConfig(r.Context(), s.adminOperator(r), fixed, rate, min, single, daily, count, clearMin, clearSingle, clearDaily, clearCount)
	if err != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	ok(w, cfg)
}

// ---- 管理端提现流转 HTTP（复用 hAdminWithdrawOrdersSub 的 approve/reject；
// 本函数处理 Phase 9 新增的 start-processing / confirm-payout / fail / refund / auto-payout） ----

type withdrawPayoutReq struct {
	PayoutProofNo string `json:"payout_proof_no"`
	PayoutAt      string `json:"payout_at"` // RFC3339 可选，缺省为确认时刻
	PayoutNote    string `json:"payout_note"`
	FailureReason string `json:"failure_reason"`
}

func (s *Server) hAdminWithdrawFlow(w http.ResponseWriter, r *http.Request, id, action string) {
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	switch action {
	case "start-processing":
		if err := s.startWithdrawProcessing(r.Context(), id, adminID); err != nil {
			withdrawErr(w, err)
			return
		}
		ok(w, map[string]string{"stage": "processing"})
	case "confirm-payout":
		var req withdrawPayoutReq
		body, _ := readAll(r)
		if len(body) > 0 {
			var raw map[string]any
			if err := json.Unmarshal(body, &raw); err != nil {
				fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
				return
			}
			if v, ok := raw["payout_proof_no"].(string); ok {
				req.PayoutProofNo = v
			}
			if v, ok := raw["payout_at"].(string); ok {
				req.PayoutAt = v
			}
			if v, ok := raw["payout_note"].(string); ok {
				req.PayoutNote = v
			}
		}
		var payoutAt *time.Time
		if req.PayoutAt != "" {
			tm, err := time.Parse(time.RFC3339, req.PayoutAt)
			if err != nil {
				fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid payout_at, must be RFC3339")
				return
			}
			payoutAt = &tm
		}
		if err := s.confirmWithdrawPayout(r.Context(), id, adminID, req.PayoutProofNo, payoutAt, req.PayoutNote); err != nil {
			withdrawErr(w, err)
			return
		}
		ok(w, map[string]string{"stage": "success"})
	case "fail":
		var req withdrawPayoutReq
		body, _ := readAll(r)
		if len(body) > 0 {
			var raw map[string]any
			if err := json.Unmarshal(body, &raw); err != nil {
				fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
				return
			}
			if v, ok := raw["failure_reason"].(string); ok {
				req.FailureReason = v
			}
		}
		if err := s.failWithdrawPayout(r.Context(), id, adminID, req.FailureReason); err != nil {
			withdrawErr(w, err)
			return
		}
		ok(w, map[string]string{"stage": "failed"})
	case "refund":
		if err := s.refundWithdraw(r.Context(), id, adminID); err != nil {
			withdrawErr(w, err)
			return
		}
		ok(w, map[string]string{"stage": "refunded"})
	case "auto-payout":
		// 自动打款渠道骨架：商户未签约前恒回 501，绝不打款（与 Phase 8 真实渠道回调 501 同口径）。
		fail(w, http.StatusNotImplemented, "CHANNEL_NOT_CONFIGURED", "automatic payout channel not configured; use manual payout flow")
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown withdraw order path")
	}
}
