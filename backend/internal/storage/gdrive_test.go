package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeGDrive 是一个最小可用的 Drive 假服务：它只实现本项目真正用到的四个动作
// —— 换令牌、开 resumable 会话、PUT 内容、alt=media 读取、删除，
// 让 Drive 适配器可以在没有真实 Google 凭据的情况下被完整验证。
type fakeGDrive struct {
	t *testing.T

	mu             sync.Mutex
	tokenCalls     int
	lastTokenForm  url.Values
	lastMetadata   map[string]any
	lastInitHeader http.Header
	lastPutAuth    string
	lastPutType    string
	lastPutBody    []byte
	lastRange      string
	lastMediaQuery string
	lastDeleteID   string
	files          map[string][]byte
	deleted        map[string]bool

	// OAuth 与文件夹相关
	folders            map[string]string // 可访问的文件夹 id -> name
	trashedFolders     map[string]bool
	lastCreateFolder   map[string]any
	lastTokenGrantType string
	refreshTokenIssued string
	accountEmail       string
	omitRefreshToken   bool
	folderMetadataCode int

	// 行为开关
	tokenExpiresIn  int64
	failToken       bool
	tokenStatus     int
	tokenMessage    string
	omitLocation    bool
	uploadStatus    int
	uploadMessage   string
	mediaStatus     int
	mediaMessage    string
	mediaStatusCode int
	missingFileID   string
	// tamperRead 让读取返回与写入不一致的字节，用来验证回读失败的处理。
	tamperRead bool
}

func newFakeGDrive(t *testing.T) *fakeGDrive {
	t.Helper()
	return &fakeGDrive{
		t:                  t,
		tokenExpiresIn:     3600,
		files:              map[string][]byte{},
		deleted:            map[string]bool{},
		folders:            map[string]string{},
		trashedFolders:     map[string]bool{},
		refreshTokenIssued: "test-refresh-token",
		accountEmail:       "operator@example.com",
	}
}

func (f *fakeGDrive) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", f.handleToken)
	mux.HandleFunc("/v1/userinfo", f.handleUserInfo)
	// 建文件夹走 /drive/v3/files（POST），与子路径的读取/删除分开注册。
	mux.HandleFunc("/drive/v3/files", f.handleCreateFolder)
	mux.HandleFunc("/upload/drive/v3/files", f.handleInitiate)
	mux.HandleFunc("/upload/session/1", f.handlePut)
	mux.HandleFunc("/drive/v3/files/", f.handleFile)
	return httptest.NewServer(mux)
}

func (f *fakeGDrive) handleUserInfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	auth := r.Header.Get("Authorization")
	f.mu.Unlock()
	if !strings.HasPrefix(auth, "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_token"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"email": f.accountEmail})
}

func (f *fakeGDrive) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var metadata map[string]any
	if err := json.Unmarshal(body, &metadata); err != nil {
		f.t.Errorf("folder metadata decode: %v", err)
	}
	f.mu.Lock()
	f.lastCreateFolder = metadata
	f.mu.Unlock()
	name, _ := metadata["name"].(string)
	created := "folder-created"
	f.mu.Lock()
	f.folders[created] = name
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": created, "name": name})
}

