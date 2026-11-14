package app

import (
	"bytes"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	"yingce/backend/internal/storage"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// newGDriveOAuthTestService 预置好存储加密密钥。
// 不依赖 settingsEncryptionKey 的首次生成路径：它要对目录做 fsync，
// 而 Windows 不允许以写方式打开目录，这条路径在本机测试环境里必然失败。
func newGDriveOAuthTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, ".settings-key"), bytes.Repeat([]byte{9}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open("file:"+newID()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.OAuthState{},
		&model.SystemSetting{},
		&model.UserOSSSetting{},
		&model.StorageLocation{},
		&model.Resource{},
	); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db), dataDir: dataDir}, db
}

func gdriveAdmin(id string) *model.User {
	return &model.User{ID: id, Username: id, Role: model.UserRoleAdmin, Status: model.UserStatusActive}
}

func seedPlatformOSS(t *testing.T, svc *Service, value ossSettingValue) {
	t.Helper()
	if err := svc.savePlatformOSSSetting(nil, value, "admin-1"); err != nil {
		t.Fatal(err)
	}
}

func rawPlatformOSSSetting(t *testing.T, db *gorm.DB) model.SystemSetting {
	t.Helper()
	var row model.SystemSetting
	if err := db.First(&row, "key = ?", ossSettingKey).Error; err != nil {
		t.Fatal(err)
	}
	return row
}

// stubGDriveStorage 替掉三个真正出网的 Drive 调用，让授权编排可以在本机完整验证。
// 返回恢复函数，用法：defer stubGDriveStorage(t, ...)()
func stubGDriveStorage(t *testing.T, exchange func(storage.Settings, string, string, string) (string, string, error),
	email func(storage.Settings, string) (string, error),
	ensure func(storage.Settings) (string, string, error)) func() {
	t.Helper()
	prevExchange, prevEmail, prevEnsure := exchangeGDriveCode, readGDriveAccountEmail, ensureGDriveRootFolder
	if exchange != nil {
		exchangeGDriveCode = exchange
	}
	if email != nil {
		readGDriveAccountEmail = email
	}
	if ensure != nil {
		ensureGDriveRootFolder = ensure
	}
	return func() {
		exchangeGDriveCode = prevExchange
		readGDriveAccountEmail = prevEmail
		ensureGDriveRootFolder = prevEnsure
	}
}

// authorizeStateFromURL 从授权地址里取出 state —— 它是唯一一次出现在客户端的凭据，
// 服务端只保存它的摘要。
func authorizeStateFromURL(t *testing.T, authorizeURL string) string {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize URL 无法解析：%v", err)
	}
	state := parsed.Query().Get("state")
	if state == "" {
		t.Fatal("authorize URL 缺少 state")
	}
	return state
}

func TestGDriveOAuthRequiresAdmin(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	member := &model.User{ID: "user-1", Role: model.UserRoleUser, Status: model.UserStatusActive}

	if _, err := svc.GDriveSetup(member, "https://canvas.example"); err == nil {
		t.Fatal("GDriveSetup() error = nil for a non-admin")
	}
	if _, err := svc.BeginGDriveAuthorization(member, "https://canvas.example", GDriveAuthorizeRequest{ClientID: "id", ClientSecret: "secret"}); err == nil {
		t.Fatal("BeginGDriveAuthorization() error = nil for a non-admin")
	}
	if _, err := svc.DisconnectGDrive(member); err == nil {
		t.Fatal("DisconnectGDrive() error = nil for a non-admin")
	}
}

func TestBeginGDriveAuthorizationRequiresCompleteClientCredentials(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "https://canvas.example"})
	admin := gdriveAdmin("admin-1")

	// 只填一个字段几乎总是漏填，必须直接拒绝而不是默默用旧值。
	_, err := svc.BeginGDriveAuthorization(admin, "https://canvas.example", GDriveAuthorizeRequest{ClientID: "only-id"})
	if err == nil || !strings.Contains(err.Error(), "同时填写") {
		t.Fatalf("BeginGDriveAuthorization() error = %v, want both-fields error", err)
	}
	// 一个都没有、也没有已保存的凭据时同样拒绝。
	_, err = svc.BeginGDriveAuthorization(admin, "https://canvas.example", GDriveAuthorizeRequest{})
	if err == nil || !strings.Contains(err.Error(), "客户端 ID") {
		t.Fatalf("BeginGDriveAuthorization() error = %v, want missing-credentials error", err)
	}
}

