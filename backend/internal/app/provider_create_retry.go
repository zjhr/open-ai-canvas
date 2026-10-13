package app

// 视频「创建」阶段的瞬时错误重试。
//
// 背景：声明式协议任务的 create、poll、download 三个阶段里，只有 poll 与 download
// 有重试（见 provider_video_polling.go 的 runVideoPollLoop / retryableVideoPollError）。
// create 阶段一旦拿到 429/503 就直接失败，而 Agnes 视频创建接口在高峰期会大量返回
// 503「视频队列已满」——同一个瞬时错误，发生在轮询会被自动重试，发生在创建却变成
// 用户可见的失败。本文件只补上创建阶段的重试，消除这个不对称。
//
// 作用域刻意收窄到「Agnes 视频」：
//   - runProtocolAdapterTaskWithPolicy 同时服务 text/image/audio/video 四种能力，
//     在共用函数里加重试会顺带改变文本流式与其他模态的行为，因此接线点放在
//     视频专属的 provider_video.go；
//   - 即使将来放开到所有视频模型，也应保持「只重试明确未受理的错误」这一前提。
//
// 计费安全：create 是计费操作（providerRequestIsBillable 对 POST 返回 true），
// 且 Agnes 适配器当前没有把宿主幂等键映射到上游 Header，上游无法去重。因此这里
// 只重试「上游明确未受理」的错误（429 与 5xx，且必须拿到了 HTTP 响应）。连接中断、
// EOF、超时这些「可能已被上游受理并计费」的错误一律不重试——盲目重试会导致重复扣费。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"yingce/backend/internal/protocol"
)

const (
	// agnesVideoAdapterID 与 protocol 包 agnesAdapter() 的 metadata ID 保持一致。
	agnesVideoAdapterID = "agnes-video"
	// videoCreateMaxRetries 是首次尝试之外的重试次数（总计至多 1+2 次请求）。
	videoCreateMaxRetries = 2
	// videoCreateRetryBaseDelay 是无 Retry-After 时的退避基数，按次数线性递增。
	videoCreateRetryBaseDelay = 700 * time.Millisecond
)

// videoCreateRetryable 判断创建阶段的错误是否值得自动重试。
//
// 只有拿到了明确的 HTTP 失败响应、且该状态码表示「上游未受理本次请求」时才重试：
//   - 429 Too Many Requests：限速，请求未被受理；
//   - 5xx（含 503「视频队列已满」）：上游瞬时不可用，请求未被受理。
//
// 明确不重试的情况：
//   - 没有任何 HTTP 响应（EOF、连接重置、超时、DNS 失败）：请求可能已经送达并计费，
//     重试会造成重复扣费；
//   - 4xx（含 400 参数错误）：重试不会改变结果；
//   - 上下文已取消/超时：调用方已经放弃，继续重试没有意义。
func videoCreateRetryable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var httpErr providerHTTPError
	if !errors.As(err, &httpErr) {
		// 非 HTTP 层错误：没有可判定的状态码，按「可能已受理」处理。
		return false
	}
	if httpErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return httpErr.StatusCode >= http.StatusInternalServerError
}

// videoCreateRetryDelay 复用轮询阶段的退避口径：优先尊重上游 Retry-After，
// 否则按尝试次数线性退避。
func videoCreateRetryDelay(err error, attempt int) time.Duration {
	if retryAfter := providerRetryAfter(err); retryAfter > 0 {
		return retryAfter
	}
	return time.Duration(attempt+1) * videoCreateRetryBaseDelay
}

// runProtocolAdapterCreateWithRetry 在 create 阶段对瞬时错误做有限次重试。
//
// build 每次重试都会重新构造请求规格：Agnes 的 2.5 请求体包含时间相关的
// 参数校验，重建比复用更安全；同时在途重试也不会复用已经被消费的请求体。
func runProtocolAdapterCreateWithRetry(
	ctx context.Context,
	attempt func(context.Context) ([]byte, *protocol.Result, error),
	policy videoPollPolicy,
) ([]byte, *protocol.Result, error) {
	policy = normalizeVideoPollPolicy(policy)
	var lastErr error
	for attemptIndex := 0; attemptIndex <= videoCreateMaxRetries; attemptIndex++ {
		body, streamedResult, err := attempt(ctx)
		if err == nil {
			return body, streamedResult, nil
		}
		lastErr = err
		if attemptIndex == videoCreateMaxRetries || !videoCreateRetryable(ctx, err) {
			break
		}
		if sleepErr := policy.Sleep(ctx, videoCreateRetryDelay(err, attemptIndex)); sleepErr != nil {
			// 上下文在退避期间结束：返回真实的最后一次上游错误，让调用方看到根因。
			break
		}
	}
	return nil, nil, lastErr
}

// videoCreateRetryApplies 决定当前调用是否启用创建重试。
// 双重限定：只有视频模态、且适配器是 Agnes 视频协议时才启用。
func videoCreateRetryApplies(adapter protocol.Adapter, input canvasGenerationInput) bool {
	if strings.TrimSpace(input.Mode) != "video" {
		return false
	}
	return isAgnesVideoAdapter(adapter)
}

// isAgnesVideoAdapter 判断当前适配器是否为 Agnes 视频协议。
// 用 metadata ID 判定，避免依赖模型名（模型名可变，协议 ID 稳定）。
func isAgnesVideoAdapter(adapter protocol.Adapter) bool {
	if adapter == nil {
		return false
	}
	return strings.TrimSpace(adapter.Metadata().ID) == agnesVideoAdapterID
}
