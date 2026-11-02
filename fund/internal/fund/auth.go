package fund

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// TokenVerifier：IM token 校验抽象。生产用 HTTPVerifier（调 OpenIM parse_token）；
// 测试用 stub，不依赖真实 IM 服务。
type TokenVerifier interface {
	VerifyToken(ctx context.Context, token string) (userID string, err error)
}

var errTokenInvalid = errors.New("invalid IM token")
var errVerifierDown = errors.New("IM token verifier unavailable")

// HTTPTokenVerifier：调 OpenIM server 的 POST /auth/parse_token（白名单接口，无需 admin token，
// 但须带 operationID 头）。返回 data.userID；errCode != 0 视为无效 token。
// 注意：IM 的错误信息可能回显 token 原文，本实现绝不把 token 写入日志。
type HTTPTokenVerifier struct {
	BaseURL string // 如 http://127.0.0.1:10002
	Timeout time.Duration
	Client  *http.Client
}

func (v *HTTPTokenVerifier) VerifyToken(ctx context.Context, token string) (string, error) {
	if v.BaseURL == "" {
		return "", errVerifierDown
	}
	timeout := v.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"token": token})
	url := strings.TrimRight(v.BaseURL, "/") + "/auth/parse_token"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return "", errVerifierDown
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("operationID", "fund-"+newID("op"))
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errVerifierDown
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", errVerifierDown
	}
	var env struct {
		ErrCode int    `json:"errCode"`
		ErrMsg  string `json:"errMsg"`
		Data    struct {
			UserID            string `json:"userID"`
			ExpireTimeSeconds int64  `json:"expireTimeSeconds"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", errVerifierDown
	}
	if env.ErrCode != 0 || env.Data.UserID == "" {
		return "", errTokenInvalid
	}
	if env.Data.ExpireTimeSeconds > 0 && env.Data.ExpireTimeSeconds < time.Now().Unix() {
		return "", errTokenInvalid
	}
	return env.Data.UserID, nil
}

// bearerToken：只接受 Authorization: Bearer <token>；X-Internal-Secret 已废除。
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

// authUser：IM token 校验。返回 (userID, ok, httpCode, msg)。
// 401：缺 token / token 无效；403：平台结算户；503：IM 校验服务不可达（绝不 fail-open）。
func (s *Server) authUser(r *http.Request) (string, bool, int, string) {
	token, ok := bearerToken(r)
	if !ok {
		return "", false, http.StatusUnauthorized, "missing Authorization Bearer IM token"
	}
	if s.verifier == nil {
		return "", false, http.StatusServiceUnavailable, "IM token verifier not configured"
	}
	uid, err := s.verifier.VerifyToken(r.Context(), token)
	if err != nil {
		if errors.Is(err, errVerifierDown) {
			return "", false, http.StatusServiceUnavailable, "IM token service unavailable"
		}
		return "", false, http.StatusUnauthorized, "invalid IM token"
	}
	if uid == PlatformUserID {
		return "", false, http.StatusForbidden, "platform settlement account is not operable via API"
	}
	if !validUserID(uid) {
		return "", false, http.StatusUnauthorized, "invalid IM token subject"
	}
	return uid, true, 0, ""
}

func (s *Server) authAdmin(r *http.Request) bool {
	return r.Header.Get("X-Admin-Secret") != "" && r.Header.Get("X-Admin-Secret") == s.cfg.AdminSecret
}

// authAdminID：双人复核类管理操作专用：管理密钥 + X-Admin-ID（区分两名管理员）。
// 返回 (adminID, ok)。普通管理接口仍用 authAdmin（X-Admin-ID 可选，审计时有则记录）。
func (s *Server) authAdminID(r *http.Request) (string, bool) {
	if !s.authAdmin(r) {
		return "", false
	}
	id := strings.TrimSpace(r.Header.Get("X-Admin-ID"))
	if id == "" {
		return "", false
	}
	return id, true
}

func (s *Server) adminOperator(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Admin-ID")); id != "" {
		return id
	}
	return "admin"
}