func TestBeginGDriveAuthorizationPersistsCredentialsAndRegistersState(t *testing.T) {
	svc, db := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "https://canvas.example"})
	admin := gdriveAdmin("admin-1")

	authorizeURL, err := svc.BeginGDriveAuthorization(admin, "https://canvas.example", GDriveAuthorizeRequest{
		ClientID: "client-id-1", ClientSecret: "client-secret-1",
	})
	if err != nil {
		t.Fatalf("BeginGDriveAuthorization() error = %v", err)
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("client_id"); got != "client-id-1" {
		t.Fatalf("client_id = %q", got)
	}
	if got := parsed.Query().Get("redirect_uri"); got != "https://canvas.example"+gdriveCallbackPath {
		t.Fatalf("redirect_uri = %q", got)
	}
	if got := parsed.Query().Get("code_challenge_method"); got != "S256" {
		t.Fatalf("code_challenge_method = %q", got)
	}
	// 客户端密钥绝不能出现在浏览器可见的地址里。
	if strings.Contains(authorizeURL, "client-secret-1") {
		t.Fatalf("authorize URL leaked the client secret: %s", authorizeURL)
	}

	// 凭据必须在发起连接时就落库：回调是另一个请求，那时请求体已经不存在了。
	row := rawPlatformOSSSetting(t, db)
	if strings.Contains(row.ValueJSON, "client-secret-1") {
		t.Fatalf("客户端密钥明文落库：%s", row.ValueJSON)
	}
	if !strings.Contains(row.ValueJSON, "enc:v1:") {
		t.Fatalf("客户端密钥没有加密：%s", row.ValueJSON)
	}
	_, value, err := svc.readOSSSetting()
	if err != nil {
		t.Fatal(err)
	}
	if value.GDrive.ClientID != "client-id-1" || value.GDrive.ClientSecret != "client-secret-1" {
		t.Fatalf("stored GDrive credentials = %#v", value.GDrive)
	}
	if value.GDrive.RefreshToken != "" {
		t.Fatalf("发起连接时不应写入刷新令牌：%q", value.GDrive.RefreshToken)
	}

	var state model.OAuthState
	if err := db.First(&state, "provider = ?", gdriveOAuthStateProvider).Error; err != nil {
		t.Fatalf("授权状态未登记：%v", err)
	}
	if state.ReauthUserID != admin.ID {
		t.Fatalf("state.ReauthUserID = %q, want %q", state.ReauthUserID, admin.ID)
	}
	if state.CodeVerifier == "" {
		t.Fatal("PKCE verifier 未保存")
	}
	if state.StateHash == "" || state.StateHash == authorizeStateFromURL(t, authorizeURL) {
		t.Fatal("state 必须只保存摘要，不能存原文")
	}
	if !state.ExpiresAt.After(time.Now()) || state.UsedAt != nil {
		t.Fatalf("state 生命周期异常：expires=%v used=%v", state.ExpiresAt, state.UsedAt)
	}
}

func TestBeginGDriveAuthorizationReusesStoredCredentials(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{
		Provider:      gdriveProvider,
		PublicBaseURL: "https://canvas.example",
		GDrive:        storage.GDriveSettings{ClientID: "saved-id", ClientSecret: "saved-secret"},
	})
	authorizeURL, err := svc.BeginGDriveAuthorization(gdriveAdmin("admin-1"), "https://canvas.example", GDriveAuthorizeRequest{})
	if err != nil {
		t.Fatalf("BeginGDriveAuthorization() error = %v", err)
	}
	if got := authorizeStateFromURL(t, authorizeURL); got == "" {
		t.Fatal("authorize URL 缺少 state")
	}
	parsed, _ := url.Parse(authorizeURL)
	if got := parsed.Query().Get("client_id"); got != "saved-id" {
		t.Fatalf("client_id = %q, want the stored value", got)
	}
}

