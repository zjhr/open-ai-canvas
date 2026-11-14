package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"yingce/backend/internal/outbound"
)

const (
	gdriveDefaultAPIBase  = "https://www.googleapis.com"
	gdriveDefaultTokenURL = "https://oauth2.googleapis.com/token"
	gdriveDefaultAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	gdriveDefaultUserInfo = "https://openidconnect.googleapis.com/v1/userinfo"
	// drive.file 只覆盖本应用创建或被显式选中的文件。它是「非敏感」权限，
	// 因此管理员不需要为自建 OAuth 客户端跑 Google 的验证与 CASA 安全评估。
	// openid + email 同样是非敏感权限，用来回显「连的是哪个账号」。
	gdriveScopeDriveFile = "https://www.googleapis.com/auth/drive.file"
	gdriveScopeOpenID    = "openid"
	gdriveScopeEmail     = "email"
	gdriveFolderMimeType = "application/vnd.google-apps.folder"
	// 目标文件夹固定用这个名字；不搜索同名文件夹，避免拿到一个没有权限的 ID。
	gdriveDefaultRootFolderName = "yingce-storage"
	// access token 提前一分钟视为过期，避免请求在途中失效。
	gdriveTokenSafetyWindow = time.Minute
	// Drive 的错误正文很短；只读有限字节，避免把异常响应体灌进内存。
	gdriveErrorBodyLimit = 2 << 10
	// Drive 的 fileId 是不透明的短标识；这里只做长度与字符的防御性校验。
	gdriveMaxFileIDLength = 256
)

// GDriveScopes 是发起授权时要申请的权限集合，按固定顺序拼接以保证授权地址稳定。
func GDriveScopes() []string {
	return []string{gdriveScopeOpenID, gdriveScopeEmail, gdriveScopeDriveFile}
}

// errGDriveFolderMissing 表示已登记的目标文件夹已被删除或权限已失效，
// 与网络/配额类错误区分开：前者应退回新建，后者必须如实上报。
var errGDriveFolderMissing = errors.New("Google Drive 目标文件夹不可用")

// gdriveToken 是内存中的 access token 缓存。
//
// 资源读取走平台代理，一次页面加载可能触发多次 Range 请求；如果每次读取都去换一次
// access token，会白白消耗 Drive 的配额并显著拉高延迟，因此必须缓存。
type gdriveToken struct {
	AccessToken string
	ExpiresAt   time.Time
}

var (
	gdriveTokenMu    sync.Mutex
	gdriveTokenCache = map[string]gdriveToken{}
)

func gdriveAPIBase(setting Settings) string {
	if base := strings.TrimRight(strings.TrimSpace(setting.GDrive.APIBaseURL), "/"); base != "" {
		return base
	}
	return gdriveDefaultAPIBase
}

func gdriveTokenEndpoint(setting Settings) string {
	if endpoint := strings.TrimSpace(setting.GDrive.TokenURL); endpoint != "" {
		return endpoint
	}
	return gdriveDefaultTokenURL
}

func gdriveAuthorizeEndpoint(setting Settings) string {
	if endpoint := strings.TrimSpace(setting.GDrive.AuthorizeURL); endpoint != "" {
		return endpoint
	}
	return gdriveDefaultAuthURL
}

func gdriveUserInfoEndpoint(setting Settings) string {
	if endpoint := strings.TrimSpace(setting.GDrive.UserInfoURL); endpoint != "" {
		return endpoint
	}
	return gdriveDefaultUserInfo
}

// gdriveTokenCacheKey 只由凭据决定，因此同一份配置在进程内共享一个 access token。
// 用摘要而不是原文做 key，避免凭据出现在 map 的键里被意外打印。
func gdriveTokenCacheKey(setting Settings) string {
	value := NormalizeGDriveSettings(setting.GDrive)
	sum := sha256.Sum256([]byte(value.ClientID + "\x00" + value.RefreshToken))
	return hex.EncodeToString(sum[:])
}

// ResetGDriveTokenCache 清空 access token 缓存。配置变更或连接测试后调用，
// 避免拿旧凭据换来的 token 继续命中。
func ResetGDriveTokenCache() {
	gdriveTokenMu.Lock()
	gdriveTokenCache = map[string]gdriveToken{}
	gdriveTokenMu.Unlock()
}

