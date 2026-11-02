package fund

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

func readAll(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

// Handler 返回 net/http 处理器。路由全部为 /v1/*，鉴权见 authUser/authAdmin。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { ok(w, map[string]string{"status": "ok"}) })

	// 钱包
	mux.HandleFunc("POST /v1/wallet/create", s.hCreateWallet)
	mux.HandleFunc("GET /v1/wallet", s.hGetWallet)
	mux.HandleFunc("GET /v1/wallet/balance", s.hGetWallet)
	mux.HandleFunc("GET /v1/wallet/ledger", s.hListLedger)
	mux.HandleFunc("GET /v1/profile/phone", s.hGetProfilePhone)
	mux.HandleFunc("POST /v1/profile/phone/bind", s.hBindProfilePhone)
	// 账务 primitives（Phase 1 内部口径；真实充值/提现/红包/转账在后续阶段接交易密码与渠道）
	mux.HandleFunc("POST /v1/ledger/income", s.hLedgerOp(TxnIncome))
	mux.HandleFunc("POST /v1/ledger/expense", s.hLedgerOp(TxnExpense))
	mux.HandleFunc("POST /v1/ledger/refund", s.hLedgerOp(TxnRefund))
	mux.HandleFunc("POST /v1/ledger/freeze", s.hFreeze)
	mux.HandleFunc("POST /v1/ledger/unfreeze", s.hFreezeMove(TxnUnfreeze))
	mux.HandleFunc("POST /v1/ledger/freeze-charge", s.hFreezeMove(TxnFreezeCharge))
	mux.HandleFunc("POST /v1/ledger/freeze-refund", s.hFreezeMove(TxnFreezeRefund))
	mux.HandleFunc("POST /v1/ledger/reverse", s.hReverse)
	// 交易密码
	mux.HandleFunc("GET /v1/tx-password/status", s.hTxPwdStatus)
	mux.HandleFunc("POST /v1/tx-password/set", s.hTxPwdSet)
	mux.HandleFunc("POST /v1/tx-password/change", s.hTxPwdChange)
	mux.HandleFunc("POST /v1/tx-password/verify", s.hTxPwdVerify)
	mux.HandleFunc("POST /v1/tx-password/recover/request", s.hRecoverRequest)
	mux.HandleFunc("POST /v1/tx-password/recover/reset", s.hRecoverReset)
	mux.HandleFunc("GET /v1/tx-password/recover/requests", s.hListRecoverRequests)
	mux.HandleFunc("POST /v1/admin/tx-password/recover/approve", s.hRecoverApprove)
	mux.HandleFunc("POST /v1/admin/tx-password/recover/reject", s.hRecoverReject)
	mux.HandleFunc("GET /v1/admin/tx-password/recover/", s.hGetRecoverRequest)
	// 提现账户
	mux.HandleFunc("GET /v1/withdraw-methods", s.hListMethods)
	mux.HandleFunc("POST /v1/withdraw-methods/upload-qr", s.hUploadMethodQR)
	mux.HandleFunc("POST /v1/withdraw-methods", s.hAddMethod)
	mux.HandleFunc("PUT /v1/withdraw-methods/", s.hUpdateMethod)
	mux.HandleFunc("DELETE /v1/withdraw-methods/", s.hDeleteMethod)
	mux.HandleFunc("POST /v1/withdraw-methods/", s.hSetDefaultMethod) // 实际按路径后缀 /set-default 分发，见 hSetDefaultMethod 的守卫
	// 好友红包（Phase 3）
	mux.HandleFunc("POST /v1/redpackets", s.hSendPacket)
	mux.HandleFunc("GET /v1/redpackets", s.hListPackets)
	mux.HandleFunc("POST /v1/redpackets/", s.hClaimPacket) // 按 /claim 后缀分发，见 hClaimPacket 守卫
	mux.HandleFunc("GET /v1/redpackets/", s.hPacketSubGet) // 详情或 /claims
	mux.HandleFunc("POST /v1/admin/redpackets/expire", s.hExpirePackets)
	// 群普通红包（Phase 4，定额均分）
	mux.HandleFunc("POST /v1/group-redpackets", s.hSendGroupPacket)
	mux.HandleFunc("GET /v1/group-redpackets", s.hListGroupPackets)
	mux.HandleFunc("POST /v1/group-redpackets/", s.hClaimGroupPacket) // 按 /claim 后缀分发，见 hClaimGroupPacket 守卫
	mux.HandleFunc("GET /v1/group-redpackets/", s.hGroupPacketSubGet) // 详情或 /claims
	mux.HandleFunc("POST /v1/admin/group-redpackets/expire", s.hExpireGroupPackets)
	// 群拼手气红包（Phase 5，随机预拆分；与群普通红包表分离）
	mux.HandleFunc("POST /v1/group-lucky-redpackets", s.hSendLuckyPacket)
	mux.HandleFunc("GET /v1/group-lucky-redpackets", s.hListLuckyPackets)
	mux.HandleFunc("POST /v1/group-lucky-redpackets/", s.hClaimLuckyPacket) // 按 /claim 后缀分发，见 hClaimLuckyPacket 守卫
	mux.HandleFunc("GET /v1/group-lucky-redpackets/", s.hLuckyPacketSubGet) // 详情或 /claims 或 /ranking
	mux.HandleFunc("POST /v1/admin/group-lucky-redpackets/expire", s.hExpireLuckyPackets)
	// 充值（Phase 8，回调入账；mock 回调入口只在 FUND_MOCK_PAYMENT=1 时注册精确路由，
	// 开关关闭时 mock 路径落入 fallback 回 404；真实渠道回调恒回 501，见 hPaymentCallbackFallback）
	mux.HandleFunc("POST /v1/recharges", s.hCreateRecharge)
	mux.HandleFunc("GET /v1/recharges", s.hListRecharges)
	mux.HandleFunc("GET /v1/recharges/", s.hGetRecharge)
	mux.HandleFunc("GET /v1/admin/recharges", s.hAdminListRecharges)
	mux.HandleFunc("GET /v1/admin/recharges/", s.hAdminGetRecharge)
	if s.cfg.MockPaymentEnabled {
		mux.HandleFunc("POST /v1/payments/callback/mock", s.hMockCallback)
	}
	mux.HandleFunc("POST /v1/payments/callback/", s.hPaymentCallbackFallback)
	// 手动充值 + 网关支付（0010；Phase 8 回调接口本身不动，回调单仍走 /v1/recharges 与 mock 回调）。
	// 注意单数 /v1/recharge/* 与复数 /v1/recharges/* 为不同路径段，互不冲突；
	// 精确路由（/manual、/gateway、/orders、/methods）优先于 /v1/recharge/ 子树匹配。
	mux.HandleFunc("GET /v1/recharge/methods", s.hListPayMethodsUser)
	mux.HandleFunc("GET /v1/recharge/channels", s.hRechargeChannels)
	mux.HandleFunc("POST /v1/recharge/manual", s.hCreateManualRecharge)
	mux.HandleFunc("POST /v1/recharge/", s.hUploadManualProof) // 仅 /v1/recharge/<id>/proof 命中，见守卫
	mux.HandleFunc("GET /v1/recharge/orders", s.hListRechargeOrders)
	mux.HandleFunc("GET /v1/recharge/orders/", s.hGetRechargeOrder)
	mux.HandleFunc("POST /v1/recharge/gateway", s.hCreateGatewayRecharge)
	mux.HandleFunc("POST /v1/payments/callback/gateway", s.hGatewayCallback)
	mux.HandleFunc("GET /v1/uploads/", s.hGetUpload)
	mux.HandleFunc("POST /v1/admin/payment-methods", s.hAdminCreatePayMethod)
	mux.HandleFunc("GET /v1/admin/payment-methods", s.hAdminListPayMethods)
	mux.HandleFunc("GET /v1/admin/payment-methods/", s.hAdminPayMethodSub)
	mux.HandleFunc("PUT /v1/admin/payment-methods/", s.hAdminPayMethodSub)
	mux.HandleFunc("POST /v1/admin/payment-methods/", s.hAdminPayMethodSub)
	mux.HandleFunc("POST /v1/admin/gateway-channels", s.hAdminCreateGatewayChannel)
	mux.HandleFunc("GET /v1/admin/gateway-channels", s.hAdminListGatewayChannels)
	mux.HandleFunc("GET /v1/admin/gateway-channels/", s.hAdminGatewayChannelSub)
	mux.HandleFunc("PUT /v1/admin/gateway-channels/", s.hAdminGatewayChannelSub)
	mux.HandleFunc("GET /v1/admin/recharge/pending", s.hListManualPendingAdmin)
	mux.HandleFunc("POST /v1/admin/recharge/", s.hAdminManualReview) // 仅 /approve|/reject，见守卫
	mux.HandleFunc("GET /v1/admin/recharge/orders", s.hAdminListRechargeOrders)
	mux.HandleFunc("POST /v1/admin/recharge/gateway/expire", s.hExpireGatewayOrders)
	// 提现（Phase 9，7 态全流程；quote 为精确路径，优先于 /v1/withdrawals/ 子树匹配）
	mux.HandleFunc("POST /v1/withdrawals", s.hSubmitWithdraw)
	mux.HandleFunc("GET /v1/withdrawals", s.hListWithdraws)
	mux.HandleFunc("GET /v1/withdrawals/quote", s.hWithdrawQuote)
	mux.HandleFunc("GET /v1/withdrawals/", s.hGetWithdraw)
	mux.HandleFunc("GET /v1/admin/withdraw-config", s.hAdminWithdrawConfig)
	mux.HandleFunc("PUT /v1/admin/withdraw-config", s.hAdminWithdrawConfig)
	// 转账（Phase 6，即时到账）
	mux.HandleFunc("POST /v1/transfers", s.hSendTransfer)
	mux.HandleFunc("GET /v1/transfers", s.hListTransfers)
	mux.HandleFunc("GET /v1/transfers/", s.hGetTransfer)
	mux.HandleFunc("GET /v1/admin/transfers", s.hAdminListTransfers)
	mux.HandleFunc("GET /v1/admin/transfers/", s.hAdminGetTransfer)
	// 准入与对账
	mux.HandleFunc("GET /v1/finance/status", s.hFinanceStatus)
	mux.HandleFunc("POST /v1/admin/reconcile", s.hRunReconcile)
	mux.HandleFunc("GET /v1/admin/reconcile/", s.hGetReconcile)
	// 管理端资金中心（Phase 7：只查 + 调整单双人复核 + 提现审核脚手架 + 限额配置）
	mux.HandleFunc("GET /v1/admin/wallets/", s.hAdminWallet)
	mux.HandleFunc("GET /v1/admin/withdraw-methods", s.hAdminWithdrawMethods)
	mux.HandleFunc("GET /v1/admin/withdraw-methods/default", s.hAdminDefaultMethod)
	mux.HandleFunc("GET /v1/admin/tx-password/status", s.hAdminTxPasswordStatus)
	mux.HandleFunc("GET /v1/admin/tx-password/recover", s.hAdminListRecoveries)
	// 交易密码管理员发起重置（用户自设新密码；审批语义沿用既有双人复核，发起不等于通过）
	mux.HandleFunc("POST /v1/admin/tx-password/reset-request", s.hAdminTxPasswordResetRequest)
	mux.HandleFunc("POST /v1/admin/adjustments", s.hAdminCreateAdjustment)
	mux.HandleFunc("GET /v1/admin/adjustments", s.hAdminListAdjustments)
	mux.HandleFunc("GET /v1/admin/adjustments/", s.hAdminAdjustmentsSub)
	mux.HandleFunc("POST /v1/admin/adjustments/", s.hAdminAdjustmentsSub)
	// 人工余额修改（单人直连入账，无双人复核；面板权限 xianliao:fund:adjust 由 RuoYi 代理层 gating）
	mux.HandleFunc("POST /v1/admin/balance-adjustments", s.hAdminCreateBalanceAdjustment)
	mux.HandleFunc("GET /v1/admin/balance-adjustments", s.hAdminListBalanceAdjustments)
	mux.HandleFunc("GET /v1/admin/balance-adjustments/", s.hAdminGetBalanceAdjustment)
	mux.HandleFunc("GET /v1/admin/withdraw-orders", s.hAdminListWithdrawOrders)
	mux.HandleFunc("GET /v1/admin/withdraw-orders/", s.hAdminWithdrawOrdersSub)
	mux.HandleFunc("POST /v1/admin/withdraw-orders/", s.hAdminWithdrawOrdersSub)
	mux.HandleFunc("GET /v1/admin/risk-limits", s.hAdminRiskLimits)
	mux.HandleFunc("PUT /v1/admin/risk-limits", s.hAdminRiskLimits)
	mux.HandleFunc("GET /v1/admin/audit-logs", s.hAdminAuditLogs)
	return mux
}