func TestCompleteGDriveAuthorizationRejectsUnknownOrMissingState(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "https://canvas.example"})

	if err := svc.CompleteGDriveAuthorization("", "code", "https://canvas.example", "admin-1"); err == nil {
		t.Fatal("空 state 必须被拒绝")
	}
	if err := svc.CompleteGDriveAuthorization("unknown-state", "", "https://canvas.example", "admin-1"); err == nil {
		t.Fatal("空 code 必须被拒绝")
	}
	if err := svc.CompleteGDriveAuthorization("unknown-state", "code", "https://canvas.example", "admin-1"); err == nil {
		t.Fatal("未登记的 state 必须被拒绝")
	}
}

func TestCompleteGDriveAuthorizationRejectsAnotherAdmin(t *testing.T) {
	svc, db := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "https://canvas.example"})
	defer stubGDriveStorage(t, nil, nil, nil)()

	authorizeURL, err := svc.BeginGDriveAuthorization(gdriveAdmin("admin-1"), "https://canvas.example", GDriveAuthorizeRequest{ClientID: "id", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	state := authorizeStateFromURL(t, authorizeURL)

	// 换成另一个管理员完成授权 = 可能把别人的云盘接到平台上，必须拒绝。
	err = svc.CompleteGDriveAuthorization(state, "code", "https://canvas.example", "admin-2")
	if err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("CompleteGDriveAuthorization() error = %v, want owner-mismatch rejection", err)
	}
	// 失败关闭：状态已被消费，不能再被真正的主人重放。
	var stored model.OAuthState
	if err := db.First(&stored, "provider = ?", gdriveOAuthStateProvider).Error; err != nil {
		t.Fatal(err)
	}
	if stored.UsedAt == nil {
		t.Fatal("越权尝试后 state 必须已作废")
	}
}

