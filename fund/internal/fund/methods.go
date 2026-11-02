package fund

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"
)

// promoteEarliestDefault：把该用户最早绑定的剩余正常方式升为默认（删默认/取消默认时的递补）。
// excludeID 排除正被解绑或取消默认的那条；无剩余方式时返回 ""。
func promoteEarliestDefault(ctx context.Context, tx *sql.Tx, userID, excludeID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx,
		`SELECT method_id FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 AND method_id<>$2 ORDER BY bind_time LIMIT 1 FOR UPDATE`, userID, excludeID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET is_default=TRUE, updated_at=now() WHERE method_id=$1`, id); err != nil {
		return "", err
	}
	return id, nil
}

type WithdrawMethod struct {
	MethodID   string    `json:"method_id"`
	UserID     string    `json:"user_id"`
	Type       int       `json:"type"`
	RealName   string    `json:"real_name"`
	AccountNo  string    `json:"account_no"` // 脱敏：**** + 后 4 位，永不返回完整卡号；二维码方式下为空
	BankName   *string   `json:"bank_name"`
	BankBranch *string   `json:"bank_branch"`
	QRFileID   *string   `json:"qr_file_id,omitempty"`
	QRURL      *string   `json:"qr_url,omitempty"` // 鉴权取用 /v1/uploads/<file_id>，绝不暴露磁盘路径
	IsDefault  bool      `json:"is_default"`
	Status     int       `json:"status"`
	BindTime   time.Time `json:"bind_time"`
}

func (s *Server) listMethods(ctx context.Context, userID string) ([]WithdrawMethod, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT method_id, user_id, type, real_name, account_no_enc, bank_name, bank_branch, qr_file_id, is_default, status, bind_time
		 FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 ORDER BY bind_time`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WithdrawMethod{}
	for rows.Next() {
		var m WithdrawMethod
		var enc, qr sql.NullString
		if err := rows.Scan(&m.MethodID, &m.UserID, &m.Type, &m.RealName, &enc, &m.BankName, &m.BankBranch, &qr, &m.IsDefault, &m.Status, &m.BindTime); err != nil {
			return nil, err
		}
		if enc.Valid {
			m.AccountNo = maskFromEnc(s, enc.String)
		}
		if qr.Valid && qr.String != "" {
			q := qr.String
			m.QRFileID = &q
			m.QRURL = qrURL(m.QRFileID)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// validateWithdrawQR：二维码文件必须已上传且为 qr 类型，且未被其他正常方式绑定。
func (s *Server) validateWithdrawQR(ctx context.Context, qrFileID, excludeMethodID string) error {
	var kind string
	if err := s.db.QueryRowContext(ctx,
		`SELECT kind FROM fund_upload_file WHERE file_id=$1`, qrFileID).Scan(&kind); err != nil {
		return errors.New("qr file not found")
	}
	if kind != uploadKindQR {
		return errors.New("qr file type is invalid")
	}
	var other string
	err := s.db.QueryRowContext(ctx,
		`SELECT method_id FROM wallet_withdraw_method WHERE qr_file_id=$1 AND status=0 AND method_id<>$2`, qrFileID, excludeMethodID).Scan(&other)
	if err == nil {
		return errors.New("qr code is already bound to another withdraw method")
	}
	if err != sql.ErrNoRows {
		return err
	}
	return nil
}

var bankCardRe = regexp.MustCompile(`^[0-9]{12,19}$`)

func validAccountNo(typ int, accountNo string) bool {
	if accountNo == "" || len(accountNo) > 128 {
		return false
	}
	if typ == MethodBank {
		return bankCardRe.MatchString(accountNo)
	}
	return true
}

func (s *Server) addMethod(ctx context.Context, userID string, typ int, realName, accountNo, bankName, bankBranch string, qrFileID string, wantDefault bool) (*WithdrawMethod, error) {
	if typ != MethodAlipay && typ != MethodWechat && typ != MethodBank {
		return nil, errors.New("invalid withdraw method type")
	}
	// 方式与凭证二选一：银行卡只能填账号（且须开户银行）；微信/支付宝填账号或传收款二维码二选一，
	// 二者都填或都不填一律拒绝，二维码不代表商户 API 自动打款，只供人工打款时查看。
	if typ == MethodBank {
		if qrFileID != "" {
			return nil, errors.New("bank card does not support QR code")
		}
		if realName == "" || !validAccountNo(typ, accountNo) {
			return nil, errors.New("real_name is required and account_no format is invalid for this method type")
		}
		if bankName == "" {
			return nil, errors.New("bank_name is required for bank card")
		}
	} else {
		if realName == "" {
			return nil, errors.New("real_name is required")
		}
		if (accountNo == "") == (qrFileID == "") {
			return nil, errors.New("exactly one of account_no or qr_file_id is required for this method type")
		}
		if accountNo != "" && !validAccountNo(typ, accountNo) {
			return nil, errors.New("account_no format is invalid for this method type")
		}
	}
	var enc string
	if accountNo != "" {
		var err error
		enc, err = s.encryptAccountNo(accountNo)
		if err != nil {
			return nil, err
		}
	}
	methodID := newID("mtd_")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// 每种方式类型每账号最多绑一次：同用户同类型已有正常方式时直接拦下（不再是三选多绑多条）。
	var typeCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 AND type=$2`, userID, typ).Scan(&typeCount); err != nil {
		return nil, err
	}
	if typeCount > 0 {
		return nil, errors.New("该方式已绑定，如需解绑或修改请联系客服")
	}
	if qrFileID != "" {
		if err := s.validateWithdrawQR(ctx, qrFileID, ""); err != nil {
			return nil, err
		}
	}
	// 提现实名一致性（0014，原 0013）：首绑姓名为基准，后续方式姓名必须与首绑一致。
	if base, err := baselineRealName(ctx, tx, userID); err != nil {
		return nil, err
	} else if base != "" && normalizeName(realName) != normalizeName(base) {
		return nil, errors.New("须与第一次绑定姓名一致")
	}
	// 同用户同类型同账号去重：密文带随机 nonce 无法靠唯一索引，须解密比对（行锁防并发重复绑定）。
	// 账号为空（二维码方式）时跳过本段，上面的一型一次校验已覆盖去重。
	if accountNo != "" {
		rows, err := tx.QueryContext(ctx, `SELECT account_no_enc FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 AND type=$2 FOR UPDATE`, userID, typ)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var e sql.NullString
			if err := rows.Scan(&e); err != nil {
				rows.Close()
				return nil, err
			}
			if e.Valid {
				if plain, err := s.decryptAccountNo(e.String); err == nil && plain == accountNo {
					rows.Close()
					return nil, errors.New("this account is already bound")
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	// 首个正常方式自动设默认；显式要求默认时清旧默认。partial unique index 为最后一道防线。
	var existingDefault int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 AND is_default`, userID).Scan(&existingDefault); err != nil {
		return nil, err
	}
	isDefault := wantDefault || existingDefault == 0
	if isDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET is_default=FALSE, updated_at=now() WHERE user_id=$1 AND status=0`, userID); err != nil {
			return nil, err
		}
	}
	var encVal any = enc
	if accountNo == "" {
		encVal = nil // 二维码方式：账号密文留空，提现仍按 method_id 走，不读账号
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_withdraw_method (method_id, user_id, type, real_name, account_no_enc, bank_name, bank_branch, qr_file_id, is_default, status)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,0)`,
		methodID, userID, typ, realName, encVal, nullStr(bankName), nullStr(bankBranch), nullStr(qrFileID), isDefault); err != nil {
		return nil, err
	}
	masked := "qr"
	if accountNo != "" {
		masked = maskAccountNo(accountNo)
	}
	if err := insertAudit(ctx, tx, userID, userID, "withdraw_method_add", "method type="+itoa(typ)+" masked="+masked, "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	methods, err := s.listMethods(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, m := range methods {
		if m.MethodID == methodID {
			return &m, nil
		}
	}
	return nil, sql.ErrNoRows
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [16]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// updateMethod / deleteMethod / setDefaultMethod 均须先验交易密码（调用方在 HTTP 层完成 requireTxPassword）。
// 注意：App 新规则下已绑定方式不再从界面提供修改/解绑入口（只留设默认），本接口保留供服务端
// 兼容与未来恢复使用；账号与二维码仍互斥，切换方式时旧凭证由 HTTP 层清理旧二维码文件。
func (s *Server) updateMethod(ctx context.Context, userID, methodID string, realName, accountNo, bankName, bankBranch *string, qrFileID *string, wantDefault *bool) (*WithdrawMethod, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var owner string
	var status int
	var wasDefault bool
	var mtype int
	var curEnc, curQR sql.NullString
	var curName string
	if err := tx.QueryRowContext(ctx, `SELECT user_id, status, is_default, type, account_no_enc, qr_file_id, real_name FROM wallet_withdraw_method WHERE method_id=$1 FOR UPDATE`, methodID).Scan(&owner, &status, &wasDefault, &mtype, &curEnc, &curQR, &curName); err != nil {
		return nil, err
	}
	if owner != userID || status != 0 {
		return nil, errors.New("withdraw method not found or not active")
	}
	// 提现实名一旦绑定不得修改（0014，原 0013）：改名一律拒绝；同名或不传姓名放行。
	if realName != nil && normalizeName(*realName) != normalizeName(curName) {
		return nil, errors.New("姓名已绑定，不得修改")
	}
	if accountNo != nil && *accountNo != "" && !validAccountNo(mtype, *accountNo) {
		return nil, errors.New("account_no format is invalid for this method type")
	}
	if qrFileID != nil && *qrFileID != "" {
		if mtype == MethodBank {
			return nil, errors.New("bank card does not support QR code")
		}
		if accountNo != nil && *accountNo != "" {
			return nil, errors.New("exactly one of account_no or qr_file_id is required for this method type")
		}
		if err := s.validateWithdrawQR(ctx, *qrFileID, methodID); err != nil {
			return nil, err
		}
	}
	if qrFileID != nil && *qrFileID == "" && (accountNo == nil || *accountNo == "") && (!curEnc.Valid || curEnc.String == "") {
		return nil, errors.New("account_no or qr_file_id is required")
	}
	if wantDefault != nil && *wantDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET is_default=FALSE, updated_at=now() WHERE user_id=$1 AND status=0`, userID); err != nil {
			return nil, err
		}
	}
	// 动态更新：只改传入的非空字段；account_no 传入时重新加密。
	if realName != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET real_name=$1, updated_at=now() WHERE method_id=$2`, *realName, methodID); err != nil {
			return nil, err
		}
	}
	if accountNo != nil && *accountNo != "" {
		enc, err := s.encryptAccountNo(*accountNo)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET account_no_enc=$1, qr_file_id=NULL, updated_at=now() WHERE method_id=$2`, enc, methodID); err != nil {
			return nil, err
		}
	}
	if qrFileID != nil {
		if *qrFileID == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET qr_file_id=NULL, updated_at=now() WHERE method_id=$2`, methodID); err != nil {
				return nil, err
			}
		} else {
			if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET qr_file_id=$1, account_no_enc=NULL, updated_at=now() WHERE method_id=$2`, *qrFileID, methodID); err != nil {
				return nil, err
			}
		}
	}
	if bankName != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET bank_name=$1, updated_at=now() WHERE method_id=$2`, nullStr(*bankName), methodID); err != nil {
			return nil, err
		}
	}
	if bankBranch != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET bank_branch=$1, updated_at=now() WHERE method_id=$2`, nullStr(*bankBranch), methodID); err != nil {
			return nil, err
		}
	}
	if wantDefault != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET is_default=$1, updated_at=now() WHERE method_id=$2`, *wantDefault, methodID); err != nil {
			return nil, err
		}
		// 取消当前默认时自动递补最早绑定的剩余方式，保证始终有默认（与解绑递补一致）。
		if !*wantDefault && wasDefault {
			if _, err := promoteEarliestDefault(ctx, tx, userID, methodID); err != nil {
				return nil, err
			}
		}
	}
	if err := insertAudit(ctx, tx, userID, userID, "withdraw_method_update", "method_id="+methodID, "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	methods, err := s.listMethods(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, m := range methods {
		if m.MethodID == methodID {
			return &m, nil
		}
	}
	return nil, sql.ErrNoRows
}

