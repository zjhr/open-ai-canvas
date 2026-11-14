package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"time"

	"yingce/backend/internal/model"
	"yingce/backend/internal/storage"

	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/tencentyun/cos-go-sdk-v5"
	"gorm.io/gorm"
)

type OSSConnectionTestResult struct {
	OK           bool      `json:"ok"`
	Message      string    `json:"message,omitempty"`
	TestedAt     time.Time `json:"testedAt"`
	TestedDigest string    `json:"testedDigest"`
}

var errOSSConnectionReadMismatch = errors.New("对象存储读取内容不一致")

func (s *Service) TestAdminOSSSetting(actor *model.User, req OSSSettingRequest) (*OSSConnectionTestResult, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	_, current, err := s.readOSSSetting()
	if err != nil {
		return nil, err
	}
	return s.testOSSSetting("platform", "", actor.ID, req, current)
}

func (s *Service) TestUserOSSSetting(actor *model.User, req OSSSettingRequest) (*OSSConnectionTestResult, error) {
	if actor == nil {
		return nil, Unauthorized("请先登录")
	}
	_, platform, err := s.readOSSSetting()
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(req.Provider), s3Provider) && !platform.AllowUserS3 {
		return nil, Forbidden("平台管理员尚未允许个人 S3 兼容存储")
	}
	if strings.EqualFold(strings.TrimSpace(req.Provider), gdriveProvider) {
		return nil, Forbidden("Google Drive 只能由管理员配置为平台存储，暂不支持按用户绑定")
	}
	_, current, err := s.readUserOSSSetting(actor.ID)
	if err != nil {
		return nil, err
	}
	return s.testOSSSetting("user", actor.ID, actor.ID, req, current)
}

func (s *Service) testOSSSetting(scope string, ownerID string, actorID string, req OSSSettingRequest, current ossSettingValue) (*OSSConnectionTestResult, error) {
	req.Enabled = true
	value, err := ossSettingFromRequest(req, current)
	if err != nil {
		return nil, err
	}
	key := scope + ":" + actorID
	s.storageTestMu.Lock()
	if s.activeStorageTests == nil {
		s.activeStorageTests = make(map[string]bool)
	}
	if s.activeStorageTests[key] {
		s.storageTestMu.Unlock()
		return nil, NewAppError(http.StatusConflict, "对象存储连接测试正在进行")
	}
	s.activeStorageTests[key] = true
	s.storageTestMu.Unlock()
	defer func() {
		s.storageTestMu.Lock()
		delete(s.activeStorageTests, key)
		s.storageTestMu.Unlock()
	}()

	testKey := path.Join(value.PathPrefix, ".yingce-tests", scope, newID())
	if err := verifyOSSConnection(s.storageSettingWithRuntimePolicy(value), testKey); err != nil {
		return nil, err
	}

	location, err := s.upsertStorageLocation(scope, ownerID, value)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	location.TestedDigest = storageTestDigest(value)
	location.TestedAt = &now
	if err := s.repo.SaveStorageLocation(location); err != nil {
		return nil, err
	}
	return &OSSConnectionTestResult{OK: true, Message: "连接测试通过", TestedAt: now, TestedDigest: location.TestedDigest}, nil
}

func verifyOSSConnection(value ossSettingValue, testKey string) error {
	// Drive 的对象句柄（fileId）要等写入之后才存在，无法像 S3 那样用同一个 key
	// 走完「写 → 读 → 删」三步，因此单独走一条连接测试。
	if value.Provider == gdriveProvider {
		return verifyGDriveConnection(value, testKey)
	}
	payload := []byte("yingce-storage-test")
	// 连接测试验证服务端到对象存储的真实读写权限。CDN 是浏览器读取出口，
	// 可能存在回源鉴权或边缘同步延迟，不能参与刚写入对象的最小读写测试。
	testValue := value
	testValue.CDNBaseURL = ""
	if _, err := putOSSObject(testValue, testKey, "application/octet-stream", int64(len(payload)), bytes.NewReader(payload)); err != nil {
		return storageConnectionTestError(testValue.Provider, "写入", err)
	}
	stream, err := getOSSObjectRange(testValue, testKey, "bytes=0-3")
	var rangeErr error
	if err != nil {
		rangeErr = err
	} else {
		rangeErr = verifyOSSConnectionRead(stream, payload)
	}
	if err := deleteOSSObject(testValue, testKey); err != nil {
		return storageConnectionTestError(testValue.Provider, "删除", err)
	}
	if rangeErr != nil {
		return storageConnectionTestError(testValue.Provider, "Range 读取", rangeErr)
	}
	return nil
}