func TestCompleteGDriveAuthorizationStoresRefreshTokenAndFolder(t *testing.T) {
	svc, db := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "https://canvas.example"})
	defer stubGDriveStorage(t,
		func(_ storage.Settings, code string, redirectURI string, verifier string) (string, string, error) {
			if code != "auth-code" || verifier == "" {
				t.Fatalf("exchange got code=%q verifier=%q", code, verifier)
			}
			if redirectURI != "https://canvas.example"+gdriveCallbackPath {
				t.Fatalf("exchange got redirectURI=%q", redirectURI)
			}
			return "refresh-token-1", "access-token-1", nil
		},
		func(_ storage.Settings, accessToken string) (string, error) {
			if accessToken != "access-token-1" {
				t.Fatalf("email lookup got token %q", accessToken)
			}
			return "owner@example.com", nil
		},
		func(_ storage.Settings) (string, string, error) { return "folder-1", "yingce-storage", nil },
	)()

	authorizeURL, err := svc.BeginGDriveAuthorization(gdriveAdmin("admin-1"), "https://canvas.example", GDriveAuthorizeRequest{ClientID: "id", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteGDriveAuthorization(authorizeStateFromURL(t, authorizeURL), "auth-code", "https://canvas.example", "admin-1"); err != nil {
		t.Fatalf("CompleteGDriveAuthorization() error = %v", err)
	}

	if strings.Contains(rawPlatformOSSSetting(t, db).ValueJSON, "refresh-token-1") {
		t.Fatal("刷新令牌明文落库")
	}
	_, value, err := svc.readOSSSetting()
	if err != nil {
		t.Fatal(err)
	}
	if value.GDrive.RefreshToken != "refresh-token-1" {
		t.Fatalf("RefreshToken = %q", value.GDrive.RefreshToken)
	}
	if value.GDrive.AccountEmail != "owner@example.com" {
		t.Fatalf("AccountEmail = %q", value.GDrive.AccountEmail)
	}
	if value.GDrive.RootFolderID != "folder-1" || value.GDrive.RootFolderName != "yingce-storage" {
		t.Fatalf("目标文件夹 = %q / %q", value.GDrive.RootFolderID, value.GDrive.RootFolderName)
	}
}

func TestCompleteGDriveAuthorizationConsumesStateOnce(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "https://canvas.example"})
	defer stubGDriveStorage(t,
		func(storage.Settings, string, string, string) (string, string, error) {
			return "refresh-token-1", "access-token-1", nil
		},
		func(storage.Settings, string) (string, error) { return "owner@example.com", nil },
		func(storage.Settings) (string, string, error) { return "folder-1", "yingce-storage", nil },
	)()

	authorizeURL, err := svc.BeginGDriveAuthorization(gdriveAdmin("admin-1"), "https://canvas.example", GDriveAuthorizeRequest{ClientID: "id", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	state := authorizeStateFromURL(t, authorizeURL)
	if err := svc.CompleteGDriveAuthorization(state, "auth-code", "https://canvas.example", "admin-1"); err != nil {
		t.Fatalf("CompleteGDriveAuthorization() error = %v", err)
	}
	if err := svc.CompleteGDriveAuthorization(state, "auth-code", "https://canvas.example", "admin-1"); err == nil {
		t.Fatal("同一个 state 不能被重复使用")
	}
}

func TestCompleteGDriveAuthorizationKeepsOldConfigWhenFolderFails(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "https://canvas.example"})
	defer stubGDriveStorage(t,
		func(storage.Settings, string, string, string) (string, string, error) {
			return "refresh-token-new", "access-token-1", nil
		},
		func(storage.Settings, string) (string, error) { return "owner@example.com", nil },
		func(storage.Settings) (string, string, error) {
			return "", "", errors.New("Drive 创建目标文件夹失败")
		},
	)()

	authorizeURL, err := svc.BeginGDriveAuthorization(gdriveAdmin("admin-1"), "https://canvas.example", GDriveAuthorizeRequest{ClientID: "id", ClientSecret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CompleteGDriveAuthorization(authorizeStateFromURL(t, authorizeURL), "auth-code", "https://canvas.example", "admin-1"); err == nil {
		t.Fatal("目标文件夹准备失败时连接必须失败")
	}
	// 一次失败的连接不能留下「有令牌但没有文件夹」的半成品配置。
	_, value, err := svc.readOSSSetting()
	if err != nil {
		t.Fatal(err)
	}
	if value.GDrive.RefreshToken != "" || value.GDrive.RootFolderID != "" {
		t.Fatalf("失败连接写入了半成品配置：%#v", value.GDrive)
	}
	// 客户端凭据是上一步有意保存的，应当保留，方便管理员直接重试。
	if value.GDrive.ClientID != "id" {
		t.Fatalf("客户端凭据被回滚：%#v", value.GDrive)
	}
}

func TestBeginGDriveAuthorizationKeepsProviderAndEnabled(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	// 平台当前是启用中的阿里云存储：连接 Drive 不能顺手改掉现网存储位置。
	seedPlatformOSS(t, svc, ossSettingValue{
		Enabled: true, Provider: aliyunOSSProvider, Endpoint: "https://oss-cn.example.com", Bucket: "bucket",
		AccessKeyID: "ak", AccessKeySecret: "sk", PublicBaseURL: "https://canvas.example",
	})
	if _, err := svc.BeginGDriveAuthorization(gdriveAdmin("admin-1"), "https://canvas.example", GDriveAuthorizeRequest{ClientID: "id", ClientSecret: "secret"}); err != nil {
		t.Fatalf("BeginGDriveAuthorization() error = %v", err)
	}
	_, value, err := svc.readOSSSetting()
	if err != nil {
		t.Fatal(err)
	}
	if !value.Enabled || value.Provider != aliyunOSSProvider {
		t.Fatalf("连接 Drive 改变了现网存储配置：enabled=%v provider=%q", value.Enabled, value.Provider)
	}
	if value.Bucket != "bucket" || value.AccessKeyID != "ak" {
		t.Fatalf("连接 Drive 清掉了现有对象存储配置：%#v", value)
	}
	// 归档凭据也不能被这次保存动到。
	if len(value.ArchivedCredentials) != 0 {
		t.Fatalf("连接 Drive 意外改动了归档凭据：%#v", value.ArchivedCredentials)
	}
}

func TestGDriveSetupReportsRedirectURIAndConnectionState(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{
		Provider:      gdriveProvider,
		PublicBaseURL: "https://canvas.example",
		GDrive: storage.GDriveSettings{
			ClientID: "id", ClientSecret: "secret", RefreshToken: "token",
			RootFolderID: "folder-1", RootFolderName: "yingce-storage", AccountEmail: "owner@example.com",
		},
	})
	status, err := svc.GDriveSetup(gdriveAdmin("admin-1"), "")
	if err != nil {
		t.Fatalf("GDriveSetup() error = %v", err)
	}
	if status.RedirectURI != "https://canvas.example"+gdriveCallbackPath {
		t.Fatalf("RedirectURI = %q", status.RedirectURI)
	}
	if !status.Connected || !status.HasClientCredentials {
		t.Fatalf("status = %#v", status)
	}
	if status.AccountEmail != "owner@example.com" || status.RootFolderName != "yingce-storage" || status.RootFolderID != "folder-1" {
		t.Fatalf("回显信息不完整：%#v", status)
	}
}

