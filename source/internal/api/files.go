package api

// files.go W4: 文件域 REST handlers(FR-05~10, FR-21~24)。
// 服务层只做参数解析/校验与响应整形, 业务全部委托 xengine.Engine;
// sort/order/page 语义与 xengine.List(§0.3)一致: 非法值回落 name/asc, page ≤0 取全量。
// 响应错误体格式见设计 §4: {"code":"<机器码>","message":"<中文>"}。

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"baidupcs-web/internal/xengine"
)

// filesAPI 文件域 handler 集合, 由 server 装配。
type filesAPI struct {
	eng xengine.Engine
}

// NewFiles 构造文件域 handlers。
func NewFiles(eng xengine.Engine) *filesAPI { return &filesAPI{eng: eng} }

// pageArgs 解析公共分页参数: page(页码, 从 1 起)/pageSize(每页条数)。
// 非法或缺省返回 0(语义=全量, xengine.sliceFDList 的约定); pageSize 上限 1000 防滥用。
func pageArgs(q url.Values) (page, pageSize int) {
	page, _ = strconv.Atoi(strings.TrimSpace(q.Get("page")))
	pageSize, _ = strconv.Atoi(strings.TrimSpace(q.Get("pageSize")))
	if pageSize <= 0 {
		// 兼容 ?limit= 形式
		pageSize, _ = strconv.Atoi(strings.TrimSpace(q.Get("limit")))
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	if page <= 0 || pageSize <= 0 {
		return 0, 0 // 任一缺省 → 全量
	}
	return page, pageSize
}

// fileItem /api/files /api/search 响应行(§11 W4: 名称/大小/isDir/修改时间)。
type fileItem struct {
	FsID     int64  `json:"fs_id"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	IsDir    bool   `json:"isDir"`
	Mtime    int64  `json:"mtime"`
	Ctime    int64  `json:"ctime,omitempty"`
	MD5      string `json:"md5,omitempty"`
	Category string `json:"category,omitempty"`
}

func newFileItem(fd *xengine.FileDirectory) fileItem {
	cat := "file"
	if fd.Isdir {
		cat = "dir"
	}
	return fileItem{
		FsID:     fd.FsID,
		Path:     fd.Path,
		Name:     fd.Filename,
		Size:     fd.Size,
		IsDir:    fd.Isdir,
		Mtime:    fd.Mtime,
		Ctime:    fd.Ctime,
		MD5:      fd.MD5,
		Category: cat,
	}
}

// HandleQuota GET /api/quota FR-05: {quota, used, free}。
func (f *filesAPI) HandleQuota(w http.ResponseWriter, r *http.Request) {
	quota, used, err := f.eng.Quota(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]int64{"quota": quota, "used": used, "free": quota - used})
}

// HandleList GET /api/files?dir&page&pageSize&sort&order FR-06。
// sort( name|time|size )/order( asc|desc )非法值回落 name/asc。
func (f *filesAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	dir := strings.TrimSpace(r.URL.Query().Get("dir"))
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "缺少 dir 参数")
		return
	}
	if !strings.HasPrefix(dir, "/") {
		dir = "/" + dir
	}
	page, pageSize := pageArgs(r.URL.Query())
	fdl, err := f.eng.List(r.Context(), dir, page, pageSize,
		r.URL.Query().Get("sort"), r.URL.Query().Get("order"))
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	list := make([]fileItem, 0, len(fdl))
	for _, fd := range fdl {
		list = append(list, newFileItem(fd))
	}
	writeOK(w, map[string]interface{}{
		"dir":      dir,
		"page":     page,
		"pageSize": pageSize,
		"total":    len(list),
		"list":     list,
	})
}

// HandleMeta GET /api/meta?path FR-09: 单文件元信息。
func (f *filesAPI) HandleMeta(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSpace(r.URL.Query().Get("path"))
	if p == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "缺少 path 参数")
		return
	}
	fd, err := f.eng.Meta(r.Context(), p)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"meta": newFileItem(fd)})
}

// HandleSearch GET /api/search?dir&keyword&recursive FR-10: 递归搜索, 结果含所在目录(path)可跳转。
func (f *filesAPI) HandleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir := strings.TrimSpace(q.Get("dir"))
	if dir == "" {
		dir = "/"
	}
	keyword := strings.TrimSpace(q.Get("keyword"))
	if keyword == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "缺少 keyword 参数")
		return
	}
	recursive := q.Get("recursive") != "0" && q.Get("recursive") != "false"
	fdl, err := f.eng.Search(r.Context(), dir, keyword, recursive)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	list := make([]fileItem, 0, len(fdl))
	for _, fd := range fdl {
		it := newFileItem(fd)
		it.Ctime = 0
		list = append(list, it)
	}
	writeOK(w, map[string]interface{}{"keyword": keyword, "dir": dir, "total": len(list), "list": list})
}

// pathsRequest 写操作 JSON 请求体。
type pathsRequest struct {
	Paths  []string `json:"paths"`
	Dir    string   `json:"dir"`
	To     string   `json:"to"`
	Rename string   `json:"rename"`
}

// parsePaths 解析并校验 paths 数组(去空/去重, 至少一条)。
func parsePaths(raw []string) ([]string, bool) {
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" || !strings.HasPrefix(p, "/") {
			continue
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, len(out) > 0
}

// HandleMkdir POST /api/files/mkdir {dir} FR-21。
func (f *filesAPI) HandleMkdir(w http.ResponseWriter, r *http.Request) {
	var req pathsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Dir) == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"dir\":\"/绝对路径\"}")
		return
	}
	p := path.Clean(strings.TrimSpace(req.Dir))
	if !strings.HasPrefix(p, "/") || p == "/" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "目录路径须为 / 开头的绝对路径")
		return
	}
	if err := f.eng.Mkdir(r.Context(), p); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "path": p})
}

// HandleRemove POST /api/files/rm {paths:[...]} FR-22。
func (f *filesAPI) HandleRemove(w http.ResponseWriter, r *http.Request) {
	var req pathsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"paths\":[\"/a\",\"/b\"]}")
		return
	}
	paths, ok := parsePaths(req.Paths)
	if !ok {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "paths 须为非空的绝对路径数组")
		return
	}
	if err := f.eng.Remove(r.Context(), paths); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "removed": paths})
}

// HandleCopy POST /api/files/cp {paths:[...], to:"/目标目录"} FR-23。
func (f *filesAPI) HandleCopy(w http.ResponseWriter, r *http.Request) {
	f.handleCpMv(w, r, false)
}

// HandleMove POST /api/files/mv {paths:[...], to:"/目标目录"} FR-24。
func (f *filesAPI) HandleMove(w http.ResponseWriter, r *http.Request) {
	f.handleCpMv(w, r, true)
}

// handleCpMv cp/mv 共用编排(仅引擎调用不同)。
func (f *filesAPI) handleCpMv(w http.ResponseWriter, r *http.Request, isMove bool) {
	var req pathsRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"paths\":[...],\"to\":\"/目标目录\"}")
		return
	}
	paths, ok := parsePaths(req.Paths)
	if !ok {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "paths 须为非空的绝对路径数组")
		return
	}
	to := path.Clean(strings.TrimSpace(req.To))
	if to == "" || !strings.HasPrefix(to, "/") {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "to 须为 / 开头的目标目录绝对路径")
		return
	}
	var err error
	if isMove {
		err = f.eng.Move(r.Context(), paths, to)
	} else {
		err = f.eng.Copy(r.Context(), paths, to)
	}
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "to": to, "count": len(paths)})
}
