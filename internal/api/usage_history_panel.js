const historyProviders = quotaProviders.map(provider => ({...provider, id: quotaPolicyKey(provider)}));
const historySeries = [
  ["cached", "缓存命中", "var(--viz-high, #16a34a)"],
  ["otherInput", "其余输入", "#4f77e8"],
  ["output", "输出", "#a061dd"]
];
const historyPresets = [["24h", "24 小时"], ["7d", "7 天"], ["30d", "30 天"], ["6m", "半年"], ["1y", "一年"], ["custom", "自定义"]];
const historyOutcomes = {succeeded: "成功", failed: "失败", rejected: "拒绝", canceled: "取消", imported: "历史回填"};
const historyTime = new Intl.DateTimeFormat("zh-CN", {year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23", timeZone: "Asia/Shanghai"});
const historyDay = 86400000;
const historyLimit = 50;
const historyValue = value => value == null ? "未上报" : quotaNumber.format(value);
const historyStamp = value => value && Number.isFinite(Date.parse(value)) ? historyTime.format(new Date(value)) : "—";
const historyTitle = id => historyProviders.find(provider => provider.id === id)?.name ?? (id || "未归属");

function historyRange(filter, now = new Date()) {
  const end = new Date(now);
  const start = new Date(now);
  const {preset, startDate, endDate} = filter;
  if (preset === "custom") {
    if (!startDate || !endDate || endDate < startDate) return null;
    start.setTime(Date.parse(startDate + "T00:00:00+08:00"));
    end.setTime(Date.parse(endDate + "T00:00:00+08:00") + historyDay);
  } else if (preset === "6m" || preset === "1y") {
    start.setTime(start.getTime() + 8 * 3600000);
    const day = start.getUTCDate();
    start.setUTCDate(1);
    start.setUTCMonth(start.getUTCMonth() - (preset === "6m" ? 6 : 12));
    const lastDay = new Date(Date.UTC(start.getUTCFullYear(), start.getUTCMonth() + 1, 0)).getUTCDate();
    start.setUTCDate(Math.min(day, lastDay));
    start.setTime(start.getTime() - 8 * 3600000);
  } else {
    start.setTime(end.getTime() - ({ "24h": 1, "7d": 7, "30d": 30 }[preset] ?? 1) * historyDay);
  }
  const days = (end - start) / historyDay;
  if (!Number.isFinite(days) || days <= 0 || days > 400) return null;
  return {from: start.toISOString(), to: end.toISOString(), bucket: days <= 1 ? "hour" : days <= 31 ? "day" : "month"};
}

function historyQuery(filter) {
  const range = historyRange(filter);
  if (!range) return null;
  const params = new URLSearchParams({from: range.from, to: range.to});
  for (const field of ["provider", "client", "model", "outcome"]) {
    if (filter[field].trim()) params.set(field, filter[field].trim());
  }
  return {params: params.toString(), ...range};
}

function historyMetrics(row, callsKnown = true) {
  return [
    !callsKnown || (!row.calls && row.imported) ? "调用次数未知" : "调用 " + quotaNumber.format(row.calls ?? 0) + " 次",
    "输入 " + historyValue(row.input_tokens),
    "输出 " + historyValue(row.output_tokens),
    "缓存命中 " + historyValue(row.cached_tokens),
    row.imported ? "回填 " + quotaNumber.format(row.imported) + " 条" : null
  ].filter(Boolean);
}

function historyBars(rows, label, identify) {
  return rows.map(metrics => {
    const cached = metrics.cached_tokens == null ? null : Math.min(metrics.cached_tokens, metrics.input_tokens ?? metrics.cached_tokens);
    const otherInput = metrics.input_tokens == null ? null : Math.max(0, metrics.input_tokens - (cached ?? 0));
    return {
      id: identify(metrics), label: label(metrics), metrics,
      values: {cached, otherInput, output: metrics.output_tokens},
      total: (cached ?? 0) + (otherInput ?? 0) + (metrics.output_tokens ?? 0)
    };
  });
}

function HistoryProviderCharts({state, providerId, onSelect, dashboard = false, from}) {
  const create = y.createElement;
  const providers = providerId ? historyProviders.filter(provider => provider.id === providerId) : historyProviders;
  if (!dashboard && state.pending && !state.data) return create(PanelNote, null, "正在查询用量。");
  if (!dashboard && state.error && !state.data) return create(PanelNote, {error: true}, "暂时无法查询用量历史。");
  const unassigned = (state.data?.clients ?? []).some(row => !row.provider);
  if (!dashboard && !providerId && unassigned) providers.push({id: "", name: "未归属", icon: "?"});
  return create(y.Fragment, null,
    state.error && create(PanelNote, {error: true}, state.data ? "刷新失败，以下为上次查询结果。" : "暂时无法查询用量历史。"),
    dashboard && (state.data?.incomplete_since || (state.data?.coverage_start && Date.parse(from) < Date.parse(state.data.coverage_start))) &&
      create(PanelNote, null, "最近 24 小时的调用记录尚未完整覆盖；柱图显示已记录的用量。"),
    create("div", {className: PN.grid}, providers.map(provider => {
      const rows = historyBars((state.data?.clients ?? []).filter(row => (row.provider ?? "") === provider.id),
        row => row.client_id || "未归属客户端", row => row.client_id).sort((a, b) => b.total - a.total || a.label.localeCompare(b.label));
      return create(PanelCard, {key: provider.id, title: provider.name, icon: provider.icon,
        actions: dashboard && create(Yi, {to: "/usage-history?provider=" + provider.id}, "查询历史")},
        !state.data ? create(PanelNote, null, state.pending ? "正在查询用量。" : "用量暂不可用。") :
          create(PanelUsageBars, {rows, label: provider.name + " 客户端用量",
            onSelect: onSelect && (client => onSelect(provider.id, client))}));
    })));
}

function ProviderUsageOverview() {
  const create = y.createElement;
  const connected = Om(state => state.connectionStatus) === "connected";
  const [range, setRange] = y.useState(() => historyRange({preset: "24h"}));
  const refresh = y.useCallback(() => setRange(historyRange({preset: "24h"})), []);
  ng(refresh, connected);
  y.useEffect(() => {
    if (!connected) return;
    const timer = window.setInterval(refresh, 60000);
    return () => window.clearInterval(timer);
  }, [connected, refresh]);
  const state = usePanelRead("/usage-history/summary?from=" + encodeURIComponent(range.from) + "&to=" + encodeURIComponent(range.to), connected);
  return create(PanelSection, {title: "客户端 Token 用量"},
    create(PanelNote, null, "最近 24 小时 · 每根柱子代表一个客户端"),
    create(HistoryProviderCharts, {state, dashboard: true, from: range.from}));
}

function PanelHistoryPage() {
  const search = dr().search;
  return y.createElement(PanelHistory, {key: search, search});
}

function PanelHistory({search}) {
  const create = y.createElement;
  const connected = Om(state => state.connectionStatus) === "connected";
  const initialProvider = new URLSearchParams(search).get("provider");
  const [draft, setDraft] = y.useState({
    preset: "24h", startDate: "", endDate: "", provider: historyProviders.some(item => item.id === initialProvider) ? initialProvider : "",
    client: new URLSearchParams(search).get("client") ?? "", model: "", outcome: ""
  });
  const [filter, setFilter] = y.useState(draft);
  const [query, setQuery] = y.useState(() => historyQuery(draft));
  const [page, setPage] = y.useState(0);
  const [notice, setNotice] = y.useState("");
  const [view, setView] = y.useState("clients");
  const clientOptions = y.useId();
  const inventory = usePanelRead(panelOverviewPath, connected);
  const summary = usePanelRead("/usage-history/summary?" + query.params + "&bucket=" + query.bucket, connected);
  const detail = usePanelRead("/usage-history/requests?" + query.params + "&limit=" + historyLimit + "&offset=" + page * historyLimit, connected);
  const refresh = y.useCallback(() => { void summary.refresh(); void detail.refresh(); }, [summary.refresh, detail.refresh]);
  ng(refresh, connected);
  const clients = [...new Set([...(inventory.data?.clients ?? []).map(client => client.id),
    ...(summary.data?.clients ?? []).map(client => client.client_id)].filter(Boolean))].sort();
  function apply(next, keepRange = false) {
    const nextQuery = historyQuery(next);
    if (!nextQuery) {
      setNotice("请选择有效的北京时间日期，范围最多 400 天。");
      return;
    }
    if (keepRange) {
      const params = new URLSearchParams(nextQuery.params);
      for (const field of ["from", "to"]) params.set(field, query[field]);
      Object.assign(nextQuery, {from: query.from, to: query.to, bucket: query.bucket, params: params.toString()});
    }
    if (nextQuery.params === query.params && page === 0) refresh();
    setNotice("");
    setPage(0);
    setFilter(next);
    setDraft(next);
    setQuery(nextQuery);
  }
  const bind = field => ({name: field, value: draft[field], onChange: event => setDraft({...draft, [field]: event.target.value})});
  const input = (field, label, props) => create("label", null, label,
    create("input", {className: "input", "aria-label": label, style: {display: "block"}, ...bind(field), ...props}));
  const select = (field, label, options) => create("label", null, label,
    create("select", {className: "input", "aria-label": label, style: {display: "block"}, ...bind(field)}, create("option", {value: ""}, "全部"),
      options.map(([value, name]) => create("option", {key: value, value}, name))));
  const button = (label, props) => create("button", {type: "button", className: eN.actionPill, ...props}, label);
  const totals = summary.data?.totals;
  const coverage = summary.data?.coverage_start ?? detail.data?.coverage_start;
  const incomplete = summary.data?.incomplete_since ?? detail.data?.incomplete_since;
  const callsKnown = !(coverage && Date.parse(query.to) <= Date.parse(coverage)) && filter.outcome !== "imported";
  const maxPage = Math.max(0, Math.ceil((detail.data?.total ?? 0) / historyLimit) - 1);
  const byTime = historyBars(summary.data?.timeline ?? [], row => query.bucket === "month"
    ? new Date(row.at).toLocaleDateString("zh-CN", {year: "numeric", month: "numeric", timeZone: "Asia/Shanghai"})
    : query.bucket === "day" ? new Date(row.at).toLocaleDateString("zh-CN", {month: "numeric", day: "numeric", timeZone: "Asia/Shanghai"})
      : quotaTime.format(new Date(row.at)), row => row.at);
  return create("div", {className: E9.page},
    create("h1", null, "用量历史"),
    create("form", {onSubmit: event => { event.preventDefault(); apply(draft); }},
      create("fieldset", {style: {border: 0, padding: 0, margin: "0 0 12px"}},
        create("legend", {className: tN.quotaAmount}, "查询时段（北京时间）"),
        create("div", {style: quotaControls}, historyPresets.map(([preset, label]) => button(label, {
          key: preset, "aria-pressed": draft.preset === preset, style: {fontWeight: draft.preset === preset ? 700 : 400},
          onClick: () => preset === "custom" ? setDraft({...draft, preset}) : apply({...draft, preset})
        }))),
        draft.preset === "custom" && create("div", {style: {...quotaControls, marginTop: 8}},
          input("startDate", "开始日期", {type: "date", required: true}), input("endDate", "结束日期（含）", {type: "date", required: true, min: draft.startDate}))),
      create("div", {style: {...quotaControls, alignItems: "end"}},
        select("provider", "供应商", historyProviders.map(item => [item.id, item.name])),
        input("client", "客户端 ID", {list: clientOptions, placeholder: "全部"}),
        create("datalist", {id: clientOptions}, clients.map(id => create("option", {key: id, value: id}))),
        input("model", "模型", {placeholder: "全部模型"}),
        select("outcome", "结果", Object.entries(historyOutcomes)),
        create("button", {type: "submit", className: eN.actionPill}, "查询")),
      notice && create(PanelNote, {error: true}, notice)),
    create(PanelNote, null, historyStamp(query.from) + " 至 " + historyStamp(query.to) + "（结束时刻不含）"),
    coverage && Date.parse(query.from) < Date.parse(coverage) && create(PanelNote, null, "完整调用记录自 " + historyStamp(coverage) + " 起。此前只显示可以回填的 Token 用量。"),
    incomplete && Date.parse(incomplete) < Date.parse(query.to) && create(PanelNote, {error: true}, "自 " + historyStamp(incomplete) + " 起曾出现记录中断；此时段的统计可能不完整。"),
    totals && create("p", {className: tN.quotaAmount}, historyMetrics(totals, callsKnown).join(" · ") +
      (!callsKnown ? "" :
        " · 成功 " + (totals.succeeded ?? 0) + " · 失败 " + (totals.failed ?? 0) +
        " · 拒绝 " + (totals.rejected ?? 0) + " · 取消 " + (totals.canceled ?? 0))),
    create(PanelSection, {title: "用量柱状图", action: create("div", {style: quotaControls},
      button("按客户端", {"aria-pressed": view === "clients", onClick: () => setView("clients")}),
      button("按时间", {"aria-pressed": view === "time", onClick: () => setView("time")}))},
      view === "time" && summary.data ? create(PanelUsageBars, {rows: byTime, label: "用量趋势"}) :
        create(HistoryProviderCharts, {state: summary, providerId: filter.provider,
          onSelect: (provider, client) => apply({...filter, provider, client}, true)})),
    create(PanelSection, {title: "调用明细"},
      detail.error && create(PanelNote, {error: true}, "无法读取明细。"),
      detail.pending && !detail.data && create(PanelNote, null, "正在读取明细。"),
      detail.data && !detail.data.rows?.length && create(PanelNote, null, "没有符合条件的记录。"),
      detail.data?.rows?.length > 0 && create(HistoryTable, {rows: detail.data.rows}),
      detail.data?.total > 0 && create("div", {style: quotaControls},
        button("上一页", {disabled: page === 0 || detail.pending, onClick: () => setPage(page - 1)}),
        create("span", {role: "status"}, "第 " + (page + 1) + " / " + (maxPage + 1) + " 页 · 共 " + quotaNumber.format(detail.data.total) + " 条"),
        button("下一页", {disabled: page >= maxPage || detail.pending, onClick: () => setPage(page + 1)}))));
}

function HistoryTable({rows}) {
  const create = y.createElement;
  const columns = ["时间（北京时间）", "客户端", "供应商", "模型", "结果", "输入", "输出", "缓存命中", "请求 ID"];
  const cellStyle = {textAlign: "left", padding: "9px 12px", borderBottom: "1px solid var(--border-color)", whiteSpace: "nowrap"};
  return create("div", {style: {overflowX: "auto", marginBottom: 12}},
    create("table", {"aria-label": "调用明细", style: {width: "100%", borderCollapse: "collapse", fontSize: "0.85rem"}},
      create("thead", null, create("tr", null, columns.map(name => create("th", {key: name, scope: "col", style: cellStyle}, name)))),
      create("tbody", null, rows.map((row, index) => create("tr", {key: (row.request_id || row.at) + ":" + index}, [
        historyStamp(row.at), row.client_id || "—", historyTitle(row.provider), row.model || "—",
        (row.usage_only ? "仅用量 · " : "") + (historyOutcomes[row.outcome] ?? row.outcome) + (row.status_code ? " · " + row.status_code : ""),
        historyValue(row.input_tokens), historyValue(row.output_tokens), historyValue(row.cached_tokens), row.request_id || "—"
      ].map((value, column) => create("td", {key: column, style: cellStyle}, value)))))));
}