func TestDisconnectGDriveClearsTokenButKeepsClientCredentials(t *testing.T) {
	svc, db := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{
		Provider:      gdriveProvider,
		PublicBaseURL: "https://canvas.example",
		GDrive: storage.GDriveSettings{
			ClientID: "id", ClientSecret: "secret", RefreshToken: "token",
			RootFolderID: "folder-1", RootFolderName: "yingce-storage", AccountEmail: "owner@example.com",
		},
	})
	public, err := svc.DisconnectGDrive(gdriveAdmin("admin-1"))
	if err != nil {
		t.Fatalf("DisconnectGDrive() error = %v", err)
	}
	if public.HasGDriveRefreshToken {
		t.Fatal("断开后仍报告已连接")
	}
	if public.GDriveAccountEmail != "" || public.GDriveRootFolderID != "" || public.GDriveRootFolderName != "" {
		t.Fatalf("断开后仍回显旧账号信息：%#v", public)
	}
	// 客户端 ID/密钥要留着，否则换个账号重连还得再填一遍。
	if public.GDriveClientID != "id" || !public.HasGDriveClientSecret {
		t.Fatalf("断开连接把客户端凭据也清掉了：%#v", public)
	}
	_, value, err := svc.readOSSSetting()
	if err != nil {
		t.Fatal(err)
	}
	if value.GDrive.RefreshToken != "" {
		t.Fatalf("刷新令牌未被清除：%q", value.GDrive.RefreshToken)
	}
	if strings.Contains(rawPlatformOSSSetting(t, db).ValueJSON, "token") && value.GDrive.ClientSecret != "secret" {
		t.Fatal("客户端密钥在断开后被破坏")
	}
}

func TestGDriveRedirectURIResolutionOrder(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	admin := gdriveAdmin("admin-1")

	// 1) 管理员配置的对外根地址优先。
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "https://configured.example"})
	t.Setenv("CANVAS_PUBLIC_BASE_URL", "https://from-env.example")
	got, err := svc.GDriveSetup(admin, "https://from-origin.example")
	if err != nil {
		t.Fatal(err)
	}
	if got.RedirectURI != "https://configured.example"+gdriveCallbackPath {
		t.Fatalf("RedirectURI = %q, want the configured base", got.RedirectURI)
	}

	// 2) 没配置时用 CANVAS_PUBLIC_BASE_URL。
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider})
	got, err = svc.GDriveSetup(admin, "https://from-origin.example")
	if err != nil {
		t.Fatal(err)
	}
	if got.RedirectURI != "https://from-env.example"+gdriveCallbackPath {
		t.Fatalf("RedirectURI = %q, want the env base", got.RedirectURI)
	}

	// 3) 都没有时退回当前请求的 Origin。
	t.Setenv("CANVAS_PUBLIC_BASE_URL", "")
	got, err = svc.GDriveSetup(admin, "https://from-origin.example")
	if err != nil {
		t.Fatal(err)
	}
	if got.RedirectURI != "https://from-origin.example"+gdriveCallbackPath {
		t.Fatalf("RedirectURI = %q, want the request origin", got.RedirectURI)
	}

	// 4) 连 Origin 都没有时必须明确报错，不能拼出一个相对地址。
	if _, err := svc.GDriveSetup(admin, ""); err == nil {
		t.Fatal("无法确定对外访问地址时必须报错")
	}

	// 5) 非法地址要被既有校验拦住。
	seedPlatformOSS(t, svc, ossSettingValue{Provider: gdriveProvider, PublicBaseURL: "ftp://bad.example"})
	if _, err := svc.GDriveSetup(admin, "https://from-origin.example"); err == nil {
		t.Fatal("非 http/https 的对外地址必须被拒绝")
	}
}

