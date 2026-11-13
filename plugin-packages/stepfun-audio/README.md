# StepFun 语音合成

该目录是独立官方协议插件源码。后端从生成的 `stepfun-audio.yingce-plugin` 包加载，不依赖系统内置 `host:` 适配器。

完整字段清单见 [docs/interface.md](docs/interface.md)。

## 快速开始

1. 在 [StepFun 开放平台](https://platform.stepfun.com) 创建 API Key。
2. 在模型渠道中新增自定义渠道，协议选择「StepFun 语音合成」，填写 API Key。
3. 模型填 `stepaudio-3-tts`；也可用 `stepaudio-2.5-tts`、`step-tts-2`、`step-tts-mini`。
4. 音色填官方 Voice-id（如 `cixingnansheng`、`livelybreezy-female`），默认 `cixingnansheng`。
5. 在画布音频节点或创作页输入文本后提交，插件请求 `POST https://api.stepfun.com/v1/audio/speech` 并返回音频。

## 上游兼容性

- 请求地址与 OpenAI Audio Speech 同构：`POST /v1/audio/speech`，返回二进制音频流。
- `response_format` 支持 `wav`、`mp3`、`flac`、`opus`、`pcm`，默认 `mp3`。
- `speed` 取值 0.5～2，`instruction` 仅 `stepaudio-2.5-tts`、`stepaudio-3-tts` 生效。
- 旧模型可用 `voice_label`（`language` / `emotion` / `style`）控制音色标签；2.5/3 模型不支持该字段。
- 文本上限 1000 字符。需要长文本时由宿主分段提交，插件不自动切分。
- 流式 `stream_format: "sse"` 与非流式 URL 返回（`return_url`）暂未接入，当前只使用二进制文件响应。

## 不包含的能力

StepFun 的 ASR（`/v1/audio/transcriptions`）、Realtime WebSocket 对话、Music / Gen 生成不在本插件内。宿主当前没有语音识别和实时音频会话的协议位，接入它们需要先扩展宿主能力。
