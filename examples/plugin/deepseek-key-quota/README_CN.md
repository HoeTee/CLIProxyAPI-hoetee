# DeepSeek 客户端永久额度插件

`deepseek-key-quota` 从 OpenAI 兼容响应的 `usage` 中读取缓存命中输入、缓存未命中输入和输出 token，按人民币单价永久累计。客户端 key 与 Codex 额度共用顶层 `clients` 配置，只写一次。DeepSeek 上游的 `openai-compatibility.name` 必须为 `deepseek`；核心传给插件的提供商标识是 `openai-compatible-deepseek`。

```yaml
plugins:
  configs:
    deepseek-key-quota:
      enabled: true
      pricing:
        off-peak: {cache-hit: 0.02, cache-miss: 1.00, output: 4.00}
        peak: {cache-hit: 0.04, cache-miss: 2.00, output: 8.00}
```

```text
cost_cny = (缓存命中 × 命中单价 + 缓存未命中 × 未命中单价
          + 输出 × 输出单价) / 1,000,000
```

默认账本路径为 `logs/deepseek-key-quota/usage.ndjson`，默认预留金额为 `0.01` 元。插件内部按北京时间判断峰谷：工作日 09:00–12:00、14:00–18:00 为高峰，其余为低峰。官方价格变化后只需更新价格；已有事件保留当时的价格快照。修改额度或单价不会清空历史用量。账本不保存客户端 key。

```powershell
$repo = (Get-Location).Path
docker run --rm --mount "type=bind,source=$repo,target=/src" `
  -w /src/examples/plugin/deepseek-key-quota/go golang:1.26-bookworm `
  sh -c 'export PATH=/usr/local/go/bin:$PATH; go test ./... && CGO_ENABLED=1 go build -buildmode=c-shared -o /src/plugins/linux/amd64/deepseek-key-quota.so .'
```

启动与调用：

```powershell
$env:CLI_PROXY_IMAGE = 'cli-proxy-quota:local'
docker compose up -d --build --pull never
$headers = @{Authorization = 'Bearer client-key-a'}
$body = @{model='deepseek-chat'; messages=@(@{role='user'; content='hi'}); max_tokens=4} | ConvertTo-Json -Depth 4
Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8317/v1/chat/completions' -Headers $headers -ContentType 'application/json' -Body $body
```

额度不足返回 `429`，客户端未配置 DeepSeek 额度返回 `403`，账本不可用返回 `503`。
