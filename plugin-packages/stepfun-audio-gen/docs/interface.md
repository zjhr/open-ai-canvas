# StepFun 音频生成 接口字段

## 协议身份

- 插件 ID：`stepfun-audio-gen`。
- Provider ID：`stepfun-audio-gen`。
- 能力：`audio`。
- 默认 Base URL：`https://api.stepfun.com`。
- 鉴权驱动：`bearer`。
- 创建：`POST /v1/audio/generate`。
- 生命周期：同步响应。

## 配置字段

| 字段 | 类型 | 必填 | 含义 |
| --- | --- | --- | --- |
| `apiKey` | secret | 是 | API Key |

## 统一字段映射

| 统一字段 | 类型 | 必填 | 上游映射 | 说明 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | `model` | 音频模型 ID。 |
| `prompt` | string | 是 | `input` | 待合成文本。 |
| `providerOptions` | object | 否 | `provider-specific fields` | 插件命名空间内的厂商扩展字段。 |

## 上游请求模板逐字段清单

下表由插件请求模板生成，覆盖 body、query、headers 和 multipart 文件声明中的每个字段。

| 上游位置 | 值或转换表达式 |
| --- | --- |
| `create.method` | `"POST"` |
| `create.path` | `"/v1/audio/generate"` |
| `create.contentType` | `"application/json"` |
| `create.body.model` | `{"$ref":"request.model"}` |
| `create.body.task` | `"text_to_audio"` |
| `create.body.instruction` | `{"$omitEmpty":{"$coalesce":[{"$ref":"request.extra.audioInstructions"},{"$ref":"request.prompt"}]}}` |
| `create.body.scripts` | `{"$omitEmpty":{"$ref":"request.providerOptions.stepfun-audio-gen.scripts"}}` |
| `create.body.roles` | `{"$omitEmpty":{"$ref":"request.providerOptions.stepfun-audio-gen.roles"}}` |
| `create.body.response_format` | `{"$coalesce":[{"$ref":"request.extra.audioFormat"},{"$ref":"request.providerOptions.stepfun-audio-gen.response_format"},"mp3"]}` |
| `create.body.speed` | `{"$coalesce":[{"$if":{"condition":{"$ne":[{"$toFloat":{"$ref":"request.extra.audioSpeed"}},0]},"then":{"$toFloat":{"$ref":"request.extra.audioSpeed"}},"else":null}},{"$ref":"request.providerOptions.stepfun-audio-gen.speed"},1]}` |
| `create.body.volume` | `{"$omitEmpty":{"$ref":"request.providerOptions.stepfun-audio-gen.volume"}}` |
| `create.body.sample_rate` | `{"$omitEmpty":{"$ref":"request.providerOptions.stepfun-audio-gen.sample_rate"}}` |
| `create.body.text_normalization` | `{"$omitEmpty":{"$ref":"request.providerOptions.stepfun-audio-gen.text_normalization"}}` |

## Provider 扩展键

- `providerOptions.stepfun-audio-gen.response_format`
- `providerOptions.stepfun-audio-gen.roles`
- `providerOptions.stepfun-audio-gen.sample_rate`
- `providerOptions.stepfun-audio-gen.scripts`
- `providerOptions.stepfun-audio-gen.speed`
- `providerOptions.stepfun-audio-gen.text_normalization`
- `providerOptions.stepfun-audio-gen.volume`

动态模型或工作流允许使用文档声明的完整 `parameters/input/extra_body` 对象；该对象是协议本身的开放 schema，不会被宿主裁剪。

## 响应映射逐字段清单

| 映射位置 | 上游路径或转换表达式 |
| --- | --- |
| `response.binaryPayload` | `true` |
| `response.resultKind` | `"audio"` |
| `response.status` | `"succeeded"` |
| `response.errorPaths[0]` | `"error.code"` |
| `response.messagePaths[0]` | `"error.message"` |

## 响应与错误

插件把上游 task/status/text/media/usage 映射为统一结果。临时媒体 URL 标记为 ephemeral，由宿主立即下载持久化。HTTP 错误、业务 code 和 error object 保持失败语义，不包装成成功。

## 兼容边界