func TestUpdateOSSSettingSwitchesFromObjectStorageToGDrive(t *testing.T) {
	// 真实首配路径：平台本来跑在阿里云上，管理员连上 Google 账号后再保存 provider=gdrive。
	// 连接流程已经把刷新令牌写进同一条设置，保存时不能再要求管理员手填一次。
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{
		Enabled: true, Provider: aliyunOSSProvider, Endpoint: "https://oss-cn.example.com", Bucket: "bucket",
		AccessKeyID: "ak", AccessKeySecret: "sk", PublicBaseURL: "https://canvas.example",
	})
	admin := gdriveAdmin("admin-1")

	// 第一步：管理员填客户端凭据并发起连接（这一步会把凭据落库）。
	if _, err := svc.BeginGDriveAuthorization(admin, "https://canvas.example", GDriveAuthorizeRequest{
		ClientID: "client-id", ClientSecret: "client-secret",
	}); err != nil {
		t.Fatalf("BeginGDriveAuthorization() error = %v", err)
	}
	// 第二步：回调完成，写入刷新令牌与目标文件夹。
	defer stubGDriveStorage(t,
		func(storage.Settings, string, string, string) (string, string, error) {
			return "refresh-token-1", "access-token-1", nil
		},
		func(storage.Settings, string) (string, error) { return "owner@example.com", nil },
		func(storage.Settings) (string, string, error) { return "folder-1", "yingce-storage", nil },
	)()
	authorizeURL, err := svc.BeginGDriveAuthorization(admin, "https://canvas.example", GDriveAuthorizeRequest{})
	if err != nil {
		t.Fatalf("BeginGDriveAuthorization() error = %v", err)
	}
	if err := svc.CompleteGDriveAuthorization(authorizeStateFromURL(t, authorizeURL), "auth-code", "https://canvas.example", admin.ID); err != nil {
		t.Fatalf("CompleteGDriveAuthorization() error = %v", err)
	}

	// 第三步：管理员在设置页把 provider 切成 gdrive 并保存。
	// 前端不会、也不该回传刷新令牌，后端必须从已保存的配置里继承。
	public, err := svc.UpdateOSSSetting(admin, OSSSettingRequest{
		Enabled: true, Provider: gdriveProvider, PublicBaseURL: "https://canvas.example",
		GDriveClientID: "client-id", GDriveRootFolderID: "folder-1",
	})
	if err != nil {
		t.Fatalf("UpdateOSSSetting() error = %v, want the saved Drive credentials to be inherited", err)
	}
	if public.Provider != gdriveProvider {
		t.Fatalf("provider = %q, want gdrive", public.Provider)
	}
	if !public.HasGDriveRefreshToken {
		t.Fatal("切到 Drive 后刷新令牌丢失，存储会立刻不可用")
	}
	if public.GDriveAccountEmail != "owner@example.com" || public.GDriveRootFolderName != "yingce-storage" {
		t.Fatalf("回显字段在切换 provider 时丢失：%#v", public)
	}
	_, value, err := svc.readOSSSetting()
	if err != nil {
		t.Fatal(err)
	}
	if value.GDrive.RefreshToken != "refresh-token-1" {
		t.Fatalf("RefreshToken = %q", value.GDrive.RefreshToken)
	}
	if !value.Enabled || value.Provider != gdriveProvider {
		t.Fatalf("保存后 enabled=%v provider=%q", value.Enabled, value.Provider)
	}
}

