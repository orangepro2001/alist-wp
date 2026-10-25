package api

// accounts.go W4: 账号域 REST handlers(FR-01~04)。
// 响应脱敏(§4): 仅 uid/昵称, BDUSS/STOKEN/Cookie 凭证字段一律不回传浏览器。

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"baidupcs-web/internal/xengine"
)

// accountsAPI 账号域 handler 集合, 由 server 装配。
type accountsAPI struct {
	eng xengine.Engine
}

// NewAccounts 构造账号域 handlers。
func NewAccounts(eng xengine.Engine) *accountsAPI { return &accountsAPI{eng: eng} }

// accountView 脱敏后的账号视图(§4: 仅 uid/昵称)。
type accountView struct {
	UID    uint64 `json:"uid"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// HandleList GET /api/accounts FR-02: 账号列表 + 当前活跃标记。
func (a *accountsAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	list := a.eng.Accounts()
	active, hasActive := a.eng.ActiveAccount()
	out := make([]accountView, 0, len(list))
	for _, acc := range list {
		out = append(out, accountView{UID: acc.UID, Name: acc.Name, Active: hasActive && acc.UID == active.UID})
	}
	writeOK(w, map[string]interface{}{"accounts": out, "total": len(out)})
}

// HandleSwitch POST /api/accounts/{uid}/switch FR-03: 切换活跃账号。
func (a *accountsAPI) HandleSwitch(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUID(w, r.PathValue("uid"))
	if !ok {
		return
	}
	if err := a.eng.SwitchAccount(uid); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// HandleRemove DELETE /api/accounts/{uid} FR-04。
func (a *accountsAPI) HandleRemove(w http.ResponseWriter, r *http.Request) {
	uid, ok := parseUID(w, r.PathValue("uid"))
	if !ok {
		return
	}
	if err := a.eng.RemoveAccount(uid); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// parseUID 解析路径参数 uid。
func parseUID(w http.ResponseWriter, raw string) (uint64, bool) {
	uid, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil || uid == 0 {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "uid 须为正整数")
		return 0, false
	}
	return uid, true
}

// addRequest POST /api/accounts JSON 请求体(FR-01 两种方式二选一)。
type addRequest struct {
	Cookie string `json:"cookie"`
	BDUSS  string `json:"bduss"`
	STOKEN string `json:"stoken"`
}

// HandleAdd POST /api/accounts FR-01: Cookie 整串或 BDUSS+STOKEN 添加账号。
// 响应同样脱敏, 仅回 uid/昵称。
func (a *accountsAPI) HandleAdd(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"cookie\":\"...\"} 或 {\"bduss\":\"...\",\"stoken\":\"...\"}")
		return
	}
	var (
		acc *xengine.Account
		err error
	)
	switch {
	case strings.TrimSpace(req.Cookie) != "":
		acc, err = a.eng.AddAccountByCookie(req.Cookie)
	case strings.TrimSpace(req.BDUSS) != "":
		acc, err = a.eng.AddAccountByBDUSS(req.BDUSS, req.STOKEN)
	default:
		writeErr(w, http.StatusBadRequest, "INTERNAL", "cookie 与 bduss 至少提供一个")
		return
	}
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"uid": acc.UID, "name": acc.Name})
}