// 注意：Go ServeMux 中 "POST /v1/withdraw-methods/" 与 "PUT/DELETE ..." 共存时，
// POST 到 /v1/withdraw-methods/<id>/set-default 会命中 POST 模式；POST 到 /v1/withdraw-methods（无尾斜杠）命中精确模式。
// hSetDefaultMethod 内部校验路径必须以 /set-default 结尾，否则 404。

func (s *Server) userCtx(w http.ResponseWriter, r *http.Request) (string, bool) {
	uid, okAuth, code, msg := s.authUser(r)
	if !okAuth {
		fail(w, code, "UNAUTHORIZED", msg)
		return "", false
	}
	// query user_id 参数必须等于 IM token 解析出的用户
	if q := r.URL.Query().Get("user_id"); q != "" && q != uid {
		fail(w, http.StatusForbidden, "FORBIDDEN", "user_id parameter does not match IM token user")
		return "", false
	}
	return uid, true
}

func (s *Server) decodeAndCheckUser(w http.ResponseWriter, r *http.Request, dst any) (string, bool) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return "", false
	}
	// 先以 map 校验 body user_id，再正式解码
	var raw map[string]any
	bodyBytes, err := readAll(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "cannot read body")
		return "", false
	}
	if len(bodyBytes) > 0 {
		if err := json.Unmarshal(bodyBytes, &raw); err != nil {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
			return "", false
		}
		if bodyUserIDMismatch(raw, uid) {
			fail(w, http.StatusForbidden, "FORBIDDEN", "user_id in body does not match IM token user")
			return "", false
		}
		if err := json.Unmarshal(bodyBytes, dst); err != nil {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body: "+err.Error())
			return "", false
		}
	}
	return uid, true
}

