function PanelClientsPage() {
  const create = y.createElement;
  const connected = Om(state => state.connectionStatus) === "connected";
  const clients = usePanelRead("/clients", connected);
  const [selected, setSelected] = y.useState(null);
  const [issued, setIssued] = y.useState(null);
  const [notice, setNotice] = y.useState(null);
  const [busy, setBusy] = y.useState(false);
  ng(clients.refresh, connected);
  async function mutate(run) {
    setBusy(true);
    setNotice(null);
    try {
      const result = await run();
      if (result.api_key) setIssued({id: result.client.id, key: result.api_key});
      setSelected(null);
      await clients.refresh();
      setNotice({text: "客户端配置已生效。"});
    } catch {
      await clients.refresh();
      setNotice({error: true, text: "未能确认操作生效。请核对当前客户端状态后再试。"});
    } finally {
      setBusy(false);
    }
  }
  const controls = (label, onClick, disabled = false) => create("button", {
    type: "button", className: eN.actionPill, disabled: busy || !connected || disabled, onClick
  }, label);
  return create("div", {className: E9.page},
    create("h1", null, "客户端"),
    create(PanelNote, null, "管理发给用户的 API Key、供应商权限和本地配额。停用或轮换 Key 后，客户端的历史仍会保留。"),
    notice && create(PanelNote, {error: notice.error}, notice.text),
    issued && create(PanelIssuedKey, {issued, close: () => setIssued(null)}),
    create("div", {style: quotaControls}, controls("新建客户端", () => {setSelected({}); setNotice(null);}),
      controls("刷新", clients.refresh, clients.pending), create(Yi, {to: "/config"}, "高级设置")),
    clients.error && create(PanelNote, {error: true}, "无法读取客户端。"),
    clients.pending && !clients.data && create(PanelNote, null, "正在读取客户端。"),
    selected && create(PanelClientEditor, {key: selected.id ?? "new", client: selected, disabled: busy || !connected,
      close: () => setSelected(null), save: body => mutate(() => selected.id
        ? op.patch("/clients/" + encodeURIComponent(selected.id), body)
        : op.post("/clients", body))}),
    clients.data?.clients?.length === 0 && create(PanelNote, null, "还没有受管理的客户端。"),
    create("div", {className: PN.grid}, clients.data?.clients?.map(client => {
      const path = "/clients/" + encodeURIComponent(client.id);
      const allowed = client.allowed_providers?.length ? client.allowed_providers.map(id =>
        quotaProviders.find(provider => provider.id === id)?.name ?? id).join("、") : "全部供应商";
      return create(PanelCard, {key: client.id, icon: "K", title: client.id, actions: create(y.Fragment, null,
        controls("编辑权限与配额", () => {setSelected(client); setNotice(null);}),
        controls(client.disabled ? "启用" : "停用", () => mutate(() => op.patch(path, {disabled: !client.disabled}))),
        controls("轮换 Key", () => {
          if (window.confirm("轮换后，客户端「" + client.id + "」的旧 Key 将立即失效。继续？"))
            void mutate(() => op.post(path + "/rotate", {}));
        }))},
        create("p", {className: tN.quotaAmount}, (client.disabled ? "已停用" : "已启用") + " · " + allowed),
        create("p", {className: tN.quotaAmount}, quotaProviders.map(provider => provider.name + " " +
          (client.quotas?.[quotaPolicyKey(provider)] ? "已配置额度" : "未设本地上限")).join(" · ")),
        create(Yi, {to: "/usage-history?client=" + encodeURIComponent(client.id)}, "查询此客户端历史"));
    })));
}

function PanelIssuedKey({issued, close}) {
  const create = y.createElement;
  const [copyStatus, setCopyStatus] = y.useState("");
  y.useEffect(() => setCopyStatus(""), [issued]);
  async function copy() {
    try {
      await navigator.clipboard.writeText(issued.key);
      setCopyStatus("已复制。");
    } catch {
      setCopyStatus("无法访问剪贴板，请选中 Key 并手动复制。");
    }
  }
  return create(PanelCard, {title: issued.id + " 的新 Key", icon: "K", actions: create(y.Fragment, null,
    create("button", {className: eN.actionPill, type: "button", onClick: copy}, "复制 Key"),
    create("button", {className: eN.actionPill, type: "button", onClick: close}, "已保存，关闭"))},
    create(PanelNote, null, "此 Key 只在生成或轮换时显示一次，请现在保存。"),
    create("input", {className: "input", "aria-label": "新 API Key", readOnly: true, autoComplete: "off",
      spellCheck: false, value: issued.key, onFocus: event => event.target.select(), style: {width: "100%", fontFamily: "monospace"}}),
    copyStatus && create(PanelNote, null, copyStatus));
}