func gdriveAccessToken(setting Settings) (string, error) {
	setting = NormalizeSettings(setting)
	if !GDriveConfigured(setting.GDrive) {
		return "", errors.New("Google Drive 凭据不完整，请填写客户端 ID、客户端密钥与刷新令牌")
	}
	key := gdriveTokenCacheKey(setting)
	now := time.Now()
	gdriveTokenMu.Lock()
	cached, ok := gdriveTokenCache[key]
	gdriveTokenMu.Unlock()
	if ok && now.Add(gdriveTokenSafetyWindow).Before(cached.ExpiresAt) {
		return cached.AccessToken, nil
	}

	form := url.Values{}
	form.Set("client_id", setting.GDrive.ClientID)
	form.Set("client_secret", setting.GDrive.ClientSecret)
	form.Set("refresh_token", setting.GDrive.RefreshToken)
	form.Set("grant_type", "refresh_token")
	payload, err := requestGDriveToken(setting, form)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return "", errors.New("Google Drive 未返回访问令牌，请检查刷新令牌是否已失效")
	}
	gdriveTokenMu.Lock()
	gdriveTokenCache[key] = gdriveToken{AccessToken: payload.AccessToken, ExpiresAt: now.Add(gdriveTokenLifetime(payload.ExpiresIn))}
	gdriveTokenMu.Unlock()
	return payload.AccessToken, nil
}

type gdriveTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// gdriveTokenLifetime 缺省一小时是 Drive access token 的标准寿命；拿不到就用保守值。
func gdriveTokenLifetime(expiresIn int64) time.Duration {
	if lifetime := time.Duration(expiresIn) * time.Second; lifetime > 0 {
		return lifetime
	}
	return time.Hour
}

// requestGDriveToken 是换取与刷新令牌共用的出口：refresh_token 授权与
// authorization_code 授权只在 form 内容上不同，端点、鉴权和错误翻译完全一致。
func requestGDriveToken(setting Settings, form url.Values) (*gdriveTokenResponse, error) {
	req, err := http.NewRequest(http.MethodPost, gdriveTokenEndpoint(setting), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := outbound.OutboundHTTPClient(transferTimeout(setting)).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Google Drive 换取访问令牌失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, gdriveStatusError("换取访问令牌", resp)
	}
	var payload gdriveTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, gdriveErrorBodyLimit)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("Google Drive 访问令牌响应无法解析：%w", err)
	}
	return &payload, nil
}

// gdriveStatusError 把 Google 的错误正文提炼成可读原因，避免把 401 笼统报成“上传失败”。
func gdriveStatusError(operation string, resp *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, gdriveErrorBodyLimit))
	var payload struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.Unmarshal(detail, &payload)
	message := strings.TrimSpace(payload.Error.Message)
	if message == "" {
		message = strings.TrimSpace(payload.ErrorDescription)
	}
	if message == "" {
		message = strings.TrimSpace(string(detail))
	}
	if message == "" {
		message = resp.Status
	}
	return fmt.Errorf("Google Drive %s失败（HTTP %d）：%s", operation, resp.StatusCode, message)
}

// gdriveFileID 校验并从入参取出 fileId。ObjectKey 对 Drive 就是 fileId，
// 因此这里要挡住空值和明显的路径拼接。
func gdriveFileID(objectKey string) (string, error) {
	value := strings.TrimSpace(objectKey)
	if value == "" {
		return "", errors.New("Google Drive 文件标识为空")
	}
	if len(value) > gdriveMaxFileIDLength || strings.ContainsAny(value, "/\\?#") {
		return "", fmt.Errorf("Google Drive 文件标识无效：%q", value)
	}
	return value, nil
}