func (s *Server) hCreateWallet(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	acct, err := s.createWallet(r.Context(), uid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, acct)
}

func (s *Server) hGetWallet(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	acct, err := s.getAccount(r.Context(), uid)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, "WALLET_NOT_FOUND", "wallet not found; create it first")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, acct)
}

func (s *Server) hListLedger(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
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
}

type ledgerReq struct {
	AmountCents    int64  `json:"amount_cents"`
	IdempotencyKey string `json:"idempotency_key"`
	RefType        string `json:"ref_type"`
	RefID          string `json:"ref_id"`
}

func (s *Server) hLedgerOp(txnType int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ledgerReq
		uid, okAuth := s.decodeAndCheckUser(w, r, &req)
		if !okAuth {
			return
		}
		if req.AmountCents <= 0 {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "amount_cents must be > 0")
			return
		}
		var txn *Txn
		var err error
		err = s.withUserLock(r.Context(), uid, func() error {
			switch txnType {
			case TxnIncome:
				txn, err = s.opIncome(r.Context(), uid, req.AmountCents, req.RefType, req.RefID, req.IdempotencyKey)
			case TxnExpense:
				txn, err = s.opExpense(r.Context(), uid, req.AmountCents, req.RefType, req.RefID, req.IdempotencyKey)
			case TxnRefund:
				txn, err = s.opRefund(r.Context(), uid, req.AmountCents, req.RefType, req.RefID, req.IdempotencyKey)
			}
			return err
		})
		if err != nil {
			s.ledgerErr(w, err)
			return
		}
		ok(w, txn)
	}
}

