package fund

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// 交易密码安全找回（Phase 2 完整流程，替代 Phase 1 的“申请 + 单管理员密钥确认”）：
//  1. 用户申请：须已用 IM token 登录（证明会话归属）+ 身份核验（实名与已绑方式的后 4 位
//     须匹配同一条正常方式）；核验失败只回通用错误，不透露哪项不对、该用户有无绑定。
//  2. 两名不同管理员先后 approve（X-Admin-ID 区分身份）；同一人两次 approve 拒绝；
//     任一管理员可 reject。管理员全程不接触、不设定新密码。
//  3. 双人通过后，用户本人凭 request_id 重置新密码（一次性、24 小时有效）；
//     重置成功清锁清错误计数，原申请标记已使用。
// 申请单状态：0 待复核 1 双人通过 2 已使用 3 已拒绝 4 已过期。过期在读取时惰性标记。

const (
	RecoverPending  = 0
	RecoverApproved = 1
	RecoverUsed     = 2
	RecoverRejected = 3
	RecoverExpired  = 4
)

const recoverTTL = 24 * time.Hour
const maxPendingRecovers = 3

type RecoveryRequest struct {
	RequestID     string    `json:"request_id"`
	UserID        string    `json:"user_id"`
	Status        int       `json:"status"`
	FirstAdminID  *string   `json:"first_admin_id"`
	SecondAdminID *string   `json:"second_admin_id"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

func (s *Server) verifyRecoverIdentity(ctx context.Context, userID, realName, last4 string) (bool, error) {
	if realName == "" || len([]rune(last4)) != 4 {
		return false, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT real_name, account_no_enc FROM wallet_withdraw_method WHERE user_id=$1 AND status=0`, userID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var name, enc string
		if err := rows.Scan(&name, &enc); err != nil {
			return false, err
		}
		if name != realName {
			continue
		}
		plain, err := s.decryptAccountNo(enc)
		if err != nil {
			continue
		}
		runes := []rune(plain)
		if len(runes) < 4 {
			continue
		}
		if string(runes[len(runes)-4:]) == last4 {
			found = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return found, nil
}

func (s *Server) recoverRequest(ctx context.Context, userID, realName, last4 string) (string, error) {
	ok, err := s.verifyRecoverIdentity(ctx, userID, realName, last4)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("identity verification failed")
	}
	// 已有待复核申请：身份既已通过，直接返回旧单（不重复建单、不透露额外信息）。
	var existing string
	err = s.db.QueryRowContext(ctx,
		`SELECT request_id FROM wallet_tx_password_recovery WHERE user_id=$1 AND status=0 AND expires_at>now() ORDER BY created_at LIMIT 1`, userID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if err != sql.ErrNoRows {
		return "", err
	}
	var pending int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wallet_tx_password_recovery WHERE user_id=$1 AND status=0`, userID).Scan(&pending); err != nil {
		return "", err
	}
	if pending >= maxPendingRecovers {
		return "", errors.New("too many pending recover requests")
	}
	reqID := newID("rec_")
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO wallet_tx_password_recovery (request_id, user_id, status, expires_at) VALUES ($1,$2,0,now()+$3::interval)`,
		reqID, userID, "24 hours")
	if err != nil {
		return "", err
	}
	s.auditStandalone(ctx, userID, userID, "tx_password_recover_request", "recover request_id="+reqID, nil)
	return reqID, nil
}

