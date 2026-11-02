package fund

// Phase 8：支付回调验签框架。
// 验签器做成接口（PaymentVerifier），回调入口只认验签结果，不认客户端断言：
// 验签失败一律拒绝入账（401），订单与资金均不动。
// 本阶段只实现 HMAC mock 渠道验签器（测试/开发模式专用，见 FUND_MOCK_PAYMENT 开关）；
// 真实支付宝/微信验签器只写接口骨架（UnconfiguredVerifier，恒返回未配置，回调入口回 501），
// 绝不连真实渠道端点、不写真实商户密钥。商户签约后的接入清单见 API 文档 §13。

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	errSignatureInvalid   = errors.New("invalid callback signature")
	errChannelNotConfigured = errors.New("payment channel verifier not configured: merchant signup pending")
)

// PaymentVerifier：渠道回调验签抽象。payload 由各渠道 handler 按该渠道规约构造后传入；
// mock 渠道为 canonical 字符串（见 MockCallbackPayload）；真实渠道接入时传入渠道原始报文
// 与签名头，由对应验签器按官方规约验签。
type PaymentVerifier interface {
	VerifySignature(channel string, payload []byte, signature string) error
}

// HMACVerifier：mock 渠道验签器（测试用）：HMAC-SHA256(secret, payload)，hex 编码比对（恒时比较）。
// secret 只来自环境变量 FUND_MOCK_PAY_CALLBACK_SECRET；mock 开关关闭时本验签器不注册，回调入口直接 404。
type HMACVerifier struct {
	Secret []byte
}

func (v HMACVerifier) VerifySignature(channel string, payload []byte, signature string) error {
	if len(v.Secret) == 0 || signature == "" {
		return errSignatureInvalid
	}
	mac := hmac.New(sha256.New, v.Secret)
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(signature)) {
		return errSignatureInvalid
	}
	return nil
}

// MockCallbackPayload：mock 回调签名的 canonical 载荷。字段顺序固定，杜绝 JSON 字段重排导致验签不一致；
// 金额与状态均参与签名，回调体被篡改任一字段即验签失败。
func MockCallbackPayload(orderID, channelOrderNo string, amountCents int64, status string) []byte {
	return []byte(fmt.Sprintf("%s|%s|%d|%s", orderID, channelOrderNo, amountCents, status))
}

// UnconfiguredVerifier：真实渠道验签骨架。商户未签约前恒返回未配置错误，回调入口据此回 501
// CHANNEL_NOT_CONFIGURED，绝不入账、绝不把回调体当作支付成功处理。
// 接入时替换为：支付宝 RSA2 验签器（官方文档见 API 文档 §13：支付宝开放平台，
// 应用公钥/支付宝公钥验签，签名算法 RSA2）；微信支付 v3 验签器（微信支付商户平台文档，
// APIv3 密钥 + 平台证书验签，签名算法 WECHATPAY2-SHA256-RSA2048）。
// 两种真实验签器均须以商户号/密钥/证书为构造参数（只来自 fund.env，绝不进仓库），
// 且须先在测试模式跑通官方沙箱回调后再谈生产启用。
type UnconfiguredVerifier struct {
	Channel string
}

func (v UnconfiguredVerifier) VerifySignature(channel string, payload []byte, signature string) error {
	return errChannelNotConfigured
}
