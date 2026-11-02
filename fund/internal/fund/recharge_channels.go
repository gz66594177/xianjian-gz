package fund

// 充值通道总览（用户侧只读接口 GET /v1/recharge/channels）。
// 语义（与用户指令一致，后台开关为准）：
//   - 人工通道：只返回服务端已过滤的启用中收款方式（listPayMethodsUser 本就
//     只查 enabled=TRUE；App 端仍要再按 enabled 字段过滤一次兜底）。
//   - 网关通道：只有同时满足两点才算开启——① 存在至少一行 enabled=TRUE 的
//     网关通道配置（wallet_gateway_channel），② 服务端已注册真实厂商适配器
//     （s.gateway != nil）。任一不满足即关闭，绝不生成假收银台。
//   - 开关开了 ≠ 能收款：本接口只决定“显示与否”，建单时仍要过
//     gatewayChannelEnabled + 适配器两道门禁；开但无适配器仍建单 400。
//   - 响应里只有厂商与展示名，绝不含 params（更不含密钥；密钥只走服务端
//     环境变量，绝不进本接口、本表响应与日志）。
// 注意：本接口部署前，老生产服务端没有该路由，App 调它会走 NETWORK/404
// 兜底（见 App 侧 loadChannels）：网关 fail-closed 隐藏，人工走旧
// /v1/recharge/methods 列表。服务端部署前不要声称网关显示已生效。

import (
	"context"
	"net/http"
)

// GatewayChannelPublic：用户侧可见的网关通道（无 params、无密钥）。
type GatewayChannelPublic struct {
	Vendor      string `json:"vendor"`
	DisplayName string `json:"display_name"`
}

// RechargeChannels：充值通道总览响应。
type RechargeChannels struct {
	ManualAvailable  bool                   `json:"manual_available"`
	ManualMethods    []PaymentMethod        `json:"manual_methods"`
	GatewayAvailable bool                   `json:"gateway_available"`
	GatewayChannels  []GatewayChannelPublic `json:"gateway_channels"`
}

func (s *Server) rechargeChannels(ctx context.Context) (*RechargeChannels, error) {
	methods, err := s.listPayMethodsUser(ctx)
	if err != nil {
		return nil, err
	}
	if methods == nil {
		methods = []PaymentMethod{}
	}
	out := &RechargeChannels{
		ManualMethods:   methods,
		ManualAvailable: len(methods) > 0,
		GatewayChannels: []GatewayChannelPublic{},
	}
	// 无适配器 = 网关整体关闭，不论通道表里有没有启用行。
	if s.gateway == nil {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT vendor, display_name FROM wallet_gateway_channel WHERE enabled=TRUE ORDER BY vendor ASC`)
	if err != nil {
		// 网关表异常（如旧库未建 0012 表）时 fail-closed：只关网关，
		// 不连带毁掉人工通道的展示。
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var c GatewayChannelPublic
		if err := rows.Scan(&c.Vendor, &c.DisplayName); err != nil {
			return out, nil
		}
		out.GatewayChannels = append(out.GatewayChannels, c)
	}
	out.GatewayAvailable = len(out.GatewayChannels) > 0
	return out, nil
}

func (s *Server) hRechargeChannels(w http.ResponseWriter, r *http.Request) {
	if _, okAuth := s.userCtx(w, r); !okAuth {
		return
	}
	ch, err := s.rechargeChannels(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	ok(w, ch)
}