func (f *fakeGDrive) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		f.t.Errorf("token form parse: %v", err)
	}
	f.mu.Lock()
	f.tokenCalls++
	f.lastTokenForm = r.PostForm
	f.lastTokenGrantType = r.PostForm.Get("grant_type")
	f.mu.Unlock()
	if f.failToken || f.tokenStatus != 0 {
		status := f.tokenStatus
		if status == 0 {
			status = http.StatusBadRequest
		}
		message := f.tokenMessage
		if message == "" {
			message = "invalid_grant"
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"`+message+`"}`)
		return
	}
	payload := map[string]any{"access_token": "test-access-token", "expires_in": f.tokenExpiresIn}
	// 只有 authorization_code 授权会下发 refresh_token；refresh_token 授权不会。
	if r.PostForm.Get("grant_type") == "authorization_code" && !f.omitRefreshToken {
		payload["refresh_token"] = f.refreshTokenIssued
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func (f *fakeGDrive) handleInitiate(w http.ResponseWriter, r *http.Request) {
	if got := r.URL.Query().Get("uploadType"); got != "resumable" {
		f.t.Errorf("uploadType = %q, want resumable", got)
	}
	body, _ := io.ReadAll(r.Body)
	var metadata map[string]any
	if err := json.Unmarshal(body, &metadata); err != nil {
		f.t.Errorf("metadata decode: %v", err)
	}
	f.mu.Lock()
	f.lastMetadata = metadata
	f.lastInitHeader = r.Header.Clone()
	f.mu.Unlock()
	if f.omitLocation {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Header().Set("Location", "http://"+r.Host+"/upload/session/1")
	w.WriteHeader(http.StatusOK)
}

func (f *fakeGDrive) handlePut(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.lastPutAuth = r.Header.Get("Authorization")
	f.lastPutType = r.Header.Get("Content-Type")
	f.lastPutBody = body
	f.mu.Unlock()
	if f.uploadStatus != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.uploadStatus)
		_, _ = io.WriteString(w, `{"error":{"code":`+fmt.Sprint(f.uploadStatus)+`,"message":"`+f.uploadMessage+`"}}`)
		return
	}
	f.mu.Lock()
	f.files["file-1"] = body
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "file-1", "md5Checksum": "d41d8cd98f00b204e9800998ecf8427e", "size": fmt.Sprint(len(body))})
}

func (f *fakeGDrive) handleFile(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/drive/v3/files/")
	isMedia := r.URL.Query().Get("alt") == "media"
	f.mu.Lock()
	if r.Method == http.MethodGet && isMedia {
		// 只记录媒体读取：删除与元数据读取都会走这个 handler，
		// 别让它们把 Range 断言冲掉。
		f.lastRange = r.Header.Get("Range")
		f.lastMediaQuery = r.URL.RawQuery
	}
	if r.Method == http.MethodDelete {
		f.lastDeleteID = id
	}
	content, ok := f.files[id]
	f.mu.Unlock()

	if r.Method == http.MethodDelete {
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		f.mu.Lock()
		delete(f.files, id)
		f.deleted[id] = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// 不带 alt=media 的 GET 是元数据读取（我们只用它查目标文件夹）。
	if r.Method == http.MethodGet && !isMedia {
		f.mu.Lock()
		name, folderOK := f.folders[id]
		trashed := f.trashedFolders[id]
		code := f.folderMetadataCode
		f.mu.Unlock()
		if code != 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"error":{"code":`+fmt.Sprint(code)+`,"message":"metadata failure"}}`)
			return
		}
		if !folderOK {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":404,"message":"File not found"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": name, "trashed": trashed})
		return
	}
	if f.mediaStatus != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.mediaStatus)
		_, _ = io.WriteString(w, `{"error":{"code":`+fmt.Sprint(f.mediaStatus)+`,"message":"`+f.mediaMessage+`"}}`)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":404,"message":"File not found"}}`)
		return
	}
	status := f.mediaStatusCode
	source := content
	if f.tamperRead {
		source = []byte("tampered-payload")
	}
	body := source
	rangeHeader := r.Header.Get("Range")
	if rangeHeader != "" && status == 0 {
		// 真正按 Range 切片，这样测试验证的是适配器的透传而不是假服务的宽容。
		start, end, ok := parseTestRange(rangeHeader, len(source))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		status = http.StatusPartialContent
		body = source[start : end+1]
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(source)))
		w.Header().Set("Accept-Ranges", "bytes")
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// parseTestRange 只解析 `bytes=start-end` 这一种形式，够驱动前面的读取断言。
func parseTestRange(header string, size int) (int, int, bool) {
	value := strings.TrimPrefix(header, "bytes=")
	startText, endText, ok := strings.Cut(value, "-")
	if !ok {
		return 0, 0, false
	}
	start, err := strconv.Atoi(startText)
	if err != nil || start < 0 || start >= size {
		return 0, 0, false
	}
	end, err := strconv.Atoi(endText)
	if err != nil || end < start {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

func (f *fakeGDrive) tokenCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokenCalls
}

func gdriveTestSetting(server *httptest.Server) Settings {
	return NormalizeSettings(Settings{
		Provider: gdriveProvider,
		GDrive: GDriveSettings{
			ClientID:     "client-id",
			ClientSecret: "client-secret",
			RefreshToken: "refresh-token",
			RootFolderID: "root-folder-id",
			APIBaseURL:   server.URL,
			TokenURL:     server.URL + "/token",
			AuthorizeURL: server.URL + "/authorize",
			UserInfoURL:  server.URL + "/v1/userinfo",
		},
	})
}

func TestPutGDriveObjectUsesResumableSessionAndReturnsFileID(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	payload := []byte("yingce-storage-test")
	setting := gdriveTestSetting(server)
	fileID, etag, err := PutGDriveObject(setting, "open-ai-canvas/users/u1/image/2026/10/12/abc.png", "image/png", int64(len(payload)), bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("PutGDriveObject() error = %v", err)
	}
	if fileID != "file-1" {
		t.Fatalf("fileID = %q, want file-1", fileID)
	}
	if etag != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Fatalf("etag = %q, want Drive md5Checksum", etag)
	}

	// Drive 的名字不是路径：展示名取末段，完整逻辑路径留在 description 里可回溯。
	if got := fake.lastMetadata["name"]; got != "abc.png" {
		t.Fatalf("metadata name = %v, want abc.png", got)
	}
	if got := fake.lastMetadata["description"]; got != "open-ai-canvas/users/u1/image/2026/10/12/abc.png" {
		t.Fatalf("metadata description = %v, want the logical object key", got)
	}
	if got := fake.lastMetadata["mimeType"]; got != "image/png" {
		t.Fatalf("metadata mimeType = %v, want image/png", got)
	}
	parents, ok := fake.lastMetadata["parents"].([]any)
	if !ok || len(parents) != 1 || parents[0] != "root-folder-id" {
		t.Fatalf("metadata parents = %#v, want [root-folder-id]", fake.lastMetadata["parents"])
	}
	if got := fake.lastInitHeader.Get("X-Upload-Content-Type"); got != "image/png" {
		t.Fatalf("X-Upload-Content-Type = %q", got)
	}
	if got := fake.lastInitHeader.Get("Authorization"); got != "Bearer test-access-token" {
		t.Fatalf("initiate Authorization = %q", got)
	}
	if got := fake.lastInitHeader.Get("X-Upload-Content-Length"); got != fmt.Sprint(len(payload)) {
		t.Fatalf("X-Upload-Content-Length = %q", got)
	}
	if fake.lastPutAuth != "Bearer test-access-token" || fake.lastPutType != "image/png" {
		t.Fatalf("PUT headers = auth %q type %q", fake.lastPutAuth, fake.lastPutType)
	}
	if !bytes.Equal(fake.lastPutBody, payload) {
		t.Fatalf("PUT body = %q, want %q", fake.lastPutBody, payload)
	}
}

