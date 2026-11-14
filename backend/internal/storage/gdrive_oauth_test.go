package storage

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGDriveScopesStayNonSensitive(t *testing.T) {
	// 权限集合是「管理员不用跑 Google 验证」这个性质的全部依据：
	// 一旦有人把 drive 或 drive.readonly 换进来，就会触发受限权限审核，
	// 而这一点在别处无法体现，所以在这里钉死。
	want := []string{"openid", "email", "https://www.googleapis.com/auth/drive.file"}
	got := GDriveScopes()
	if len(got) != len(want) {
		t.Fatalf("GDriveScopes() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("GDriveScopes() = %v, want %v", got, want)
		}
	}
	for _, scope := range got {
		if scope == "https://www.googleapis.com/auth/drive" || scope == "https://www.googleapis.com/auth/drive.readonly" {
			t.Fatalf("受限权限 %q 出现在默认权限集合里", scope)
		}
	}
}

func TestPKCEChallengeMatchesRFC7636Vector(t *testing.T) {
	// RFC 7636 附录 B 的官方示例，用来确认我们没有把 base64 填充或字符集写错。
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const challenge = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := PKCEChallenge(verifier); got != challenge {
		t.Fatalf("PKCEChallenge() = %q, want %q", got, challenge)
	}
}

func TestGDriveAuthorizeURLCarriesOfflineConsentAndPKCE(t *testing.T) {
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	verifier := "verifier-value"
	authorizeURL, err := GDriveAuthorizeURL(gdriveTestSetting(server), "https://canvas.example/api/admin/settings/oss/gdrive/callback", "state-value", PKCEChallenge(verifier))
	if err != nil {
		t.Fatalf("GDriveAuthorizeURL() error = %v", err)
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatalf("authorize URL 无法解析：%v", err)
	}
	if parsed.Path != "/authorize" {
		t.Fatalf("path = %q, want /authorize", parsed.Path)
	}
	query := parsed.Query()
	checks := map[string]string{
		"client_id":             "client-id",
		"redirect_uri":          "https://canvas.example/api/admin/settings/oss/gdrive/callback",
		"response_type":         "code",
		"state":                 "state-value",
		"code_challenge":        PKCEChallenge(verifier),
		"code_challenge_method": "S256",
		// 没有 offline 就不会下发 refresh_token；没有 consent 就可能在重复授权时省略它。
		"access_type": "offline",
		"prompt":      "consent",
	}
	for key, want := range checks {
		if got := query.Get(key); got != want {
			t.Fatalf("authorize query %s = %q, want %q", key, got, want)
		}
	}
	scope := query.Get("scope")
	for _, want := range GDriveScopes() {
		if !strings.Contains(scope, want) {
			t.Fatalf("scope = %q, want it to contain %q", scope, want)
		}
	}
	// 客户端密钥绝不能出现在浏览器可见的授权地址里。
	if strings.Contains(authorizeURL, "client-secret") || query.Get("client_secret") != "" {
		t.Fatalf("authorize URL leaked the client secret: %s", authorizeURL)
	}
}

func TestGDriveAuthorizeURLRequiresClientCredentials(t *testing.T) {
	setting := NormalizeSettings(Settings{Provider: gdriveProvider, GDrive: GDriveSettings{ClientID: "only-id"}})
	if _, err := GDriveAuthorizeURL(setting, "https://canvas.example/cb", "state", "challenge"); err == nil {
		t.Fatal("GDriveAuthorizeURL() error = nil, want missing-credentials error")
	}
	complete := NormalizeSettings(Settings{Provider: gdriveProvider, GDrive: GDriveSettings{ClientID: "id", ClientSecret: "secret"}})
	if _, err := GDriveAuthorizeURL(complete, "", "state", "challenge"); err == nil {
		t.Fatal("GDriveAuthorizeURL() error = nil, want missing-parameter error")
	}
}

