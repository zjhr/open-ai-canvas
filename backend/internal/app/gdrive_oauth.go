// Google Drive 存储的「一键连接」授权编排。
//
// 复用 oauth_states 表（与 Linux.do 登录共用），但用独立的 provider 值隔开：
// 两者不会互相消费，登录流程也不会因为这里的状态失效而受影响。

package app

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"time"

	"yingce/backend/internal/model"
)

const (
	// 状态记录用独立的 provider 值，与 Linux.do 登录的 "linuxdo" 彻底分开。
	gdriveOAuthStateProvider = "gdrive-storage"
	// 回调放在 /api 之下：/api 前缀已由 nginx 与 Vite 统一代理，
	// 不需要再为它单独加部署配置（/oauth/* 那种写法必须同时改 nginx 和 vite）。
	gdriveCallbackPath = "/api/admin/settings/oss/gdrive/callback"
	gdriveReturnPath   = "/admin/settings/storage"
	gdriveOAuthTTL     = 10 * time.Minute
	// 回调结果标记，前端据此提示成功或失败。
	gdriveResultConnected = "connected"
	gdriveResultError     = "error"
	gdriveResultCancelled = "cancelled"
)

// GDriveAuthorizeRequest 允许在发起连接的同一步顺带保存客户端凭据，
// 省掉「先保存再连接」的两步操作。留空表示沿用已保存的值。
type GDriveAuthorizeRequest struct {
	ClientID     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

// GDriveConnectStatus 是给设置页回显连接状态的只读结构。
type GDriveConnectStatus struct {
	RedirectURI          string `json:"redirectUri"`
	Connected            bool   `json:"connected"`
	AccountEmail         string `json:"accountEmail"`
	RootFolderID         string `json:"rootFolderId"`
	RootFolderName       string `json:"rootFolderName"`
	HasClientCredentials bool   `json:"hasClientCredentials"`
}

// GDriveCallbackURL 把回调结果翻译成前端存储设置页的跳转地址。
func GDriveCallbackURL(result string, message string) string {
	values := url.Values{}
	values.Set("gdrive", result)
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		values.Set("gdriveMessage", trimmed)
	}
	return gdriveReturnPath + "?" + values.Encode()
}

// GDriveSetup 回显当前连接状态与「必须登记到 Google Cloud Console 的回调地址」。
// 这个回调地址由服务端推导，前端只读展示，避免管理员手填出错导致 redirect_uri_mismatch。
func (s *Service) GDriveSetup(actor *model.User, originHint string) (*GDriveConnectStatus, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	_, value, err := s.readOSSSetting()
	if err != nil {
		return nil, err
	}
	redirectURI, err := s.gdriveRedirectURI(originHint)
	if err != nil {
		return nil, err
	}
	status := &GDriveConnectStatus{
		RedirectURI:          redirectURI,
		Connected:            strings.TrimSpace(value.GDrive.RefreshToken) != "",
		AccountEmail:         value.GDrive.AccountEmail,
		RootFolderID:         value.GDrive.RootFolderID,
		RootFolderName:       value.GDrive.RootFolderName,
		HasClientCredentials: value.GDrive.ClientID != "" && value.GDrive.ClientSecret != "",
	}
	return status, nil
}

// BeginGDriveAuthorization 保存客户端凭据（如提供）、登记一次性 state 与 PKCE verifier，
// 返回让浏览器跳到 Google 同意页的地址。
func (s *Service) BeginGDriveAuthorization(actor *model.User, originHint string, req GDriveAuthorizeRequest) (string, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return "", err
	}
	currentSetting, value, err := s.readOSSSetting()
	if err != nil {
		return "", err
	}
	clientID := strings.TrimSpace(req.ClientID)
	clientSecret := strings.TrimSpace(req.ClientSecret)
	// 只提供一个字段几乎总是漏填，直接拒绝比默默用旧值更好。
	if (clientID == "") != (clientSecret == "") {
		return "", BadAuthRequest("请同时填写 Google Drive 客户端 ID 与客户端密钥")
	}
	if clientID != "" {
		value.GDrive.ClientID = clientID
		value.GDrive.ClientSecret = clientSecret
		// 凭据在这里就落库：回调发生在另一个请求里，那时请求体已经不存在了。
		if err := s.savePlatformOSSSetting(currentSetting, value, actor.ID); err != nil {
			return "", err
		}
	}
	if value.GDrive.ClientID == "" || value.GDrive.ClientSecret == "" {
		return "", BadAuthRequest("请先填写 Google Drive 客户端 ID 与客户端密钥")
	}
	redirectURI, err := s.gdriveRedirectURI(originHint)
	if err != nil {
		return "", err
	}

	state := randomToken()
	verifier := randomToken()
	record := &model.OAuthState{
		ID:       newID(),
		Provider: gdriveOAuthStateProvider,
		// state 只存摘要，verifier 只留在服务端：浏览器只拿到 state。
		StateHash: hashToken(state),
		// ReauthUserID 在这张表里就是「本次特权操作绑定的用户」。
		// 回调必须由同一个管理员完成，否则就是他人把自己的云盘接到了平台上。
		ReauthUserID: actor.ID,
		CodeVerifier: verifier,
		NextPath:     gdriveReturnPath,
		ExpiresAt:    time.Now().Add(gdriveOAuthTTL),
	}
	if err := s.repo.CreateOAuthState(record); err != nil {
		return "", err
	}
	authorizeURL, err := gdriveAuthorizeURL(s.storageSettingWithRuntimePolicy(value), redirectURI, state, pkceChallenge(verifier))
	if err != nil {
		return "", BadAuthRequest(err.Error())
	}
	return authorizeURL, nil
}