func (s *Server) ledgerErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errBusy) {
		fail(w, http.StatusTooManyRequests, "SYSTEM_BUSY", "system busy, please retry")
		return
	}
	msg := err.Error()
	if strings.Contains(msg, "insufficient") || strings.Contains(msg, "not found") || strings.Contains(msg, "not normal") || strings.Contains(msg, "does not belong") {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", msg)
		return
	}
	fail(w, http.StatusInternalServerError, "INTERNAL", msg)
}

type freezeReq struct {
	AmountCents    int64  `json:"amount_cents"`
	FreezeID       string `json:"freeze_id"`
	IdempotencyKey string `json:"idempotency_key"`
	RefType        string `json:"ref_type"`
	RefID          string `json:"ref_id"`
}

func (s *Server) hFreeze(w http.ResponseWriter, r *http.Request) {
	var req freezeReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if req.AmountCents <= 0 {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "amount_cents must be > 0")
		return
	}
	var txn *Txn
	var fid string
	err := s.withUserLock(r.Context(), uid, func() error {
		var err error
		txn, fid, err = s.opFreeze(r.Context(), uid, req.AmountCents, req.FreezeID, req.RefType, req.RefID, req.IdempotencyKey)
		return err
	})
	if err != nil {
		s.ledgerErr(w, err)
		return
	}
	ok(w, map[string]any{"txn": txn, "freeze_id": fid})
}

