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

- ⚠️ **免费额度有来源校验**：光带 `Authorization: Bearer public` 会拿到
  `403 OpenCode's free tier can only be used from within OpenCode`。zen 要求请求带上 CLI 自己
  会带的头：`User-Agent: opencode/<version>`、`x-opencode-client: cli`、`x-opencode-session`、
  `x-opencode-request`（见下面「403 排查」）。本仓库的 sidecar 会自动补齐这些头。

模型清单由 `scripts/extract-models.mjs` 直接从本机 opencode 二进制里提取，
生成 `models_gen.go`（本次基于 opencode **1.18.15**：zen 共 86 个模型，其中 25 个免费；go 共 24 个）。

## 403 "free tier can only be used from within OpenCode" 排查

opencode 会校验请求是不是 CLI 发出来的。CLI 对 zen 的每个请求都带：

| header | 值 |
| --- | --- |
| `User-Agent` | `opencode/<opencode 版本>`，例如 `opencode/1.18.15` |
| `x-opencode-client` | `cli` |
| `x-opencode-session` | 会话 id（随机串即可） |
| `x-opencode-request` | 请求 id（随机串即可） |
| `Authorization` | `Bearer public`（未登录）或你的 key |

- 用本仓库 sidecar：**不用管**，它会自动补上（`--user-agent` 可改，`--header k=v` 可加别的）。
- 用 cpa 直连 zen：必须在 provider 里加自定义 header，见 `cliproxyapi/config.example.yaml`
  里的 `headers:` 段；如果你的 cpa 版本不支持自定义 header，就只能走 sidecar。
- 手动确认：

```bash
curl -s https://opencode.ai/zen/v1/chat/completions \
  -H 'Authorization: Bearer public' \
  -H 'User-Agent: opencode/1.18.15' \
  -H 'x-opencode-client: cli' \
  -H 'Content-Type: application/json' \
  -d '{"model":"grok-code","messages":[{"role":"user","content":"hi"}]}'
```

## 方式 A：cpa 直接接入（零代码，但需要能自定义 header）

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

## 方式 B：sidecar（`opencode-zen-proxy`，推荐）

思路：本机跑一个小代理，它去访问 zen 并自动补齐 opencode 的那些头；
你的网关（cpa / 其它面板）只要把它当成一个普通的 OpenAI 兼容供应商即可。

**1. 编译**（需要 Go 1.22+，只要一次）

```bash
cd ~/projects/opencode2cpa
go build -buildvcs=false -o bin/opencode-zen-proxy .
```

**2. 自检**（不会监听端口，只看解析出来的配置）

```bash
./bin/opencode-zen-proxy -check
```

输出示例：`key_source: auth.json:opencode-go`、`models: 25 free / 86 total`。
`key_source` 是 `public` 也没关系，免费模型照样能用。

**3. 启动**

```bash
./bin/opencode-zen-proxy --addr 127.0.0.1:8320
```

前台运行，`Ctrl-C` 停止。想后台常驻见第 6 步。

**4. 自测**（另开一个终端）

```bash
curl -s http://127.0.0.1:8320/healthz
curl -s http://127.0.0.1:8320/v1/models | head -c 300
curl -s http://127.0.0.1:8320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"zen/grok-code","messages":[{"role":"user","content":"hi"}]}'
```

第 3 条能返回内容，说明 zen 那边已经放行了（不会再 403）。

**5. 在你的网关里加一个供应商**

| 字段 | 填什么 |
| --- | --- |
| 名称 | `opencode-zen`（随便） |
| 服务地址 / base-url | `http://127.0.0.1:8320/v1` |
| API 密钥 | 任意非空值，例如 `public`（如果 sidecar 加了 `--client-key`，就填那个值） |
| 测试模型 | `grok-code` 或 `zen/grok-code` |
| 自定义 header | 不需要 |

保存后点「测试」，应该能看到 25 个免费模型可选。

> - 网关在 Windows、sidecar 在 WSL 里：WSL2 默认有 localhost 转发，直接填 `127.0.0.1:8320` 一般就行；
>   不通的话用 `hostname -I` 拿到 WSL 的 IP，并给 sidecar 加 `--addr 0.0.0.0:8320`。
> - 网关跑在 Docker 容器里：`127.0.0.1` 指的是容器自己，要用 `host.docker.internal:8320`
>   并加 `--addr 0.0.0.0:8320`。
> - 跑在别的机器上：换成宿主机 IP，并建议同时设 `--client-key` 免得被人白嫖。

**6. 后台常驻**

