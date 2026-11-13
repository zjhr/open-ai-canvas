package app

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestJevReviewEndpoint(t *testing.T) {
	cases := []struct {
		protocol string
		baseURL  string
		want     string
	}{
		{jevProtocolSystemOne, "https://api.typesafe.ai", "https://api.typesafe.ai/v1/systemone"},
		{jevProtocolSystemOne, "https://api.typesafe.ai/v1", "https://api.typesafe.ai/v1/systemone"},
		{jevProtocolOpenRouter, "https://openrouter.ai/api/v1", "https://openrouter.ai/api/v1/chat/completions"},
		{jevProtocolOpenRouter, "https://openrouter.ai/api/v1/chat/completions", "https://openrouter.ai/api/v1/chat/completions"},
	}
	for _, test := range cases {
		if got := jevReviewEndpoint(test.protocol, test.baseURL); got != test.want {
			t.Errorf("%s %s = %s, want %s", test.protocol, test.baseURL, got, test.want)
		}
	}
}

func TestDecodeJevOpenRouterResponse(t *testing.T) {
	content := "```json\n{\"compliant\":0.87,\"risk\":\"none\",\"quality\":3,\"reason\":\"可直接使用\"}\n```"
	raw, err := json.Marshal(map[string]any{
		"model": "typesafe/jev-router", "usage": map[string]any{"total_tokens": 12},
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeJevOpenRouterResponse(raw, "fallback")
	if err != nil {
		t.Fatal(err)
	}
	if decoded["model"] != "typesafe/jev-router" {
		t.Fatalf("model = %v", decoded["model"])
	}
	answers, _ := decoded["answers"].(map[string]any)
	compliant, _ := answers["compliant"].(map[string]any)
	if compliant["noul"] != 0.87 {
		t.Fatalf("compliant = %#v", answers["compliant"])
	}
	risk, _ := answers["risk"].(map[string]any)
	if risk["choice"] != "none" {
		t.Fatalf("risk = %#v", answers["risk"])
	}
	quality, _ := answers["quality"].(map[string]any)
	if quality["score"] != float64(3) {
		t.Fatalf("quality = %#v", answers["quality"])
	}
}

func TestDecodeJevOpenRouterResponseRejectsNonJSON(t *testing.T) {
	if _, err := decodeJevOpenRouterResponse([]byte(`{"choices":[{"message":{"content":"无法判断"}}]}`), "m"); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := decodeJevOpenRouterResponse([]byte(`{"choices":[]}`), "m"); err == nil {
		t.Fatal("expected missing choices error")
	}
}

func TestJevOpenRouterPromptCarriesQuestions(t *testing.T) {
	prompt := jevOpenRouterPrompt("剧本正文", contentReviewQuestions(contentReviewArgs{Target: "script"}))
	for _, want := range []string{"compliant", "risk", "quality", "剧本正文", "只输出一个 JSON 对象"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
}

func TestContentReviewStateReferencesImagePlaceholders(t *testing.T) {
	state := contentReviewState(contentReviewArgs{Target: "image", NodeIDs: []string{"a", "b"}})
	for _, want := range []string{"image1", "image2"} {
		if !strings.Contains(state, want) {
			t.Fatalf("state does not reference %q: %s", want, state)
		}
	}
}

func TestJevReviewErrorMessageReadsNestedDetail(t *testing.T) {
	got := jevReviewErrorMessage(400, []byte(`{"detail":{"error_type":"api_usage_error","message":"Invalid image request."}}`))
	if !strings.Contains(got, "Invalid image request.") || !strings.Contains(got, "api_usage_error") {
		t.Fatalf("unexpected message: %s", got)
	}
}

func TestJevReviewImageFormat(t *testing.T) {
	cases := map[string]string{"image/png": "png", "image/jpeg": "jpeg", "image/webp": "", "": ""}
	for mimeType, want := range cases {
		if got := jevReviewImageFormat(mimeType); got != want {
			t.Errorf("%q = %q, want %q", mimeType, got, want)
		}
	}
}
