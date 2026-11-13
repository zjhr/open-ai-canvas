# StepFun 音频生成

该目录是独立官方协议插件源码。后端从生成的 `stepfun-audio-gen.yingce-plugin` 包加载，不依赖系统内置 `host:` 适配器。

完整字段清单见 [docs/interface.md](docs/interface.md)。

## 快速开始

1. 在 [StepFun 开放平台](https://platform.stepfun.com) 创建 API Key。
2. 在模型渠道中新增自定义渠道，协议选择「StepFun 音频生成」，填写 API Key。
3. 模型填 `stepaudio-3-gen-preview`。
4. 在画布音频节点或创作页输入描述后提交，插件请求 `POST https://api.stepfun.com/v1/audio/generate` 并返回音频。

## 与 StepFun 语音合成的区别

| 项 | 语音合成（`stepfun-audio`） | 音频生成（本插件） |
| --- | --- | --- |
| 端点 | `/v1/audio/speech` | `/v1/audio/generate` |
| 输入 | 单段文本 `input` | `instruction` + 可选多角色 `roles`/`scripts` |
| 音色 | `voice` 必填 | 不支持 `voice`，传了返回 400 |
| 用途 | 单一音色播报 | 多人对白、音效、环境音、BGM 统一编排 |

## 字段映射

- 宿主 `prompt` 映射为上游 `instruction`（全局语境描述）。
- `audioInstructions` 优先于 `prompt` 写入 `instruction`。
- `audioFormat` → `response_format`（wav/mp3/flac/opus/pcm，默认 mp3）；`audioSpeed` → `speed`。
- 请求体固定携带 `task: "text_to_audio"`。
- 多角色与音效通过渠道 providerOptions 传入：
  - `"stepfun-audio-gen".roles`：`[{ "name": "机长", "description": "中年男性，无线电压缩音色" }]`
  - `"stepfun-audio-gen".scripts`：`[{ "speaker": "机长", "text": "收到，请求后推许可" }, { "text": "[引擎低频轰鸣]" }]`

## 限制

- `roles` 的 `name` + `description` 合计最多 500 字符；`scripts` 合计最多 1000 字符；`instruction` 最多 500 字符。
- 超出限制或传入 `voice`、`voice_label`、`timestamp` 会返回 HTTP 400。
- 当前仅支持 `text_to_audio`；`reference_to_audio` 尚未开放。
- 流式 `stream_format: "sse"` 与 `return_url` 未接入，当前使用二进制文件响应。
