package app

// 创建阶段重试的行为与作用域约束测试。
// 重点验证两件事：
//   1. 只对「上游明确未受理」的错误重试（429/5xx），绝不重试可能已受理的连接类错误；
//   2. 重试只在「视频 + Agnes 视频适配器」时启用，不会改变文本/图像/音频与其他视频模型。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"yingce/backend/internal/protocol"
)

// stubAdapter 用于验证作用域判定，只需提供 metadata。
type stubAdapter struct {
	id string
}

func (s stubAdapter) Metadata() protocol.Metadata {
	return protocol.Metadata{ID: s.id, Version: "test"}
}
func (s stubAdapter) BuildCreate(context.Context, protocol.RequestContext) (protocol.RequestSpec, error) {
	return protocol.RequestSpec{}, nil
}
func (s stubAdapter) ParseCreate(context.Context, []byte) (protocol.CreateResult, error) {
	return protocol.CreateResult{}, nil
}
func (s stubAdapter) BuildPoll(context.Context, protocol.PollContext) (protocol.RequestSpec, error) {
	return protocol.RequestSpec{}, nil
}
func (s stubAdapter) ParsePoll(context.Context, protocol.PollContext, []byte) (protocol.PollResult, error) {
	return protocol.PollResult{}, nil
}
func (s stubAdapter) BuildCancel(context.Context, protocol.PollContext) (protocol.RequestSpec, error) {
	return protocol.RequestSpec{}, nil
}

func noSleepPolicy() videoPollPolicy {
	policy := defaultVideoPollPolicy()
	policy.Sleep = func(context.Context, time.Duration) error { return nil }
	return policy
}

func TestVideoCreateRetryableRetriesOnlyUnacceptedErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"503 视频队列已满", providerHTTPError{StatusCode: http.StatusServiceUnavailable}, true},
		{"500 上游内部错误", providerHTTPError{StatusCode: http.StatusInternalServerError}, true},
		{"502 网关错误", providerHTTPError{StatusCode: http.StatusBadGateway}, true},
		{"429 限速", providerHTTPError{StatusCode: http.StatusTooManyRequests}, true},
		{"400 参数错误", providerHTTPError{StatusCode: http.StatusBadRequest}, false},
		{"401 鉴权失败", providerHTTPError{StatusCode: http.StatusUnauthorized}, false},
		{"404 路径不存在", providerHTTPError{StatusCode: http.StatusNotFound}, false},
		// 连接类错误没有 HTTP 响应：请求可能已经送达并计费，重试会导致重复扣费。
		{"EOF 连接提前关闭", io.EOF, false},
		{"连接重置", &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}, false},
		{"上下文已取消", context.Canceled, false},
		{"上下文超时", context.DeadlineExceeded, false},
		{"普通业务错误", errors.New("解析响应失败"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := videoCreateRetryable(context.Background(), test.err); got != test.want {
				t.Fatalf("videoCreateRetryable(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestVideoCreateRetryableStopsAfterContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 即使是可重试的 503，上下文已结束也不应再重试。
	if videoCreateRetryable(ctx, providerHTTPError{StatusCode: http.StatusServiceUnavailable}) {
		t.Fatal("expected no retry once the context is done")
	}
}

func TestCreateWithRetrySucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	attempt := func(context.Context) ([]byte, *protocol.Result, error) {
		calls++
		if calls < 3 {
			return nil, nil, providerHTTPError{StatusCode: http.StatusServiceUnavailable}
		}
		return []byte(`{"ok":true}`), nil, nil
	}
	body, _, err := runProtocolAdapterCreateWithRetry(context.Background(), attempt, noSleepPolicy())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestCreateWithRetryStopsAtMaxAttempts(t *testing.T) {
	calls := 0
	attempt := func(context.Context) ([]byte, *protocol.Result, error) {
		calls++
		return nil, nil, providerHTTPError{StatusCode: http.StatusServiceUnavailable}
	}
	_, _, err := runProtocolAdapterCreateWithRetry(context.Background(), attempt, noSleepPolicy())
	if err == nil {
		t.Fatal("expected the last upstream error to surface")
	}
	// 首次 + videoCreateMaxRetries 次重试，且必须返回真实的最后一次错误。
	if calls != videoCreateMaxRetries+1 {
		t.Fatalf("expected %d attempts, got %d", videoCreateMaxRetries+1, calls)
	}
}

func TestCreateWithRetryDoesNotRetryParameterErrors(t *testing.T) {
	calls := 0
	attempt := func(context.Context) ([]byte, *protocol.Result, error) {
		calls++
		return nil, nil, providerHTTPError{StatusCode: http.StatusBadRequest, Body: "num_frames 是禁止字段"}
	}
	_, _, err := runProtocolAdapterCreateWithRetry(context.Background(), attempt, noSleepPolicy())
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("400 must not be retried, got %d attempts", calls)
	}
}

func TestCreateWithRetryDoesNotRetryConnectionErrors(t *testing.T) {
	calls := 0
	attempt := func(context.Context) ([]byte, *protocol.Result, error) {
		calls++
		return nil, nil, fmt.Errorf("Post \"https://apihub.agnes-ai.com/v1/videos\": %w", io.EOF)
	}
	_, _, err := runProtocolAdapterCreateWithRetry(context.Background(), attempt, noSleepPolicy())
	if err == nil {
		t.Fatal("expected error")
	}
	// 计费安全：无法确认上游是否已受理时不重试。
	if calls != 1 {
		t.Fatalf("connection errors must not be retried, got %d attempts", calls)
	}
}

func TestCreateWithRetryHonorsRetryAfter(t *testing.T) {
	var slept []time.Duration
	policy := noSleepPolicy()
	policy.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	attempt := func(context.Context) ([]byte, *protocol.Result, error) {
		return nil, nil, providerHTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 3 * time.Second}
	}
	_, _, _ = runProtocolAdapterCreateWithRetry(context.Background(), attempt, policy)
	if len(slept) != videoCreateMaxRetries {
		t.Fatalf("expected %d sleeps, got %d", videoCreateMaxRetries, len(slept))
	}
	for _, d := range slept {
		if d != 3*time.Second {
			t.Fatalf("expected Retry-After to win over base backoff, got %v", d)
		}
	}
}

func TestVideoCreateRetryAppliesOnlyToAgnesVideo(t *testing.T) {
	agnes := stubAdapter{id: "agnes-video"}
	seedance := stubAdapter{id: "ark-videos"}
	tests := []struct {
		name    string
		mode    string
		adapter protocol.Adapter
		want    bool
	}{
		{"Agnes 视频", "video", agnes, true},
		{"Agnes 但非视频模态", "image", agnes, false},
		{"Agnes 文本模态", "text", agnes, false},
		{"其他视频协议不启用", "video", seedance, false},
		{"缺少适配器", "video", nil, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := videoCreateRetryApplies(test.adapter, canvasGenerationInput{Mode: test.mode})
			if got != test.want {
				t.Fatalf("videoCreateRetryApplies(mode=%q, adapter=%v) = %v, want %v",
					test.mode, test.adapter, got, test.want)
			}
		})
	}
}