// PutGDriveObject 把对象写入 Drive 并返回 fileId 与内容摘要。
//
// 返回值里的 etag 是 Drive 计算的 MD5，语义与 S3 的 ETag 对齐；
// fileId 才是后续读取和删除要用的句柄，因此调用方必须把它回填到资源记录上
// （对象名与句柄在 Drive 上是两个不同的东西，这是与 S3 系最大的差别）。
func PutGDriveObject(setting Settings, name string, mimeType string, size int64, body io.Reader) (string, string, error) {
	setting = NormalizeSettings(setting)
	if body == nil {
		return "", "", errors.New("Google Drive 上传内容为空")
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	content := body
	contentLength := size
	if contentLength <= 0 {
		// 长度未知时无法直接开 resumable 会话：Drive 需要事先声明长度。
		buffered, err := io.ReadAll(io.LimitReader(body, maxBufferedUploadSize(setting)+1))
		if err != nil {
			return "", "", fmt.Errorf("Google Drive 上传内容读取失败：%w", err)
		}
		if int64(len(buffered)) > maxBufferedUploadSize(setting) {
			return "", "", fmt.Errorf("Google Drive 上传内容长度未知且超过 %d 字节缓冲上限", maxBufferedUploadSize(setting))
		}
		content = bytes.NewReader(buffered)
		contentLength = int64(len(buffered))
	}

	token, err := gdriveAccessToken(setting)
	if err != nil {
		return "", "", err
	}
	metadata := map[string]any{"name": gdriveObjectName(name), "mimeType": mimeType}
	if description := strings.TrimSpace(name); description != "" {
		// 展示名被压平了，把原始逻辑路径留在描述里，便于在 Drive 里回溯对象来源。
		metadata["description"] = description
	}
	if root := strings.TrimSpace(setting.GDrive.RootFolderID); root != "" {
		metadata["parents"] = []string{root}
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return "", "", err
	}

	sessionURL, err := gdriveStartUpload(setting, token, payload, mimeType, contentLength)
	if err != nil {
		return "", "", err
	}
	return gdriveUploadContent(setting, token, sessionURL, mimeType, contentLength, content)
}

// gdriveObjectName 把逻辑对象路径压成 Drive 文件展示名。
// Drive 的名字不是路径，为避免不同客户端对 `/` 的处理差异，这里统一用末段命名，
// 完整逻辑路径另行写入 description。
func gdriveObjectName(name string) string {
	trimmed := strings.Trim(strings.TrimSpace(name), "/")
	if index := strings.LastIndex(trimmed, "/"); index >= 0 {
		trimmed = trimmed[index+1:]
	}
	if trimmed == "" {
		return "yingce-object"
	}
	return trimmed
}

func gdriveStartUpload(setting Settings, token string, metadata []byte, mimeType string, contentLength int64) (string, error) {
	endpoint := gdriveAPIBase(setting) + "/upload/drive/v3/files"
	query := url.Values{}
	query.Set("uploadType", "resumable")
	query.Set("supportsAllDrives", "true")
	query.Set("fields", "id,md5Checksum,size")
	req, err := http.NewRequest(http.MethodPost, endpoint+"?"+query.Encode(), bytes.NewReader(metadata))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", mimeType)
	if contentLength > 0 {
		req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(contentLength, 10))
	}
	resp, err := outbound.OutboundHTTPClient(transferTimeout(setting)).Do(req)
	if err != nil {
		return "", fmt.Errorf("Google Drive 创建上传会话失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", gdriveStatusError("创建上传会话", resp)
	}
	session := strings.TrimSpace(resp.Header.Get("Location"))
	if session == "" {
		return "", errors.New("Google Drive 未返回上传会话地址")
	}
	return session, nil
}

func gdriveUploadContent(setting Settings, token string, sessionURL string, mimeType string, contentLength int64, body io.Reader) (string, string, error) {
	req, err := http.NewRequest(http.MethodPut, sessionURL, body)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mimeType)
	if contentLength > 0 {
		req.ContentLength = contentLength
	}
	resp, err := outbound.OutboundHTTPClient(transferTimeout(setting)).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("Google Drive 上传失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", gdriveStatusError("上传", resp)
	}
	var payload struct {
		ID          string `json:"id"`
		MD5Checksum string `json:"md5Checksum"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, gdriveErrorBodyLimit)).Decode(&payload); err != nil {
		return "", "", fmt.Errorf("Google Drive 上传响应无法解析：%w", err)
	}
	fileID := strings.TrimSpace(payload.ID)
	if fileID == "" {
		return "", "", errors.New("Google Drive 上传成功但未返回文件标识")
	}
	return fileID, strings.TrimSpace(payload.MD5Checksum), nil
}

// GetGDriveObjectRange 读取对象字节。objectKey 是上传返回的 fileId。
//
// Range 语义与其它对象存储保持一致：把 206 的 Content-Range 原样带回给上层，
// 让平台代理可以继续支持视频拖动进度条。
func GetGDriveObjectRange(setting Settings, objectKey string, rangeHeader string) (*ObjectStream, error) {
	setting = NormalizeSettings(setting)
	fileID, err := gdriveFileID(objectKey)
	if err != nil {
		return nil, err
	}
	token, err := gdriveAccessToken(setting)
	if err != nil {
		return nil, err
	}
	endpoint := gdriveAPIBase(setting) + "/drive/v3/files/" + url.PathEscape(fileID)
	query := url.Values{}
	query.Set("alt", "media")
	query.Set("supportsAllDrives", "true")
	req, err := http.NewRequest(http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if value := strings.TrimSpace(rangeHeader); value != "" {
		req.Header.Set("Range", value)
	}
	resp, err := outbound.OutboundHTTPClient(transferTimeout(setting)).Do(req)
	if err != nil {
		return nil, fmt.Errorf("Google Drive 读取失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		return nil, gdriveStatusError("读取", resp)
	}
	length, _ := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("Content-Length")), 10, 64)
	return &ObjectStream{
		Body:          resp.Body,
		StatusCode:    resp.StatusCode,
		ContentLength: length,
		ContentRange:  resp.Header.Get("Content-Range"),
		AcceptRanges:  resp.Header.Get("Accept-Ranges"),
	}, nil
}

// DeleteGDriveObject 删除对象。fileId 已不存在时按成功处理：
// 资源清理是幂等的，重复删除不应该把已经收敛的状态报成失败。
func DeleteGDriveObject(setting Settings, objectKey string) error {
	setting = NormalizeSettings(setting)
	fileID, err := gdriveFileID(objectKey)
	if err != nil {
		return err
	}
	token, err := gdriveAccessToken(setting)
	if err != nil {
		return err
	}
	endpoint := gdriveAPIBase(setting) + "/drive/v3/files/" + url.PathEscape(fileID)
	query := url.Values{}
	query.Set("supportsAllDrives", "true")
	req, err := http.NewRequest(http.MethodDelete, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := outbound.OutboundHTTPClient(transferTimeout(setting)).Do(req)
	if err != nil {
		return fmt.Errorf("Google Drive 删除失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return gdriveStatusError("删除", resp)
	}
	return nil
}

// ErrGDriveReadMismatch 让上层能把「回读内容与刚写入的不一致」翻译成
// 比笼统的“连接测试失败”更具体的原因。
var ErrGDriveReadMismatch = errors.New("Google Drive 读取内容不一致")

// VerifyGDriveObject 是 Drive 的连接测试：写入 → 按 fileId 回读 → 删除。
//
// 不能照搬 S3 的「同一个 key 走完三步」：Drive 的对象名与对象句柄不是一回事，
// 句柄必须由写入结果带回，因此这里显式串起三步。
//
// 返回值第一个是失败的步骤名（写入 / Range 读取 / 删除），成功时为空串。
// 由这里给出步骤而不是让上层去匹配错误文本，避免提示语一改就失准。
func VerifyGDriveObject(setting Settings, name string) (string, error) {
	payload := []byte("yingce-storage-test")
	fileID, _, err := PutGDriveObject(setting, name, "application/octet-stream", int64(len(payload)), bytes.NewReader(payload))
	if err != nil {
		return "写入", err
	}
	stream, readErr := GetGDriveObjectRange(setting, fileID, "bytes=0-3")
	var rangeErr error
	if readErr != nil {
		rangeErr = readErr
	} else {
		rangeErr = verifyGDriveRangeRead(stream, payload)
	}
	// 回读失败也要先把测试对象删掉，否则每次失败的连接测试都会在用户 Drive 里留垃圾。
	if err := DeleteGDriveObject(setting, fileID); err != nil {
		return "删除", err
	}
	if rangeErr != nil {
		return "Range 读取", rangeErr
	}
	return "", nil
}

// verifyGDriveRangeRead 与对象存储的连接测试保持同一套判定：
// 服务端可以忽略 Range 返回 200 + 完整对象，也可以返回 206 分段。
func verifyGDriveRangeRead(stream *ObjectStream, payload []byte) error {
	if stream == nil || stream.Body == nil {
		return fmt.Errorf("%w：响应为空", ErrGDriveReadMismatch)
	}
	data, readErr := io.ReadAll(io.LimitReader(stream.Body, int64(len(payload)+1)))
	closeErr := stream.Body.Close()
	if readErr != nil {
		return fmt.Errorf("Google Drive 响应读取失败：%w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("Google Drive 响应关闭失败：%w", closeErr)
	}
	switch stream.StatusCode {
	case http.StatusPartialContent:
		if len(payload) >= 4 && bytes.Equal(data, payload[:4]) {
			return nil
		}
	case http.StatusOK:
		if bytes.Equal(data, payload) {
			return nil
		}
	}
	return fmt.Errorf("%w（HTTP %d，读取 %d 字节）", ErrGDriveReadMismatch, stream.StatusCode, len(data))
}

// GDriveAuthorizeURL 拼出让管理员跳到 Google 同意页的地址。
//
// access_type=offline 才会下发 refresh_token；prompt=consent 保证重复连接时
// Google 仍然重新发一枚（否则它会省略 refresh_token，我们拿不到可长期使用的凭据）。
func GDriveAuthorizeURL(setting Settings, redirectURI string, state string, codeChallenge string) (string, error) {
	setting = NormalizeSettings(setting)
	value := setting.GDrive
	if value.ClientID == "" || value.ClientSecret == "" {
		return "", errors.New("请先填写 Google Drive 客户端 ID 与客户端密钥")
	}
	redirectURI = strings.TrimSpace(redirectURI)
	state = strings.TrimSpace(state)
	codeChallenge = strings.TrimSpace(codeChallenge)
	if redirectURI == "" || state == "" || codeChallenge == "" {
		return "", errors.New("Google Drive 授权参数不完整")
	}
	authorize, err := url.Parse(gdriveAuthorizeEndpoint(setting))
	if err != nil {
		return "", err
	}
	query := authorize.Query()
	query.Set("client_id", value.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("scope", strings.Join(GDriveScopes(), " "))
	query.Set("state", state)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("access_type", "offline")
	query.Set("prompt", "consent")
	query.Set("include_granted_scopes", "true")
	authorize.RawQuery = query.Encode()
	return authorize.String(), nil
}

// PKCEChallenge 由 code_verifier 算出 S256 challenge。
// 授权与换取令牌分属两个请求，verifier 必须由服务端保存，不能经浏览器往返。
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GDriveExchangeCode 用授权码换长期凭据。
// 返回 (refreshToken, accessToken, error)：accessToken 只用于紧接着读一次账号信息，
// 不落库、不缓存。
func GDriveExchangeCode(setting Settings, code string, redirectURI string, codeVerifier string) (string, string, error) {
	setting = NormalizeSettings(setting)
	value := setting.GDrive
	if value.ClientID == "" || value.ClientSecret == "" {
		return "", "", errors.New("请先填写 Google Drive 客户端 ID 与客户端密钥")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", "", errors.New("Google Drive 未返回授权码")
	}
	form := url.Values{}
	form.Set("client_id", value.ClientID)
	form.Set("client_secret", value.ClientSecret)
	form.Set("code", code)
	form.Set("code_verifier", strings.TrimSpace(codeVerifier))
	form.Set("grant_type", "authorization_code")
	if trimmed := strings.TrimSpace(redirectURI); trimmed != "" {
		form.Set("redirect_uri", trimmed)
	}
	payload, err := requestGDriveToken(setting, form)
	if err != nil {
		return "", "", err
	}
	refresh := strings.TrimSpace(payload.RefreshToken)
	if refresh == "" {
		// Google 只在第一次授权（或 prompt=consent）时下发 refresh_token。
		// 这里不猜、不重试，直接把恢复动作交给管理员。
		return "", "", errors.New("Google Drive 未返回刷新令牌，请到 Google 账号的第三方授权页撤销本应用后重新连接")
	}
	return refresh, strings.TrimSpace(payload.AccessToken), nil
}

// GDriveAccountEmail 用刚换到的 access token 读一次账号邮箱，仅供界面回显
// 「连的是哪个账号」。读不到不算致命，调用方可以只记录为空。
func GDriveAccountEmail(setting Settings, accessToken string) (string, error) {
	setting = NormalizeSettings(setting)
	token := strings.TrimSpace(accessToken)
	if token == "" {
		return "", errors.New("Google Drive 访问令牌为空，无法读取账号信息")
	}
	req, err := http.NewRequest(http.MethodGet, gdriveUserInfoEndpoint(setting), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := outbound.OutboundHTTPClient(transferTimeout(setting)).Do(req)
	if err != nil {
		return "", fmt.Errorf("Google Drive 读取账号信息失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", gdriveStatusError("读取账号信息", resp)
	}
	var payload struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, gdriveErrorBodyLimit)).Decode(&payload); err != nil {
		return "", fmt.Errorf("Google Drive 账号信息响应无法解析：%w", err)
	}
	return strings.TrimSpace(payload.Email), nil
}

// GDriveRootFolderInfo 只读回显已登记的目标文件夹。没有登记时返回空值，
// 绝不在这里新建目录 —— 读状态不应该产生副作用。
func GDriveRootFolderInfo(setting Settings) (string, string, error) {
	setting = NormalizeSettings(setting)
	folderID := strings.TrimSpace(setting.GDrive.RootFolderID)
	if folderID == "" {
		return "", "", nil
	}
	name, err := gdriveFolderName(setting, folderID)
	if err != nil {
		return folderID, "", err
	}
	return folderID, name, nil
}

func gdriveFolderName(setting Settings, folderID string) (string, error) {
	token, err := gdriveAccessToken(setting)
	if err != nil {
		return "", err
	}
	id, err := gdriveFileID(folderID)
	if err != nil {
		return "", err
	}
	endpoint := gdriveAPIBase(setting) + "/drive/v3/files/" + url.PathEscape(id)
	query := url.Values{}
	query.Set("fields", "id,name,trashed")
	query.Set("supportsAllDrives", "true")
	req, err := http.NewRequest(http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := outbound.OutboundHTTPClient(transferTimeout(setting)).Do(req)
	if err != nil {
		return "", fmt.Errorf("Google Drive 读取目标文件夹失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, gdriveErrorBodyLimit))
		return "", fmt.Errorf("%w（HTTP %d）", errGDriveFolderMissing, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", gdriveStatusError("读取目标文件夹", resp)
	}
	var payload struct {
		Name    string `json:"name"`
		Trashed bool   `json:"trashed"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, gdriveErrorBodyLimit)).Decode(&payload); err != nil {
		return "", fmt.Errorf("Google Drive 目标文件夹响应无法解析：%w", err)
	}
	if payload.Trashed {
		return "", fmt.Errorf("%w：文件夹位于回收站", errGDriveFolderMissing)
	}
	return strings.TrimSpace(payload.Name), nil
}