type DeleteMethodResult struct {
	Deleted            bool    `json:"deleted"`
	WasLastMethod      bool    `json:"was_last_method"`
	NewDefaultMethodID *string `json:"new_default_method_id"`
}

// deleteMethod：解绑（软删）。删的是默认方式且有剩余方式时，自动把最早绑定的剩余方式
// 升为默认；删唯一方式时允许，返回 was_last_method=true，准入随即变 need_method
// （Phase 9 提现提交时仍须重查准入与方式存在性）。
func (s *Server) deleteMethod(ctx context.Context, userID, methodID string) (*DeleteMethodResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var owner string
	var status int
	var wasDefault bool
	if err := tx.QueryRowContext(ctx, `SELECT user_id, status, is_default FROM wallet_withdraw_method WHERE method_id=$1 FOR UPDATE`, methodID).Scan(&owner, &status, &wasDefault); err != nil {
		return nil, err
	}
	if owner != userID || status != 0 {
		return nil, errors.New("withdraw method not found or not active")
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 AND method_id<>$2`, userID, methodID).Scan(&remaining); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET status=1, is_default=FALSE, updated_at=now() WHERE method_id=$1`, methodID); err != nil {
		return nil, err
	}
	res := &DeleteMethodResult{Deleted: true, WasLastMethod: remaining == 0}
	if wasDefault && remaining > 0 {
		next, err := promoteEarliestDefault(ctx, tx, userID, methodID)
		if err != nil {
			return nil, err
		}
		if next != "" {
			res.NewDefaultMethodID = &next
		}
	}
	if err := insertAudit(ctx, tx, userID, userID, "withdraw_method_delete", "method_id="+methodID, "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Server) setDefaultMethod(ctx context.Context, userID, methodID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	var status int
	if err := tx.QueryRowContext(ctx, `SELECT user_id, status FROM wallet_withdraw_method WHERE method_id=$1 FOR UPDATE`, methodID).Scan(&owner, &status); err != nil {
		return err
	}
	if owner != userID || status != 0 {
		return errors.New("withdraw method not found or not active")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET is_default=FALSE, updated_at=now() WHERE user_id=$1 AND status=0`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE wallet_withdraw_method SET is_default=TRUE, updated_at=now() WHERE method_id=$1`, methodID); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, userID, userID, "withdraw_method_set_default", "method_id="+methodID, "", ""); err != nil {
		return err
	}
	return tx.Commit()
}

