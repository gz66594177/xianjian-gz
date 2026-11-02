package fund

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"
)

// 交易密码管理员发起重置（用户定模式：管理员发起、用户自设新密码，管理员全程不知道密码）。
//
// 复用 wallet_tx_password_recovery 找回表与既有审批语义，绝不弱化：
//   - 管理员发起只建一条待复核记录（status=0），不等于通过；
//   - 仍须两名不同管理员先后 approve（既有 recoverApprove 逻辑原样），任一管理员可 reject；
//   - 双人通过后，用户本人才可用现有 /v1/tx-password/recover/reset 自设 6 位新密码（bcrypt）；
//   - 发起人不占审批位：first_admin_id/second_admin_id 只由 approve 写入，发起只写
//     initiated_by_admin_id + reason，发起本身不计入审批。
// 用户侧现有 requests 列表按 user_id 查同一张表，管理员发起的记录天然可见；
// reason 与发起人只在管理端发起响应里返回，用户侧列表结构不变、不泄露管理备注。

var errTxResetUserNotFound = errors.New("user has no fund record")

// AdminRecoveryRequest：管理端视角的找回记录（在 RecoveryRequest 基础上加发起人与理由）。
type AdminRecoveryRequest struct {
	RecoveryRequest
	InitiatedByAdminID *string `json:"initiated_by_admin_id"`
	Reason             *string `json:"reason"`
}

func (s *Server) adminInitiateTxPasswordReset(ctx context.Context, userID, adminID, reason string) (*AdminRecoveryRequest, error) {
	userID = strings.TrimSpace(userID)
	reason = strings.TrimSpace(reason)
	if userID == "" {
		return nil, errors.New("user_id is required")
	}
	if reason == "" {
		return nil, errors.New("reason is required")
	}
	if utf8.RuneCountInString(reason) > 200 {
		return nil, errors.New("reason is too long")
	}
	// 资金侧用户存在性：钱包行或交易密码行任一存在即视为资金用户；
	// 两者皆无（IM 有号但从未用过资金功能）则 404。注意 setTxPassword 不建钱包行，
	// 故只查钱包会误拦已有密码的用户，必须两表任一存在。
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM wallet_account WHERE user_id=$1 UNION ALL SELECT 1 FROM wallet_tx_password WHERE user_id=$1 LIMIT 1`, userID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			return nil, errTxResetUserNotFound
		}
		return nil, err
	}
	// 幂等：同一用户已有未过期待复核记录时返回原记录，不重复建单、不覆盖理由。
	var existingID string
	err := s.db.QueryRowContext(ctx,
		`SELECT request_id FROM wallet_tx_password_recovery WHERE user_id=$1 AND status=0 AND expires_at>now() ORDER BY created_at LIMIT 1`, userID).Scan(&existingID)
	if err == nil {
		return s.loadAdminRecovery(ctx, existingID)
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var pending int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM wallet_tx_password_recovery WHERE user_id=$1 AND status=0`, userID).Scan(&pending); err != nil {
		return nil, err
	}
	if pending >= maxPendingRecovers {
		return nil, errors.New("too many pending recover requests")
	}
	reqID := newID("rec_")
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO wallet_tx_password_recovery (request_id, user_id, status, expires_at, initiated_by_admin_id, reason) VALUES ($1,$2,0,now()+$3::interval,$4,$5)`,
		reqID, userID, "24 hours", adminID, reason); err != nil {
		return nil, err
	}
	s.auditStandalone(ctx, userID, adminID, "tx_password_admin_reset_request", "admin reset request_id="+reqID+" reason="+reason, nil)
	return s.loadAdminRecovery(ctx, reqID)
}

func (s *Server) loadAdminRecovery(ctx context.Context, requestID string) (*AdminRecoveryRequest, error) {
	var r AdminRecoveryRequest
	err := s.db.QueryRowContext(ctx,
		`SELECT request_id, user_id, status, first_admin_id, second_admin_id, created_at, expires_at, initiated_by_admin_id, reason FROM wallet_tx_password_recovery WHERE request_id=$1`, requestID).
		Scan(&r.RequestID, &r.UserID, &r.Status, &r.FirstAdminID, &r.SecondAdminID, &r.CreatedAt, &r.ExpiresAt, &r.InitiatedByAdminID, &r.Reason)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

type adminTxPasswordResetRequestReq struct {
	UserID string `json:"user_id"`
	Reason string `json:"reason"`
}

// hAdminTxPasswordResetRequest：POST /v1/admin/tx-password/reset-request（X-Admin-Secret + X-Admin-ID）。
func (s *Server) hAdminTxPasswordResetRequest(w http.ResponseWriter, r *http.Request) {
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	var req adminTxPasswordResetRequestReq
	if !decodeBody(w, r, &req) {
		return
	}
	rec, err := s.adminInitiateTxPasswordReset(r.Context(), req.UserID, adminID, req.Reason)
	if err != nil {
		if errors.Is(err, errTxResetUserNotFound) {
			fail(w, http.StatusNotFound, "NOT_FOUND", err.Error())
			return
		}
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, rec)
}
