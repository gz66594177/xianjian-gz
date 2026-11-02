package fund

import (
	"context"
	"time"
)

type ReconcileDetail struct {
	UserID          string `json:"user_id"`
	BalanceCents    int64  `json:"balance_cents"`
	FrozenCents     int64  `json:"frozen_cents"`
	DerivedBalance  int64  `json:"derived_balance_cents"`
	DerivedFrozen   int64  `json:"derived_frozen_cents"`
	BalanceDiff     int64  `json:"balance_diff_cents"`
	FrozenDiff      int64  `json:"frozen_diff_cents"`
}

type ReconcileResult struct {
	ReconcileID   string            `json:"reconcile_id"`
	ReconcileDate string            `json:"reconcile_date"`
	Channel       string            `json:"channel"`
	ExpectedCents int64             `json:"expected_cents"`
	ActualCents   int64             `json:"actual_cents"`
	DiffCents     int64             `json:"diff_cents"`
	Status        int               `json:"status"` // 0 平账 1 差异待处理
	Details       []ReconcileDetail `json:"details"`
}

// runReconcile：对账核心——逐户核对 sum(分录带符号) == 账户余额（可用与冻结分别核对）。
// 派生余额：用户户可用 = sum(可用分录：贷+ / 借-)；平台结算户取反（借增贷减，见 ledger.go deltaFor）。冻结同理。
func (s *Server) runReconcile(ctx context.Context) (*ReconcileResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.user_id, a.balance_cents, a.frozen_cents,
		   CASE WHEN a.user_id='platform:settlement' THEN -COALESCE(SUM(CASE WHEN l.account_type=1 THEN CASE WHEN l.direction=2 THEN l.amount_cents ELSE -l.amount_cents END ELSE 0 END),0)
		        ELSE COALESCE(SUM(CASE WHEN l.account_type=1 THEN CASE WHEN l.direction=2 THEN l.amount_cents ELSE -l.amount_cents END ELSE 0 END),0) END,
		   CASE WHEN a.user_id='platform:settlement' THEN -COALESCE(SUM(CASE WHEN l.account_type=2 THEN CASE WHEN l.direction=2 THEN l.amount_cents ELSE -l.amount_cents END ELSE 0 END),0)
		        ELSE COALESCE(SUM(CASE WHEN l.account_type=2 THEN CASE WHEN l.direction=2 THEN l.amount_cents ELSE -l.amount_cents END ELSE 0 END),0) END
		 FROM wallet_account a LEFT JOIN wallet_ledger l ON l.user_id=a.user_id
		 GROUP BY a.user_id, a.balance_cents, a.frozen_cents ORDER BY a.user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	res := &ReconcileResult{
		ReconcileID:   newID("rec_"),
		ReconcileDate: time.Now().UTC().Format("2006-01-02"),
		Channel:       "internal",
		Details:       []ReconcileDetail{},
	}
	flat := false
	for rows.Next() {
		var d ReconcileDetail
		if err := rows.Scan(&d.UserID, &d.BalanceCents, &d.FrozenCents, &d.DerivedBalance, &d.DerivedFrozen); err != nil {
			return nil, err
		}
		d.BalanceDiff = d.BalanceCents - d.DerivedBalance
		d.FrozenDiff = d.FrozenCents - d.DerivedFrozen
		if d.BalanceDiff != 0 || d.FrozenDiff != 0 {
			flat = false
			_ = flat
		}
		res.ExpectedCents += d.BalanceCents + d.FrozenCents
		res.ActualCents += d.DerivedBalance + d.DerivedFrozen
		res.Details = append(res.Details, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.DiffCents = res.ExpectedCents - res.ActualCents
	if res.DiffCents != 0 {
		res.Status = 1
	} else {
		// 总差为 0 但个别户有差也会被上面的逐户差捕获：重新判定
		for _, d := range res.Details {
			if d.BalanceDiff != 0 || d.FrozenDiff != 0 {
				res.Status = 1
				break
			}
		}
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO wallet_reconcile (reconcile_id, reconcile_date, channel, expected_cents, actual_cents, diff_cents, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		res.ReconcileID, res.ReconcileDate, res.Channel, res.ExpectedCents, res.ActualCents, res.DiffCents, res.Status)
	if err != nil {
		return nil, err
	}
	return res, nil
}