// EnsureGDriveRootFolder 保证存在一个应用可写的目标文件夹。
//
// drive.file 只覆盖本应用创建或被显式选中的文件，因此「按名字搜索一个已存在的
// 同名文件夹」会得到一个没有权限的 ID，要等到写入时才报错。这里只复用已登记且
// 仍可访问的文件夹，否则新建一个，不去猜管理员已有的目录。
func EnsureGDriveRootFolder(setting Settings) (string, string, error) {
	setting = NormalizeSettings(setting)
	if folderID := strings.TrimSpace(setting.GDrive.RootFolderID); folderID != "" {
		name, err := gdriveFolderName(setting, folderID)
		if err == nil {
			return folderID, name, nil
		}
		if !errors.Is(err, errGDriveFolderMissing) {
			return "", "", err
		}
		// 文件夹被删除或权限失效时退回新建，否则之后每次上传都会失败。
	}
	return createGDriveRootFolder(setting)
}

func createGDriveRootFolder(setting Settings) (string, string, error) {
	token, err := gdriveAccessToken(setting)
	if err != nil {
		return "", "", err
	}
	name := strings.TrimSpace(setting.GDrive.RootFolderName)
	if name == "" {
		name = gdriveDefaultRootFolderName
	}
	payload, err := json.Marshal(map[string]any{"name": name, "mimeType": gdriveFolderMimeType})
	if err != nil {
		return "", "", err
	}
	query := url.Values{}
	query.Set("supportsAllDrives", "true")
	query.Set("fields", "id,name")
	req, err := http.NewRequest(http.MethodPost, gdriveAPIBase(setting)+"/drive/v3/files?"+query.Encode(), bytes.NewReader(payload))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := outbound.OutboundHTTPClient(transferTimeout(setting)).Do(req)
	if err != nil {
		return "", "", fmt.Errorf("Google Drive 创建目标文件夹失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", gdriveStatusError("创建目标文件夹", resp)
	}
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, gdriveErrorBodyLimit)).Decode(&created); err != nil {
		return "", "", fmt.Errorf("Google Drive 目标文件夹响应无法解析：%w", err)
	}
	createdID := strings.TrimSpace(created.ID)
	if createdID == "" {
		return "", "", errors.New("Google Drive 创建文件夹成功但未返回标识")
	}
	createdName := strings.TrimSpace(created.Name)
	if createdName == "" {
		createdName = name
	}
	return createdID, createdName, nil
}