func verifyOSSConnectionRead(stream *ossObjectStream, payload []byte) error {
	if stream == nil || stream.Body == nil {
		return fmt.Errorf("%w：响应为空", errOSSConnectionReadMismatch)
	}
	data, readErr := io.ReadAll(io.LimitReader(stream.Body, int64(len(payload)+1)))
	closeErr := stream.Body.Close()
	if readErr != nil {
		return fmt.Errorf("对象存储响应读取失败：%w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("对象存储响应关闭失败：%w", closeErr)
	}

	// HTTP 允许服务端忽略 Range 并返回 200 + 完整对象。应用的资源代理也会
	// 原样透传这种响应，因此连接测试应同时接受完整读取与标准的 206 分段读取。
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
	return fmt.Errorf("%w（HTTP %d，读取 %d 字节）", errOSSConnectionReadMismatch, stream.StatusCode, len(data))
}

func storageConnectionTestError(provider string, operation string, cause error) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	providerName := "对象存储"
	// 每个 provider 需要用户检查的东西不一样，提示必须跟着换：
	// Drive 没有 Bucket/Endpoint/访问密钥，照搬对象存储的指引只会把人带偏。
	networkHint := "请检查 Endpoint 和服务端网络"
	credentialHint := "请检查 Endpoint、Bucket、访问密钥和存储桶权限"
	switch provider {
	case aliyunOSSProvider:
		providerName = "阿里云 OSS"
	case tencentCOSProvider:
		providerName = "腾讯云 COS"
	case qiniuKodoProvider:
		providerName = "七牛云 Kodo"
	case volcengineTOSProvider:
		providerName = "火山引擎 TOS"
	case s3Provider:
		providerName = "S3 兼容存储"
	case gdriveProvider:
		providerName = "Google Drive"
		networkHint = "请检查服务器的出网网络能否访问 Google"
		credentialHint = "请检查 OAuth 客户端 ID、客户端密钥、刷新令牌，以及目标文件夹的写入权限"
	}

	if errors.Is(cause, context.DeadlineExceeded) {
		return WrapAppError(http.StatusGatewayTimeout, fmt.Sprintf("%s %s测试超时，%s", providerName, operation, networkHint), cause)
	}
	var networkErr net.Error
	if errors.As(cause, &networkErr) && networkErr.Timeout() {
		return WrapAppError(http.StatusGatewayTimeout, fmt.Sprintf("%s %s测试超时，%s", providerName, operation, networkHint), cause)
	}
	// Drive 的回读不一致由 storage 层用独立哨兵报出，与对象存储共用同一句解释。
	if operation == "Range 读取" && (errors.Is(cause, errOSSConnectionReadMismatch) || errors.Is(cause, storage.ErrGDriveReadMismatch)) {
		return WrapAppError(http.StatusBadGateway, fmt.Sprintf("%s Range 读取返回的数据与刚写入对象不一致，请检查 Endpoint 或出网代理是否忽略、改写了 Range 请求", providerName), cause)
	}

	if provider == tencentCOSProvider {
		var responseErr *cos.ErrorResponse
		if errors.As(cause, &responseErr) && responseErr.Response != nil {
			switch responseErr.Response.StatusCode {
			case http.StatusBadRequest, http.StatusUnprocessableEntity:
				return WrapAppError(http.StatusBadGateway, fmt.Sprintf("腾讯云 COS %s请求被拒绝，请检查 Bucket、Region 和 Endpoint", operation), cause)
			case http.StatusUnauthorized, http.StatusForbidden:
				return WrapAppError(http.StatusBadGateway, fmt.Sprintf("腾讯云 COS %s鉴权失败，请检查 SecretId、SecretKey 和 Bucket 权限", operation), cause)
			case http.StatusNotFound:
				return WrapAppError(http.StatusBadGateway, fmt.Sprintf("腾讯云 COS %s未找到 Bucket，请检查 Bucket、Region 和 Endpoint", operation), cause)
			case http.StatusTooManyRequests:
				return WrapAppError(http.StatusBadGateway, fmt.Sprintf("腾讯云 COS %s请求过于频繁，请稍后重试", operation), cause)
			}
			if responseErr.Response.StatusCode >= http.StatusInternalServerError {
				return WrapAppError(http.StatusBadGateway, fmt.Sprintf("腾讯云 COS %s服务暂时不可用，请稍后重试", operation), cause)
			}
		}
	}

	if provider == volcengineTOSProvider {
		var requestFailure awserr.RequestFailure
		if errors.As(cause, &requestFailure) {
			switch requestFailure.StatusCode() {
			case http.StatusBadRequest:
				return WrapAppError(http.StatusBadGateway, fmt.Sprintf("火山引擎 TOS %s请求被拒绝，请检查 Bucket、Region 和 Endpoint", operation), cause)
			case http.StatusUnauthorized, http.StatusForbidden:
				return WrapAppError(http.StatusBadGateway, fmt.Sprintf("火山引擎 TOS %s鉴权失败，请检查 Access Key ID、Secret Access Key、Region 和 Bucket 权限", operation), cause)
			case http.StatusNotFound:
				return WrapAppError(http.StatusBadGateway, fmt.Sprintf("火山引擎 TOS %s未找到 Bucket，请检查 Bucket、Region 和 Endpoint", operation), cause)
			}
		}
	}

	return WrapAppError(http.StatusBadGateway, fmt.Sprintf("%s %s测试失败，%s", providerName, operation, credentialHint), cause)
}