// 统一准入检查（全部服务端判断；顺序：先绑方式、再设密码；两项齐 + 账户正常才放行）。
// NextStep 供客户端引导：bind_method（去绑提现方式）/ set_password（去设交易密码）/
// none（已完成）/ contact_support（账户冻结或注销，须联系客服）。
// 业务方（红包/转账/提现提交）必须先查本状态：非 ok 时只返回引导，绝不擅自扣款、
// 不改变业务单据状态、不判成功。
type FinanceStatus struct {
	HasWithdrawalMethod    bool   `json:"has_withdrawal_method"`
	HasTransactionPassword bool   `json:"has_transaction_password"`
	FinanceAccountStatus   string `json:"finance_account_status"` // ok | need_method | need_password | frozen | closed
	WalletStatus           int    `json:"wallet_status"`
	MethodCount            int    `json:"method_count"`
	HasDefaultMethod       bool   `json:"has_default_method"`
	NextStep               string `json:"next_step"`
	Guidance               string `json:"guidance"`
}

func (s *Server) financeStatus(ctx context.Context, userID string) (*FinanceStatus, error) {
	acct, err := s.getAccount(ctx, userID)
	if err != nil {
		return nil, err
	}
	var methodCount int
	var defaultCount int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wallet_withdraw_method WHERE user_id=$1 AND status=0`, userID).Scan(&methodCount); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 AND is_default`, userID).Scan(&defaultCount); err != nil {
		return nil, err
	}
	pwd, err := s.txPasswordStatus(ctx, userID)
	if err != nil {
		return nil, err
	}
	st := &FinanceStatus{
		HasWithdrawalMethod:    methodCount > 0,
		HasTransactionPassword: pwd.HasPassword,
		WalletStatus:           acct.Status,
		MethodCount:            methodCount,
		HasDefaultMethod:       defaultCount > 0,
	}
	switch {
	case acct.Status == 1:
		st.FinanceAccountStatus = "frozen"
		st.NextStep = "contact_support"
		st.Guidance = "资金账户已冻结，请联系客服处理"
	case acct.Status == 2:
		st.FinanceAccountStatus = "closed"
		st.NextStep = "contact_support"
		st.Guidance = "资金账户已注销，请联系客服处理"
	case !st.HasWithdrawalMethod:
		st.FinanceAccountStatus = "need_method"
		st.NextStep = "bind_method"
		st.Guidance = "请先绑定一种提现方式（支付宝、微信支付或银行卡）"
	case !st.HasTransactionPassword:
		st.FinanceAccountStatus = "need_password"
		st.NextStep = "set_password"
		st.Guidance = "请先设置 6 位交易密码"
	default:
		st.FinanceAccountStatus = "ok"
		st.NextStep = "none"
		st.Guidance = ""
	}
	return st, nil
}

// requireAdmission：业务方资金操作前的统一守卫（红包/转账/提现提交时调用）。
// 非 ok 返回业务错误，调用方须把 Guidance 原样返回客户端做引导，不得继续扣款。
func (s *Server) requireAdmission(ctx context.Context, userID string) error {
	st, err := s.financeStatus(ctx, userID)
	if err != nil {
		return err
	}
	if st.FinanceAccountStatus != "ok" {
		return errors.New("finance admission not satisfied: " + st.FinanceAccountStatus + " - " + st.Guidance)
	}
	return nil
}
