package app

// Agnes 视频创建重试的端到端测试：走真实的 runVideoTaskWithPolicy → 声明式适配器
// → 宿主 HTTP 执行链路，上游用本地 stub 模拟 Agnes 高峰期的 503「视频队列已满」。
// 目的是证明修复真的能在真实调用路径上把「队列已满」自动穿越到成功，
// 而不只是单测里那个重试函数本身可用。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

type agnesStubUpstream struct {
	mu           sync.Mutex
	createCalls  int
	failTimes    int    // 前 N 次创建返回 503
	terminalCode int    // 非 0 时创建始终返回该状态码
	statuses     []int  // 记录每次创建的响应码
	base         string // stub 自身地址，用于返回媒体 URL
}

func (s *agnesStubUpstream) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/videos", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.createCalls++
		n := s.createCalls
		s.mu.Unlock()

		if s.terminalCode != 0 {
			s.mu.Lock()
			s.statuses = append(s.statuses, s.terminalCode)
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.terminalCode)
			_, _ = w.Write([]byte(`{"code":"invalid_request","message":"stub rejection"}`))
			return
		}
		if n <= s.failTimes {
			s.mu.Lock()
			s.statuses = append(s.statuses, http.StatusServiceUnavailable)
			s.mu.Unlock()
			// 与 Agnes 高峰期返回体形态一致，并带 Retry-After 之外的自然语言提示。
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":"service_unavailable","message":"video queue is full, please retry later (request id: stub-` + strconv.Itoa(n) + `)"}`))
			return
		}
		s.mu.Lock()
		s.statuses = append(s.statuses, http.StatusOK)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task_stub_ok","status":"pending","object":"video"}`))
	})
	mux.HandleFunc("/agnesapi", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"task_stub_ok","status":"completed","progress":100,"metadata":{"url":"` + s.baseURL() + `/video.mp4"}}`))
	})
	mux.HandleFunc("/video.mp4", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("\x00\x00\x00\x18ftypmp42stub-video-bytes"))
	})
	return mux
}

func (s *agnesStubUpstream) baseURL() string { return s.base }

// recordingPolicy 记录退避等待但实际不睡眠，让测试快速完成。
func recordingPolicy() (videoPollPolicy, *[]time.Duration) {
	slept := &[]time.Duration{}
	policy := defaultVideoPollPolicy()
	policy.Sleep = func(_ context.Context, d time.Duration) error {
		*slept = append(*slept, d)
		return nil
	}
	return policy, slept
}

func agnesVideoInput(baseURL string) canvasGenerationInput {
	return canvasGenerationInput{
		Mode:   "video",
		Prompt: "stub agnes video prompt",
		Config: providerConfig{
			BaseURL:       baseURL,
			APIKey:        "stub-key",
			Model:         "agnes-video-2.5-flash",
			InterfaceType: "agnes-video",
			VideoSeconds:  "6",    // 适配器要求 4–12 秒
			VQuality:      "720p", // Flash 仅支持 720P；适配器按 720p/960p/2k 归一
			Size:          "16:9",
		},
	}
}

// 端到端：连续 2 次 503（队列已满）后成功生成，且退避符合指数预期。
func TestAgnesVideoCreateRetryEndToEndSucceedsAfterQueueFull(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "1")
	stub := &agnesStubUpstream{failTimes: 2}
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.base = server.URL

	policy, slept := recordingPolicy()
	result, err := runVideoTaskWithPolicy(context.Background(), agnesVideoInput(server.URL), policy)
	if err != nil {
		t.Fatalf("expected the queue-full window to be retried through to success, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected a video result")
	}
	if stub.createCalls != 3 {
		t.Fatalf("expected 3 create attempts (2x503 + 1 success), got %d", stub.createCalls)
	}
	var raw []int
	raw = append(raw, stub.statuses...)
	if len(raw) != 3 || raw[0] != 503 || raw[1] != 503 || raw[2] != 200 {
		t.Fatalf("unexpected upstream status sequence: %v", raw)
	}
	// 前两次退避应为指数 2s、4s（之后进入轮询，退避为 Interval）。
	if len(*slept) < 2 {
		t.Fatalf("expected at least 2 backoff sleeps, got %v", *slept)
	}
	if (*slept)[0] != 2*time.Second || (*slept)[1] != 4*time.Second {
		t.Fatalf("expected exponential backoff 2s then 4s, got %v", (*slept)[:2])
	}
	// 结果应包含可用的视频媒体。
	if encoded, err := json.Marshal(result); err != nil || len(encoded) == 0 {
		t.Fatalf("result not serializable: %v", err)
	}
}

// 端到端：队列一直满 → 用尽重试后如实失败，且尝试次数等于上限。
func TestAgnesVideoCreateRetryEndToEndGivesUpAfterMaxAttempts(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "1")
	stub := &agnesStubUpstream{failTimes: 1 << 30}
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.base = server.URL

	policy, _ := recordingPolicy()
	if _, err := runVideoTaskWithPolicy(context.Background(), agnesVideoInput(server.URL), policy); err == nil {
		t.Fatal("expected a failure once retries are exhausted")
	}
	if stub.createCalls != videoCreateMaxRetries+1 {
		t.Fatalf("expected %d create attempts, got %d", videoCreateMaxRetries+1, stub.createCalls)
	}
}

// 端到端：参数类 4xx（如 400）必须立即失败，不能重试。
func TestAgnesVideoCreateRetryEndToEndDoesNotRetryParameterError(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "1")
	stub := &agnesStubUpstream{terminalCode: http.StatusBadRequest}
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.base = server.URL

	policy, _ := recordingPolicy()
	if _, err := runVideoTaskWithPolicy(context.Background(), agnesVideoInput(server.URL), policy); err == nil {
		t.Fatal("expected a failure for 400")
	}
	if stub.createCalls != 1 {
		t.Fatalf("400 must not be retried, got %d create attempts", stub.createCalls)
	}
}