```bash
# 简单方式
nohup ./bin/opencode-zen-proxy --addr 127.0.0.1:8320 >/tmp/opencode-zen-proxy.log 2>&1 &

# 或者用 systemd user service（模板见 deploy/opencode-zen-proxy.service）
mkdir -p ~/.config/systemd/user
cp deploy/opencode-zen-proxy.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now opencode-zen-proxy
systemctl --user status opencode-zen-proxy
```

**它比直连多做的事**

- 自动补上 zen 免费额度要求的 `User-Agent: opencode/<version>` / `x-opencode-client` /
  `x-opencode-session` / `x-opencode-request` 头（客户端自己的 UA 会被覆盖）；
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
| `--user-agent` | `opencode/1.18.15` | 发给上游的 UA，zen 免费额度靠它放行（`ZEN_PROXY_USER_AGENT`） |
| `--header` | 空 | 额外上游 header，`k=v`，可重复 |

## 怎么以 OpenAI 兼容方式调用

三种接法的区别只有 `base_url` / `api_key` / `model` 三个值：

| 接法 | base_url | api_key | model |
| --- | --- | --- | --- |
| 直连 OpenCode Zen | `https://opencode.ai/zen/v1` | `public`（或你自己的 opencode key） | `grok-code`（原始 id） |
| 本仓库 sidecar | `http://127.0.0.1:8320/v1` | 任意非空值（设了 `--client-key` 就填它） | `grok-code` 或 `zen/grok-code` |
| 经 cpa 转发 | `http://<cpa 地址>:8317/v1` | cpa `api-keys` 里的那一把 | `zen/grok-code`（config 里的 alias） |

- 三条路径最终都打到 `https://opencode.ai/zen/v1`，都是 OpenAI 的
  `POST /v1/chat/completions` + `GET /v1/models` 两个接口。
- sidecar 会把 `zen/`、`opencode/` 前缀剥掉再发给上游，所以带不带前缀都能用；
  cpa 侧则按 `alias` 匹配，用别名即可。
- 直连和 sidecar 都只转发 `chat/completions` 与 `models`，
  `/v1/responses`、`/v1/embeddings`、`/v1/images/*` 没做处理。

### curl

```bash
# 直连（注意：必须带 opencode 的 UA / x-opencode-* 头，否则 403）
curl -s https://opencode.ai/zen/v1/chat/completions \
  -H 'Authorization: Bearer public' \
  -H 'User-Agent: opencode/1.18.15' \
  -H 'x-opencode-client: cli' \
  -H 'Content-Type: application/json' \
  -d '{"model":"grok-code","messages":[{"role":"user","content":"你好"}]}'

# 流式
curl -N -s https://opencode.ai/zen/v1/chat/completions \
  -H 'Authorization: Bearer public' \
  -H 'Content-Type: application/json' \
  -d '{"model":"grok-code","stream":true,"messages":[{"role":"user","content":"你好"}]}'

# sidecar（把 base_url 换成它即可）
curl -s http://127.0.0.1:8320/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"zen/grok-code","messages":[{"role":"user","content":"你好"}]}'
```

### Python（openai SDK）

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://opencode.ai/zen/v1",   # 或 http://127.0.0.1:8320/v1
    api_key="public",                        # 或你的 opencode key
    # 直连 zen 时必填，否则 403；走 sidecar 可以省掉
    default_headers={"User-Agent": "opencode/1.18.15", "x-opencode-client": "cli"},
)

stream = client.chat.completions.create(
    model="grok-code",                       # sidecar 也接受 "zen/grok-code"
    messages=[{"role": "user", "content": "你好"}],
    stream=True,
)
for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="")

print(client.models.list().data[0].id)
```

完整可运行示例见 `examples/openai_client.py`。

### Node（openai SDK）

```js
import OpenAI from "openai";

const client = new OpenAI({
  baseURL: "https://opencode.ai/zen/v1",
  apiKey: "public",
});

const stream = await client.chat.completions.create({
  model: "grok-code",
  messages: [{ role: "user", content: "你好" }],
  stream: true,
});
for await (const chunk of stream) process.stdout.write(chunk.choices[0]?.delta?.content ?? "");
```

### 常见客户端

Cherry Studio / ChatBox / NextChat / Cline / Continue / Roo Code 等：
选「自定义 OpenAI 兼容供应商」，`Base URL` 填上表里的地址，`API Key` 填 `public`
（或 sidecar/cpa 的 key），模型名手填 `grok-code`（走 cpa 时填 `zen/grok-code`）。

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