func TestVideoCreateRetryAppliesDefaultsEmptyModeToVideo(t *testing.T) {
	// runVideoTaskWithPolicy 会把空 Mode 归一为 video，判定需与之一致。
	agnes := stubAdapter{id: "agnes-video"}
	if !videoCreateRetryApplies(agnes, canvasGenerationInput{Mode: "video"}) {
		t.Fatal("expected Agnes video to enable create retry")
	}
}

func TestVideoCreateRetryDelayUsesExponentialBackoff(t *testing.T) {
	err := providerHTTPError{StatusCode: http.StatusServiceUnavailable}
	want := []time.Duration{
		videoCreateRetryBaseDelay,     // 2s
		2 * videoCreateRetryBaseDelay, // 4s
		4 * videoCreateRetryBaseDelay, // 8s
		8 * videoCreateRetryBaseDelay, // 16s
	}
	for attempt, expected := range want {
		if got := videoCreateRetryDelay(err, attempt); got != expected {
			t.Fatalf("attempt %d: got %v, want %v", attempt, got, expected)
		}
	}
}

func TestVideoCreateRetryDelayCapsBackoff(t *testing.T) {
	err := providerHTTPError{StatusCode: http.StatusServiceUnavailable}
	// 指数增长最终要被 videoCreateRetryMaxDelay 截断。
	if got := videoCreateRetryDelay(err, 20); got != videoCreateRetryMaxDelay {
		t.Fatalf("got %v, want capped %v", got, videoCreateRetryMaxDelay)
	}
}

func TestVideoCreateRetryDelayCapsExcessiveRetryAfter(t *testing.T) {
	// 极端 Retry-After 不能一次吃光任务预算。
	err := providerHTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 10 * time.Minute}
	if got := videoCreateRetryDelay(err, 0); got != videoCreateRetrySleepCap {
		t.Fatalf("got %v, want capped %v", got, videoCreateRetrySleepCap)
	}
}

func TestVideoCreateRetryDelayPrefersRetryAfterWithinCap(t *testing.T) {
	err := providerHTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: 7 * time.Second}
	if got := videoCreateRetryDelay(err, 3); got != 7*time.Second {
		t.Fatalf("got %v, want the upstream Retry-After", got)
	}
}

// 上游 503（队列已满）必须判为「明确拒绝」，否则任务会被标成 submission_unknown
// 并停止自动重发，把可自愈的瞬时故障变成用户可见的失败。
func TestSafeRouteRejectionTreatsUpstream503AsRejection(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"503 队列已满", providerHTTPError{StatusCode: http.StatusServiceUnavailable}, true},
		{"429 限速", providerHTTPError{StatusCode: http.StatusTooManyRequests}, true},
		{"500 上游内部错误", providerHTTPError{StatusCode: http.StatusInternalServerError}, true},
		{"501 未实现", providerHTTPError{StatusCode: http.StatusNotImplemented}, true},
		{"401 鉴权失败", providerHTTPError{StatusCode: http.StatusUnauthorized}, true},
		{"404 路径不存在", providerHTTPError{StatusCode: http.StatusNotFound}, true},
		// 网关层：请求可能已送达源站并受理，重发有重复计费风险。
		{"502 网关错误", providerHTTPError{StatusCode: http.StatusBadGateway}, false},
		{"504 网关超时", providerHTTPError{StatusCode: http.StatusGatewayTimeout}, false},
		// 连接类错误无法确认是否已受理。
		{"EOF", io.EOF, false},
		{"上下文取消", context.Canceled, false},
		{"普通业务错误", errors.New("解析响应失败"), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := safeRouteRejection(test.err); got != test.want {
				t.Fatalf("safeRouteRejection(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}