function PanelClientEditor({client, disabled, close, save}) {
  const create = y.createElement;
  const [restricted, setRestricted] = y.useState(Boolean(client.allowed_providers?.length));
  const [notice, setNotice] = y.useState("");
  function submit(event) {
    event.preventDefault();
    if (disabled) return;
    const form = new FormData(event.currentTarget);
    const allowed = restricted ? form.getAll("provider") : [];
    if (restricted && !allowed.length) {
      setNotice("请至少选择一个允许的供应商，或切回全部供应商。");
      return;
    }
    const quotas = {};
    for (const provider of quotaProviders) {
      const name = quotaPolicyKey(provider);
      const policy = {};
      let set = false;
      for (const [field, yaml] of provider.fields) {
        const raw = form.get(name + ":" + field);
        const value = raw === "" ? null : Number(raw);
        if (value != null && (!Number.isFinite(value) || value <= 0 || (name === "glm" && !Number.isSafeInteger(value)))) {
          setNotice("请填写有效的正数额度。GLM Token 上限必须为整数。");
          return;
        }
        set ||= value != null;
        policy[yaml] = value;
      }
      if (set || (name === "glm" && client.quotas?.glm)) quotas[name] = policy;
    }
    setNotice("");
    void save({...(!client.id && {id: form.get("id").trim()}), allowed_providers: allowed, quotas});
  }
  return create(PanelSection, {title: client.id ? "编辑 " + client.id : "新建客户端"},
    create("form", {onSubmit: submit},
      create("fieldset", {disabled, style: {border: 0, padding: 0, margin: 0}},
        !client.id && create("label", null, "客户端 ID", create("input", {
          name: "id", className: "input", required: true, maxLength: 64, pattern: "[A-Za-z0-9][A-Za-z0-9_.\\-]{0,63}",
          title: "以字母或数字开头；仅可使用字母、数字、点、连字符或下划线", placeholder: "例如 team-a", autoFocus: true
        })),
        create("p", {className: tN.quotaAmount}, "供应商权限"),
        create("div", {style: quotaControls},
          create("label", null, create("input", {type: "radio", name: "restriction", checked: !restricted, onChange: () => setRestricted(false)}), "全部供应商"),
          create("label", null, create("input", {type: "radio", name: "restriction", checked: restricted, onChange: () => setRestricted(true)}), "仅允许所选供应商")),
        create("div", {style: {...quotaControls, margin: "8px 0", display: restricted ? "flex" : "none"}},
          quotaProviders.map(provider => create("label", {key: provider.id}, create("input", {type: "checkbox", name: "provider", value: provider.id,
            disabled: !restricted, defaultChecked: client.allowed_providers?.includes(provider.id) ?? false}), provider.name))),
        create("p", {className: tN.quotaAmount}, "本地配额 · 留空表示不限"),
        create("div", {style: {...quotaControls, alignItems: "end"}},
          quotaProviders.flatMap(provider => {
            const name = quotaPolicyKey(provider);
            return provider.fields.map(([field, yaml, label]) => create("label", {key: name + ":" + field}, provider.name + " · " + label,
              create("input", {name: name + ":" + field, type: "number", className: "input", min: name === "glm" ? 1 : 0,
                step: name === "glm" ? 1 : "any", defaultValue: client.quotas?.[name]?.[yaml] ?? "", placeholder: "不限", style: {display: "block", width: "10em"}})));
          })),
        notice && create(PanelNote, {error: true}, notice),
        create("div", {style: {...quotaControls, marginTop: 12}},
          create("button", {className: eN.actionPill, type: "submit"}, client.id ? "保存" : "创建并生成 Key"),
          create("button", {className: eN.actionPill, type: "button", onClick: close}, "取消")))));
}

function PanelAdvancedSettings({field, children}) {
  const create = y.createElement;
  const [open, setOpen] = y.useState(Boolean(field));
  y.useEffect(() => { if (field) setOpen(true); }, [field]);
  return create(y.Fragment, null,
    create(PanelSection, {title: "日常管理", action: create(Yi, {to: "/clients"}, "管理客户端与 Key")},
      create(Yi, {to: "/quota"}, "查看供应商额度"), " · ", create(Yi, {to: "/usage-history"}, "查询用量历史")),
    create("details", {open, onToggle: event => setOpen(event.currentTarget.open)},
      create("summary", null, "高级设置（服务配置与 YAML）"), open && children));
}
