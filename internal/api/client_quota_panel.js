const quotaProviders = [
  {id: "codex", tab: "codex", name: "Codex", icon: "C", fields: [["weekly_usd", "weekly-usd", "每周上限（等值 USD）"]]},
  {id: "deepseek", tab: "DeepSeek", name: "DeepSeek", icon: "D", fields: [["limit_cny", "limit-cny", "累计额度（CNY）"]]},
  {id: "glm-coding-plan", tab: "GLM", name: "GLM", icon: "G", fields: [["five_hour_tokens", "five_hour_tokens", "5 小时上限（Token）"], ["weekly_tokens", "weekly_tokens", "每周上限（Token）"]]}
];
const quotaProviderTabs = new Set(quotaProviders.map(provider => provider.tab));
const quotaWindows = [["five_hour", "5 小时"], ["weekly", "每周"]];
const quotaNumber = new Intl.NumberFormat("zh-CN", {maximumFractionDigits: 2});
const quotaAmount = new Intl.NumberFormat("zh-CN", {maximumFractionDigits: 8});
const quotaTime = new Intl.DateTimeFormat("zh-CN", {month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", timeZone: "Asia/Shanghai"});
const quotaControls = {display: "flex", alignItems: "center", flexWrap: "wrap", gap: 8};
const quotaLocalPath = provider => "/" + provider.tab.toLowerCase() + "-client-quotas";
const quotaPolicyKey = provider => provider.tab.toLowerCase();
const panelOverviewPath = "/provider-overview";

function usePanelRead(path, enabled, interval) {
  const scope = Om(state => state.managementKey);
  const base = Om(state => state.apiBase);
  const [result, setResult] = y.useState({data: null, error: false, pending: true});
  const sequence = y.useRef(0);
  const refresh = y.useCallback(async () => {
    if (!enabled) return;
    const request = ++sequence.current;
    setResult(old => ({...old, pending: true}));
    try {
      const data = await op.get(path);
      if (request === sequence.current) setResult({data, error: false, pending: false});
    } catch {
      if (request === sequence.current) setResult(old => ({...old, error: true, pending: false}));
    }
  }, [path, enabled, scope, base]);
  y.useEffect(() => {
    setResult({data: null, error: false, pending: true});
    void refresh();
    const timer = enabled && interval ? window.setInterval(refresh, interval) : null;
    return () => { ++sequence.current; if (timer !== null) window.clearInterval(timer); };
  }, [refresh, enabled, interval]);
  return {...result, refresh};
}

function useClientQuotaUsage(enabled) {
  return [
    usePanelRead("/codex-client-quotas", enabled, 5000),
    usePanelRead("/deepseek-client-quotas", enabled, 5000),
    usePanelRead("/glm-client-quotas", enabled, 5000)
  ];
}

function useQuotaPanel(enabled) {
  const overview = usePanelRead(panelOverviewPath, enabled);
  const deepseek = usePanelRead("/deepseek-balance", enabled);
  const glm = usePanelRead("/glm-plan-quota", enabled);
  const usage = useClientQuotaUsage(enabled);
  const upstream = [null, deepseek, glm];
  const refresh = y.useCallback(() => {
    void overview.refresh();
    void deepseek.refresh();
    void glm.refresh();
    for (const item of usage) void item.refresh();
  }, [overview.refresh, deepseek.refresh, glm.refresh, ...usage.map(item => item.refresh)]);
  return {overview, upstream, usage, refresh};
}

function panelQuotaCounts(native, panel) {
  const counts = {...native};
  for (const [index, provider] of quotaProviders.entries()) {
    if (!index) continue;
    const record = panel.overview.data?.providers.find(item => item.id === provider.id);
    counts[provider.tab] = Number(Boolean(record?.monitored || panel.upstream[index].data?.configured));
    counts.all += counts[provider.tab];
  }
  return counts;
}

function panelQuotaReady(panel) {
  return panel.upstream.slice(1).filter(item => item.data?.configured && !item.error && !item.pending).length;
}

function PanelNote({children, error}) {
  return y.createElement("p", {className: error ? eN.errorStrip : tN.quotaMessage, role: "status"}, children);
}

function PanelCard({title, icon, children, actions}) {
  const create = y.createElement;
  return create("article", {className: eN.card},
    create("header", {className: eN.head},
      create("span", {className: eN.iconWrap, "aria-hidden": true}, create("span", {className: eN.iconFallback}, icon)),
      create("span", {className: eN.fileName, title}, title)),
    create("div", {className: eN.body}, children),
    actions && create("footer", {className: eN.actionRow}, actions));
}

function PanelSection({title, name, children, action}) {
  const create = y.createElement;
  return create("section", {"aria-label": name ?? title, style: {margin: "12px 0 24px"}},
    create("div", {style: {...quotaControls, marginBottom: 10}},
      create("h3", {style: {margin: 0}}, title), action),
    children);
}

function PanelRefresh({onClick, disabled}) {
  return y.createElement("button", {type: "button", className: eN.actionPill, onClick, disabled}, "刷新额度");
}

function QuotaLine({label, percent, remaining, limit, used, reset, unit, unlimited, unknown, estimatedPoints, unpriced}) {
  const create = y.createElement;
  const validReset = reset && !reset.startsWith("0001-") && Number.isFinite(Date.parse(reset));
  const amounts = [
    remaining == null ? null : "剩余 " + quotaAmount.format(remaining) + (limit == null ? "" : " / " + quotaAmount.format(limit)) + " " + unit,
    remaining == null && limit != null ? "上限 " + quotaAmount.format(limit) + " " + unit : null,
    used == null ? null : "已用 " + quotaAmount.format(used) + " " + unit,
    unknown ? "待核对 " + unknown + " 次请求" : null,
    estimatedPoints == null ? null : "估算 " + quotaAmount.format(estimatedPoints) + " 积分",
    unpriced ? unpriced + " 次未折算积分" : null,
    validReset ? "北京时间 " + quotaTime.format(new Date(reset)) + " 重置" : null
  ].filter(Boolean);
  const percentage = percent == null ? null : Math.min(100, Math.max(0, percent));
  return create("div", {className: tN.quotaRow},
    create("div", {className: tN.quotaRowHeader},
      create("span", {className: tN.quotaModel}, label),
      create("span", {className: tN.quotaPercent}, unlimited ? "无限" : percentage == null ? "暂无数据" : quotaNumber.format(percentage) + "%")),
    percentage != null && create("div", {role: "progressbar", "aria-label": label + "剩余额度",
      "aria-valuemin": 0, "aria-valuemax": 100, "aria-valuenow": percentage},
      create(xO, {percent: percentage, classes: tN})),
    amounts.length > 0 && create("div", {className: tN.quotaAmount}, amounts.join(" · ")));
}

const tokenSeries = [
  ["cache_hit_tokens", "缓存命中", "var(--viz-high, #16a34a)"],
  ["cache_miss_tokens", "未命中", "#4f77e8"],
  ["cache_write_tokens", "缓存写入", "#a46a32"],
  ["cache_unknown_tokens", "缓存状态未知", "#8a8a8a"],
  ["output_tokens", "输出", "#a061dd"]
];

function TokenUsageChart({hours}) {
  const create = y.createElement;
  if (!hours?.length) return create(PanelNote, null, "暂无 Token 用量数据。");
  const series = tokenSeries.filter(([field]) => hours.some(hour => hour[field] != null));
  const sum = (hour, field) => Number(hour[field] ?? 0);
  const max = Math.max(1, ...hours.map(hour => series.reduce((total, [field]) => total + sum(hour, field), 0)));
  return create("figure", {style: {margin: 0}, "aria-label": "最近 24 小时 Token 用量"},
    create("figcaption", {className: tN.quotaModel}, "最近 24 小时 Token 用量"),
    create("div", {style: {display: "grid", gridTemplateColumns: "repeat(24, minmax(0, 1fr))", gap: 3, height: 64}},
      hours.map(hour => {
        const tooltip = [quotaTime.format(new Date(hour.start)), ...series.map(([field, label]) => label + " " + quotaNumber.format(sum(hour, field)))];
        if (hour.unknown_requests) tooltip.push("待核对 " + hour.unknown_requests + " 次");
        return create("div", {key: hour.start, role: "img", title: tooltip.join(" · "), "aria-label": tooltip.join("，"),
          style: {display: "flex", flexDirection: "column-reverse", borderBottom: "1px solid var(--border-color)"}},
          series.map(([field, , color]) => create("span", {key: field,
            style: {height: (sum(hour, field) / max * 100) + "%", background: color}})));
      })),
    create("div", {className: tN.quotaAmount, style: {...quotaControls, marginTop: 6}},
      series.map(([field, label, color]) => create("span", {key: field},
        create("span", {style: {color}, "aria-hidden": true}, "■ "), label + " " + quotaNumber.format(hours.reduce((total, hour) => total + sum(hour, field), 0))))));
}

function QuotaEditor({provider, id, policy, onSaved, disabled}) {
  const create = y.createElement;
  const [saving, setSaving] = y.useState(false);
  const [notice, setNotice] = y.useState("");
  const isGLM = provider.id === "glm-coding-plan";
  async function save(event) {
    event.preventDefault();
    if (disabled || saving) return;
    const form = new FormData(event.currentTarget);
    const body = {id};
    for (const [field] of provider.fields) {
      const value = form.get(field);
      if (isGLM && value === "") {
        body[field] = null;
        continue;
      }
      const number = Number(value);
      if (number <= 0 || !Number.isFinite(number) || (isGLM && !Number.isSafeInteger(number))) {
        setNotice("请输入有效的正数额度。");
        return;
      }
      body[field] = number;
    }
    setSaving(true);
    setNotice("");
    try {
      await op.patch(quotaLocalPath(provider), body);
      await onSaved();
      setNotice("额度已保存。");
    } catch {
      setNotice("未能确认保存，请刷新后核对。");
    } finally {
      setSaving(false);
    }
  }
  return create("form", {onSubmit: save, style: quotaControls},
    provider.fields.map(([field, setting, label]) => create("label", {key: field, className: tN.quotaAmount}, label + " ",
      create("input", {key: String(policy?.[setting]), className: "input", name: field, type: "number",
        required: !isGLM, step: isGLM ? 1 : "any", min: isGLM ? 1 : 0, placeholder: isGLM ? "无限" : "",
        defaultValue: policy?.[setting] ?? "", disabled: disabled || saving, style: {width: "8em", padding: "4px 8px"}}))),
    create("button", {className: eN.actionPill, type: "submit", disabled: disabled || saving}, "保存额度"),
    notice && create("span", {role: "status", className: tN.quotaAmount}, notice));
}

function UpstreamQuotaCard({provider, state}) {
  const create = y.createElement;
  const {data, error, pending, refresh} = state;
  const balance = data?.balance;
  const plan = data?.plan;
  const title = provider.id === "deepseek" ? "DeepSeek · 账户余额" : "GLM · Coding Plan" + (plan?.level ? " · " + plan.level.toUpperCase() : "");
  return create(PanelCard, {title, icon: provider.icon, actions: create(PanelRefresh, {onClick: refresh, disabled: pending})},
    pending && !data && create(PanelNote, null, "正在读取上游账户额度。"),
    error && create(PanelNote, {error: true}, data?.configured ? "刷新失败，以下为上次获取的数据。" : "暂时无法读取上游账户额度。"),
    data && !data.configured && create(PanelNote, null, "尚未配置可查询额度的上游凭证。"),
    balance && create(PanelNote, null, balance.is_available ? "API 可用" : "API 不可用"),
    balance?.balance_infos?.map(item => create("div", {key: item.currency, className: tN.quotaRow},
      create("div", {className: tN.quotaRowHeader},
        create("span", {className: tN.quotaModel}, item.currency + " 可用余额"),
        create("span", {className: tN.quotaPercent}, item.total_balance)),
      create("div", {className: tN.quotaAmount}, "赠金 " + item.granted_balance + " · 充值余额 " + item.topped_up_balance))),
    plan && quotaWindows.map(([key, label]) => {
      const item = plan.windows?.[key];
      return create(QuotaLine, {key, label: "套餐 " + label, unit: "积分", percent: item?.remaining_percent,
        remaining: item?.remaining_credits, limit: item?.limit_credits, used: item?.used_credits, reset: item?.reset_at});
    }));
}

function localQuotaLines(provider, policy, row, usage) {
  const create = y.createElement;
  if (provider.id === "glm-coding-plan") return quotaWindows.map(([key, label], index) => {
    const limit = policy[provider.fields[index][1]];
    const item = row?.[key];
    const remaining = limit == null || item == null ? null : Math.max(0, limit - item.used_tokens);
    return create(QuotaLine, {key, label: "本地 " + label, unit: "Token", limit, remaining,
      percent: remaining == null ? null : remaining / limit * 100, unlimited: limit == null,
      used: item?.used_tokens, reset: item?.reset_at, unknown: item?.unknown_requests,
      estimatedPoints: item?.estimated_points, unpriced: item?.unpriced_requests});
  });
  const codex = provider.id === "codex";
  const limit = policy[provider.fields[0][1]];
  const used = codex ? row?.usage?.used_usd : row?.used_cny;
  const remaining = used == null ? null : Math.max(0, limit - used);
  return [
    create(QuotaLine, {key: "limit", label: codex ? "本地每周额度" : "本地累计额度", unit: codex ? "等值 USD" : "CNY",
      limit, used, remaining, percent: remaining == null ? null : remaining / limit * 100, reset: row?.usage?.reset_at}),
    !codex && usage?.active && remaining != null && remaining < usage.reservation_cny &&
      create(PanelNote, {key: "reservation", error: true}, "剩余额度低于单次预留 " + quotaAmount.format(usage.reservation_cny) + " CNY，新请求会被拦截。")
  ];
}

function quotaClientRows(provider, overview, data) {
  const field = quotaPolicyKey(provider);
  if (overview) return overview.clients.filter(client => client.quotas[field]).map(client => ({id: client.id, policy: client.quotas[field]}));
  return (data?.clients ?? []).flatMap(row => {
    const policy = provider.id === "codex" ? {"weekly-usd": row.weekly_usd} :
      provider.id === "deepseek" ? row.limit_cny > 0 && {"limit-cny": row.limit_cny} : row.policy;
    return policy ? [{id: row.id, policy}] : [];
  });
}

function LocalQuotaSection({provider, overview, usage}) {
  const create = y.createElement;
  const rows = quotaClientRows(provider, overview.data, usage.data);
  const onSaved = () => Promise.all([overview.refresh(), usage.refresh()]);
  return create(PanelSection, {title: "本地客户端额度", name: provider.name + " 本地客户端额度"},
    usage.error && create(PanelNote, {error: true}, usage.data ? "刷新失败，以下为上次获取的用量。" : "本地用量暂不可用。"),
    usage.data?.active === false && create(PanelNote, {error: true}, "额度插件未加载，当前额度不会拦截请求。"),
    !rows.length && create(PanelNote, null, overview.data || usage.data ? "没有客户端配置该供应商额度。" : "正在读取客户端额度。"),
    create("div", {className: PN.grid}, rows.map(({id, policy}) => {
      const row = usage.data?.clients?.find(item => item.id === id);
      return create(PanelCard, {key: id, title: id + " · 本地客户端", icon: provider.icon,
        actions: create(QuotaEditor, {provider, id, policy, onSaved})},
        localQuotaLines(provider, policy, row, usage.data),
        create(TokenUsageChart, {hours: row?.hours ?? row?.usage?.hours}));
    })));
}

function renderCodexQuota(entries, quotaFor, theme, canRefresh, resetting, refresh, reset) {
  const create = y.createElement;
  return create(y.Fragment, null,
    create("div", {className: PN.grid}, entries.map(entry => {
      const file = entry.file;
      return create(nN, {key: bm(file), entry, quota: quotaFor(entry), resolvedTheme: theme,
        canRefresh: canRefresh && !file.disabled, resetting: resetting === bm(file),
        onRefresh: () => refresh(file, Qk.codex), onReset: () => reset(file, Qk.codex)});
    })),
    create(bN, {entries, quotaFor, displayNameFor: LN, resolvedTheme: theme}));
}

function panelQuotaSections(tab, panel, codexEntries, native) {
  const create = y.createElement;
  return quotaProviders.map((provider, index) => {
    if (tab !== "all" && tab !== provider.tab) return null;
    const upstream = index === 0
      ? codexEntries.length ? native : create(PanelNote, null, "没有匹配的 Codex OAuth 账户。")
      : create("div", {className: PN.grid}, create(UpstreamQuotaCard, {provider, state: panel.upstream[index]}));
    return create("section", {key: provider.id, "aria-label": provider.name, style: {marginTop: 18}},
      tab === "all" && create("h2", {style: {margin: 0}}, provider.name),
      create(PanelSection, {title: "上游账户额度", name: provider.name + " 上游账户额度"}, upstream),
      create(LocalQuotaSection, {provider, overview: panel.overview, usage: panel.usage[index]}));
  });
}

function codexOAuthCredentials(overview) {
  return overview.data?.providers.find(provider => provider.id === "codex")?.credentials.filter(item => item.kind === "oauth") ?? [];
}

function CodexOAuthList({credentials}) {
  const create = y.createElement;
  if (!credentials.length) return null;
  return create("div", {style: {padding: "12px 16px"}},
    create(PanelSection, {title: "Codex OAuth 凭证", action: create(Yi, {to: "/auth-files"}, "管理 OAuth 凭证")},
      create("div", {className: PN.grid}, credentials.map((item, index) =>
        create(PanelCard, {key: item.name || index, icon: "C", title: item.name || "Codex OAuth"},
          create("p", {className: tN.quotaAmount}, (item.disabled ? "已停用" : "已启用") + " · " + item.models.length + " 个已注册模型"))))));
}

function ProviderInventory({overview}) {
  const create = y.createElement;
  return create(PanelSection, {title: "运行中的供应商与模型"},
    overview.error && create(PanelNote, {error: true}, "无法刷新供应商总览。"),
    create("div", {className: PN.grid}, quotaProviders.map(provider => {
      const credentials = overview.data?.providers.find(item => item.id === provider.id)?.credentials ?? [];
      const live = credentials.filter(item => !item.disabled);
      const models = [...new Set(live.flatMap(item => item.models))].sort();
      return create(PanelCard, {key: provider.id, icon: provider.icon, title: provider.name},
        create("p", {className: tN.quotaAmount}, overview.data ? live.length + " / " + credentials.length + " 个启用凭证 · " + models.length + " 个已注册模型" : "正在读取运行状态。"),
        credentials.map((item, index) => create("p", {key: index, className: tN.quotaAmount},
          (item.kind === "oauth" ? "OAuth" : item.kind === "api_key" ? "API Key" : "凭证") + (item.disabled ? " · 已停用" : "") + (item.name ? " · " + item.name : ""))),
        models.length > 0 && create("details", null, create("summary", null, "查看模型"),
          create("ul", null, models.map(model => create("li", {key: model}, model)))),
        credentials.some(item => item.kind === "oauth") && create(Yi, {to: "/auth-files"}, "管理 OAuth 凭证"));
    })));
}

function PanelUsageBars({rows, label, onSelect}) {
  const create = y.createElement;
  const [hovered, setHovered] = y.useState(null);
  const [focused, setFocused] = y.useState(null);
  const id = y.useId();
  const active = rows.find(row => row.id === (hovered ?? focused));
  const series = historySeries.filter(([field]) => rows.some(row => row.values[field] != null));
  const peak = rows.reduce((max, row) => Math.max(max, row.total), 1);
  const step = 10 ** Math.floor(Math.log10(peak / 4));
  const top = Math.max(4, Math.ceil(peak / (4 * step)) * 4 * step);
  const ticks = [top, top * .75, top * .5, top * .25, 0];
  const compact = new Intl.NumberFormat("zh-CN", {notation: "compact", maximumFractionDigits: 1});
  const plotHeight = 138;
  const tooltip = active && historyMetrics(active.metrics).join(" · ");
  if (!rows.length) return create(PanelNote, null, "此范围没有可用的用量明细。");
  return create("figure", {style: {margin: 0}, "aria-label": label},
      create("div", {id, role: active ? "tooltip" : undefined,
        style: {minHeight: "5em", marginBottom: 8, fontSize: "0.8rem", overflowWrap: "anywhere"}},
        active ? create(y.Fragment, null, create("strong", null, active.label), create("div", null, tooltip))
          : create("span", {className: tN.quotaAmount}, "悬停或聚焦柱子查看用量。")),
      create("div", {style: {display: "flex", gap: 8}},
        create("div", {"aria-hidden": true, style: {height: plotHeight, display: "flex", flexDirection: "column", justifyContent: "space-between", textAlign: "right", fontSize: "0.72rem", color: "var(--text-secondary)"}},
          ticks.map(tick => create("span", {key: tick}, compact.format(tick)))),
        create("div", {style: {overflowX: "auto", flex: 1}},
          create("div", {style: {display: "grid", gridTemplateColumns: "repeat(" + rows.length + ", minmax(60px, 1fr))"}},
            rows.map(row => create("button", {type: "button", key: row.id, "aria-label": row.label + "：" + historyMetrics(row.metrics).join("，"),
              "aria-describedby": active?.id === row.id ? id : undefined,
              onPointerEnter: () => setHovered(row.id), onPointerLeave: () => setHovered(null),
              onFocus: () => setFocused(row.id), onBlur: () => setFocused(null),
              onKeyDown: event => { if (event.key === "Escape") { setHovered(null); setFocused(null); } },
              onClick: () => { setFocused(row.id); onSelect?.(row.id); },
              style: {background: "transparent", color: "inherit", border: 0, padding: "0 0 4px", minWidth: 0, cursor: "pointer", font: "inherit"}},
              create("span", {style: {display: "flex", height: plotHeight, alignItems: "flex-end", justifyContent: "center",
                borderBottom: "1px solid var(--border-hover)", background: "repeating-linear-gradient(to top, transparent 0 calc(25% - 1px), var(--border-color) calc(25% - 1px) 25%)"}},
                create("span", {"aria-hidden": true, style: {display: "flex", flexDirection: "column-reverse", width: "min(52%, 44px)", height: (row.total / top * 100) + "%",
                  minHeight: row.total === 0 ? 2 : 0, background: row.total === 0 ? "var(--border-hover)" : undefined, borderRadius: "4px 4px 0 0",
                  overflow: "hidden", opacity: active && active.id !== row.id ? .55 : 1}},
                  series.map(([field, , color]) => create("span", {key: field,
                    style: {background: color, flexGrow: row.values[field] ?? 0, flexBasis: 0}})))),
              create("span", {style: {display: "block", marginTop: 6, paddingInline: 4, fontSize: "0.75rem", overflowWrap: "anywhere"}}, row.label)))))),
      create("figcaption", {className: tN.quotaAmount, style: {...quotaControls, marginTop: 10}},
        series.map(([field, label, color]) => create("span", {key: field}, create("span", {style: {color}, "aria-hidden": true}, "■ "), label))));
}