func TestGDriveExchangeCodeSendsVerifierAndReturnsRefreshToken(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	fake.refreshTokenIssued = "fresh-refresh-token"
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	setting.GDrive.RefreshToken = ""
	refresh, accessToken, err := GDriveExchangeCode(setting, "auth-code", "https://canvas.example/cb", "verifier-value")
	if err != nil {
		t.Fatalf("GDriveExchangeCode() error = %v", err)
	}
	if refresh != "fresh-refresh-token" {
		t.Fatalf("refreshToken = %q", refresh)
	}
	if accessToken != "test-access-token" {
		t.Fatalf("accessToken = %q", accessToken)
	}
	form := fake.lastTokenForm
	checks := map[string]string{
		"grant_type":    "authorization_code",
		"code":          "auth-code",
		"code_verifier": "verifier-value",
		"redirect_uri":  "https://canvas.example/cb",
		"client_id":     "client-id",
		"client_secret": "client-secret",
	}
	for key, want := range checks {
		if got := form.Get(key); got != want {
			t.Fatalf("token form %s = %q, want %q", key, got, want)
		}
	}
}

func TestGDriveExchangeCodeRejectsMissingRefreshToken(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	// Google 只在首次授权（或 prompt=consent）时下发 refresh_token；
	// 拿不到时必须明确报错，不能假装连接成功。
	fake.omitRefreshToken = true
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	setting.GDrive.RefreshToken = ""
	_, _, err := GDriveExchangeCode(setting, "auth-code", "https://canvas.example/cb", "verifier")
	if err == nil || !strings.Contains(err.Error(), "撤销") {
		t.Fatalf("GDriveExchangeCode() error = %v, want actionable missing-refresh-token error", err)
	}
}

func TestGDriveExchangeCodeRequiresClientCredentials(t *testing.T) {
	setting := NormalizeSettings(Settings{Provider: gdriveProvider, GDrive: GDriveSettings{ClientID: "id"}})
	if _, _, err := GDriveExchangeCode(setting, "code", "https://canvas.example/cb", "verifier"); err == nil {
		t.Fatal("GDriveExchangeCode() error = nil, want missing-credentials error")
	}
	complete := NormalizeSettings(Settings{Provider: gdriveProvider, GDrive: GDriveSettings{ClientID: "id", ClientSecret: "secret"}})
	if _, _, err := GDriveExchangeCode(complete, "  ", "https://canvas.example/cb", "verifier"); err == nil {
		t.Fatal("GDriveExchangeCode() error = nil, want missing-code error")
	}
}

func TestGDriveAccountEmailReadsUserInfo(t *testing.T) {
	fake := newFakeGDrive(t)
	fake.accountEmail = "owner@example.com"
	server := fake.server()
	defer server.Close()

	email, err := GDriveAccountEmail(gdriveTestSetting(server), "test-access-token")
	if err != nil {
		t.Fatalf("GDriveAccountEmail() error = %v", err)
	}
	if email != "owner@example.com" {
		t.Fatalf("email = %q, want owner@example.com", email)
	}
}

func TestGDriveAccountEmailRequiresToken(t *testing.T) {
	setting := NormalizeSettings(Settings{Provider: gdriveProvider})
	if _, err := GDriveAccountEmail(setting, "   "); err == nil {
		t.Fatal("GDriveAccountEmail() error = nil, want empty-token error")
	}
}

func TestEnsureGDriveRootFolderReusesAccessibleFolder(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	fake.folders["root-folder-id"] = "my-existing-folder"
	server := fake.server()
	defer server.Close()

	folderID, name, err := EnsureGDriveRootFolder(gdriveTestSetting(server))
	if err != nil {
		t.Fatalf("EnsureGDriveRootFolder() error = %v", err)
	}
	if folderID != "root-folder-id" || name != "my-existing-folder" {
		t.Fatalf("EnsureGDriveRootFolder() = %q, %q", folderID, name)
	}
	// 已登记的文件夹可用时不该再建一个，否则每次重连都会在用户云盘里多一个空目录。
	if fake.lastCreateFolder != nil {
		t.Fatalf("EnsureGDriveRootFolder() created a folder despite an accessible one: %#v", fake.lastCreateFolder)
	}
}

func TestEnsureGDriveRootFolderCreatesWhenUnset(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	setting.GDrive.RootFolderID = ""
	folderID, name, err := EnsureGDriveRootFolder(setting)
	if err != nil {
		t.Fatalf("EnsureGDriveRootFolder() error = %v", err)
	}
	if folderID != "folder-created" {
		t.Fatalf("folderID = %q, want folder-created", folderID)
	}
	if name != gdriveDefaultRootFolderName {
		t.Fatalf("name = %q, want %q", name, gdriveDefaultRootFolderName)
	}
	if got := fake.lastCreateFolder["mimeType"]; got != gdriveFolderMimeType {
		t.Fatalf("created mimeType = %v, want %v", got, gdriveFolderMimeType)
	}
	// 新建的文件夹必须落在「我的云端硬盘」根目录：显式传 parents 会要求对父目录有权限。
	if _, exists := fake.lastCreateFolder["parents"]; exists {
		t.Fatalf("created folder should not set parents, got %#v", fake.lastCreateFolder["parents"])
	}
}

