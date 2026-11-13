package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// Jev（TypeSafe System One）是封闭判断模型：它只回答 noul/choice/score，
// 不生成内容，也不参与画布写入。这里只保存接入配置和出站调用。
const jevReviewSettingKey = "jev_review"

const (
	defaultJevReviewBaseURL     = "https://api.typesafe.ai"
	defaultJevReviewModel       = "jev-latest"
	jevProtocolSystemOne        = "typesafe"
	jevProtocolOpenRouter       = "openrouter"
	defaultJevOpenRouterBaseURL = "https://openrouter.ai/api/v1"
	defaultJevOpenRouterModel   = "typesafe/jev-router"
	jevReviewTimeout            = 30 * time.Second
	maxJevReviewTextRunes       = 20000
	maxJevReviewResponse        = 1 << 20
	maxJevReviewImages          = 4
)

type JevReviewSettingRequest struct {
	Enabled  bool   `json:"enabled"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
}

type PublicJevReviewSetting struct {
	Enabled   bool      `json:"enabled"`
	Protocol  string    `json:"protocol"`
	BaseURL   string    `json:"baseUrl"`
	Model     string    `json:"model"`
	HasAPIKey bool      `json:"hasApiKey"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type jevReviewSettingValue struct {
	Enabled  bool   `json:"enabled"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
}

func defaultJevReviewSetting() jevReviewSettingValue {
	return jevReviewSettingValue{Protocol: jevProtocolSystemOne, BaseURL: defaultJevReviewBaseURL, Model: defaultJevReviewModel}
}

func (s *Service) AdminJevReviewSetting(actor *model.User) (*PublicJevReviewSetting, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	value, row, err := s.jevReviewSetting()
	if err != nil {
		return nil, err
	}
	setting := &PublicJevReviewSetting{Enabled: value.Enabled, Protocol: value.Protocol, BaseURL: value.BaseURL, Model: value.Model, HasAPIKey: strings.TrimSpace(value.APIKey) != ""}
	if row != nil {
		setting.UpdatedAt = row.UpdatedAt
	}
	return setting, nil
}

// UpdateJevReviewSetting 保留已保存的 API Key：请求留空表示沿用，避免管理页
// 每次保存都必须重新粘贴密钥。
func (s *Service) UpdateJevReviewSetting(actor *model.User, req JevReviewSettingRequest) (*PublicJevReviewSetting, error) {
	if err := s.RequireAdmin(actor); err != nil {
		return nil, err
	}
	current, _, err := s.jevReviewSetting()
	if err != nil {
		return nil, err
	}
	next := jevReviewSettingValue{
		Enabled:  req.Enabled,
		Protocol: normalizeJevProtocol(req.Protocol),
		BaseURL:  strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		Model:    strings.TrimSpace(req.Model),
		APIKey:   strings.TrimSpace(req.APIKey),
	}
	if next.Protocol == "" {
		return nil, BadAuthRequest("内容审核协议必须是 typesafe 或 openrouter")
	}
	if next.BaseURL == "" {
		next.BaseURL = jevProtocolBaseURL(next.Protocol)
	}
	if next.Model == "" {
		next.Model = jevProtocolModel(next.Protocol)
	}
	if next.APIKey == "" {
		next.APIKey = current.APIKey
	}
	if next.Enabled && next.APIKey == "" {
		return nil, BadAuthRequest("启用内容审核前请填写 Jev API Key")
	}
	if _, err := ValidateOutboundURL(jevReviewEndpoint(next.Protocol, next.BaseURL)); err != nil {
		return nil, err
	}
	stored, err := s.encryptJevReviewSettingSecret(next)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	row, err := s.repo.SystemSetting(jevReviewSettingKey)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	setting := &model.SystemSetting{Key: jevReviewSettingKey, ValueJSON: string(encoded), UpdatedBy: actor.ID}
	if row != nil {
		setting.CreatedAt = row.CreatedAt
	}
	if err := s.repo.SaveSystemSetting(setting); err != nil {
		return nil, err
	}
	if err := s.appendAdminAudit(actor, "jev_review.update", "system_setting", jevReviewSettingKey, "更新内容审核配置", map[string]any{"enabled": next.Enabled, "baseUrl": next.BaseURL, "model": next.Model}); err != nil {
		return nil, err
	}
	result, _, err := s.jevReviewSetting()
	if err != nil {
		return nil, err
	}
	return &PublicJevReviewSetting{Enabled: result.Enabled, Protocol: result.Protocol, BaseURL: result.BaseURL, Model: result.Model, HasAPIKey: result.APIKey != ""}, nil
}

func (s *Service) jevReviewSetting() (jevReviewSettingValue, *model.SystemSetting, error) {
	row, err := s.repo.SystemSetting(jevReviewSettingKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return defaultJevReviewSetting(), nil, nil
	}
	if err != nil {
		return jevReviewSettingValue{}, nil, err
	}
	value := defaultJevReviewSetting()
	if strings.TrimSpace(row.ValueJSON) != "" {
		if err := json.Unmarshal([]byte(row.ValueJSON), &value); err != nil {
			return jevReviewSettingValue{}, nil, fmt.Errorf("内容审核配置格式错误: %w", err)
		}
	}
	value.Protocol = normalizeJevProtocol(value.Protocol)
	if value.Protocol == "" {
		value.Protocol = jevProtocolSystemOne
	}
	if value.BaseURL == "" {
		value.BaseURL = jevProtocolBaseURL(value.Protocol)
	}
	if value.Model == "" {
		value.Model = jevProtocolModel(value.Protocol)
	}
	secret, err := s.decryptSettingSecret(value.APIKey)
	if err != nil {
		return jevReviewSettingValue{}, nil, err
	}
	value.APIKey = secret
	return value, row, nil
}

func (s *Service) encryptJevReviewSettingSecret(value jevReviewSettingValue) (jevReviewSettingValue, error) {
	if strings.TrimSpace(value.APIKey) == "" || strings.HasPrefix(value.APIKey, encryptedSettingPrefix) {
		return value, nil
	}
	secret, err := s.encryptSettingSecret(value.APIKey)
	if err != nil {
		return jevReviewSettingValue{}, err
	}
	value.APIKey = secret
	return value, nil
}

// jevReviewEnabled 只决定本轮是否暴露 content_review；调用时仍会重新校验
// 配置，避免运行中改配置后沿用旧授权。
func (s *Service) jevReviewEnabled() bool {
	setting, _, err := s.jevReviewSetting()
	if err != nil {
		return false
	}
	return setting.Enabled && strings.TrimSpace(setting.APIKey) != ""
}
func normalizeJevProtocol(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", jevProtocolSystemOne, "systemone":
		return jevProtocolSystemOne
	case jevProtocolOpenRouter, "open-router":
		return jevProtocolOpenRouter
	default:
		return ""
	}
}

func jevProtocolBaseURL(protocol string) string {
	if protocol == jevProtocolOpenRouter {
		return defaultJevOpenRouterBaseURL
	}
	return defaultJevReviewBaseURL
}

func jevProtocolModel(protocol string) string {
	if protocol == jevProtocolOpenRouter {
		return defaultJevOpenRouterModel
	}
	return defaultJevReviewModel
}

func jevReviewEndpoint(protocol, baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if protocol == jevProtocolOpenRouter {
		if strings.HasSuffix(base, "/chat/completions") {
			return base
		}
		if strings.HasSuffix(base, "/v1") {
			return base + "/chat/completions"
		}
		return base + "/v1/chat/completions"
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/systemone"
	}
	return base + "/v1/systemone"
}

// JevSystemOne 调用封闭判断接口。state 是待判断内容，questions 按稳定 ID 组织，
// assets 只放图片占位符，调用方负责把图片读成 base64。
// typesafe 走原生 /v1/systemone；openrouter 走兼容 Chat Completions 的转录接口。
func (s *Service) JevSystemOne(ctx context.Context, state string, questions map[string]any, assets map[string]any) (map[string]any, error) {
	setting, _, err := s.jevReviewSetting()
	if err != nil {
		return nil, err
	}
	if !setting.Enabled || strings.TrimSpace(setting.APIKey) == "" {
		return nil, BadAuthRequest("内容审核服务未启用，请先在管理端配置 Jev")
	}
	if len(questions) == 0 {
		return nil, BadAuthRequest("内容审核至少需要一个判断问题")
	}
	if setting.Protocol == jevProtocolOpenRouter {
		return s.jevReviewOpenRouter(ctx, setting, state, questions, assets)
	}
	endpoint := jevReviewEndpoint(setting.Protocol, setting.BaseURL)
	if _, err := ValidateOutboundURL(endpoint); err != nil {
		return nil, err
	}
	payload := map[string]any{"model": setting.Model, "state": state, "questions": questions}
	if len(assets) > 0 {
		payload["assets"] = assets
	}
	raw, err := s.jevReviewRequest(ctx, endpoint, setting.APIKey, payload, len(assets) > 0)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("解析内容审核响应失败: %w", err)
	}
	if decoded["answers"] == nil {
		return nil, fmt.Errorf("内容审核响应缺少 answers")
	}
	return decoded, nil
}

// jevReviewOpenRouter 把类型化问题转成一次 Chat Completions 请求：OpenRouter 上的
// Jev 不是 /v1/systemone，只返回文本，因此要求模型输出固定 JSON 后再归一化。
func (s *Service) jevReviewOpenRouter(ctx context.Context, setting jevReviewSettingValue, state string, questions map[string]any, assets map[string]any) (map[string]any, error) {
	endpoint := jevReviewEndpoint(setting.Protocol, setting.BaseURL)
	if _, err := ValidateOutboundURL(endpoint); err != nil {
		return nil, err
	}
	parts := []any{map[string]any{"type": "text", "text": jevOpenRouterPrompt(state, questions)}}
	for index := 1; index <= len(assets); index++ {
		asset, _ := assets["image"+strconv.Itoa(index)].(map[string]any)
		data, _ := asset["data_base64"].(string)
		format, _ := asset["format"].(string)
		if data == "" || format == "" {
			continue
		}
		parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/" + format + ";base64," + data}})
	}
	payload := map[string]any{
		"model":       setting.Model,
		"temperature": 0,
		"messages":    []any{map[string]any{"role": "user", "content": parts}},
	}
	raw, err := s.jevReviewRequest(ctx, endpoint, setting.APIKey, payload, false)
	if err != nil {
		return nil, err
	}
	return decodeJevOpenRouterResponse(raw, setting.Model)
}

func decodeJevOpenRouterResponse(raw []byte, fallbackModel string) (map[string]any, error) {
	var decoded struct {
		Model   string         `json:"model"`
		Usage   map[string]any `json:"usage"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("解析内容审核响应失败: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return nil, fmt.Errorf("内容审核响应缺少 choices")
	}
	content := strings.TrimSpace(decoded.Choices[0].Message.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	var answers map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &answers); err != nil {
		return nil, fmt.Errorf("内容审核模型没有返回可解析的 JSON: %s", truncateRunes(content, 200))
	}
	return map[string]any{
		"model":   firstNonEmpty(decoded.Model, fallbackModel),
		"answers": jevOpenRouterAnswers(answers),
		"usage":   decoded.Usage,
	}, nil
}

func (s *Service) jevReviewRequest(ctx context.Context, endpoint, apiKey string, payload map[string]any, images bool) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	if images {
		req.Header.Set("TypeSafe-Danger-Zone-Images", "true")
	}
	ApplyDefaultOutboundHeaders(req)
	response, err := OutboundHTTPClient(jevReviewTimeout).Do(req)
	if err != nil {
		return nil, fmt.Errorf("内容审核请求失败: %w", err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxJevReviewResponse))
	if err != nil {
		return nil, fmt.Errorf("读取内容审核响应失败: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, BadAuthRequest("内容审核服务返回错误: " + jevReviewErrorMessage(response.StatusCode, raw))
	}
	return raw, nil
}

func jevOpenRouterPrompt(state string, questions map[string]any) string {
	ids := make([]string, 0, len(questions))
	for id := range questions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	lines := []string{"你是内容审核器。根据待审核内容回答下列问题，只输出一个 JSON 对象，不要 Markdown 或解释。"}
	for _, id := range ids {
		question, _ := questions[id].(map[string]any)
		kind := stringValue(question["type"])
		instruction := stringValue(question["instructions"])
		switch kind {
		case "noul":
			lines = append(lines, fmt.Sprintf("- %s：返回 0 到 1 的概率（1 表示 true）。%s", id, instruction))
		case "choice":
			lines = append(lines, fmt.Sprintf("- %s：从 %s 中选一个字符串。%s", id, jevCriteriaNames(question["criteria"]), instruction))
		case "score":
			lines = append(lines, fmt.Sprintf("- %s：返回 0 到 %d 的分数。%s", id, jevScoreMax(question["criteria"]), instruction))
		}
	}
	lines = append(lines, "", "待审核内容：", state)
	example := map[string]any{"reason": "一句话说明"}
	for _, id := range ids {
		question, _ := questions[id].(map[string]any)
		switch stringValue(question["type"]) {
		case "noul":
			example[id] = 0.87
		case "choice":
			names := jevCriteriaNameList(question["criteria"])
			if len(names) > 0 {
				example[id] = names[0]
			}
		case "score":
			example[id] = 0
		}
	}
	encoded, _ := json.Marshal(example)
	lines = append(lines, "", "输出格式："+string(encoded))
	return strings.Join(lines, "\n")
}

func jevCriteriaNameList(criteria any) []string {
	values, _ := criteria.(map[string]any)
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func jevCriteriaNames(criteria any) string {
	return "[" + strings.Join(jevCriteriaNameList(criteria), ", ") + "]"
}

func jevScoreMax(criteria any) int {
	values, _ := criteria.([]any)
	if len(values) == 0 {
		return 1
	}
	return len(values) - 1
}

func jevOpenRouterAnswers(parsed map[string]any) map[string]any {
	answers := map[string]any{}
	if value, exists := parsed["compliant"]; exists {
		answers["compliant"] = map[string]any{"noul": jevNumber(value)}
	}
	if value, ok := parsed["risk"].(string); ok && strings.TrimSpace(value) != "" {
		answers["risk"] = map[string]any{"choice": strings.TrimSpace(value)}
	}
	if value, exists := parsed["quality"]; exists {
		answers["quality"] = map[string]any{"score": jevNumber(value)}
	}
	if value, ok := parsed["reason"].(string); ok && strings.TrimSpace(value) != "" {
		answers["reason"] = strings.TrimSpace(value)
	}
	return answers
}

func jevNumber(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case bool:
		if typed {
			return 1
		}
		return 0
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed
	default:
		return 0
	}
}

func jevReviewErrorMessage(status int, raw []byte) string {
	// detail 既可能是字符串，也可能是 {error_type, message} 对象。
	var payload struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(raw, &payload) == nil && len(payload.Detail) > 0 {
		var text string
		if json.Unmarshal(payload.Detail, &text) == nil && strings.TrimSpace(text) != "" {
			return truncateRunes(strings.TrimSpace(text), 300)
		}
		var detail struct {
			ErrorType string `json:"error_type"`
			Message   string `json:"message"`
		}
		if json.Unmarshal(payload.Detail, &detail) == nil {
			message := strings.TrimSpace(detail.Message)
			if kind := strings.TrimSpace(detail.ErrorType); kind != "" {
				message = "[" + kind + "] " + message
			}
			if strings.TrimSpace(message) != "" {
				return truncateRunes(message, 300)
			}
		}
	}
	message := strings.TrimSpace(string(raw))
	if message == "" {
		message = http.StatusText(status)
	}
	return truncateRunes(message, 300)
}
