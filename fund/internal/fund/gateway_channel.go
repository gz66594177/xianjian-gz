package fund

// 0012 网关充值通道配置：面板可配的厂商开关。
// 一行 = 一个厂商（vendor 为主键）。display_name 为面板展示名，enabled 为通道开关，
// params 只放非密钥参数（如商户号、回调地址等公开标识）。
// 密钥红线：API Key/私钥/签名密钥一律只走服务端环境变量，绝不写入本表、绝不经管理接口
// 返回、绝不进浏览器与日志。本文件在写入前对 params 键名做密钥类键名拒绝
// （含 secret/private/password/token 等子串的键一律 400），宁可误拒也不让密钥落库。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	errGatewayChannelNotFound  = errors.New("gateway channel not found")
	errGatewayChannelExists    = errors.New("gateway channel already exists")
	errGatewayChannelBadVendor = errors.New("vendor must be 1..64 chars of [a-z0-9_-]")
	errGatewayChannelBadName   = errors.New("display_name must be 1..128 chars")
	errGatewayChannelBadParams = errors.New("params must be a JSON object without secret-like keys")
)

var gatewayVendorRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// secretLikeKey：密钥类键名判定（小写后子串匹配，fail-closed）。
func secretLikeKey(key string) bool {
	lower := strings.ToLower(key)
	for _, sub := range []string{"secret", "apikey", "api_key", "private", "password", "passwd", "token", "access_key", "sign_key"} {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	return false
}

func checkGatewayParams(params map[string]any) error {
	for k := range params {
		if secretLikeKey(k) {
			return errGatewayChannelBadParams
		}
	}
	return nil
}

// GatewayChannel：网关通道配置行。响应里只有这些字段，绝无密钥字段。
type GatewayChannel struct {
	Vendor          string         `json:"vendor"`
	DisplayName     string         `json:"display_name"`
	Enabled         bool           `json:"enabled"`
	Params          map[string]any `json:"params"`
	CreatedByAdmin  *string        `json:"created_by_admin_id"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

const gatewayChannelColumns = `vendor, display_name, enabled, params, created_by_admin_id, created_at, updated_at`

func scanGatewayChannel(row interface{ Scan(...any) error }) (*GatewayChannel, error) {
	var c GatewayChannel
	var paramsRaw []byte
	var createdBy sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(&c.Vendor, &c.DisplayName, &c.Enabled, &paramsRaw, &createdBy, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	c.Params = map[string]any{}
	if len(paramsRaw) > 0 {
		_ = json.Unmarshal(paramsRaw, &c.Params)
	}
	if createdBy.Valid {
		c.CreatedByAdmin = &createdBy.String
	}
	c.CreatedAt = createdAt
	c.UpdatedAt = updatedAt
	return &c, nil
}

func (s *Server) getGatewayChannel(ctx context.Context, vendor string) (*GatewayChannel, error) {
	c, err := scanGatewayChannel(s.db.QueryRowContext(ctx,
		`SELECT `+gatewayChannelColumns+` FROM wallet_gateway_channel WHERE vendor=$1`, vendor))
	if err == sql.ErrNoRows {
		return nil, errGatewayChannelNotFound
	}
	return c, err
}

func (s *Server) listGatewayChannels(ctx context.Context) ([]GatewayChannel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+gatewayChannelColumns+` FROM wallet_gateway_channel ORDER BY vendor ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GatewayChannel{}
	for rows.Next() {
		c, err := scanGatewayChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Server) createGatewayChannel(ctx context.Context, adminID, vendor, displayName string, enabled bool, params map[string]any) (*GatewayChannel, error) {
	if !gatewayVendorRe.MatchString(vendor) {
		return nil, errGatewayChannelBadVendor
	}
	if strings.TrimSpace(displayName) == "" || utf8.RuneCountInString(displayName) > 128 {
		return nil, errGatewayChannelBadName
	}
	if params == nil {
		params = map[string]any{}
	}
	if err := checkGatewayParams(params); err != nil {
		return nil, err
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO wallet_gateway_channel (vendor, display_name, enabled, params, created_by_admin_id) VALUES ($1,$2,$3,$4,$5)`,
		vendor, displayName, enabled, string(paramsJSON), adminID); err != nil {
		if isUniqueViolation(err) {
			return nil, errGatewayChannelExists
		}
		return nil, err
	}
	s.auditStandalone(ctx, "", adminID, "admin_gateway_channel_create", "gateway channel "+vendor, nil)
	return s.getGatewayChannel(ctx, vendor)
}

func (s *Server) updateGatewayChannel(ctx context.Context, adminID, vendor string, displayName *string, enabled *bool, params *map[string]any) (*GatewayChannel, error) {
	if _, err := s.getGatewayChannel(ctx, vendor); err != nil {
		return nil, err
	}
	setParts := []string{}
	args := []any{}
	if displayName != nil {
		if strings.TrimSpace(*displayName) == "" || utf8.RuneCountInString(*displayName) > 128 {
			return nil, errGatewayChannelBadName
		}
		setParts = append(setParts, fmt.Sprintf("display_name=$%d", len(args)+1))
		args = append(args, *displayName)
	}
	if enabled != nil {
		setParts = append(setParts, fmt.Sprintf("enabled=$%d", len(args)+1))
		args = append(args, *enabled)
	}
	if params != nil {
		if err := checkGatewayParams(*params); err != nil {
			return nil, err
		}
		paramsJSON, err := json.Marshal(*params)
		if err != nil {
			return nil, err
		}
		setParts = append(setParts, fmt.Sprintf("params=$%d", len(args)+1))
		args = append(args, string(paramsJSON))
	}
	if len(setParts) == 0 {
		return s.getGatewayChannel(ctx, vendor)
	}
	setParts = append(setParts, "updated_at=now()")
	args = append(args, vendor)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE wallet_gateway_channel SET `+strings.Join(setParts, ", ")+fmt.Sprintf(` WHERE vendor=$%d`, len(args)), args...); err != nil {
		return nil, err
	}
	s.auditStandalone(ctx, "", adminID, "admin_gateway_channel_update", "gateway channel "+vendor, nil)
	return s.getGatewayChannel(ctx, vendor)
}

// gatewayChannelEnabled：建单门禁（vendor 作用域）。
//   - vendor 为空（老口径建单）：不查通道表，只走适配器门禁（生产无适配器 = 关）。
//     厂商接入时 App 必须传 vendor，否则不受通道开关约束——见 API 文档 §17。
//   - vendor 非空：行不存在或 enabled=false 一律关（fail-closed）；行 enabled=true 才放行到适配器门禁。
func (s *Server) gatewayChannelEnabled(ctx context.Context, vendor string) (bool, error) {
	if vendor == "" {
		return true, nil
	}
	c, err := s.getGatewayChannel(ctx, vendor)
	if err == errGatewayChannelNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return c.Enabled, nil
}

// ---- 管理接口（/v1/admin/gateway-channels*；密钥绝不经过本组接口） ----

type gatewayChannelCreateReq struct {
	Vendor      string         `json:"vendor"`
	DisplayName string         `json:"display_name"`
	Enabled     *bool          `json:"enabled"`
	Params      map[string]any `json:"params"`
}

func (s *Server) hAdminListGatewayChannels(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	list, err := s.listGatewayChannels(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, list)
}

func (s *Server) hAdminCreateGatewayChannel(w http.ResponseWriter, r *http.Request) {
	adminID, okAuth := s.authAdminID(r)
	if !okAuth {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret or missing X-Admin-ID")
		return
	}
	var req gatewayChannelCreateReq
	if !decodeBody(w, r, &req) {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	c, err := s.createGatewayChannel(r.Context(), adminID, req.Vendor, req.DisplayName, enabled, req.Params)
	if err != nil {
		gatewayChannelErr(w, err)
		return
	}
	ok(w, c)
}

func gatewayChannelIDFromPath(r *http.Request) string {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/admin/gateway-channels/")
	parts := strings.Split(rest, "/")
	if len(parts) == 1 && parts[0] != "" {
		return parts[0]
	}
	return ""
}

func (s *Server) hAdminGatewayChannelSub(w http.ResponseWriter, r *http.Request) {
	if !s.authAdmin(r) {
		fail(w, http.StatusUnauthorized, "UNAUTHORIZED", "bad admin secret")
		return
	}
	vendor := gatewayChannelIDFromPath(r)
	if vendor == "" {
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown gateway channel path")
		return
	}
	switch {
	case r.Method == http.MethodGet:
		c, err := s.getGatewayChannel(r.Context(), vendor)
		if err != nil {
			gatewayChannelErr(w, err)
			return
		}
		ok(w, c)
	case r.Method == http.MethodPut:
		var raw map[string]any
		body, _ := readAll(r)
		if len(body) > 0 {
			if err := json.Unmarshal(body, &raw); err != nil {
				fail(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json body")
				return
			}
		}
		var displayName *string
		var enabled *bool
		var params *map[string]any
		if v, ok := raw["display_name"].(string); ok {
			displayName = &v
		}
		if v, ok := raw["enabled"].(bool); ok {
			enabled = &v
		}
		if v, ok := raw["params"].(map[string]any); ok {
			params = &v
		}
		c, err := s.updateGatewayChannel(r.Context(), s.adminOperator(r), vendor, displayName, enabled, params)
		if err != nil {
			gatewayChannelErr(w, err)
			return
		}
		ok(w, c)
	default:
		fail(w, http.StatusNotFound, "NOT_FOUND", "unknown gateway channel path")
	}
}

func gatewayChannelErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errGatewayChannelNotFound):
		fail(w, http.StatusNotFound, "GATEWAY_CHANNEL_NOT_FOUND", err.Error())
	case errors.Is(err, errGatewayChannelExists):
		fail(w, http.StatusConflict, "GATEWAY_CHANNEL_EXISTS", err.Error())
	case errors.Is(err, errGatewayChannelBadVendor) || errors.Is(err, errGatewayChannelBadName) || errors.Is(err, errGatewayChannelBadParams):
		fail(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
	default:
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
	}
}