// CompleteGDriveAuthorization 校验 state、换取长期凭据、准备好目标文件夹并落库。
//
// 整个连接是一次性提交：刷新令牌、账号邮箱与文件夹信息在同一次保存里写入。
// 中途失败时旧配置保持不变，不会留下「换到了令牌但没准备好文件夹」的半成品。
func (s *Service) CompleteGDriveAuthorization(stateValue string, code string, originHint string, actingUserID string) error {
	stateValue = strings.TrimSpace(stateValue)
	code = strings.TrimSpace(code)
	if stateValue == "" || code == "" {
		return BadAuthRequest("Google Drive 授权回调缺少必要参数")
	}
	// 消费即作废：同一个 state 不能被重复使用。绑定校验失败时状态同样已被消费，
	// 这是有意的失败关闭——不确定归属的授权宁可让管理员重来一次。
	state, err := s.repo.ConsumeOAuthState(gdriveOAuthStateProvider, hashToken(stateValue))
	if err != nil {
		return BadAuthRequest("授权状态无效或已过期，请重新发起连接")
	}
	if state.ReauthUserID == "" || state.ReauthUserID != actingUserID {
		return Forbidden("授权状态与当前登录管理员不一致，请重新发起连接")
	}
	currentSetting, value, err := s.readOSSSetting()
	if err != nil {
		return err
	}
	if value.GDrive.ClientID == "" || value.GDrive.ClientSecret == "" {
		return BadAuthRequest("Google Drive 客户端凭据已丢失，请重新发起连接")
	}
	redirectURI, err := s.gdriveRedirectURI(originHint)
	if err != nil {
		return err
	}
	setting := s.storageSettingWithRuntimePolicy(value)
	refreshToken, accessToken, err := exchangeGDriveCode(setting, code, redirectURI, state.CodeVerifier)
	if err != nil {
		return BadAuthRequest(err.Error())
	}
	value.GDrive.RefreshToken = refreshToken
	// 账号邮箱只用于回显；读不到不影响连接本身。
	if email, err := readGDriveAccountEmail(s.storageSettingWithRuntimePolicy(value), accessToken); err == nil {
		value.GDrive.AccountEmail = email
	}
	folderID, folderName, err := ensureGDriveRootFolder(s.storageSettingWithRuntimePolicy(value))
	if err != nil {
		return BadAuthRequest(err.Error())
	}
	value.GDrive.RootFolderID = folderID
	value.GDrive.RootFolderName = folderName
	return s.savePlatformOSSSetting(currentSetting, value, actingUserID)
}

// DisconnectGDrive 清除长期凭据与派生的文件夹信息，保留客户端 ID/密钥，
// 便于管理员换一个 Google 账号重连而不必重填客户端凭据。
func (s *Service) DisconnectGDrive(actor *model.User) (*PublicOSSSetting, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	currentSetting, value, err := s.readOSSSetting()
	if err != nil {
		return nil, err
	}
	value.GDrive.RefreshToken = ""
	value.GDrive.AccountEmail = ""
	value.GDrive.RootFolderID = ""
	value.GDrive.RootFolderName = ""
	if err := s.savePlatformOSSSetting(currentSetting, value, actor.ID); err != nil {
		return nil, err
	}
	public, err := s.publicOSSSetting(currentSetting, value, "platform", "")
	if err != nil {
		return nil, err
	}
	return &public, nil
}

// savePlatformOSSSetting 是「只改 Drive 相关字段」的窄保存入口。
//
// 不走 UpdateOSSSetting：那条路径会重算 enabled/provider/StorageLocationID 并归档凭据，
// 而连接流程与这些字段无关，误改会静默改变现网存储位置。
func (s *Service) savePlatformOSSSetting(current *model.SystemSetting, value ossSettingValue, actorID string) error {
	stored, err := s.encryptOSSSettingSecrets(normalizeOSSSetting(value))
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	next := model.SystemSetting{Key: ossSettingKey, ValueJSON: string(encoded), UpdatedBy: actorID}
	if current != nil {
		next.CreatedAt = current.CreatedAt
	}
	if err := s.repo.SaveSystemSetting(&next); err != nil {
		return err
	}
	// 凭据变了，缓存的 access token 必须作废。
	resetGDriveTokenCache()
	return nil
}

// gdriveRedirectURI 推导回调地址。
//
// 优先级：管理员配置的对外根地址 → CANVAS_PUBLIC_BASE_URL → 当前请求的 Origin。
// 这里只用于拼 redirect_uri，Google 会拿它与登记值逐字符比对，
// 因此即便 Origin 被伪造，结果也只是 redirect_uri_mismatch，不会变成开放重定向。
func (s *Service) gdriveRedirectURI(originHint string) (string, error) {
	base := ""
	if _, setting, err := s.readOSSSetting(); err == nil {
		base = strings.TrimRight(strings.TrimSpace(setting.PublicBaseURL), "/")
	}
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("CANVAS_PUBLIC_BASE_URL")), "/")
	}
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(originHint), "/")
	}
	if base == "" {
		return "", BadAuthRequest("无法确定对外访问地址，请先填写服务器访问地址或设置 CANVAS_PUBLIC_BASE_URL")
	}
	parsed, err := validatePublicResourceBaseURL(base)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(parsed.String(), "/") + gdriveCallbackPath, nil
}
