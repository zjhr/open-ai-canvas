package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yingce/backend/internal/model"
	"yingce/backend/internal/storage"
)

func gdriveRequest(overrides func(*OSSSettingRequest)) OSSSettingRequest {
	req := OSSSettingRequest{
		Enabled:            true,
		Provider:           gdriveProvider,
		GDriveClientID:     "client-id",
		GDriveClientSecret: "client-secret",
		GDriveRefreshToken: "refresh-token",
		GDriveRootFolderID: "root-folder",
	}
	if overrides != nil {
		overrides(&req)
	}
	return req
}

// TestOSSSettingFromRequestAcceptsGDriveWithoutObjectStorageFields 覆盖 Drive 与对象存储的
// 最大差别：它没有 Bucket / Endpoint / Region / 访问密钥，这些字段不能成为启用前提。
func TestOSSSettingFromRequestAcceptsGDriveWithoutObjectStorageFields(t *testing.T) {
	next, err := ossSettingFromRequest(gdriveRequest(nil), defaultOSSSetting())
	if err != nil {
		t.Fatalf("ossSettingFromRequest() error = %v", err)
	}
	if next.Provider != gdriveProvider {
		t.Fatalf("Provider = %q, want gdrive", next.Provider)
	}
	if next.Bucket != "" || next.Endpoint != "" || next.AccessKeyID != "" || next.AccessKeySecret != "" {
		t.Fatalf("Drive setting must not carry object-storage fields: %#v", next)
	}
	if !storage.GDriveConfigured(next.GDrive) || next.GDrive.RootFolderID != "root-folder" {
		t.Fatalf("GDrive = %#v", next.GDrive)
	}
	if next.PathPrefix != defaultOSSPathPrefix {
		t.Fatalf("PathPrefix = %q, want the shared default", next.PathPrefix)
	}
}

func TestOSSSettingFromRequestRejectsIncompleteGDriveCredentials(t *testing.T) {
	cases := map[string]func(*OSSSettingRequest){
		"missing client id":     func(r *OSSSettingRequest) { r.GDriveClientID = "" },
		"missing client secret": func(r *OSSSettingRequest) { r.GDriveClientSecret = "" },
		"missing refresh token": func(r *OSSSettingRequest) { r.GDriveRefreshToken = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			req := gdriveRequest(mutate)
			_, err := ossSettingFromRequest(req, defaultOSSSetting())
			if err == nil || !strings.Contains(err.Error(), "刷新令牌") {
				t.Fatalf("ossSettingFromRequest() error = %v, want incomplete-credentials error", err)
			}
		})
	}
}

func TestOSSSettingFromRequestRejectsGDriveCDNConfiguration(t *testing.T) {
	// Drive 没有可签名的分发出口；允许填 CDN 只会让后续投递策略给出一个拿不到的地址。
	_, err := ossSettingFromRequest(gdriveRequest(func(r *OSSSettingRequest) { r.CDNBaseURL = "https://cdn.example.com" }), defaultOSSSetting())
	if err == nil || !strings.Contains(err.Error(), "不支持 CDN") {
		t.Fatalf("CDN base URL error = %v, want rejection", err)
	}
	_, err = ossSettingFromRequest(gdriveRequest(func(r *OSSSettingRequest) { r.RequireCDN = true }), defaultOSSSetting())
	if err == nil || !strings.Contains(err.Error(), "不支持 CDN") {
		t.Fatalf("RequireCDN error = %v, want rejection", err)
	}
}

func TestOSSSettingFromRequestKeepsGDriveSecretsWhenBlank(t *testing.T) {
	// 与对象存储一致的「留空表示不修改」：否则每次改根目录都会把凭据清空。
	current := normalizeOSSSetting(ossSettingValue{
		Provider: gdriveProvider,
		GDrive:   storage.GDriveSettings{ClientID: "client-id", ClientSecret: "existing-secret", RefreshToken: "existing-token", RootFolderID: "old-root"},
	})
	next, err := ossSettingFromRequest(gdriveRequest(func(r *OSSSettingRequest) {
		r.GDriveClientSecret = ""
		r.GDriveRefreshToken = ""
		r.GDriveRootFolderID = "new-root"
	}), current)
	if err != nil {
		t.Fatalf("ossSettingFromRequest() error = %v", err)
	}
	if next.GDrive.ClientSecret != "existing-secret" || next.GDrive.RefreshToken != "existing-token" {
		t.Fatalf("GDrive secrets were dropped: %#v", next.GDrive)
	}
	if next.GDrive.RootFolderID != "new-root" {
		t.Fatalf("RootFolderID = %q, want new-root", next.GDrive.RootFolderID)
	}
}

