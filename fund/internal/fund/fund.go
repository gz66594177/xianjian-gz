// Package fund 实现仙聊 Phase 1 独立资金服务：人民币钱包、统一账务核心、
// 交易密码与提现账户基础服务。金额全部 int64 分，禁浮点。
// 安全要点：密钥只来自环境变量（fund.env），绝不进仓库；敏感字段只脱敏返回与记录。
package fund

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const PlatformUserID = "platform:settlement"

const (
	AcctAvailable = 1
	AcctFrozen    = 2
	DirDebit      = 1 // 借，带符号为负
	DirCredit     = 2 // 贷，带符号为正
)

const (
	TxnIncome         = 1
	TxnExpense        = 2
	TxnFreeze         = 3
	TxnUnfreeze       = 4
	TxnFreezeCharge   = 5
	TxnFreezeRefund   = 6
	TxnRefund         = 7
	TxnReversal       = 8
	TxnTransfer       = 18 // Phase 6 转账（转出方可用 DR / 转入方可用 CR；冲正复用已有 type 8，不另立 type 19）
	TxnRecharge       = 20 // Phase 8 充值入账（用户可用 CR / 平台结算 DR）
	TxnRechargeRefund = 21 // Phase 8 充值退款（用户可用 DR / 平台结算 CR，仅 mock 渠道演练）
	TxnWithdrawSubmit = 22 // Phase 9 提现提交冻结（用户可用 DR / 用户冻结 CR，金额 + 手续费）
	TxnWithdrawReject = 23 // Phase 9 审核拒绝解冻（用户冻结 DR / 用户可用 CR，金额 + 手续费）
	TxnWithdrawPayout = 24 // Phase 9 人工打款确认成功（用户冻结 DR / 平台结算 CR，金额 + 手续费）
	TxnWithdrawRefund = 25 // Phase 9 渠道失败后退款解冻（用户冻结 DR / 用户可用 CR，金额 + 手续费）
)

const (
	MethodAlipay = 1
	MethodWechat = 2
	MethodBank   = 3
)

type Config struct {
	Addr          string
	DatabaseURL   string
	RedisAddr     string
	AdminSecret   string
	AESKey        []byte // 32 字节
	IMApiBase     string // OpenIM server API，如 http://127.0.0.1:10002；为空则拒绝启动（Phase 2 起用户鉴权必须走 IM token）
	IMAPITimeout  time.Duration
	TokenVerifier TokenVerifier // 测试注入；非空时优先于 IMApiBase
	// Phase 8 Mock 支付开关：仅测试/开发模式可用。FUND_MOCK_PAYMENT=1 时注册 mock 回调入口；
	// 生产 fund.env 必须不开启该开关（见 API 文档 §13 部署检查清单）。
	MockPaymentEnabled bool
	MockCallbackSecret string // mock 回调 HMAC 密钥；开关开启时必填，否则拒绝启动
	// 手动充值截图与收款二维码存储目录（FUND_UPLOAD_DIR，默认 /opt/xianliao/fund-uploads）。
	UploadDir string
	// 网关支付适配器：为空 = 未配置真实网关，网关通道返回“暂未开通”，绝不生成假收银台。
	// 测试注入 stub；未来签约厂商时实现 GatewayAdapter 接口并注入。
	GatewayAdapter GatewayAdapter
}

