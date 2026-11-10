# GLM 客户端额度插件

`glm-key-quota` 检查客户端的 5 小时和每周 token 额度，记录 GLM Coding Plan 的输入、输出和缓存用量，并为管理面板提供统计。

```yaml
plugins:
  enabled: true
  configs:
    glm-key-quota:
      enabled: true
      pricing:
        divisor: 10000
        off-peak-multiplier: 0.5
        peak-hours: [14, 18]
        models:
          glm-5.3: {cache-miss: 6.9, cache-hit: 1.7, output: 24}
          glm-5.3-flash: {cache-miss: 2.3, cache-hit: 0.56, output: 8}
```

每个客户端的额度填在 `clients[].quotas.glm`。将任一窗口填为 `null` 可不限额并继续记账。上游仍由 `openai-compatibility` 中名为 `glm-coding-plan` 的条目配置。达到本地额度后，新请求返回 `429`。已放行的并发请求结算时可能使累计用量超过上限。

默认沿用 `logs/glm-coding-plan/usage.ndjson`。可在插件配置下用 `storage.path` 指定账本路径，修改路径后需重启服务。

本地额度扣减使用 `usage.prompt_tokens + usage.completion_tokens`。插件读取 YAML 中的 `pricing` 折算积分。它用响应的 `model` 选取系数，从 `usage.prompt_tokens` 扣除 `usage.prompt_tokens_details.cached_tokens` 得到未命中输入。按系数加权未命中输入、缓存命中和输出后除以 `divisor`。响应的 `created` 落在北京时间工作日的 `peak-hours` 区间外时，乘以 `off-peak-multiplier`。重新加载价格只影响随后记账的请求。缺少价格配置或必要字段时，面板显示未折算的请求数。套餐实扣和剩余额度由智谱的额度查询接口提供。 [智谱积分抵扣规则](https://docs.bigmodel.cn/cn/coding-plan/overview)

在仓库根目录运行以下命令，构建 Docker 使用的 Linux 插件。

```powershell
$repo = (Get-Location).Path
docker run --rm --mount "type=bind,source=$repo,target=/src" -w /src golang:1.26-bookworm go build -buildmode=c-shared -o /src/plugins/linux/amd64/glm-key-quota.so ./examples/plugin/glm-key-quota/go
```

使用包含客户端额度插件支持的主程序镜像启动服务。插件不可用时，GLM 请求返回 `503`。