func TestOSSSettingFromRequestIgnoresCallerSuppliedGDriveEndpoints(t *testing.T) {
	// 端点覆盖只能来自运行环境；如果请求体能设它，就等价于开放了一个任意出站地址。
	current := normalizeOSSSetting(ossSettingValue{
		Provider: gdriveProvider,
		GDrive:   storage.GDriveSettings{APIBaseURL: "https://internal.example.com", TokenURL: "https://internal.example.com/token"},
	})
	next, err := ossSettingFromRequest(gdriveRequest(nil), current)
	if err != nil {
		t.Fatalf("ossSettingFromRequest() error = %v", err)
	}
	if next.GDrive.APIBaseURL != "https://internal.example.com" || next.GDrive.TokenURL != "https://internal.example.com/token" {
		t.Fatalf("caller changed the Drive endpoints: %#v", next.GDrive)
	}
}

// newGDriveCryptoTestService 预置好 32 字节的存储加密密钥。
// 不依赖 settingsEncryptionKey 的首次生成路径：它要对目录做 fsync，
// 而 Windows 不允许以写方式打开目录，这条路径在本机测试环境里必然失败。
func newGDriveCryptoTestService(t *testing.T) *Service {
	t.Helper()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, ".settings-key"), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Service{dataDir: dataDir}
}

func TestGDriveSecretsAreEncryptedAtRestAndArchivedOnProviderSwitch(t *testing.T) {
	svc := newGDriveCryptoTestService(t)
	value := normalizeOSSSetting(ossSettingValue{
		Provider: gdriveProvider,
		GDrive: storage.GDriveSettings{
			ClientID: "client-id", ClientSecret: "plain-secret", RefreshToken: "plain-token", RootFolderID: "root-folder",
		},
	})
	stored, err := svc.encryptOSSSettingSecrets(value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored.GDrive.ClientSecret, encryptedSettingPrefix) || !strings.HasPrefix(stored.GDrive.RefreshToken, encryptedSettingPrefix) {
		t.Fatalf("Drive secrets were stored in the clear: %#v", stored.GDrive)
	}
	if _, err := svc.decryptOSSSettingSecrets(&stored); err != nil {
		t.Fatal(err)
	}
	if stored.GDrive.ClientSecret != "plain-secret" || stored.GDrive.RefreshToken != "plain-token" {
		t.Fatalf("decrypted GDrive = %#v", stored.GDrive)
	}

	// 从 Drive 切到 S3 时凭据必须归档，否则历史对象的存储位置再也解析不出读取凭据。
	next := archiveOSSProviderCredentials(ossSettingValue{Provider: s3Provider}, stored)
	archived, ok := next.ArchivedCredentials[gdriveProvider]
	if !ok {
		t.Fatalf("Drive credentials were not archived: %#v", next.ArchivedCredentials)
	}
	if archived.ClientSecret != "plain-secret" || archived.RefreshToken != "plain-token" || archived.ClientID != "client-id" {
		t.Fatalf("archived Drive credentials = %#v", archived)
	}
	// 归档后仍要能解析出可用的历史配置。
	restored, err := ossSettingForProvider(next, gdriveProvider)
	if err != nil {
		t.Fatalf("ossSettingForProvider() error = %v", err)
	}
	if !storage.GDriveConfigured(restored.GDrive) {
		t.Fatalf("restored GDrive config is unusable: %#v", restored.GDrive)
	}
}

func TestGDriveWithoutCredentialsIsNotArchived(t *testing.T) {
	// 空配置不该在归档表里留下一条永远无法使用的凭据记录。
	next := archiveOSSProviderCredentials(ossSettingValue{Provider: s3Provider}, ossSettingValue{Provider: gdriveProvider})
	if _, ok := next.ArchivedCredentials[gdriveProvider]; ok {
		t.Fatalf("empty Drive config was archived: %#v", next.ArchivedCredentials)
	}
}

