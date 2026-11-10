package api

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"os"

	"github.com/gin-gonic/gin"
)

//go:embed client_quota_panel.js
var clientQuotaPanelJS []byte

//go:embed usage_history_panel.js
var usageHistoryPanelJS []byte

//go:embed client_management_panel.js
var clientManagementPanelJS []byte

func serveClientQuotaPanel(c *gin.Context, path string) bool {
	panel, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	// shortcut: The quota adapter targets the pinned CPAMC v1.24.2 bundle; update the adapter before upgrading the panel.
	const panelSHA256 = "51b24db8170a5414875602c2f4e35017185b1c1dd7e9a6b1c0de69905a81e997"
	if fmt.Sprintf("%x", sha256.Sum256(panel)) != panelSHA256 {
		return false
	}
	patches := []struct{ marker, replacement string }{
		{"function Dee(){", string(clientQuotaPanelJS) + "\n" + string(usageHistoryPanelJS) + "\n" + string(clientManagementPanelJS) + "\n" + "function Dee(){let panel=useQuotaPanel(Om(e=>e.connectionStatus)===`connected`);"},
		{"&&ON.has(e)", "&&(ON.has(e)||quotaProviderTabs.has(e))"},
		{"types:FN,counts:I", "types:[`all`,`codex`,`DeepSeek`,`GLM`,...FN.filter(type=>type!==`all`&&type!==`codex`)],counts:panelQuotaCounts(I,panel)"},
		{"className:JA.tabIconWrap,style:KD(e)?", "className:JA.tabIconWrap,\"aria-hidden\":!0,style:KD(e)?"},
		{"JM(qM(F,l),h)", "JM(qM(F,l).filter(e=>l!==`all`||e.type!==`codex`),h)"},
		{"ng(T),(0,y.useEffect)(()=>(T(),", "(0,y.useEffect)(()=>(T(),"},
		{"b||(he.current=x,T())},[b,T,x]);(0,y.useEffect)", "b||(he.current=x,T(),panel.refresh())},[b,T,x,panel.refresh]);ng(_e,!b);(0,y.useEffect)"},
		{"WM(t,x,S,!!s,b)&&de(z))}},[b,s,S,a,de,z,x])", "WM(t,x,S,!!s,b)&&de(F.filter(entry=>!entry.file.disabled)))}},[b,s,S,a,de,F,x])"},
		{"totalCount:F.length,loadedCount:le,attentionCount:B,refreshing:a||ue", "totalCount:panelQuotaCounts(I,panel).all,loadedCount:le+panelQuotaReady(panel),attentionCount:B,refreshing:a||ue||panel.upstream.slice(1).some(item=>item.pending)"},
		{"s(`quota_management.refresh_all_credentials`)]})", "`刷新全部额度`]})"},
		{"l(`auth_files.quota_refresh_single`)]})", "(t.type===`codex`?`刷新额度`:l(`auth_files.quota_refresh_single`))]})"},
		{"(0,H.jsxs)(`div`,{className:PN.toolbar", "...panelQuotaSections(l,panel,F.filter(entry=>entry.type===`codex`),renderCodexQuota(F.filter(entry=>entry.type===`codex`),ee,n,ve,fe,pe,me)),...(!quotaProviderTabs.has(l)&&(l!==`all`||F.some(entry=>entry.type!==`codex`))?[(0,H.jsxs)(`div`,{className:PN.toolbar"},
		{"(0,H.jsx)(bN,{entries:z,quotaFor:ee,displayNameFor:LN,resolvedTheme:n})]})]})}", "(0,H.jsx)(bN,{entries:z,quotaFor:ee,displayNameFor:LN,resolvedTheme:n})]:[])]})]})}"},
		{"function vD({fixedBrand:e}={}){", "function vD({fixedBrand:e}={}){let providerOverview=usePanelRead(panelOverviewPath,Om(state=>state.connectionStatus)===`connected`),codexOAuth=e?[]:codexOAuthCredentials(providerOverview);"},
		{"function CS({groups:e,activeBrand:t,onSelect:n}){", "function CS({groups:e,activeBrand:t,onSelect:n,codexOAuth=[]}){"},
		{"let i=e.id===t,a=e.resources.length,o=e.resources.filter(e=>!e.disabled).length,s=yS[e.id]", "let i=e.id===t,a=e.resources.length+(e.id===`codex`?codexOAuth.length:0),o=e.resources.filter(e=>!e.disabled).length+(e.id===`codex`?codexOAuth.filter(item=>!item.disabled).length:0),s=yS[e.id]"},
		{"totalActive:F,totalResources:P,providerFamilies:I,updatedAtLabel:R", "totalActive:F+codexOAuth.filter(item=>!item.disabled).length,totalResources:P+codexOAuth.length,providerFamilies:I+Number(codexOAuth.length>0&&!S.some(group=>group.id===`codex`&&group.resources.length)),updatedAtLabel:R"},
		{"groups:S,activeBrand:k.id,onSelect:e=>", "groups:S,activeBrand:k.id,codexOAuth,onSelect:e=>"},
		{"function Mw({group:e,filter:t,onFilterChange:n,filteredResources:r,selectedId:i,disableMutations:a,usageByProvider:o,toolbarControls:s,onView:c,onEdit:l,onDelete:u,onToggleDisabled:d,onCreate:f})", "function Mw({group:e,filter:t,onFilterChange:n,filteredResources:r,selectedId:i,disableMutations:a,usageByProvider:o,toolbarControls:s,onView:c,onEdit:l,onDelete:u,onToggleDisabled:d,onCreate:f,codexOAuth=[]})"},
		{"S=p(v?`providersPage.sponsor.emptyRegisterHint`:`providersPage.table.empty`),C=[jw.logo", "S=e.id===`codex`&&!_?`尚未配置 Codex 上游 API Key。`:p(v?`providersPage.sponsor.emptyRegisterHint`:`providersPage.table.empty`),C=[jw.logo"},
		{"className:jw.panel,children:[(0,H.jsxs)(`div`,{className:jw.header", "className:jw.panel,children:[e.id===`codex`&&(0,H.jsx)(CodexOAuthList,{credentials:codexOAuth}),(0,H.jsxs)(`div`,{className:jw.header"},
		{"(0,H.jsx)(Mw,{group:k,filter:E,onFilterChange:", "(0,H.jsx)(Mw,{group:k,codexOAuth,filter:E,onFilterChange:"},
		{"await Promise.allSettled([c.refetch(),h().catch(()=>void 0)])},[h,c])", "await Promise.allSettled([c.refetch(),h().catch(()=>void 0),providerOverview.refresh()])},[h,c,providerOverview.refresh])"},
		{"onNew:z}),re,(0,H.jsxs)(`div`,{className:`${pD.layout}", "onNew:z}),re,!e&&(0,H.jsx)(ProviderInventory,{overview:providerOverview}),!e&&(0,H.jsx)(`h3`,{children:`上游 API Key 配置`}),(0,H.jsxs)(`div`,{className:`${pD.layout}"},
		{"function Lb(){let e=Om(e=>e.connectionStatus)", "function Lb(){let providerOverview=usePanelRead(panelOverviewPath,Om(e=>e.connectionStatus)===`connected`);let e=Om(e=>e.connectionStatus)"},
		{"h=(0,y.useCallback)(async()=>{if(!(!c||!t))try{let e=await l();await s(t,e[0])}catch{}},[c,t,l,s])", "h=providerOverview.refresh"},
		{"managementKeys:n?n.apiKeys?.length??0:null,providerKeys:_?Object.values(_).reduce((e,t)=>e+t,0):null,credentials:f?f.length:null,models:a||o?null:i.length}),[n,_,f,i.length,a,o])", "managementKeys:providerOverview.data?providerOverview.data.clients.length+providerOverview.data.legacy_api_key_count:null,providerKeys:_?Object.values(_).reduce((e,t)=>e+t,0):null,credentials:f?f.length:null,models:providerOverview.data?.model_count??null}),[_,f,providerOverview.data])"},
		{"label:e(`dashboard.runtime_management_keys`),value:s.managementKeys", "label:`客户端 API Key`,value:s.managementKeys"},
		{"(0,H.jsxs)(`section`,{className:K.section,ref:m,children:", "(0,H.jsx)(ProviderUsageOverview,{}),(0,H.jsxs)(`section`,{className:K.section,ref:m,children:"},
		{"{path:`/quota`,element:(0,H.jsx)(Dee,{})}", "{path:`/quota`,element:(0,H.jsx)(Dee,{})},{path:`/usage-history`,element:(0,H.jsx)(PanelHistoryPage,{})},{path:`/clients`,element:(0,H.jsx)(PanelClientsPage,{})}"},
		{"items:[{path:`/quota`,labelKey:`nav.quota_management`", "items:[{path:`/usage-history`,label:`用量历史`,icon:K9.quota},{path:`/quota`,labelKey:`nav.quota_management`"},
		{"items:[{path:`/config`,labelKey:`nav.config_management`", "items:[{path:`/clients`,label:`客户端`,icon:K9.authFiles},{path:`/config`,labelKey:`nav.config_management`"},
		{"(0,H.jsxs)(`div`,{className:E9.toolbar,\"data-reveal\":!0,children:[x===`visual`", "(0,H.jsxs)(PanelAdvancedSettings,{field:r,children:[(0,H.jsxs)(`div`,{className:E9.toolbar,\"data-reveal\":!0,children:[x===`visual`"},
		{"theme:c,editable:!A&&!O.loading}),(0,H.jsx)(c1,", "theme:c,editable:!A&&!O.loading})]}),(0,H.jsx)(c1,"},
	}
	for _, patch := range patches {
		marker := []byte(patch.marker)
		if bytes.Count(panel, marker) != 1 {
			return false
		}
		panel = bytes.Replace(panel, marker, []byte(patch.replacement), 1)
	}
	c.Header("Cache-Control", "no-store")
	c.Data(200, "text/html; charset=utf-8", panel)
	return true
}
