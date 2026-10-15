# opencode2cpa

把 **OpenCode Zen** 的免费模型接进 **CLIProxyAPI（cpa）**。

结论先说：可行，而且大概率**不需要写 cpa 的 Go 插件**。OpenCode Zen 本身就是 OpenAI 兼容端点，
cpa 的 `openai-compatibility` 可以直接接；本仓库另外附带一个 sidecar（`opencode-zen-proxy`），
用于只想暴露免费模型、需要客户端鉴权 / 模型别名统一 / 覆盖 API key 的场景。

## 原理（从 opencode 二进制里挖出来的）

- 免费模型来自 OpenCode Zen：`base-url = https://opencode.ai/zen/v1`，OpenAI 兼容
  （`/chat/completions`、`/models`）。catalog 里 `opencode` 这个 provider 的 npm 包是
  `@ai-sdk/openai-compatible`。
- 未登录时，opencode CLI 会把该 provider 的 apiKey 设成字面量 **`"public"`**，
  然后把 catalog 中 `cost.input > 0` 的模型全部禁用。所以：
  **免费模型 = catalog 里 `cost.input == 0` 的模型**，鉴权头就是 `Authorization: Bearer public`。
- 因此接入 cpa 只需要「一个 base-url + 一个 api key + 一份模型清单」。
- 付费/订阅模型（Claude、GPT、Gemini 等）需要 `opencode auth login` 后的 key，
  订阅版走 `https://opencode.ai/zen/go/v1`。

模型清单由 `scripts/extract-models.mjs` 直接从本机 opencode 二进制里提取，
生成 `models_gen.go`（本次基于 opencode **1.18.15**：zen 共 86 个模型，其中 25 个免费；go 共 24 个）。

## 方式 A：cpa 直接接入（推荐，零代码）

把 `cliproxyapi/config.example.yaml` 里的段落合并进你的 `config.yaml`，重启 cpa：

```yaml
openai-compatibility:
  - name: opencode-zen
    base-url: https://opencode.ai/zen/v1
    api-keys:
      - "public"
    models:
      - name: "grok-code"
        alias: "zen/grok-code"
      # ... 其余 24 个免费模型见 cliproxyapi/config.example.yaml
```

之后在客户端里用 `zen/grok-code`、`zen/glm-5-free` 这样的名字即可。

> 字段名（`openai-compatibility` / `base-url` / `api-keys` / `models[].alias`）请对照你所用
> cpa 版本的 `config.example.yaml`；本文件保证正确的一项是 `base-url`、`api-keys` 和模型清单本身。

## 方式 B：sidecar（`opencode-zen-proxy`）

```bash
go build -buildvcs=false -o bin/opencode-zen-proxy .
./bin/opencode-zen-proxy -check                       # 先看解析出来的 key 来源和模型数量
./bin/opencode-zen-proxy --addr 127.0.0.1:8320        # 默认 zen 模式，只放免费模型

curl -s http://127.0.0.1:8320/healthz
curl -s http://127.0.0.1:8320/v1/models | head -c 400
curl -s http://127.0.0.1:8320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"zen/grok-code","messages":[{"role":"user","content":"hi"}]}'
```

再让 cpa 指向它（此时 cpa 的 api-keys 填你自己设的 `--client-key`）：

```yaml
openai-compatibility:
  - name: opencode-zen
    base-url: http://127.0.0.1:8320/v1
    api-keys: ["<--client-key 的值>"]
    models:
      - name: "grok-code"
        alias: "zen/grok-code"
```

sidecar 相比直接接入多做的事：

- `/v1/models` 只列出免费模型（`?all=1` 可看全部）；
- 免费白名单校验，付费模型直接返回 403 `model_not_free`，不会误扣费；
- 自动从 `~/.local/share/opencode/auth.json` 读取 key（`opencode` 优先，其次 `opencode-go`），
  读不到就退化成 `public`（`-check` 可确认命中了哪一条）；
