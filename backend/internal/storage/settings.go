package storage

import (
	"strings"
	"time"
)

const (
	aliyunOSSProvider     = "aliyun"
	tencentCOSProvider    = "tencent"
	qiniuKodoProvider     = "qiniu"
	volcengineTOSProvider = "volcengine"
	s3Provider            = "s3"
	gdriveProvider        = "gdrive"
	defaultOSSPathPrefix  = "open-ai-canvas"
	resourceAccessURLTTL  = 4 * time.Hour
)

type Settings struct {
	Delivery          DeliverySettings `json:"delivery"`
	GDrive            GDriveSettings   `json:"gdrive"`
	Enabled           bool             `json:"enabled"`
	Provider          string           `json:"provider"`
	Region            string           `json:"region"`
	Endpoint          string           `json:"endpoint"`
	CDNBaseURL        string           `json:"cdnBaseUrl"`
	Bucket            string           `json:"bucket"`
	AccessKeyID       string           `json:"accessKeyId"`
	AccessKeySecret   string           `json:"accessKeySecret"`
	PublicBaseURL     string           `json:"publicBaseUrl"`
	PathPrefix        string           `json:"pathPrefix"`
	S3Preset          string           `json:"s3Preset"`
	PathStyle         bool             `json:"pathStyle"`
	SessionToken      string           `json:"sessionToken"`
	StorageLocationID string           `json:"storageLocationId"`
	AllowUserS3       bool             `json:"allowUserS3"`
	Runtime           RuntimePolicy    `json:"-"`
	// 平台切换云厂商后仍需读取历史资源，因此仅归档非当前厂商的访问密钥。
	ArchivedCredentials map[string]Credentials `json:"archivedCredentials,omitempty"`
}

// GDriveSettings 是 Google Drive 的凭据与落点。
// Drive 没有一对 AccessKey，而是「OAuth 客户端 + 长期 refresh token」，
// 且对象标识是上传后才生成的 fileId，所以它不需要 Bucket / Endpoint / Region。
type GDriveSettings struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	RefreshToken string `json:"refreshToken"`
	// RootFolderID 是 Drive 文件夹 ID；留空表示「我的云端硬盘」根目录。
	RootFolderID string `json:"rootFolderId"`
	// AccountEmail / RootFolderName 只用于界面回显「连的是哪个账号、写进哪个文件夹」，
	// 不参与读写判定，因此不是敏感字段。
	AccountEmail   string `json:"accountEmail,omitempty"`
	RootFolderName string `json:"rootFolderName,omitempty"`
	// APIBaseURL / TokenURL / AuthorizeURL / UserInfoURL 覆盖 Drive 端点，
	// 仅供测试与私有出网代理使用，不参与持久化，也不接受请求体传入。
	APIBaseURL   string `json:"-"`
	TokenURL     string `json:"-"`
	AuthorizeURL string `json:"-"`
	UserInfoURL  string `json:"-"`
}

type RuntimePolicy struct {
	TransferTimeout       time.Duration
	AccessURLTTL          time.Duration
	ProviderAccessURLTTL  time.Duration
	MaxBufferedUploadSize int64
	ErrorBodyLimit        int64
}

type Credentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	AccessKeySecret string `json:"accessKeySecret"`
	// Drive 的凭据不是一对密钥；切换厂商后归档历史资源仍要能读回这几个字段。
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

func CloneCredentials(source map[string]Credentials) map[string]Credentials {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]Credentials, len(source))
	for provider, credentials := range source {
		cloned[strings.ToLower(strings.TrimSpace(provider))] = Credentials{
			AccessKeyID:     strings.TrimSpace(credentials.AccessKeyID),
			AccessKeySecret: strings.TrimSpace(credentials.AccessKeySecret),
			ClientID:        strings.TrimSpace(credentials.ClientID),
			ClientSecret:    strings.TrimSpace(credentials.ClientSecret),
			RefreshToken:    strings.TrimSpace(credentials.RefreshToken),
		}
	}
	return cloned
}

// NormalizeGDriveSettings 统一去空白，避免把复制粘贴带入的换行当成凭据的一部分。
func NormalizeGDriveSettings(value GDriveSettings) GDriveSettings {
	value.ClientID = strings.TrimSpace(value.ClientID)
	value.ClientSecret = strings.TrimSpace(value.ClientSecret)
	value.RefreshToken = strings.TrimSpace(value.RefreshToken)
	value.RootFolderID = strings.TrimSpace(value.RootFolderID)
	value.AccountEmail = strings.TrimSpace(value.AccountEmail)
	value.RootFolderName = strings.TrimSpace(value.RootFolderName)
	return value
}

// GDriveConfigured 判断 Drive 凭据是否足以发起一次请求。
// 注意：这里只做完整度判断，真实可用性必须由连接测试证明。
func GDriveConfigured(value GDriveSettings) bool {
	value = NormalizeGDriveSettings(value)
	return value.ClientID != "" && value.ClientSecret != "" && value.RefreshToken != ""
}

func NormalizeSettings(value Settings) Settings {
	value.Provider = strings.ToLower(strings.TrimSpace(value.Provider))
	if value.Provider == "" {
		value.Provider = aliyunOSSProvider
	}
	value.Region = strings.TrimSpace(value.Region)
	value.Endpoint = strings.TrimRight(strings.TrimSpace(value.Endpoint), "/")
	if value.Provider == tencentCOSProvider && value.Endpoint == "" && value.Region != "" {
		value.Endpoint = "https://cos." + value.Region + ".myqcloud.com"
	}
	value.CDNBaseURL = strings.TrimRight(strings.TrimSpace(value.CDNBaseURL), "/")
	value.Delivery.CDNAuthMode = strings.ToLower(strings.TrimSpace(value.Delivery.CDNAuthMode))
	value.Bucket = strings.TrimSpace(value.Bucket)
	if value.Provider == volcengineTOSProvider {
		value = normalizeVolcengineTOSLocation(value)
	}
	value.AccessKeyID = strings.TrimSpace(value.AccessKeyID)
	value.AccessKeySecret = strings.TrimSpace(value.AccessKeySecret)
	value.PublicBaseURL = strings.TrimRight(strings.TrimSpace(value.PublicBaseURL), "/")
	value.PathPrefix = strings.Trim(strings.TrimSpace(value.PathPrefix), "/")
	if value.PathPrefix == "" {
		value.PathPrefix = defaultOSSPathPrefix
	}
	value.S3Preset = strings.ToLower(strings.TrimSpace(value.S3Preset))
	if value.S3Preset == "" {
		value.S3Preset = "custom"
	}
	value.SessionToken = strings.TrimSpace(value.SessionToken)
	value.StorageLocationID = strings.TrimSpace(value.StorageLocationID)
	value.GDrive = NormalizeGDriveSettings(value.GDrive)
	if value.Runtime.TransferTimeout <= 0 {
		value.Runtime.TransferTimeout = 2 * time.Minute
	}
	if value.Runtime.AccessURLTTL <= 0 {
		value.Runtime.AccessURLTTL = resourceAccessURLTTL
	}
	if value.Runtime.ProviderAccessURLTTL <= 0 {
		value.Runtime.ProviderAccessURLTTL = 4 * time.Hour
	}
	if value.Runtime.MaxBufferedUploadSize <= 0 {
		value.Runtime.MaxBufferedUploadSize = 64 << 20
	}
	if value.Runtime.ErrorBodyLimit <= 0 {
		value.Runtime.ErrorBodyLimit = 1024
	}
	value.ArchivedCredentials = CloneCredentials(value.ArchivedCredentials)
	return value
}