func TestEnsureGDriveRootFolderRecreatesWhenRegisteredFolderIsGone(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	// setting 里登记了 root-folder-id，但服务端已经没有它（被删除/权限丢失）。
	server := fake.server()
	defer server.Close()

	folderID, name, err := EnsureGDriveRootFolder(gdriveTestSetting(server))
	if err != nil {
		t.Fatalf("EnsureGDriveRootFolder() error = %v", err)
	}
	if folderID != "folder-created" || name != gdriveDefaultRootFolderName {
		t.Fatalf("EnsureGDriveRootFolder() = %q, %q; want a recreated folder", folderID, name)
	}
	if fake.lastCreateFolder == nil {
		t.Fatal("EnsureGDriveRootFolder() did not recreate the folder")
	}
}

func TestEnsureGDriveRootFolderRecreatesWhenFolderIsTrashed(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	fake.folders["root-folder-id"] = "old-folder"
	fake.trashedFolders["root-folder-id"] = true
	server := fake.server()
	defer server.Close()

	folderID, _, err := EnsureGDriveRootFolder(gdriveTestSetting(server))
	if err != nil {
		t.Fatalf("EnsureGDriveRootFolder() error = %v", err)
	}
	if folderID != "folder-created" {
		t.Fatalf("folderID = %q, want a recreated folder when the registered one is trashed", folderID)
	}
}

func TestEnsureGDriveRootFolderReportsNonMissingFailure(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	// 500 不是「文件夹不存在」，必须原样上报：退回新建只会把真实故障盖成一个空目录。
	fake.folderMetadataCode = http.StatusInternalServerError
	server := fake.server()
	defer server.Close()

	_, _, err := EnsureGDriveRootFolder(gdriveTestSetting(server))
	if err == nil {
		t.Fatal("EnsureGDriveRootFolder() error = nil, want the upstream failure")
	}
	if errors.Is(err, errGDriveFolderMissing) {
		t.Fatalf("error = %v, must not be classified as a missing folder", err)
	}
	if fake.lastCreateFolder != nil {
		t.Fatalf("EnsureGDriveRootFolder() created a folder after a non-missing failure: %#v", fake.lastCreateFolder)
	}
}

func TestGDriveRootFolderInfoDoesNotCreateWhenUnset(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	setting.GDrive.RootFolderID = ""
	folderID, name, err := GDriveRootFolderInfo(setting)
	if err != nil {
		t.Fatalf("GDriveRootFolderInfo() error = %v", err)
	}
	if folderID != "" || name != "" {
		t.Fatalf("GDriveRootFolderInfo() = %q, %q; want empty for an unconnected setting", folderID, name)
	}
	// 读状态不能产生副作用。
	if fake.lastCreateFolder != nil {
		t.Fatalf("GDriveRootFolderInfo() created a folder: %#v", fake.lastCreateFolder)
	}
}

func TestGDriveRootFolderInfoReturnsRegisteredFolder(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	fake.folders["root-folder-id"] = "my-folder"
	server := fake.server()
	defer server.Close()

	folderID, name, err := GDriveRootFolderInfo(gdriveTestSetting(server))
	if err != nil {
		t.Fatalf("GDriveRootFolderInfo() error = %v", err)
	}
	if folderID != "root-folder-id" || name != "my-folder" {
		t.Fatalf("GDriveRootFolderInfo() = %q, %q", folderID, name)
	}
}

func TestGDriveRootFolderInfoReportsMissingFolder(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	folderID, _, err := GDriveRootFolderInfo(gdriveTestSetting(server))
	if !errors.Is(err, errGDriveFolderMissing) {
		t.Fatalf("GDriveRootFolderInfo() error = %v, want errGDriveFolderMissing", err)
	}
	// 失败时仍要回传已登记的 ID，界面才能告诉管理员「登记的文件夹已经不可用」。
	if folderID != "root-folder-id" {
		t.Fatalf("folderID = %q, want the registered id", folderID)
	}
}