func TestUpdateOSSSettingActivatesGDriveFromLocalMode(t *testing.T) {
	// 用户实际遇到的路径：平台停在「本地存储」（enabled=false），管理员已经连上 Drive，
	// 现在点「保存修改」把平台切过去。这一步不能因为 provider 从 aliyun 变成 gdrive
	// 就要求管理员再填一次刷新令牌。
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{
		Enabled: false, Provider: aliyunOSSProvider, PublicBaseURL: "https://canvas.example",
		GDrive: storage.GDriveSettings{
			ClientID: "client-id", ClientSecret: "client-secret", RefreshToken: "refresh-token-1",
			RootFolderID: "folder-1", RootFolderName: "yingce-storage", AccountEmail: "owner@example.com",
		},
	})
	// 前端保存时带上表单里的可见字段；秘密字段留空，由后端继承。
	// 根文件夹 ID 也留空：它是连接流程写下的服务端状态，前端在任何情况下
	// 都不应该有权把它清空 —— 清空会让之后生成的文件全部落到云盘根目录。
	public, err := svc.UpdateOSSSetting(gdriveAdmin("admin-1"), OSSSettingRequest{
		Enabled: true, Provider: gdriveProvider, PublicBaseURL: "https://canvas.example",
		GDriveClientID: "client-id",
	})
	if err != nil {
		t.Fatalf("UpdateOSSSetting() error = %v", err)
	}
	if !public.Enabled || public.Provider != gdriveProvider {
		t.Fatalf("保存后 enabled=%v provider=%q，平台没有切到 Drive", public.Enabled, public.Provider)
	}
	if !public.HasGDriveRefreshToken {
		t.Fatal("切到 Drive 后刷新令牌丢失")
	}
	_, value, err := svc.readOSSSetting()
	if err != nil {
		t.Fatal(err)
	}
	if value.GDrive.RefreshToken != "refresh-token-1" {
		t.Fatalf("RefreshToken = %q", value.GDrive.RefreshToken)
	}
	// 根文件夹 ID 必须保住：它是连接流程建好文件夹后写下的状态。
	// 丢了之后 PutGDriveObject 不再设置 parents，生成的文件会落到云盘根目录。
	if value.GDrive.RootFolderID != "folder-1" {
		t.Fatalf("RootFolderID = %q，目标文件夹在切换 provider 时丢失", value.GDrive.RootFolderID)
	}
}

func TestOSSSettingFromRequestKeepsRootFolderAndRejectsWhenMissing(t *testing.T) {
	// 根文件夹 ID 由连接流程写入，用户没有理由主动清空它。
	// 允许留空会让上传不再设置 parents，生成的文件静默落到云盘根目录。
	current := normalizeOSSSetting(ossSettingValue{
		Provider: gdriveProvider,
		GDrive:   storage.GDriveSettings{ClientID: "id", ClientSecret: "secret", RefreshToken: "token", RootFolderID: "folder-1"},
	})
	next, err := ossSettingFromRequest(gdriveRequest(func(r *OSSSettingRequest) {
		r.GDriveRootFolderID = ""
	}), current)
	if err != nil {
		t.Fatalf("ossSettingFromRequest() error = %v", err)
	}
	if next.GDrive.RootFolderID != "folder-1" {
		t.Fatalf("RootFolderID 被清空：%q", next.GDrive.RootFolderID)
	}

	// 当前也没有根文件夹（没连过 Drive）时必须报错，不能带着空文件夹放行。
	empty := normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider})
	_, err = ossSettingFromRequest(gdriveRequest(func(r *OSSSettingRequest) {
		r.GDriveRootFolderID = ""
	}), empty)
	if err == nil || !strings.Contains(err.Error(), "目标文件夹缺失") {
		t.Fatalf("ossSettingFromRequest() error = %v, want missing-folder rejection", err)
	}
}

// TestOSSSettingFromRequestStillAllowsClearingClientID 记录一条经过修正的结论：
// 客户端 ID 是明文可见字段，前端每次保存都会回传，对它做留空继承会让清空操作失效，
// 所以它**不**继承。根文件夹 ID 则相反——它不是用户输入项，而是连接流程建好文件夹后
// 写下的状态，必须继承（否则生成的文件会落到云盘根目录）。两者的差别见下面两个用例。
func TestOSSSettingFromRequestStillAllowsClearingClientID(t *testing.T) {
	current := normalizeOSSSetting(ossSettingValue{
		Provider: gdriveProvider,
		GDrive: storage.GDriveSettings{
			ClientID: "id", ClientSecret: "secret", RefreshToken: "token", RootFolderID: "folder-1",
		},
	})
	// 清空客户端 ID 会让凭据不完整，因此这一步本来就应该被校验拒绝：
	// 断言它报的是「凭据不全」，而不是被静默补回旧值。
	_, err := ossSettingFromRequest(gdriveRequest(func(r *OSSSettingRequest) {
		r.GDriveClientID = ""
	}), current)
	if err == nil || !strings.Contains(err.Error(), "客户端 ID") {
		t.Fatalf("ossSettingFromRequest() error = %v, want incomplete-credentials rejection", err)
	}

	// 换成另一个客户端 ID 必须生效（不是被旧的覆盖）。
	// 注意：辅助函数默认会带一个根文件夹 ID，这里要显式留空才能验证「继承」。
	next, err := ossSettingFromRequest(gdriveRequest(func(r *OSSSettingRequest) {
		r.GDriveClientID = "another-id"
		r.GDriveClientSecret = ""
		r.GDriveRefreshToken = ""
		r.GDriveRootFolderID = ""
	}), current)
	if err != nil {
		t.Fatalf("ossSettingFromRequest() error = %v", err)
	}
	if next.GDrive.ClientID != "another-id" {
		t.Fatalf("ClientID = %q, want another-id", next.GDrive.ClientID)
	}
	if next.GDrive.ClientSecret != "secret" || next.GDrive.RefreshToken != "token" {
		t.Fatalf("秘密字段未继承：%#v", next.GDrive)
	}
	// 根文件夹 ID 也必须被继承，且不随客户端 ID 变化而丢失。
	if next.GDrive.RootFolderID != "folder-1" {
		t.Fatalf("RootFolderID = %q, want folder-1", next.GDrive.RootFolderID)
	}
}

