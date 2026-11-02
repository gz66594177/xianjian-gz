package fund

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"time"
)

// 0013：邮箱注册用户首次进页强制绑定手机号 + 提现实名一致性。
//
// 手机号存储：明文绝不落库。phone_enc 用资金服务现有 AES 加密（与账号密文同一密钥
// 体系），phone_hash = SHA256(area_code + "|" + phone) 供唯一约束（密文带随机
// nonce，无法直接建唯一索引）。审计日志只记动作，不记号码原文。
// “真实性”边界：本接口只做格式 + 服务端唯一；短信验证码核验在 App 层复用聊天
// 服务 account/code/send + account/code/verify（usedFor=1），资金服务本身没有
// 聊天验证码校验接口，无法在服务端二次核验验证码——直接调本接口而不走 App
// 验证码流程是已知残口（须持本人 IM token 才能调，且唯一约束仍防抢绑已绑号码，
// 但防不住给自己账号绑一个未实名验证的号码），见报告。

var (
	areaCodeRe = regexp.MustCompile(`^\+[0-9]{1,4}$`)
	phoneRe    = regexp.MustCompile(`^[0-9]{6,15}$`)
)

func phoneHash(areaCode, phone string) string {
	sum := sha256.Sum256([]byte(areaCode + "|" + phone))
	return hex.EncodeToString(sum[:])
}

func normalizeName(s string) string {
	return strings.TrimSpace(s)
}

// baselineRealName：该用户最早绑定的正常提现方式姓名；无方式时返回 ""。
func baselineRealName(ctx context.Context, tx *sql.Tx, userID string) (string, error) {
	var name sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT real_name FROM wallet_withdraw_method WHERE user_id=$1 AND status=0 ORDER BY bind_time LIMIT 1 FOR UPDATE`,
		userID).Scan(&name)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return name.String, nil
}

type PhoneBinding struct {
	HasPhone   bool   `json:"has_phone"`
	AreaCode   string `json:"area_code,omitempty"`
	Masked     string `json:"masked_phone,omitempty"`
	BoundAt    string `json:"bound_at,omitempty"`
}

// getPhoneBinding：只返回是否已绑 + 脱敏号码（前 3 后 4，中间 *），不返回完整号码。
func (s *Server) getPhoneBinding(ctx context.Context, userID string) (*PhoneBinding, error) {
	var area, enc string
	var boundAt sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT area_code, phone_enc, bound_at FROM user_phone_binding WHERE user_id=$1`, userID).Scan(&area, &enc, &boundAt)
	if err == sql.ErrNoRows {
		return &PhoneBinding{HasPhone: false}, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := s.decryptAccountNo(enc)
	if err != nil {
		return &PhoneBinding{HasPhone: true, AreaCode: area}, nil
	}
	masked := plain
	if len(plain) >= 7 {
		masked = plain[:3] + "****" + plain[len(plain)-4:]
	} else {
		masked = "****"
	}
	out := &PhoneBinding{HasPhone: true, AreaCode: area, Masked: masked}
	if boundAt.Valid {
		out.BoundAt = boundAt.Time.Format(time.RFC3339)
	}
	return out, nil
}

// bindPhone：格式 + 唯一 + 一人一号。同一用户绑同一号码幂等成功；
// 同一用户绑不同号码、或号码已被他人绑定，均拒绝。
func (s *Server) bindPhone(ctx context.Context, userID, areaCode, phone string) (*PhoneBinding, error) {
	areaCode = strings.TrimSpace(areaCode)
	phone = strings.TrimSpace(phone)
	if !areaCodeRe.MatchString(areaCode) || !phoneRe.MatchString(phone) {
		return nil, errors.New("phone number format is invalid")
	}
	hash := phoneHash(areaCode, phone)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var curArea, curHash string
	err = tx.QueryRowContext(ctx,
		`SELECT area_code, phone_hash FROM user_phone_binding WHERE user_id=$1 FOR UPDATE`, userID).Scan(&curArea, &curHash)
	if err == nil {
		if curHash == hash {
			if err := tx.Commit(); err != nil {
				return nil, err
			}
			return s.getPhoneBinding(ctx, userID)
		}
		return nil, errors.New("phone number is already bound and cannot be changed, please contact support")
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var other string
	err = tx.QueryRowContext(ctx,
		`SELECT user_id FROM user_phone_binding WHERE phone_hash=$1 FOR UPDATE`, hash).Scan(&other)
	if err == nil {
		return nil, errors.New("该手机号已绑定其他账号")
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	enc, err := s.encryptAccountNo(phone)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO user_phone_binding (user_id, area_code, phone_enc, phone_hash) VALUES ($1,$2,$3,$4)`,
		userID, areaCode, enc, hash); err != nil {
		return nil, err
	}
	if err := insertAudit(ctx, tx, userID, userID, "phone_bind", "phone_bind", "", ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.getPhoneBinding(ctx, userID)
}