func TestPutGDriveObjectOmitsParentsWithoutRootFolder(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	setting.GDrive.RootFolderID = ""
	payload := []byte("x")
	if _, _, err := PutGDriveObject(setting, "k.png", "image/png", int64(len(payload)), bytes.NewReader(payload)); err != nil {
		t.Fatalf("PutGDriveObject() error = %v", err)
	}
	if _, exists := fake.lastMetadata["parents"]; exists {
		t.Fatalf("metadata parents should be omitted without a root folder, got %#v", fake.lastMetadata["parents"])
	}
}

func TestPutGDriveObjectBuffersUnknownLength(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	payload := []byte("stream-without-length")
	// size = 0 表示调用方不知道长度；此时必须自己缓冲后声明长度，
	// 否则 resumable 会话无法开始。
	if _, _, err := PutGDriveObject(gdriveTestSetting(server), "k.bin", "application/octet-stream", 0, bytes.NewReader(payload)); err != nil {
		t.Fatalf("PutGDriveObject() error = %v", err)
	}
	if !bytes.Equal(fake.lastPutBody, payload) {
		t.Fatalf("PUT body = %q, want buffered %q", fake.lastPutBody, payload)
	}
	if got := fake.lastInitHeader.Get("X-Upload-Content-Length"); got != fmt.Sprint(len(payload)) {
		t.Fatalf("X-Upload-Content-Length = %q, want %d", got, len(payload))
	}
}

