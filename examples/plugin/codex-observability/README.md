# codex-observability

一个 CLIProxyAPI 动态插件，做两件事：

1. **模型白名单**：只允许 `config.yaml` 里列出的 ChatGPT/Codex 模型执行，其它模型在
   请求到达上游之前就被本地拒绝，并返回自定义的 HTTP 状态码与错误体。
2. **半小时观测日志**：把每个客户端 api key 的 token 用量、以及上游账号的
   `X-Codex-*` 配额百分比，按 30 分钟一桶落盘成 NDJSON，用于后续拟合
   “本地 token → 上游额度百分比”的换算关系。

同一份日志同时记录了账号级百分比和 key 级 token，因此可以用来做回归：
在同一个上游窗口实例内，逐桶取 `delta_tokens` 与 `delta_percent` 配对。

## 能力

| 能力 | 说明 |
| --- | --- |
| `request_interceptor` | 在 `request.intercept_before` 阶段做模型闸门 |
| `usage_plugin` | 接收 `usage.handle`，聚合 token 与配额信号 |

## 输出文件

默认写到 `observe.dir`（相对进程工作目录），一天一个文件：

| 文件 | 粒度 | 内容 |
| --- | --- | --- |
| `raw-YYYY-MM-DD.ndjson` | 每个上游请求一行 | 时间、key、auth、模型、token 明细、延迟、是否流式、是否带配额 |
| `key-usage-YYYY-MM-DD.ndjson` | 每桶 × key × auth × 模型一行 | 请求数、失败数、本地估算 credits、完整 token 分项 |
| `account-quota-YYYY-MM-DD.ndjson` | 每桶 × auth 一行 | 每个配额窗口的 first/last/min/max/样本数、重置标记、额度范围字段 |
| `quota-headers-YYYY-MM-DD.ndjson` | 每个带 `x-codex-*` 的响应一行 | 原始响应头快照，用于发现上游新增信号（需开启 `quota-header-dump`） |

### token 分项

`tokens` 对象按上游插件实际暴露的字段分别累加，不做合并：
`input`、`output`、`reasoning`、`cached`、`cache_read`、`cache_create`、`total`。
保留原始分项是为了后续能验证各种加权公式，而不是过早地丢掉信息。

### 桶对齐

按 `observe.timezone`（默认进程本地时区）对齐到整 `:00` / `:30`。
请求时间取 `UsageRecord.RequestedAt`，因此跨桶边界不会漂移。

### 重置检测

同一个桶内，如果某个窗口的百分比出现了下降，或 `reset_at` 发生了变化，
该桶会被标记 `reset_seen: true`。**带这个标记的桶不能用于回归**，因为窗口在桶内翻转了。

### 隐私

`hash-keys: true`（默认）时，日志里存的是 api key 的 SHA-256 前 12 位十六进制，
原始 key 不出现在磁盘上。用 `key-labels` 把哈希映射回人可读的标签：

```yaml
key-labels:
  - key: "客户A的key"
    label: "customer-a"
```

## 配置

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    codex-observability:
      enabled: true
      priority: 10

      model-allowlist:
        enabled: true
        match: "any"        # any | requested | resolved
        models:
          - "gpt-5.5"
          - "gpt-image-*"   # 尾部 * 表示前缀匹配
        status-code: 404
        error-code: "model_not_found"
        message: "model not allowed by this proxy"

      observe:
        enabled: true
        dir: "logs/quota-obs"
        bucket-minutes: 30
        timezone: "Asia/Shanghai"
        flush-interval: "30s"
        retention-days: 30
        raw-events: true
        quota-header-dump: false
        hash-keys: true
        compute-units: true
        key-labels: []
```

### `model-allowlist`

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 打开闸门；模型列表为空时闸门自动失效（等于不拦） |
| `match` | `any` | `any` 表示“客户端请求名或改写后的上游名 任一命中即放行”；`requested` 只看客户端请求名；`resolved` 只看别名改写后的名字 |
| `models` | 空 | 允许的模型名，大小写不敏感，尾部 `*` 表示前缀匹配 |
| `status-code` | `404` | 拒绝时的 HTTP 状态码 |
| `error-code` | `model_not_found` | 拒绝体里的 `error.code` |
| `message` | `model not allowed` | 拒绝体里 `error.message` 的前缀，后面拼接模型名 |

拒绝响应体形如：

```json
{"error":{"message":"model not allowed by this proxy: gpt-9","type":"invalid_request_error","code":"model_not_found","param":"model"}}
```

### `observe`

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `enabled` | `false` | 打开观测 |
| `dir` | `logs/quota-obs` | 输出目录，相对进程工作目录 |
| `bucket-minutes` | `30` | 分桶粒度 |
| `timezone` | 进程本地时区 | 分桶所用时区，如 `Asia/Shanghai` |
| `flush-interval` | `30s` | 已关闭的桶落盘检查间隔 |
| `retention-days` | `30` | 只清理本插件自己那 4 种前缀、且早于该天数的文件 |
| `raw-events` | `true` | 是否写逐请求明细 |
| `quota-header-dump` | `false` | 是否额外写原始 `x-codex-*` 头快照 |
| `hash-keys` | `true` | key 是否哈希 |
| `compute-units` | `true` | 是否输出本地 credits 估算（`units` 字段） |
| `key-labels` | 空 | key → 标签映射 |
| `units-overrides` | 空 | 覆盖内置单价表，形如 `模型子串: {input, cached, output}` |

> `units` 是用官方公布的“credits per 1M tokens”单价表本地估算出来的参考值，
> 不代表上游真实计费。它的作用是提供一个与 percent 做相关性的候选自变量。

## 配额信号覆盖范围

`account-quota` 会记录上游能给出的全部 Codex 配额信号：

- `base` / `code-review` / `additional`（含 websocket 的 `additional-<limit>` 命名空间）
- 每个 scope 的 `primary` 与 `secondary` 两个窗口
- `used-percent`、`window-minutes`、`reset-at`、`reset-after-seconds`
- `limit-name`（人可读的额度名）、`allowed`、`limit-reached`
- 顶层 `plan-type`、`active-limit`、`credits.{has-credits,unlimited,balance}`

只有同时具备 `used-percent` 和 `window-minutes` 的窗口才会被当作有效窗口记录，
这与宿主自身的判定一致。

## 构建

```bash
# Linux amd64（Docker 部署用的就是这一份）
docker run --rm -v "<repo>:/src" -w /src/examples/plugin/codex-observability/go \
  golang:1.26-bookworm sh -c "CGO_ENABLED=1 go build -buildmode=c-shared \
  -o /src/plugins/linux/amd64/codex-observability.so ."

# 测试
docker run --rm -v "<repo>:/src" -w /src/examples/plugin/codex-observability/go \
  golang:1.26-bookworm sh -c "CGO_ENABLED=1 go test ./..."
```

插件 ID 取自文件名（去掉扩展名），即 `codex-observability`，必须与
`plugins.configs` 下的键一致。宿主会先找 `plugins/<goos>/<goarch>/`，再找 `plugins/`。

## 拟合要点

1. 用 `auth_id` 过滤出目标上游账号的记录。
2. 用 `account-quota` 的 `reset_at` / `reset_at_unix` 切分窗口实例；
   丢弃 `reset_seen: true` 的桶。
3. 把同一桶的 `key-usage.tokens`（按模型分项）与 `account-quota.windows[].delta` 配对。
4. 同一窗口内对 `delta_tokens` 与 `delta_percent` 做线性回归，即可得到该模型族
   每百分点对应的 token 数；不同模型族（astra / sol / terra / luna …）
   应分别拟合，因为单价不同。