同步 /v1/audio/generate 返回原始音频流。必填 task=text_to_audio；不支持 voice，多角色台词与音效通过 providerOptions 的 roles/scripts 传入。

<!-- YINGCE_MANIFEST_CONTRACT_START -->
## Manifest 完整接口定义

以下 JSON 与插件包内实际 `manifest.json` 逐字段一致，覆盖插件身份、权限、配置、鉴权、参数、校验、创建、Agent、查询、取消、结果下载、响应和 Agent 响应映射。`documentation` 字段的值就是当前完整文档；为避免文档在自身内部无限递归，JSON 中仅用等义占位文本表示正文。

```json
{
  "apiVersion": "yingce.plugin/v2",
  "id": "stepfun-audio-gen",
  "name": "StepFun 音频生成",
  "version": "2.0.0",
  "author": "StepFun / 影策",
  "description": "StepFun 音频生成 独立请求协议插件。",
  "documentation": "<当前插件的完整 documentation，由 README.md 与 docs/interface.md 拼接而成；为避免 JSON 递归，此处不重复展开正文。>",
  "permissions": [
    "generation.run",
    "media.read"
  ],
  "configuration": {
    "fields": [
      {
        "name": "apiKey",
        "type": "secret",
        "label": "API Key",
        "required": true
      }
    ]
  },
  "contributes": {
    "providers": [
      {
        "id": "stepfun-audio-gen",
        "label": "StepFun 音频生成",
        "capabilities": [
          "audio"
        ],
        "scopes": [
          "admin.system-channel",
          "user.custom-channel",
          "canvas",
          "creation",
          "agent"
        ],
        "baseUrl": "https://api.stepfun.com",
        "requiresPublicMediaUrls": false,
        "auth": {
          "type": "bearer",
          "field": "apiKey"
        },
        "parameters": [
          {
            "name": "model",
            "type": "string",
            "required": true,
            "mapping": "model",
            "description": "音频模型 ID。"
          },
          {
            "name": "prompt",
            "type": "string",
            "required": true,
            "mapping": "input",
            "description": "待合成文本。"
          },
          {
            "name": "providerOptions",
            "type": "object",
            "required": false,
            "mapping": "provider-specific fields",
            "description": "插件命名空间内的厂商扩展字段。"
          }
        ],
        "create": {
          "method": "POST",
          "path": "/v1/audio/generate",
          "contentType": "application/json",
          "body": {
            "model": {
              "$ref": "request.model"
            },
            "task": "text_to_audio",
            "instruction": {
              "$omitEmpty": {
                "$coalesce": [
                  {
                    "$ref": "request.extra.audioInstructions"
                  },
                  {
                    "$ref": "request.prompt"
                  }
                ]
              }
            },
            "scripts": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.stepfun-audio-gen.scripts"
              }
            },
            "roles": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.stepfun-audio-gen.roles"
              }
            },
            "response_format": {
              "$coalesce": [
                {
                  "$ref": "request.extra.audioFormat"
                },
                {
                  "$ref": "request.providerOptions.stepfun-audio-gen.response_format"
                },
                "mp3"
              ]
            },
            "speed": {
              "$coalesce": [
                {
                  "$if": {
                    "condition": {
                      "$ne": [
                        {
                          "$toFloat": {
                            "$ref": "request.extra.audioSpeed"
                          }
                        },
                        0
                      ]
                    },
                    "then": {
                      "$toFloat": {
                        "$ref": "request.extra.audioSpeed"
                      }
                    },
                    "else": null
                  }
                },
                {
                  "$ref": "request.providerOptions.stepfun-audio-gen.speed"
                },
                1
              ]
            },
            "volume": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.stepfun-audio-gen.volume"
              }
            },
            "sample_rate": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.stepfun-audio-gen.sample_rate"
              }
            },
            "text_normalization": {
              "$omitEmpty": {
                "$ref": "request.providerOptions.stepfun-audio-gen.text_normalization"
              }
            }
          }
        },
        "response": {
          "binaryPayload": true,
          "resultKind": "audio",
          "status": "succeeded",
          "errorPaths": [
            "error.code"
          ],
          "messagePaths": [
            "error.message"
          ]
        }
      }
    ]
  }
}
```
<!-- YINGCE_MANIFEST_CONTRACT_END -->