func TestPutGDriveObjectRejectsOversizedUnknownLength(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	setting.Runtime.MaxBufferedUploadSize = 4
	_, _, err := PutGDriveObject(setting, "k.bin", "application/octet-stream", 0, bytes.NewReader([]byte("way-too-long")))
	if err == nil || !strings.Contains(err.Error(), "缓冲上限") {
		t.Fatalf("PutGDriveObject() error = %v, want buffer limit error", err)
	}
}

func TestGetGDriveObjectRangeForwardsRangeAndContentRange(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	fake.files["file-9"] = []byte("0123456789abcdef")
	stream, err := GetGDriveObjectRange(gdriveTestSetting(server), "file-9", "bytes=0-3")
	if err != nil {
		t.Fatalf("GetGDriveObjectRange() error = %v", err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusPartialContent {
		t.Fatalf("StatusCode = %d, want 206", stream.StatusCode)
	}
	if stream.ContentRange != "bytes 0-3/16" {
		t.Fatalf("ContentRange = %q", stream.ContentRange)
	}
	if stream.AcceptRanges != "bytes" {
		t.Fatalf("AcceptRanges = %q", stream.AcceptRanges)
	}
	data, _ := io.ReadAll(stream.Body)
	if string(data) != "0123" {
		t.Fatalf("body = %q, want the requested 4-byte range", data)
	}
	if fake.lastRange != "bytes=0-3" {
		t.Fatalf("upstream Range = %q, want bytes=0-3", fake.lastRange)
	}
	if !strings.Contains(fake.lastMediaQuery, "alt=media") {
		t.Fatalf("media query = %q, want alt=media", fake.lastMediaQuery)
	}
}

func TestGetGDriveObjectRangeSurfacesGoogleError(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	fake.mediaStatus = http.StatusForbidden
	fake.mediaMessage = "The user has not granted the app read access"
	_, err := GetGDriveObjectRange(gdriveTestSetting(server), "file-9", "")
	if err == nil {
		t.Fatal("GetGDriveObjectRange() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "Google Drive 读取失败") || !strings.Contains(err.Error(), "read access") {
		t.Fatalf("error = %v, want Drive operation and Google message", err)
	}
}

func TestDeleteGDriveObjectTreatsMissingFileAsSuccess(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	// 删除必须幂等：对象已经不在了，清理就算成功，否则重试会把已收敛的状态报成失败。
	if err := DeleteGDriveObject(gdriveTestSetting(server), "already-gone"); err != nil {
		t.Fatalf("DeleteGDriveObject() on missing file error = %v, want nil", err)
	}

	fake.files["file-7"] = []byte("payload")
	if err := DeleteGDriveObject(gdriveTestSetting(server), "file-7"); err != nil {
		t.Fatalf("DeleteGDriveObject() error = %v", err)
	}
	if fake.lastDeleteID != "file-7" || !fake.deleted["file-7"] {
		t.Fatalf("delete did not reach Drive: id=%q deleted=%v", fake.lastDeleteID, fake.deleted)
	}
}

func TestGDriveAccessTokenIsCachedAcrossRequests(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	for i := 0; i < 4; i++ {
		payload := []byte("payload")
		if _, _, err := PutGDriveObject(setting, "k.bin", "application/octet-stream", int64(len(payload)), bytes.NewReader(payload)); err != nil {
			t.Fatalf("PutGDriveObject() error = %v", err)
		}
	}
	if got := fake.tokenCallCount(); got != 1 {
		t.Fatalf("token endpoint calls = %d, want 1 (access token must be cached)", got)
	}
	if form := fake.lastTokenForm; form.Get("grant_type") != "refresh_token" ||
		form.Get("client_id") != "client-id" ||
		form.Get("client_secret") != "client-secret" ||
		form.Get("refresh_token") != "refresh-token" {
		t.Fatalf("token form = %#v", form)
	}
}

func TestGDriveAccessTokenRefreshesWhenExpired(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	// 寿命短于安全窗口，缓存必须立刻视为失效并重新换取。
	fake.tokenExpiresIn = 1
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	for i := 0; i < 2; i++ {
		if _, err := gdriveAccessToken(setting); err != nil {
			t.Fatalf("gdriveAccessToken() error = %v", err)
		}
	}
	if got := fake.tokenCallCount(); got != 2 {
		t.Fatalf("token endpoint calls = %d, want 2 (expired token must be refreshed)", got)
	}
}

func TestGDriveAccessTokenReportsInvalidGrant(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	fake.failToken = true
	fake.tokenMessage = "Token has been expired or revoked."
	server := fake.server()
	defer server.Close()

	_, err := gdriveAccessToken(gdriveTestSetting(server))
	if err == nil || !strings.Contains(err.Error(), "Token has been expired or revoked.") {
		t.Fatalf("gdriveAccessToken() error = %v, want the Google reason", err)
	}
}

func TestGDriveRequiresCompleteCredentials(t *testing.T) {
	setting := NormalizeSettings(Settings{Provider: gdriveProvider, GDrive: GDriveSettings{ClientID: "only-id"}})
	if _, err := gdriveAccessToken(setting); err == nil || !strings.Contains(err.Error(), "凭据不完整") {
		t.Fatalf("gdriveAccessToken() error = %v, want incomplete credentials error", err)
	}
	if GDriveConfigured(setting.GDrive) {
		t.Fatal("GDriveConfigured() = true for incomplete credentials")
	}
}

func TestGDriveUploadReportsMissingSessionLocation(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	fake.omitLocation = true
	server := fake.server()
	defer server.Close()

	_, _, err := PutGDriveObject(gdriveTestSetting(server), "k.bin", "application/octet-stream", 1, bytes.NewReader([]byte("x")))
	if err == nil || !strings.Contains(err.Error(), "上传会话地址") {
		t.Fatalf("PutGDriveObject() error = %v, want missing session error", err)
	}
}

func TestGDriveUploadSurfacesQuotaError(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	fake.uploadStatus = http.StatusForbidden
	fake.uploadMessage = "The user's Drive storage quota has been exceeded."
	server := fake.server()
	defer server.Close()

	_, _, err := PutGDriveObject(gdriveTestSetting(server), "k.bin", "application/octet-stream", 1, bytes.NewReader([]byte("x")))
	if err == nil || !strings.Contains(err.Error(), "quota has been exceeded") {
		t.Fatalf("PutGDriveObject() error = %v, want quota reason", err)
	}
}

func TestGDriveFileIDRejectsInvalidKeys(t *testing.T) {
	// ObjectKey 对 Drive 就是 fileId：路径式 key 说明资源记录的 provider 与
	// 对象键对不上，必须在发请求之前就失败，而不是把路径拼进 Drive 的 URL。
	for _, value := range []string{"", "   ", "users/u1/image/a.png", "id?x=1", "id#frag", "id\\x"} {
		if _, err := gdriveFileID(value); err == nil {
			t.Fatalf("gdriveFileID(%q) error = nil, want rejection", value)
		}
	}
	if got, err := gdriveFileID("  1AbC_fileId-9  "); err != nil || got != "1AbC_fileId-9" {
		t.Fatalf("gdriveFileID() = %q, %v", got, err)
	}
}

func TestVerifyGDriveObjectRunsWriteReadDelete(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	operation, err := VerifyGDriveObject(gdriveTestSetting(server), "open-ai-canvas/.yingce-tests/platform/id")
	if err != nil {
		t.Fatalf("VerifyGDriveObject() error = %v", err)
	}
	if operation != "" {
		t.Fatalf("operation = %q, want empty on success", operation)
	}
	// 三步都必须真的发生过：写入留下了对象，读取带上了 Range，删除把它清掉了。
	if len(fake.files) != 0 || !fake.deleted["file-1"] {
		t.Fatalf("files = %v deleted = %v, want write-then-delete", fake.files, fake.deleted)
	}
	if fake.lastRange != "bytes=0-3" {
		t.Fatalf("connection test Range = %q, want bytes=0-3", fake.lastRange)
	}
}

func TestVerifyGDriveObjectRejectsMismatchedReadback(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	// 只实现「写入成功、但回读内容是别的」这一种故障：200 本身是被允许的
	// （服务端可以忽略 Range 返回完整对象），但内容必须与刚写入的一致。
	mux := http.NewServeMux()
	mux.HandleFunc("/token", fake.handleToken)
	mux.HandleFunc("/upload/drive/v3/files", fake.handleInitiate)
	mux.HandleFunc("/upload/session/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "file-1", "md5Checksum": "x"})
	})
	mux.HandleFunc("/drive/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "tampered-payload")
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	operation, err := VerifyGDriveObject(gdriveTestSetting(server), "test-key")
	if operation != "Range 读取" {
		t.Fatalf("operation = %q, want Range 读取", operation)
	}
	if err == nil || !errors.Is(err, ErrGDriveReadMismatch) {
		t.Fatalf("VerifyGDriveObject() error = %v, want ErrGDriveReadMismatch", err)
	}
}

func TestVerifyGDriveObjectCleansUpAfterFailedReadback(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	fake.tamperRead = true
	server := fake.server()
	defer server.Close()

	operation, err := VerifyGDriveObject(gdriveTestSetting(server), "test-key")
	if operation != "Range 读取" || !errors.Is(err, ErrGDriveReadMismatch) {
		t.Fatalf("VerifyGDriveObject() = %q, %v; want Range 读取 + mismatch", operation, err)
	}
	// 回读失败也必须把测试对象删掉，否则每次失败的连接测试都在用户 Drive 里留垃圾。
	if len(fake.files) != 0 || !fake.deleted["file-1"] {
		t.Fatalf("files = %v deleted = %v, want cleanup after failed readback", fake.files, fake.deleted)
	}
}

func TestVerifyGDriveObjectReportsDeleteStep(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	fake.files["file-1"] = []byte("yingce-storage-test")
	// 读取正常、删除失败：失败步骤必须报「删除」而不是笼统的写入失败。
	mux := http.NewServeMux()
	mux.HandleFunc("/token", fake.handleToken)
	mux.HandleFunc("/upload/drive/v3/files", fake.handleInitiate)
	mux.HandleFunc("/upload/session/1", fake.handlePut)
	mux.HandleFunc("/drive/v3/files/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":403,"message":"Insufficient permissions"}}`)
			return
		}
		w.WriteHeader(http.StatusPartialContent)
		w.Header().Set("Content-Range", "bytes 0-3/19")
		_, _ = io.WriteString(w, "ying")
	})
	server2 := httptest.NewServer(mux)
	defer server2.Close()

	operation, err := VerifyGDriveObject(gdriveTestSetting(server2), "test-key")
	if operation != "删除" {
		t.Fatalf("operation = %q, want 删除", operation)
	}
	if err == nil || !strings.Contains(err.Error(), "Insufficient permissions") {
		t.Fatalf("VerifyGDriveObject() error = %v, want Drive permissions reason", err)
	}
}

func TestGDriveObjectNameFallsBackWhenEmpty(t *testing.T) {
	for input, want := range map[string]string{
		"a/b/c.png":  "c.png",
		"/a/b.png":   "b.png",
		"plain.png":  "plain.png",
		"":           "yingce-object",
		"/":          "yingce-object",
		"a/b/":       "b",
		"deep/x/y/z": "z",
	} {
		if got := gdriveObjectName(input); got != want {
			t.Fatalf("gdriveObjectName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGDriveEndpointsFallBackToGoogleDefaults(t *testing.T) {
	setting := NormalizeSettings(Settings{Provider: gdriveProvider, Runtime: RuntimePolicy{}})
	if got := gdriveAPIBase(setting); got != gdriveDefaultAPIBase {
		t.Fatalf("gdriveAPIBase() = %q, want default", got)
	}
	if got := gdriveTokenEndpoint(setting); got != gdriveDefaultTokenURL {
		t.Fatalf("gdriveTokenEndpoint() = %q, want default", got)
	}
}

func TestRequiresPlatformProxyOnlyForGDrive(t *testing.T) {
	// Drive 本身没有直连能力，与配置无关。
	if !RequiresPlatformProxy(NormalizeSettings(Settings{Provider: gdriveProvider, Endpoint: "https://oss.example.com"})) {
		t.Fatal("RequiresPlatformProxy() = false for Drive, but Drive has no presignable URL")
	}
	// 其它 provider 必须返回 false：私有源站该走 503，不能被这里的判断吞掉。
	if RequiresPlatformProxy(NormalizeSettings(Settings{Provider: s3Provider})) {
		t.Fatal("RequiresPlatformProxy() = true for S3 without any origin")
	}
	if RequiresPlatformProxy(NormalizeSettings(Settings{Provider: s3Provider, CDNBaseURL: "https://cdn.example.com", Delivery: DeliverySettings{CDNAuthMode: "public"}})) {
		t.Fatal("RequiresPlatformProxy() = true for S3 with a public CDN")
	}
	if RequiresPlatformProxy(NormalizeSettings(Settings{Provider: aliyunOSSProvider, Endpoint: "http://127.0.0.1:9000"})) {
		t.Fatal("RequiresPlatformProxy() = true for a private OSS origin, which must keep failing with resource_origin_private")
	}
}

func TestSignedOriginObjectURLRejectsGDrive(t *testing.T) {
	_, err := SignedOriginObjectURL(NormalizeSettings(Settings{Provider: gdriveProvider}), "file-1", time.Now().Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "不支持预签名地址") {
		t.Fatalf("SignedOriginObjectURL() error = %v, want unsupported-signing error", err)
	}
}

func TestPutOSSObjectRejectsGDrive(t *testing.T) {
	// PutOSSObject 只能回传 ETag，拿不到 Drive 的 fileId；
	// 误用会让资源记录里留下一个不可用的对象键，所以必须显式失败。
	_, err := PutOSSObject(NormalizeSettings(Settings{Provider: gdriveProvider}), "file-1", "image/png", 1, bytes.NewReader([]byte("x")))
	if err == nil || !strings.Contains(err.Error(), "PutGDriveObject") {
		t.Fatalf("PutOSSObject() error = %v, want guidance to PutGDriveObject", err)
	}
}

func TestGetOriginObjectRangeDispatchesToGDrive(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	fake.files["file-3"] = []byte("abcd")
	stream, err := GetOriginObjectRange(gdriveTestSetting(server), "file-3", "bytes=0-1")
	if err != nil {
		t.Fatalf("GetOriginObjectRange() error = %v", err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusPartialContent {
		t.Fatalf("StatusCode = %d, want the Drive dispatch to serve 206", stream.StatusCode)
	}
}

func TestResetGDriveTokenCacheForcesNewToken(t *testing.T) {
	ResetGDriveTokenCache()
	fake := newFakeGDrive(t)
	server := fake.server()
	defer server.Close()

	setting := gdriveTestSetting(server)
	if _, err := gdriveAccessToken(setting); err != nil {
		t.Fatalf("gdriveAccessToken() error = %v", err)
	}
	ResetGDriveTokenCache()
	if _, err := gdriveAccessToken(setting); err != nil {
		t.Fatalf("gdriveAccessToken() after reset error = %v", err)
	}
	if got := fake.tokenCallCount(); got != 2 {
		t.Fatalf("token calls = %d, want 2 after cache reset", got)
	}
}