- 客户端可以用 `X-Opencode-Key: <key>` 覆盖上游 key；
- 请求/响应（含 SSE 流式）原样透传，上游错误（如免费额度用尽的 `FreeUsageLimitError` / 429）原样返回。

### 参数

| flag | 默认值 | 说明 |
| --- | --- | --- |
| `--addr` | `127.0.0.1:8320` | 监听地址（`ZEN_PROXY_ADDR`） |
| `--mode` | `zen` | `zen` = 免费模型；`go` = OpenCode Go 订阅目录（`ZEN_PROXY_MODE`） |
| `--upstream` | 跟随 `--mode` | 覆盖上游 base-url |
| `--key` | `$OPENCODE_API_KEY` → `auth.json` → `public` | 上游 API key |
| `--auth-file` | `~/.local/share/opencode/auth.json` | opencode 登录态文件 |
| `--free-only` | `true` | zen 模式下拒绝非免费模型（`--free-only=false` 关闭） |
| `--client-key` | 空 | 逗号分隔，要求客户端携带的 Bearer token（`ZEN_PROXY_KEY`） |
| `-check` | `false` | 打印解析后的配置后退出，不监听端口 |

## 免费模型清单（opencode 1.18.15，25 个）

| 模型 id | 名称 | 上下文 |
| --- | --- | --- |
| `trinity-large-preview-free` | Trinity Large Preview | 131K |
| `hy3-preview-free` | Hy3 preview Free | 256K |
| `minimax-m3-free` | MiniMax-M3 Free | 200K |
| `glm-4.7-free` | GLM-4.7 Free | 204K |
| `north-mini-code-free` | North Mini Code Free | 256K |
| `longcat-2.0-free` | LongCat-2.0 Free | 1000K |
| `qwen3.6-plus-free` | Qwen3.6 Plus Free | 262K |
| `ling-3.0-flash-free` | Ling-3.0-flash Free | 262K |
| `laguna-s-2.1-free` | Laguna S 2.1 Free | 256K |
| `deepseek-v4-flash-free` | DeepSeek V4 Flash Free (New) | 200K |
| `glm-5-free` | GLM-5 Free | 204K |
| `mimo-v2-omni-free` | MiMo V2 Omni Free | 262K |
| `minimax-m2.5-free` | MiniMax-M2.5 Free | 204K |
| `minimax-m2.1-free` | MiniMax-M2.1 Free | 204K |
| `mimo-v2-flash-free` | MiMo V2 Flash Free | 262K |
| `grok-code` | Grok Code Fast 1 | 256K |
| `hy3-free` | Hy3 Free | 190K |
| `kimi-k2.5-free` | Kimi K2.5 Free | 262K |
| `mimo-v2.5-free` | MiMo V2.5 Free | 200K |
| `big-pickle` | Big Pickle | 200K |
| `nemotron-3-super-free` | Nemotron 3 Super Free | 204K |
| `nemotron-3-ultra-free` | Nemotron 3 Ultra Free | 1000K |
| `ring-2.6-1t-free` | Ring 2.6 1T Free | 262K |
| `mimo-v2-pro-free` | MiMo V2 Pro Free | 1048K |
| `ling-2.6-flash-free` | Ling 2.6 Flash Free | 262K |

## 开发

```bash
go test -buildvcs=false ./...     # 单元测试（stub 上游，不需要网络）

# 升级 opencode 后重新生成模型清单
OPENCODE_VERSION="$(opencode --version)" \
  node scripts/extract-models.mjs "$(readlink -f "$(command -v opencode)")" models_gen.go
```

## 注意

- 免费额度有限且按账号/来源限流，打满后上游返回 429（`FreeUsageLimitError`），代理只做透传。
- 本仓库是在离线环境里写的：代码逻辑、模型清单、单元测试都验证过，
  但**没有真的向 `opencode.ai` 发过请求**，第一次跑请先 `curl /v1/models` 确认连通。
- 不要提交你的 API key；`auth.json` 只在本地被读取。
