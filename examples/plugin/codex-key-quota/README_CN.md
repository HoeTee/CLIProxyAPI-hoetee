# Codex 客户端周额度插件

`codex-key-quota` 按客户端 ID 记录 Codex token 用量，并按配置的 API 单价折算为美元等价值。所有客户端共享同一个上游 ChatGPT/Codex 账号；上游百分比只用于账号整体保护。

客户端 key 只在顶层 `clients` 中配置一次：

```yaml
clients:
  - id: "user-a"
    api-key: "client-key-a"
    quotas:
      codex:
        weekly-usd: 50.00
      deepseek:
        limit-cny: 100.00
```

插件配置：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    codex-key-quota:
      enabled: true
      weekly-reset-anchor: "2025-10-05T09:00:00+08:00"
      pricing:
        gpt-6.1-sol: {input: 2.00, cached-input: 0.10, cache-write: 2.50, output: 10.00, coefficient: 1.00}
```

```text
raw_usd = (普通输入 × 输入单价 + 缓存读取 × 缓存单价
         + 缓存写入 × 写入单价 + 输出 × 输出单价) / 1,000,000
charged_usd = raw_usd × coefficient
```

`gpt-6.1-sol` 以标准 API 价格和默认 30 分钟缓存写入价折算本地额度，单价单位为美元／百万 token。输入超过 272,000 token 时，整次请求的输入和缓存单价乘以 2，输出单价乘以 1.5。价格于 2026-11-09 核对。[OpenAI 官方定价](https://developers.openai.com/api/docs/pricing) · [模型说明](https://developers.openai.com/api/docs/models/gpt-6.1-sol)

默认账本路径为 `logs/codex-key-quota/usage.ndjson`，默认时区为 `Asia/Shanghai`，默认预留金额为 `0.01` 美元，默认账号保护阈值为 90%。`weekly-reset-anchor` 可以填写过去任意时间，但只有与账号真实重置时间相差整数个 7 天时才会对齐。修改额度、单价或系数不会清空账本；新配置只影响后续请求和后续计费事件。账本保存客户端 ID、token 分类、当时单价、系数和费用，不保存客户端 key。

```powershell
$repo = (Get-Location).Path
docker run --rm --mount "type=bind,source=$repo,target=/src" `
  -w /src/examples/plugin/codex-key-quota/go golang:1.26-bookworm `
  sh -c 'export PATH=/usr/local/go/bin:$PATH; go test ./... && CGO_ENABLED=1 go build -buildmode=c-shared -o /src/plugins/linux/amd64/codex-key-quota.so .'
```

构建主程序并启动服务：

```powershell
$env:CLI_PROXY_IMAGE = 'cli-proxy-quota:local'
docker compose up -d --build --pull never
docker logs --tail 80 cli-proxy-api
```

服务监听 `http://127.0.0.1:8317`。请求使用 `Authorization: Bearer <clients.api-key>`；插件会自动处理选中 Codex 上游的请求。

额度不足返回 `429`，客户端未配置 Codex 额度返回 `403`，账本不可用返回 `503`。