func TestOSSSettingFromRequestPreservesGDriveDisplayFields(t *testing.T) {
	// 普通保存（例如只是切开关）不能把连接流程写下的账号邮箱与文件夹名清掉。
	current := normalizeOSSSetting(ossSettingValue{
		Provider: gdriveProvider,
		GDrive: storage.GDriveSettings{
			ClientID: "id", ClientSecret: "secret", RefreshToken: "token",
			RootFolderID: "folder-1", RootFolderName: "yingce-storage", AccountEmail: "owner@example.com",
		},
	})
	next, err := ossSettingFromRequest(gdriveRequest(nil), current)
	if err != nil {
		t.Fatalf("ossSettingFromRequest() error = %v", err)
	}
	if next.GDrive.AccountEmail != "owner@example.com" || next.GDrive.RootFolderName != "yingce-storage" {
		t.Fatalf("回显字段被普通保存清掉了：%#v", next.GDrive)
	}
	// 端点覆盖同样不能被请求体改写。
	if next.GDrive.APIBaseURL != "" || next.GDrive.AuthorizeURL != "" || next.GDrive.UserInfoURL != "" {
		t.Fatalf("请求体改写了服务端端点：%#v", next.GDrive)
	}
}

func TestGDriveCallbackURLEncodesMessage(t *testing.T) {
	message := "授权状态无效或已过期，请重新发起连接"
	parsed, err := url.Parse(GDriveCallbackURL(gdriveResultError, message))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != gdriveReturnPath {
		t.Fatalf("path = %q, want %q", parsed.Path, gdriveReturnPath)
	}
	if got := parsed.Query().Get("gdrive"); got != gdriveResultError {
		t.Fatalf("gdrive = %q", got)
	}
	if got := parsed.Query().Get("gdriveMessage"); got != message {
		t.Fatalf("gdriveMessage = %q, want %q", got, message)
	}
	// 成功回调不带消息，避免地址栏出现空参数。
	success, _ := url.Parse(GDriveCallbackURL(gdriveResultConnected, "  "))
	if _, exists := success.Query()["gdriveMessage"]; exists {
		t.Fatalf("成功回调不应带 gdriveMessage：%s", success.String())
	}
}

func TestGDriveSetupDoesNotCreateFolder(t *testing.T) {
	svc, _ := newGDriveOAuthTestService(t)
	seedPlatformOSS(t, svc, ossSettingValue{
		Provider: gdriveProvider, PublicBaseURL: "https://canvas.example",
		GDrive: storage.GDriveSettings{ClientID: "id", ClientSecret: "secret"},
	})
	called := false
	defer stubGDriveStorage(t, nil, nil, func(storage.Settings) (string, string, error) {
		called = true
		return "", "", nil
	})()
	if _, err := svc.GDriveSetup(gdriveAdmin("admin-1"), ""); err != nil {
		t.Fatalf("GDriveSetup() error = %v", err)
	}
	// 读状态不能产生副作用：否则管理员每次打开设置页都会在云盘里多一个空目录。
	if called {
		t.Fatal("GDriveSetup() 触发了目标文件夹创建")
	}
}