func (s *Service) upsertStorageLocation(scope string, ownerID string, value ossSettingValue) (*model.StorageLocation, error) {
	digest := storageLocationDigest(value)
	location, err := s.repo.StorageLocationByDigest(scope, ownerID, value.Provider, digest)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	stored, err := s.encryptOSSSettingSecrets(value)
	if err != nil {
		return nil, err
	}
	stored.Enabled = true
	stored.StorageLocationID = ""
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	if location == nil {
		location = &model.StorageLocation{ID: newID(), Scope: scope, OwnerID: ownerID, Provider: value.Provider, LocationDigest: digest, ValueJSON: string(encoded)}
		if err := s.repo.CreateStorageLocation(location); err != nil {
			return nil, err
		}
		return location, nil
	}
	location.ValueJSON = string(encoded)
	return location, nil
}

func (s *Service) storageLocationValue(id string) (*model.StorageLocation, ossSettingValue, error) {
	location, err := s.repo.StorageLocation(id)
	if err != nil {
		return nil, ossSettingValue{}, err
	}
	var value ossSettingValue
	if err := json.Unmarshal([]byte(location.ValueJSON), &value); err != nil {
		return nil, ossSettingValue{}, errors.New("对象存储位置配置格式无效")
	}
	if _, err := s.decryptOSSSettingSecrets(&value); err != nil {
		return nil, ossSettingValue{}, err
	}
	value.StorageLocationID = location.ID
	return location, normalizeOSSSetting(value), nil
}

func (s *Service) requireTestedS3Location(scope string, ownerID string, value ossSettingValue) (*model.StorageLocation, error) {
	location, err := s.repo.StorageLocationByDigest(scope, ownerID, s3Provider, storageLocationDigest(value))
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, BadAuthRequest("S3 关键配置尚未通过连接测试")
		}
		return nil, err
	}
	if location.TestedAt == nil || location.TestedDigest != storageTestDigest(value) {
		return nil, BadAuthRequest("S3 关键配置或凭据已变化，请重新连接测试")
	}
	return location, nil
}

func (s *Service) populateStorageLocationPublic(result *PublicOSSSetting, scope string, ownerID string, id string) error {
	count, err := s.repo.StorageLocationHistoryCount(scope, ownerID)
	if err != nil {
		return err
	}
	result.HistoryCount = count
	if id == "" {
		return nil
	}
	location, err := s.repo.StorageLocation(id)
	if err != nil {
		return err
	}
	result.TestedAt = location.TestedAt
	result.TestedDigest = location.TestedDigest
	result.ReferencedResourceCount, err = s.repo.StorageLocationResourceCount(id)
	return err
}

func deleteOSSObject(setting ossSettingValue, objectKey string) error {
	switch normalizeOSSSetting(setting).Provider {
	case gdriveProvider:
		return deleteGDriveObject(setting, objectKey)
	case s3Provider, volcengineTOSProvider:
		return deleteS3Object(setting, objectKey)
	case tencentCOSProvider:
		return deleteTencentCOSObject(setting, objectKey)
	case qiniuKodoProvider:
		return deleteQiniuObject(setting, objectKey)
	default:
		return deleteAliyunOSSObject(setting, objectKey)
	}
}

// verifyGDriveConnection 把 Drive 的连接测试收敛到一处：失败步骤由 storage 层给出，
// 这里只负责把它翻译成带「Google Drive」和正确排查方向的可读提示。
func verifyGDriveConnection(value ossSettingValue, testKey string) error {
	operation, err := verifyGDriveObject(value, testKey)
	if err == nil {
		return nil
	}
	if operation == "" {
		operation = "写入"
	}
	return storageConnectionTestError(gdriveProvider, operation, err)
}