func TestStorageLocationDigestDistinguishesGDriveRootFolders(t *testing.T) {
	first := normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider, GDrive: storage.GDriveSettings{ClientID: "id", RootFolderID: "folder-a"}})
	second := normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider, GDrive: storage.GDriveSettings{ClientID: "id", RootFolderID: "folder-b"}})
	if storageLocationDigest(first) == storageLocationDigest(second) {
		t.Fatal("不同根目录得到了相同的存储位置摘要，历史资源会被错误复用")
	}
	// 摘要不含凭据：换密钥不应被当成换了存储位置。
	rotated := normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider, GDrive: storage.GDriveSettings{ClientID: "other-id", RootFolderID: "folder-a"}})
	if storageLocationDigest(first) != storageLocationDigest(rotated) {
		t.Fatal("存储位置摘要把凭据也算进去了")
	}
	// 连接测试摘要必须包含凭据，否则换掉刷新令牌后旧测试结论仍然有效。
	if storageTestDigest(first) == storageTestDigest(rotated) {
		t.Fatal("连接测试摘要漏掉了 Drive 凭据")
	}
}

func TestStorageDigestsForOtherProvidersAreUnchangedByGDriveSupport(t *testing.T) {
	// 回归保护：给 Drive 追加摘要字段不能改变其它厂商的摘要，否则已有部署的
	// StorageLocation 摘要会全部失配，所有 S3 配置被强制要求重新连接测试。
	// 这里的期望值就是本次改动前（HEAD bd3c1bb7）的公式算出的原值。
	s3Value := normalizeOSSSetting(ossSettingValue{Provider: s3Provider, Endpoint: "https://s3.example.com", Bucket: "b", Region: "us-east-1", PathPrefix: "open-ai-canvas"})
	aliyunValue := normalizeOSSSetting(ossSettingValue{Provider: aliyunOSSProvider, Endpoint: "https://oss.example.com", Bucket: "b", PathPrefix: "open-ai-canvas"})
	if s3Value.GDrive.RootFolderID != "" || aliyunValue.GDrive.RootFolderID != "" {
		t.Fatal("非 Drive 配置不应带根目录字段")
	}
	if got, want := storageLocationDigest(s3Value), "8d23698c1f03f84a8f7b33e24b359f86bbed2dad808b1b1f7c59ec494f05842a"; got != want {
		t.Fatalf("S3 存储位置摘要变了：got %s, want %s", got, want)
	}
	if got, want := storageLocationDigest(aliyunValue), "29e9b1f2d8c12770b72e2cbc46dd90a1d090097358e9fc512d51755cd4ec491a"; got != want {
		t.Fatalf("阿里云存储位置摘要变了：got %s, want %s", got, want)
	}
	if got, want := storageTestDigest(s3Value), "7bef54074b3dcc81ce35844698514434d4a979259abc90f82211279569d5f886"; got != want {
		t.Fatalf("S3 连接测试摘要变了：got %s, want %s", got, want)
	}
	if got, want := storageTestDigest(aliyunValue), "5cbee6aef068acec23873342d7a44857324a9a3ffcaf9588e9bae8a00452cc1e"; got != want {
		t.Fatalf("阿里云连接测试摘要变了：got %s, want %s", got, want)
	}
}

func TestUpdateUserOSSSettingRejectsGDrive(t *testing.T) {
	svc := &Service{}
	actor := &model.User{ID: "user-1"}
	_, err := svc.UpdateUserOSSSetting(actor, gdriveRequest(nil))
	if err == nil || !strings.Contains(err.Error(), "只能由管理员配置为平台存储") {
		t.Fatalf("UpdateUserOSSSetting() error = %v, want platform-only rejection", err)
	}
}

