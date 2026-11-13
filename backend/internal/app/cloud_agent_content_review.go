package app

import (
	"context"
	"encoding/base64"
	"io"
	"strconv"
	"strings"

	"yingce/backend/internal/repository"
)

const maxJevReviewImageBytes = 8 << 20

type contentReviewArgs struct {
	Target  string   `json:"target"`
	Text    string   `json:"text,omitempty"`
	NodeIDs []string `json:"nodeIds,omitempty"`
	Focus   string   `json:"focus,omitempty"`
}

// cloudAgentHandleContentReview 把剧本、提示词、镜头或图片交给 Jev 做封闭判断。
// 只有判断结果，不改画布、不提交生成任务；返回值按问题 ID 原样返回概率与置信度。
func cloudAgentHandleContentReview(repo *repository.Repository, userID string, state *cloudAgentRuntime, call cloudAgentCall, services ...*Service) (any, error) {
	var service *Service
	if len(services) > 0 {
		service = services[0]
	}
	if service == nil {
		return nil, BadAuthRequest("内容审核服务不可用")
	}
	var args contentReviewArgs
	if err := decodeCloudAgentJSONObject(call.Function.Arguments, &args); err != nil {
		return nil, cloudAgentJSONArgumentError(err)
	}
	args.Target = strings.TrimSpace(args.Target)
	args.Text = strings.TrimSpace(args.Text)
	args.Focus = strings.TrimSpace(args.Focus)

	assets := map[string]any{}
	switch args.Target {
	case "prompt", "script", "storyboard":
		if args.Text == "" {
			return nil, BadAuthRequest("审核文本内容时必须提供 text")
		}
		if len([]rune(args.Text)) > maxJevReviewTextRunes {
			return nil, BadAuthRequest("待审核文本过长，请分段提交")
		}
	case "image":
		if state == nil {
			return nil, BadAuthRequest("图片审核缺少当前画布")
		}
		if len(args.NodeIDs) == 0 || len(args.NodeIDs) > maxJevReviewImages {
			return nil, BadAuthRequest("图片审核需要 1-4 个图片节点 ID")
		}
		loaded, err := service.contentReviewAssets(userID, state.Request.CanvasID, args.NodeIDs)
		if err != nil {
			return nil, err
		}
		assets = loaded
	default:
		return nil, BadAuthRequest("审核对象必须是 prompt、script、storyboard 或 image")
	}

	decoded, err := service.JevSystemOne(context.Background(), contentReviewState(args), contentReviewQuestions(args), assets)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"target":  args.Target,
		"model":   decoded["model"],
		"answers": decoded["answers"],
		"usage":   decoded["usage"],
	}, nil
}

func contentReviewState(args contentReviewArgs) string {
	if args.Target == "image" {
		// Jev 只在 state/questions/criteria 里遇到占位符时才会把 assets 换进去；
		// 未引用的图片会被判为 Invalid image request。
		placeholders := make([]string, 0, len(args.NodeIDs))
		for index := range args.NodeIDs {
			placeholders = append(placeholders, "image"+strconv.Itoa(index+1))
		}
		return "请审核图片：" + strings.Join(placeholders, "、")
	}
	if args.Focus == "" {
		return args.Text
	}
	return "审核重点：" + args.Focus + "\n\n待审核内容：\n" + args.Text
}

func contentReviewQuestions(args contentReviewArgs) map[string]any {
	subject := "这段内容"
	switch args.Target {
	case "prompt":
		subject = "这段生成提示词"
	case "script":
		subject = "这段剧本"
	case "storyboard":
		subject = "这段分镜镜头描述"
	case "image":
		subject = "这些图片"
	}
	return map[string]any{
		"compliant": map[string]any{
			"type":         "noul",
			"instructions": subject + "是否可以安全用于 AI 影视创作与公开发布。",
			"criteria": map[string]any{
				"true":  "没有违法违规、侵权、未成年人不宜或明显有害内容",
				"false": "包含至少一项违法违规、侵权、未成年人不宜或明显有害内容",
			},
		},
		"risk": map[string]any{
			"type":         "choice",
			"instructions": subject + "最主要的风险类别；没有风险时选 none。",
			"criteria": map[string]any{
				"none":     "没有明显风险",
				"politics": "涉及政治敏感或不当政治表达",
				"violence": "宣扬暴力、血腥或自残",
				"sexual":   "色情、低俗或性暗示",
				"minor":    "涉及未成年人保护风险",
				"ip":       "可能侵犯著作权、商标或肖像权",
				"other":    "其他需要人工复核的风险",
			},
		},
		"quality": map[string]any{
			"type":         "score",
			"instructions": subject + "可直接进入制作流程的程度。",
			"criteria": []string{
				"完全不可用，信息缺失或逻辑断裂",
				"需要重写",
				"需要较多修改",
				"可用但需要少量修改",
				"可直接使用",
			},
		},
	}
}

// contentReviewAssets 从当前用户画布读取图片节点，转成 Jev 的 base64 资源。
// 归属、就绪状态和 MIME 都在服务端校验，模型只能提交节点 ID。
func (s *Service) contentReviewAssets(userID, canvasID string, nodeIDs []string) (map[string]any, error) {
	canvas, err := s.repo.CanvasProjectForUser(userID, canvasID)
	if err != nil {
		return nil, err
	}
	doc, err := creationDocument(canvas.PayloadJSON)
	if err != nil {
		return nil, err
	}
	nodes, err := creationObjects(doc["nodes"])
	if err != nil {
		return nil, err
	}
	assets := map[string]any{}
	for index, nodeID := range nodeIDs {
		node := nodes[strings.TrimSpace(nodeID)]
		if node == nil || stringValue(node["type"]) != "image" {
			return nil, BadAuthRequest("审核图片必须是当前画布中的真实图片节点")
		}
		metadata, _ := node["metadata"].(map[string]any)
		key := strings.TrimSpace(stringValue(metadata["storageKey"]))
		if !strings.HasPrefix(key, "resource:") {
			return nil, BadAuthRequest("图片节点还没有可读取的账号资源")
		}
		resource, body, err := s.OpenResource(userID, strings.TrimPrefix(key, "resource:"))
		if err != nil {
			return nil, err
		}
		if resource.Size > maxJevReviewImageBytes {
			body.Close()
			return nil, BadAuthRequest("审核图片不能超过 8MB，请先压缩")
		}
		payload, err := io.ReadAll(io.LimitReader(body, maxJevReviewImageBytes))
		body.Close()
		if err != nil {
			return nil, err
		}
		format := jevReviewImageFormat(resource.MimeType)
		if format == "" {
			return nil, BadAuthRequest("Jev 只支持 png、jpg、jpeg 格式的审核图片")
		}
		assets["image"+strconv.Itoa(index+1)] = map[string]any{
			"type":        "image",
			"format":      format,
			"data_base64": base64.StdEncoding.EncodeToString(payload),
		}
	}
	return assets, nil
}

func jevReviewImageFormat(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpeg"
	default:
		return ""
	}
}