func ConfigFromEnv() (Config, error) {
	var c Config
	c.Addr = getenv("FUND_ADDR", "127.0.0.1:18081")
	c.DatabaseURL = os.Getenv("FUND_DATABASE_URL")
	c.RedisAddr = os.Getenv("FUND_REDIS_ADDR")
	c.AdminSecret = os.Getenv("FUND_ADMIN_SECRET")
	c.IMApiBase = strings.TrimSpace(os.Getenv("FUND_IM_API_BASE"))
	if ms := strings.TrimSpace(os.Getenv("FUND_IM_API_TIMEOUT_MS")); ms != "" {
		if n, err := time.ParseDuration(ms + "ms"); err == nil {
			c.IMAPITimeout = n
		}
	}
	if c.DatabaseURL == "" {
		return c, errors.New("FUND_DATABASE_URL is empty")
	}
	if c.AdminSecret == "" {
		return c, errors.New("FUND_ADMIN_SECRET must be set")
	}
	if c.IMApiBase == "" {
		return c, errors.New("FUND_IM_API_BASE is empty: user auth requires IM parse_token since Phase 2")
	}
	if v := strings.TrimSpace(os.Getenv("FUND_MOCK_PAYMENT")); v == "1" || strings.EqualFold(v, "true") {
		c.MockPaymentEnabled = true
	}
	c.MockCallbackSecret = strings.TrimSpace(os.Getenv("FUND_MOCK_PAY_CALLBACK_SECRET"))
	if c.MockPaymentEnabled && c.MockCallbackSecret == "" {
		return c, errors.New("FUND_MOCK_PAYMENT=1 requires FUND_MOCK_PAY_CALLBACK_SECRET: mock callback refuses to start unsigned")
	}
	c.UploadDir = getenv("FUND_UPLOAD_DIR", "/opt/xianliao/fund-uploads")
	raw := strings.TrimSpace(os.Getenv("FUND_AES_KEY_B64"))
	if raw == "" {
		return c, errors.New("FUND_AES_KEY_B64 is empty: service refuses to start without AES key")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return c, fmt.Errorf("decode FUND_AES_KEY_B64: %w", err)
	}
	if len(key) != 32 {
		return c, fmt.Errorf("AES key must be 32 bytes, got %d", len(key))
	}
	c.AESKey = key
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type Server struct {
	cfg       Config
	db        *sql.DB
	rdb       *redis.Client
	log       *log.Logger
	verifier  TokenVerifier
	verifiers map[string]PaymentVerifier // Phase 8：渠道回调验签器注册表（mock 仅开关开启时注册）
	gateway   GatewayAdapter             // 网关支付适配器；为空 = 未配置真实网关（通道关闭）
}

func NewServer(cfg Config, db *sql.DB) *Server {
	var rdb *redis.Client
	if cfg.RedisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		if err := rdb.Ping(context.Background()).Err(); err != nil {
			// Redis 不可用时降级为纯 DB 保证并记日志，绝不以 Redis 为准出账。
			log.Printf("[fund] WARN redis unavailable, degraded to DB-only mode: %v", err)
		}
	}
	v := cfg.TokenVerifier
	if v == nil && cfg.IMApiBase != "" {
		v = &HTTPTokenVerifier{BaseURL: cfg.IMApiBase, Timeout: cfg.IMAPITimeout}
	}
	verifiers := map[string]PaymentVerifier{
		ChannelAlipay: UnconfiguredVerifier{Channel: ChannelAlipay},
		ChannelWechat: UnconfiguredVerifier{Channel: ChannelWechat},
	}
	if cfg.MockPaymentEnabled && cfg.MockCallbackSecret != "" {
		verifiers[ChannelMock] = HMACVerifier{Secret: []byte(cfg.MockCallbackSecret)}
	}
	uploadDir := cfg.UploadDir
	if uploadDir == "" {
		uploadDir = "/opt/xianliao/fund-uploads"
	}
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		log.Printf("[fund] WARN upload dir unavailable %s: %v", uploadDir, err)
	}
	return &Server{cfg: cfg, db: db, rdb: rdb, log: log.New(os.Stdout, "[fund] ", log.LstdFlags), verifier: v, verifiers: verifiers, gateway: cfg.GatewayAdapter}
}

func (s *Server) DB() *sql.DB { return s.db }

// ---- 通用工具 ----

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b) + fmt.Sprintf("%d", time.Now().UnixNano()%1000000)
}

var userIDRe = regexp.MustCompile(`^[A-Za-z0-9_:\-]{1,128}$`)

func validUserID(id string) bool { return userIDRe.MatchString(id) }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": data})
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": msg}})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Body == nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "empty body")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body: "+err.Error())
		return false
	}
	return true
}

// 鉴权见 auth.go：用户接口用 IM token（parse_token），管理接口用 X-Admin-Secret。

// bodyUserIDMismatch：请求体中若含 user_id 字段且与请求头不一致则越权。
func bodyUserIDMismatch(raw map[string]any, headerUID string) bool {
	if v, ok := raw["user_id"]; ok {
		if str, ok := v.(string); ok && str != "" && str != headerUID {
			return true
		}
	}
	return false
}

// ---- AES-GCM 卡号加密（密钥只来自 fund.env） ----

func (s *Server) encryptAccountNo(plain string) (string, error) {
	block, err := aes.NewCipher(s.cfg.AESKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (s *Server) decryptAccountNo(enc string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.cfg.AESKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// maskAccountNo：**** + 后 4 位；长度不足 4 位只给 ****。
func maskAccountNo(plain string) string {
	if len(plain) <= 4 {
		return "****"
	}
	return "****" + plain[len(plain)-4:]
}

func maskFromEnc(s *Server, enc string) string {
	plain, err := s.decryptAccountNo(enc)
	if err != nil {
		return "****"
	}
	return maskAccountNo(plain)
}

// ---- 审计日志（detail 必须已脱敏，调用方负责） ----

func insertAudit(ctx context.Context, tx *sql.Tx, userID, operatorID, action, detailMasked, ip, device string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO wallet_audit_log (user_id, operator_id, action, detail_masked, ip, device) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, nullStr(operatorID), action, nullStr(detailMasked), nullStr(ip), nullStr(device))
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) auditStandalone(ctx context.Context, userID, operatorID, action, detailMasked string, r *http.Request) {
	ip := ""
	if r != nil {
		ip = r.RemoteAddr
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO wallet_audit_log (user_id, operator_id, action, detail_masked, ip) VALUES ($1,$2,$3,$4,$5)`,
		userID, nullStr(operatorID), action, nullStr(detailMasked), nullStr(ip))
	if err != nil {
		s.log.Printf("audit insert failed action=%s user=%s err=%v", action, userID, err)
	}
}
