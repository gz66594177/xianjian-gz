package fund

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var txPwdRe = regexp.MustCompile(`^[0-9]{6}$`)

const maxPwdErrors = 5
const lockDuration = 30 * time.Minute

type TxPasswordStatus struct {
	HasPassword bool       `json:"has_password"`
	ErrorCount  int        `json:"error_count"`
	Locked      bool       `json:"locked"`
	LockedUntil *time.Time `json:"locked_until"`
}

func (s *Server) txPasswordStatus(ctx context.Context, userID string) (*TxPasswordStatus, error) {
	var st TxPasswordStatus
	var lockedUntil sql.NullTime
	var errCount int
	err := s.db.QueryRowContext(ctx,
		`SELECT error_count, locked_until FROM wallet_tx_password WHERE user_id=$1`, userID).Scan(&errCount, &lockedUntil)
	if err == sql.ErrNoRows {
		return &TxPasswordStatus{HasPassword: false}, nil
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
	return &st, nil
}

func (s *Server) setTxPassword(ctx context.Context, userID, pwd, confirm string) error {
	if !txPwdRe.MatchString(pwd) {
		return errors.New("transaction password must be exactly 6 digits")
	}
	if pwd != confirm {
		return errors.New("two passwords do not match")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(pwd), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO wallet_tx_password (user_id, password_hash, error_count, last_changed_at) VALUES ($1,$2,0,now()) ON CONFLICT (user_id) DO NOTHING`, userID, string(hash))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return errors.New("transaction password already set; use change flow")
	}
	s.auditStandalone(ctx, userID, userID, "tx_password_set", "transaction password set", nil)
	return nil
}

// verifyTxPassword：验密；错 5 次锁定 30 分钟；成功清零。返回 (ok, locked, err)。
func (s *Server) verifyTxPassword(ctx context.Context, userID, pwd string) (bool, bool, error) {
	var hash string
	var errCount int
	var lockedUntil sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT password_hash, error_count, locked_until FROM wallet_tx_password WHERE user_id=$1`, userID).
		Scan(&hash, &errCount, &lockedUntil)
	if err == sql.ErrNoRows {
		return false, false, errors.New("transaction password not set")
	}
	if err != nil {
		return false, false, err
	}
	if lockedUntil.Valid && time.Now().Before(lockedUntil.Time) {
		return false, true, nil
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pwd)) != nil {
		errCount++
		if errCount >= maxPwdErrors {
			until := time.Now().Add(lockDuration)
			_, _ = s.db.ExecContext(ctx, `UPDATE wallet_tx_password SET error_count=$1, locked_until=$2, updated_at=now() WHERE user_id=$3`, errCount, until, userID)
			s.auditStandalone(ctx, userID, userID, "tx_password_locked", fmt.Sprintf("locked after %d errors", errCount), nil)
			return false, true, nil
		}
		_, _ = s.db.ExecContext(ctx, `UPDATE wallet_tx_password SET error_count=$1, updated_at=now() WHERE user_id=$2`, errCount, userID)
		return false, false, nil
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE wallet_tx_password SET error_count=0, locked_until=NULL, updated_at=now() WHERE user_id=$1`, userID)
	return true, false, nil
}

func (s *Server) changeTxPassword(ctx context.Context, userID, oldPwd, newPwd, confirm string) error {
	ok, locked, err := s.verifyTxPassword(ctx, userID, oldPwd)
	if err != nil {
		return err
	}
	if locked {
		return errors.New("transaction password locked")
	}
	if !ok {
		return errors.New("old transaction password incorrect")
	}
	if !txPwdRe.MatchString(newPwd) {
		return errors.New("new transaction password must be exactly 6 digits")
	}
	if newPwd != confirm {
		return errors.New("two new passwords do not match")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPwd), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE wallet_tx_password SET password_hash=$1, error_count=0, locked_until=NULL, last_changed_at=now(), updated_at=now() WHERE user_id=$2`, string(hash), userID)
	if err != nil {
		return err
	}
	s.auditStandalone(ctx, userID, userID, "tx_password_change", "transaction password changed", nil)
	return nil
}

// 安全找回完整流程见 recovery.go（Phase 2：身份核验 + 双人复核 + 用户本人重置）。
// Phase 1 的“申请 + 单管理员密钥确认并代设密码”已废除：管理员绝不能接触用户新密码。

// requireTxPassword：换绑/解绑/设默认等敏感变更的统一门禁：未设密码 -> 403 类错误；锁定 -> 锁定错误；错密 -> 错误。
func (s *Server) requireTxPassword(ctx context.Context, userID, pwd string) error {
	if pwd == "" {
		return errors.New("transaction password required")
	}
	ok, locked, err := s.verifyTxPassword(ctx, userID, pwd)
	if err != nil {
		return err
	}
	if locked {
		return errors.New("transaction password locked")
	}
	if !ok {
		return errors.New("transaction password incorrect")
	}
	return nil
}