func TestPublicOSSSettingProjectsGDriveWithoutSecrets(t *testing.T) {
	svc, _ := newResourceFallbackTestService(t)
	value := normalizeOSSSetting(ossSettingValue{
		Enabled:  true,
		Provider: gdriveProvider,
		GDrive:   storage.GDriveSettings{ClientID: "client-id", ClientSecret: "secret", RefreshToken: "token", RootFolderID: "root-folder"},
	})
	public, err := svc.publicUserOSSSetting(nil, value, "user-1", false)
	if err != nil {
		t.Fatalf("publicUserOSSSetting() error = %v", err)
	}
	if public.GDriveClientID != "client-id" || public.GDriveRootFolderID != "root-folder" {
		t.Fatalf("public GDrive identity = %#v", public)
	}
	if !public.HasGDriveClientSecret || !public.HasGDriveRefreshToken {
		t.Fatalf("public projection lost the configured flags: %#v", public)
	}
	// 结构体里根本没有承载密钥明文的字段，这里是编译期与运行期双重保证。
	if public.HasAccessKeySecret {
		t.Fatalf("Drive setting reported an object-storage secret: %#v", public)
	}
}

func TestVerifyGDriveConnectionMapsFailureSteps(t *testing.T) {
	original := verifyGDriveObject
	defer func() { verifyGDriveObject = original }()

	cases := []struct {
		name       string
		operation  string
		cause      error
		wantSubstr []string
	}{
		{"write", "写入", io.ErrUnexpectedEOF, []string{"Google Drive 写入测试失败"}},
		{"range", "Range 读取", storage.ErrGDriveReadMismatch, []string{"Range 读取返回的数据与刚写入对象不一致"}},
		{"delete", "删除", io.ErrUnexpectedEOF, []string{"Google Drive 删除测试失败"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			verifyGDriveObject = func(storage.Settings, string) (string, error) {
				return testCase.operation, testCase.cause
			}
			err := verifyGDriveConnection(normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider}), "test-key")
			if err == nil {
				t.Fatal("verifyGDriveConnection() error = nil, want failure")
			}
			for _, want := range testCase.wantSubstr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want substring %q", err.Error(), want)
				}
			}
			// Drive 的提示不能照抄对象存储的排查方向。
			if strings.Contains(err.Error(), "Bucket") {
				t.Fatalf("error = %q, must not tell the user to check a Bucket", err.Error())
			}
		})
	}

	verifyGDriveObject = func(storage.Settings, string) (string, error) { return "", nil }
	if err := verifyGDriveConnection(normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider}), "test-key"); err != nil {
		t.Fatalf("verifyGDriveConnection() error = %v, want nil", err)
	}
}

func TestVerifyGDriveConnectionTellsUserToCheckDriveCredentials(t *testing.T) {
	original := verifyGDriveObject
	defer func() { verifyGDriveObject = original }()
	verifyGDriveObject = func(storage.Settings, string) (string, error) {
		return "写入", io.ErrUnexpectedEOF
	}
	err := verifyGDriveConnection(normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider}), "test-key")
	for _, want := range []string{"刷新令牌", "文件夹"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q in the guidance", err.Error(), want)
		}
	}
}

// TestWriteResourceObjectBackfillsGDriveFileID 是 Drive 与 S3 系最大的行为差异：
// 对象名不是句柄，句柄必须由写入结果回填，否则资源记录里会留下一个读不到的对象键。
func TestWriteResourceObjectBackfillsGDriveFileID(t *testing.T) {
	original := putGDriveObject
	defer func() { putGDriveObject = original }()

	var seenName string
	putGDriveObject = func(_ ossSettingValue, name string, mimeType string, size int64, body io.Reader) (string, string, error) {
		seenName = name
		if _, err := io.Copy(io.Discard, body); err != nil {
			return "", "", err
		}
		return "drive-file-1", "drive-etag", nil
	}

	service, _ := newResourceFallbackTestService(t)
	resource := &model.Resource{ID: "resource-1", UserID: "user-1", Provider: gdriveProvider, ObjectKey: "open-ai-canvas/users/u1/image/2026/10/12/x.png"}
	setting := normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider, GDrive: storage.GDriveSettings{ClientID: "id", ClientSecret: "s", RefreshToken: "t"}})
	etag, err := service.writeResourceObject(setting, resource, "image/png", 5, bytes.NewReader([]byte("bytes")))
	if err != nil {
		t.Fatalf("writeResourceObject() error = %v", err)
	}
	if etag != "drive-etag" {
		t.Fatalf("etag = %q, want drive-etag", etag)
	}
	if seenName != "open-ai-canvas/users/u1/image/2026/10/12/x.png" {
		t.Fatalf("Drive received name %q, want the logical object key", seenName)
	}
	if resource.ObjectKey != "drive-file-1" {
		t.Fatalf("ObjectKey = %q, want the Drive fileId", resource.ObjectKey)
	}
}

