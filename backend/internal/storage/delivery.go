package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// DeliverySettings is independent from origin credentials. Public CDN access must be an explicit choice.
type DeliverySettings struct {
	CDNAuthMode       string `json:"cdnAuthMode"`
	RequireCDN        bool   `json:"requireCDN"`
	AllowPrivateProxy bool   `json:"allowPrivateProxy"`
}

func CDNEnabled(setting Settings) bool {
	if setting.CDNBaseURL == "" {
		return false
	}
	return setting.Delivery.CDNAuthMode == "public" || setting.Delivery.CDNAuthMode == "qiniu"
}

func SignCDNURL(setting Settings, objectKey string, expires time.Time) (string, error) {
	switch setting.Delivery.CDNAuthMode {
	case "public":
		return OssCDNObjectURL(setting.CDNBaseURL, objectKey)
	case "qiniu":
		if setting.Provider == qiniuKodoProvider {
			return SignedQiniuObjectURL(setting, objectKey, expires)
		}
	}
	return "", errors.New("CDN 用户访问鉴权方式未配置或不支持")
}

func PublicOrigin(setting Settings) bool {
	if setting.Provider == gdriveProvider {
		// Drive 没有公开源站；读取必须带 Authorization 头。
		return false
	}
	if setting.Provider == qiniuKodoProvider {
		return true
	} // The SDK signs the public Kodo S3 origin, not its upload endpoint.
	if setting.Provider == volcengineTOSProvider && volcengineTOSInternalEndpoint(setting.Endpoint) {
		return false // ivolces.com 只在火山引擎内网可达，浏览器拿到的预签名地址打不开。
	}
	return PublicHTTPSStorageEndpoint(setting.Endpoint)
}

// RequiresPlatformProxy 表示这家存储**本身**没有浏览器直连通道，字节只能由平台代理。
// 目前只有 Google Drive：它的每次读取都必须携带 Authorization 头，URL 无法预先签名，
// 把地址交给浏览器只会得到 401。
//
// 注意它与「当前配置有没有可用的直连出口」是两件事：后者取决于 CDN 是否配置、
// 源站是否公开，会随设置变化。投递策略必须按这里的能力判断，否则一个只是尚未配置
// CDN 的私有源站会被误判成需要代理，把刻意保留的「浏览器不代理 OSS 媒体」规则吃掉。
func RequiresPlatformProxy(setting Settings) bool {
	return NormalizeSettings(setting).Provider == gdriveProvider
}

func DeliveryRevision(setting Settings) string {
	// Only a digest leaves the server; changes to credentials/configuration invalidate descriptors.
	data, _ := json.Marshal(setting)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