type freezeMoveReq struct {
	FreezeID    string `json:"freeze_id"`
	AmountCents int64  `json:"amount_cents"`
}

func (s *Server) hFreezeMove(txnType int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req freezeMoveReq
		uid, okAuth := s.decodeAndCheckUser(w, r, &req)
		if !okAuth {
			return
		}
		if req.FreezeID == "" || req.AmountCents <= 0 {
			fail(w, http.StatusBadRequest, "BAD_REQUEST", "freeze_id and amount_cents>0 are required")
			return
		}
		auditAction := "ledger_unfreeze"
		if txnType == TxnFreezeCharge {
			auditAction = "ledger_freeze_charge"
		} else if txnType == TxnFreezeRefund {
			auditAction = "ledger_freeze_refund"
		}
		var txn *Txn
		err := s.withUserLock(r.Context(), uid, func() error {
			var err error
			txn, err = s.freezeMoveTx(r.Context(), uid, req.FreezeID, req.AmountCents, txnType, auditAction)
			return err
		})
		if err != nil {
			s.ledgerErr(w, err)
			return
		}
		ok(w, txn)
	}
}

type reverseReq struct {
	OriginalTxnID  string `json:"original_txn_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (s *Server) hReverse(w http.ResponseWriter, r *http.Request) {
	var req reverseReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if req.OriginalTxnID == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "original_txn_id is required")
		return
	}
	var txn *Txn
	err := s.withUserLock(r.Context(), uid, func() error {
		var err error
		txn, err = s.opReverse(r.Context(), uid, req.OriginalTxnID, req.IdempotencyKey)
		return err
	})
	if err != nil {
		s.ledgerErr(w, err)
		return
	}
	ok(w, txn)
}

// ---- 交易密码 HTTP ----

type phoneBindReq struct {
	AreaCode string `json:"area_code"`
	Phone    string `json:"phone"`
}

func (s *Server) hGetProfilePhone(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	pb, err := s.getPhoneBinding(r.Context(), uid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, pb)
}

func (s *Server) hBindProfilePhone(w http.ResponseWriter, r *http.Request) {
	var req phoneBindReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	pb, err := s.bindPhone(r.Context(), uid, req.AreaCode, req.Phone)
	if err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, pb)
}

func (s *Server) hTxPwdStatus(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	st, err := s.txPasswordStatus(r.Context(), uid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, st)
}

type pwdSetReq struct {
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *Server) hTxPwdSet(w http.ResponseWriter, r *http.Request) {
	var req pwdSetReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if err := s.setTxPassword(r.Context(), uid, req.Password, req.ConfirmPassword); err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, map[string]bool{"set": true})
}

type pwdChangeReq struct {
	OldPassword     string `json:"old_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *Server) hTxPwdChange(w http.ResponseWriter, r *http.Request) {
	var req pwdChangeReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if err := s.changeTxPassword(r.Context(), uid, req.OldPassword, req.NewPassword, req.ConfirmPassword); err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, map[string]bool{"changed": true})
}

type pwdVerifyReq struct {
	Password string `json:"password"`
}

func (s *Server) hTxPwdVerify(w http.ResponseWriter, r *http.Request) {
	var req pwdVerifyReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	verified, locked, err := s.verifyTxPassword(r.Context(), uid, req.Password)
	if err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, map[string]bool{"verified": verified, "locked": locked})
}