// loadRecovery：读单并惰性过期（待复核/已通过但超期 -> 置为已过期）。
func (s *Server) loadRecovery(ctx context.Context, requestID string) (*RecoveryRequest, error) {
	var r RecoveryRequest
	err := s.db.QueryRowContext(ctx,
		`SELECT request_id, user_id, status, first_admin_id, second_admin_id, created_at, expires_at FROM wallet_tx_password_recovery WHERE request_id=$1`, requestID).
		Scan(&r.RequestID, &r.UserID, &r.Status, &r.FirstAdminID, &r.SecondAdminID, &r.CreatedAt, &r.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if (r.Status == RecoverPending || r.Status == RecoverApproved) && time.Now().After(r.ExpiresAt) {
		_, _ = s.db.ExecContext(ctx, `UPDATE wallet_tx_password_recovery SET status=4, updated_at=now() WHERE request_id=$1`, requestID)
		r.Status = RecoverExpired
	}
	return &r, nil
}

func (s *Server) recoverApprove(ctx context.Context, requestID, adminID string) (string, error) {
	rec, err := s.loadRecovery(ctx, requestID)
	if err == sql.ErrNoRows {
		return "", errors.New("recover request not found")
	}
	if err != nil {
		return "", err
	}
	if rec.Status != RecoverPending {
		return "", errors.New("recover request is not pending")
	}
	if rec.FirstAdminID == nil {
		_, err = s.db.ExecContext(ctx,
			`UPDATE wallet_tx_password_recovery SET first_admin_id=$1, updated_at=now() WHERE request_id=$2`, adminID, requestID)
		if err != nil {
			return "", err
		}
		s.auditStandalone(ctx, rec.UserID, adminID, "tx_password_recover_approve", "recover approve1 request_id="+requestID, nil)
		return "awaiting_second_approval", nil
	}
	if *rec.FirstAdminID == adminID {
		return "", errors.New("second approval must come from a different admin")
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE wallet_tx_password_recovery SET second_admin_id=$1, status=1, updated_at=now() WHERE request_id=$2`, adminID, requestID)
	if err != nil {
		return "", err
	}
	s.auditStandalone(ctx, rec.UserID, adminID, "tx_password_recover_approve", "recover approve2 request_id="+requestID, nil)
	return "approved", nil
}

func (s *Server) recoverReject(ctx context.Context, requestID, adminID string) error {
	rec, err := s.loadRecovery(ctx, requestID)
	if err == sql.ErrNoRows {
		return errors.New("recover request not found")
	}
	if err != nil {
		return err
	}
	if rec.Status != RecoverPending {
		return errors.New("recover request is not pending")
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE wallet_tx_password_recovery SET status=3, updated_at=now() WHERE request_id=$1`, requestID)
	if err != nil {
		return err
	}
	s.auditStandalone(ctx, rec.UserID, adminID, "tx_password_recover_reject", "recover rejected request_id="+requestID, nil)
	return nil
}

func (s *Server) recoverReset(ctx context.Context, userID, requestID, newPwd, confirm string) error {
	if !txPwdRe.MatchString(newPwd) {
		return errors.New("new transaction password must be exactly 6 digits")
	}
	if newPwd != confirm {
		return errors.New("two new passwords do not match")
	}
	rec, err := s.loadRecovery(ctx, requestID)
	if err == sql.ErrNoRows {
		return errors.New("recover request not found")
	}
	if err != nil {
		return err
	}
	if rec.UserID != userID {
		return errors.New("recover request does not belong to this user")
	}
	if rec.Status != RecoverApproved {
		return errors.New("recover request is not approved by two admins yet")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_tx_password (user_id, password_hash, error_count, last_changed_at) VALUES ($1,$2,0,now())
		 ON CONFLICT (user_id) DO UPDATE SET password_hash=EXCLUDED.password_hash, error_count=0, locked_until=NULL, last_changed_at=now(), updated_at=now()`,
		userID, string(hash)); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE wallet_tx_password_recovery SET status=2, updated_at=now() WHERE request_id=$1 AND status=1`, requestID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("recover request already used")
	}
	if err := insertAudit(ctx, tx, userID, userID, "tx_password_recover_reset", "recover reset request_id="+requestID, "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Server) listRecoverRequests(ctx context.Context, userID string) ([]RecoveryRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT request_id, user_id, status, first_admin_id, second_admin_id, created_at, expires_at FROM wallet_tx_password_recovery WHERE user_id=$1 ORDER BY created_at DESC LIMIT 50`, userID)
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