func TestWriteResourceObjectLeavesOtherProvidersKeyUntouched(t *testing.T) {
	original := putOSSObject
	defer func() { putOSSObject = original }()
	putOSSObject = func(_ storage.Settings, objectKey string, _ string, _ int64, body io.Reader) (string, error) {
		if _, err := io.Copy(io.Discard, body); err != nil {
			return "", err
		}
		if objectKey != "open-ai-canvas/users/u1/image/x.png" {
			t.Fatalf("objectKey = %q", objectKey)
		}
		return "s3-etag", nil
	}

	service, _ := newResourceFallbackTestService(t)
	resource := &model.Resource{ID: "resource-1", UserID: "user-1", Provider: s3Provider, ObjectKey: "open-ai-canvas/users/u1/image/x.png"}
	setting := normalizeOSSSetting(ossSettingValue{Provider: s3Provider, Endpoint: "https://s3.example.com", Bucket: "b", Region: "us-east-1"})
	if _, err := service.writeResourceObject(setting, resource, "image/png", 5, bytes.NewReader([]byte("bytes"))); err != nil {
		t.Fatalf("writeResourceObject() error = %v", err)
	}
	if resource.ObjectKey != "open-ai-canvas/users/u1/image/x.png" {
		t.Fatalf("ObjectKey was rewritten for a non-Drive provider: %q", resource.ObjectKey)
	}
}

func TestDeleteOSSObjectDispatchesToGDrive(t *testing.T) {
	original := deleteGDriveObject
	defer func() { deleteGDriveObject = original }()
	called := ""
	deleteGDriveObject = func(_ storage.Settings, objectKey string) error {
		called = objectKey
		return nil
	}
	if err := deleteOSSObject(normalizeOSSSetting(ossSettingValue{Provider: gdriveProvider}), "drive-file-9"); err != nil {
		t.Fatalf("deleteOSSObject() error = %v", err)
	}
	if called != "drive-file-9" {
		t.Fatalf("Drive delete got %q, want drive-file-9", called)
	}
}

func TestSupportedResourceDeleteProviderIncludesGDrive(t *testing.T) {
	if !supportedResourceDeleteProvider(gdriveProvider) {
		t.Fatal("supportedResourceDeleteProvider(gdrive) = false, want true")
	}
	if !supportedResourceDeleteProvider("  GDrive  ") {
		t.Fatal("provider matching must ignore case and surrounding spaces")
	}
	if supportedResourceDeleteProvider("unknown-provider") {
		t.Fatal("supportedResourceDeleteProvider(unknown) = true, want false")
	}
}

func TestValidateActiveOSSSettingAcceptsConfiguredGDrive(t *testing.T) {
	value := normalizeOSSSetting(ossSettingValue{
		Enabled:  true,
		Provider: gdriveProvider,
		GDrive:   storage.GDriveSettings{ClientID: "id", ClientSecret: "secret", RefreshToken: "token"},
	})
	if _, err := validateActiveOSSSetting(value, "disabled", "incomplete"); err != nil {
		t.Fatalf("validateActiveOSSSetting() error = %v", err)
	}

	// 只有客户端 ID 是不够的：Drive 的可用性取决于刷新令牌。
	incomplete := normalizeOSSSetting(ossSettingValue{Enabled: true, Provider: gdriveProvider, GDrive: storage.GDriveSettings{ClientID: "id"}})
	_, err := validateActiveOSSSetting(incomplete, "disabled", "你的存储配置不完整")
	if err == nil || !strings.Contains(err.Error(), "你的存储配置不完整") {
		t.Fatalf("validateActiveOSSSetting() error = %v, want incomplete error", err)
	}
}