// ---- 交易密码安全找回（Phase 2 完整流程，见 recovery.go） ----

type recoverRequestReq struct {
	RealName     string `json:"real_name"`
	AccountLast4 string `json:"account_last4"`
}

func (s *Server) hRecoverRequest(w http.ResponseWriter, r *http.Request) {
	var req recoverRequestReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	reqID, err := s.recoverRequest(r.Context(), uid, req.RealName, req.AccountLast4)
	if err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, map[string]string{"request_id": reqID})
}

type recoverResetReq struct {
	RequestID       string `json:"request_id"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *Server) hRecoverReset(w http.ResponseWriter, r *http.Request) {
	var req recoverResetReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if err := s.recoverReset(r.Context(), uid, req.RequestID, req.NewPassword, req.ConfirmPassword); err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, map[string]bool{"reset": true})
}

func (s *Server) hListRecoverRequests(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	list, err := s.listRecoverRequests(r.Context(), uid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

type recoverAdminReq struct {
	RequestID string `json:"request_id"`
}

func (s *Server) hRecoverApprove(w http.ResponseWriter, r *http.Request) {
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	var req recoverAdminReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RequestID == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "request_id is required")
		return
	}
	stage, err := s.recoverApprove(r.Context(), req.RequestID, adminID)
	if err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, map[string]string{"stage": stage})
}

func (s *Server) hRecoverReject(w http.ResponseWriter, r *http.Request) {
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	var req recoverAdminReq
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RequestID == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "request_id is required")
		return
	}
	if err := s.recoverReject(r.Context(), req.RequestID, adminID); err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, map[string]bool{"rejected": true})
}

func (s *Server) hGetRecoverRequest(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/admin/tx-password/recover/")
	if id == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "request_id is required")
		return
	}
	rec, err := s.loadRecovery(r.Context(), id)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, "NOT_FOUND", "recover request not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, rec)
}

// ---- 提现账户 HTTP ----

func (s *Server) hListMethods(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	methods, err := s.listMethods(r.Context(), uid)
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, methods)
}

type methodAddReq struct {
	Type       int    `json:"type"`
	RealName   string `json:"real_name"`
	AccountNo  string `json:"account_no"`
	BankName   string `json:"bank_name"`
	BankBranch string `json:"bank_branch"`
	QRFileID   string `json:"qr_file_id"`
	IsDefault  bool   `json:"is_default"`
}

func (s *Server) hAddMethod(w http.ResponseWriter, r *http.Request) {
	var req methodAddReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	m, err := s.addMethod(r.Context(), uid, req.Type, req.RealName, req.AccountNo, req.BankName, req.BankBranch, req.QRFileID, req.IsDefault)
	if err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, m)
}

// hUploadMethodQR：POST /v1/withdraw-methods/upload-qr（multipart 字段 qr，jpg/png <= 5MB，
// 复用 saveUpload 的嗅探校验）。只返回 file_id 与鉴权取用路径，不绑定方式；绑定时再把
// qr_file_id 传给新增/修改接口。未被绑定的孤文件目前无自动清理，见交付报告。
func (s *Server) hUploadMethodQR(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	fh, okForm := parseUploadForm(w, r, "qr")
	if !okForm {
		return
	}
	fileID, _, _, _, err := s.saveUpload(r.Context(), uploadKindQR, fh)
	if err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	_ = uid
	ok(w, map[string]string{"qr_file_id": fileID, "qr_url": "/v1/uploads/" + fileID})
}

func methodIDFromPath(r *http.Request) (string, string) {
	// /v1/withdraw-methods/<id> 或 /v1/withdraw-methods/<id>/set-default
	rest := strings.TrimPrefix(r.URL.Path, "/v1/withdraw-methods/")
	parts := strings.Split(rest, "/")
	if len(parts) == 1 {
		return parts[0], ""
	}
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

type methodUpdateReq struct {
	RealName   *string `json:"real_name"`
	AccountNo  *string `json:"account_no"`
	BankName   *string `json:"bank_name"`
	BankBranch *string `json:"bank_branch"`
	QRFileID   *string `json:"qr_file_id"`
	IsDefault  *bool   `json:"is_default"`
	TxPassword string  `json:"tx_password"`
}

func (s *Server) hUpdateMethod(w http.ResponseWriter, r *http.Request) {
	id, suffix := methodIDFromPath(r)
	if id == "" || suffix != "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown withdraw method path")
		return
	}
	var req methodUpdateReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if err := s.requireTxPassword(r.Context(), uid, req.TxPassword); err != nil {
		fail(w, http.StatusForbidden, "TX_PASSWORD_ERROR", err.Error())
		return
	}
	var oldQR string
	var oldQRNull sql.NullString
	if err := s.db.QueryRowContext(r.Context(), `SELECT qr_file_id FROM wallet_withdraw_method WHERE method_id=$1`, id).Scan(&oldQRNull); err == nil && oldQRNull.Valid {
		oldQR = oldQRNull.String
	}
	m, err := s.updateMethod(r.Context(), uid, id, req.RealName, req.AccountNo, req.BankName, req.BankBranch, req.QRFileID, req.IsDefault)
	if err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	if oldQR != "" && (m.QRFileID == nil || *m.QRFileID != oldQR) {
		s.deleteUploadFile(r.Context(), oldQR)
	}
	ok(w, m)
}

type pwdOnlyReq struct {
	TxPassword string `json:"tx_password"`
}

func (s *Server) hDeleteMethod(w http.ResponseWriter, r *http.Request) {
	id, suffix := methodIDFromPath(r)
	if id == "" || suffix != "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown withdraw method path")
		return
	}
	var req pwdOnlyReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if err := s.requireTxPassword(r.Context(), uid, req.TxPassword); err != nil {
		fail(w, http.StatusForbidden, "TX_PASSWORD_ERROR", err.Error())
		return
	}
	res, err := s.deleteMethod(r.Context(), uid, id)
	if err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, res)
}

func (s *Server) hSetDefaultMethod(w http.ResponseWriter, r *http.Request) {
	id, suffix := methodIDFromPath(r)
	if id == "" || suffix != "set-default" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown withdraw method path")
		return
	}
	var req pwdOnlyReq
	uid, okAuth := s.decodeAndCheckUser(w, r, &req)
	if !okAuth {
		return
	}
	if err := s.requireTxPassword(r.Context(), uid, req.TxPassword); err != nil {
		fail(w, http.StatusForbidden, "TX_PASSWORD_ERROR", err.Error())
		return
	}
	if err := s.setDefaultMethod(r.Context(), uid, id); err != nil {
		fail(w, http.StatusBadRequest, "BUSINESS_ERROR", err.Error())
		return
	}
	ok(w, map[string]bool{"set_default": true})
}

// ---- 准入与对账 HTTP ----

func (s *Server) hFinanceStatus(w http.ResponseWriter, r *http.Request) {
	uid, okAuth := s.userCtx(w, r)
	if !okAuth {
		return
	}
	st, err := s.financeStatus(r.Context(), uid)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, "WALLET_NOT_FOUND", "wallet not found; create it first")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, st)
}

func (s *Server) hRunReconcile(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	res, err := s.runReconcile(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, res)
}

func (s *Server) hGetReconcile(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v1/admin/reconcile/")
	if id == "" {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "reconcile_id is required")
		return
	}
	var rec struct {
		ReconcileID   string `json:"reconcile_id"`
		ReconcileDate string `json:"reconcile_date"`
		Channel       string `json:"channel"`
		ExpectedCents int64  `json:"expected_cents"`
		ActualCents   int64  `json:"actual_cents"`
		DiffCents     int64  `json:"diff_cents"`
		Status        int    `json:"status"`
	}
	err := s.db.QueryRowContext(r.Context(),
		`SELECT reconcile_id, reconcile_date, channel, expected_cents, actual_cents, diff_cents, status FROM wallet_reconcile WHERE reconcile_id=$1`, id).
		Scan(&rec.ReconcileID, &rec.ReconcileDate, &rec.Channel, &rec.ExpectedCents, &rec.ActualCents, &rec.DiffCents, &rec.Status)
	if err == sql.ErrNoRows {
		fail(w, http.StatusNotFound, "NOT_FOUND", "reconcile not found")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, rec)
}
