package api

// offline.go W7: 离线下载域 REST handlers(FR-29/30, §4 接口表)。
//   POST /api/offline/add    {urls:[...], path?}      FR-29(HTTP/磁力/电驴等, 上游 CloudDlAddTask)
//   GET  /api/offline/list                            FR-30 任务列表
//   GET  /api/offline/query?ids=1,2                   FR-30 按 ID 精确查询(≤100/次, 上游限制)
//   POST /api/offline/cancel  {task_id}               FR-30
//   POST /api/offline/delete  {task_id, all?}         FR-30(all=true 清空, 前端须输入"清空"确认)

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"baidupcs-web/internal/xengine"
)

// offlineAPI 离线域 handler 集合, 由 server 装配。
type offlineAPI struct {
	eng xengine.Engine
}

// NewOffline 构造离线域 handlers。
func NewOffline(eng xengine.Engine) *offlineAPI { return &offlineAPI{eng: eng} }

// offlineTaskView /api/offline/* 响应行(§11 W4 行语义: 名称/状态/进度)。
type offlineTaskView struct {
	TaskID       int64  `json:"task_id"`
	Status       int    `json:"status"`
	StatusText   string `json:"status_text"`
	FileSize     int64  `json:"file_size"`
	FinishedSize int64  `json:"finished_size"`
	Progress     int    `json:"progress"` // 0~100
	SavePath     string `json:"save_path"`
	SourceURL    string `json:"source_url,omitempty"`
	TaskName     string `json:"task_name"`
}

func newOfflineTaskView(t *xengine.OfflineTask) offlineTaskView {
	v := offlineTaskView{
		TaskID:       t.TaskID,
		Status:       t.Status,
		StatusText:   t.StatusText,
		FileSize:     t.FileSize,
		FinishedSize: t.FinishedSize,
		SavePath:     t.SavePath,
		SourceURL:    t.SourceURL,
		TaskName:     t.TaskName,
	}
	if v.TaskName == "" && t.SourceURL != "" {
		v.TaskName = t.SourceURL
	}
	if t.FileSize > 0 {
		v.Progress = int(t.FinishedSize * 100 / t.FileSize)
		if v.Progress > 100 {
			v.Progress = 100
		}
	}
	return v
}

// viewOf 值 → 指针便捷包装(OfflineTaskList 为指针切片, Add 返回值切片)。
func viewOf(t xengine.OfflineTask) offlineTaskView { return newOfflineTaskView(&t) }

// offlineAddRequest POST /api/offline/add 请求体(FR-29)。
type offlineAddRequest struct {
	URLs []string `json:"urls"`
	Path string   `json:"path"` // 空="/": 保存路径默认当前目录
}

// HandleAdd POST /api/offline/add FR-29: 批量添加(逐 URL 独立成败, 失败项带原因)。
func (o *offlineAPI) HandleAdd(w http.ResponseWriter, r *http.Request) {
	var req offlineAddRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"urls\":[\"magnet:?xt=...\"]}")
		return
	}
	urls := make([]string, 0, len(req.URLs))
	seen := make(map[string]struct{}, len(req.URLs))
	for _, u := range req.URLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	if len(urls) == 0 {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "urls 须为非空的资源地址数组")
		return
	}
	savePath := strings.TrimSpace(req.Path)
	if savePath == "" {
		savePath = "/"
	}
	tasks, errs := o.eng.OfflineAdd(r.Context(), urls, savePath)
	ok, failed := make([]offlineTaskView, 0, len(tasks)), make([]map[string]string, 0, len(tasks))
	for i := range tasks {
		if i < len(errs) && errs[i] != nil {
			failed = append(failed, map[string]string{"url": urls[i], "message": errs[i].Error()})
			continue
		}
		ok = append(ok, viewOf(tasks[i]))
	}
	writeOK(w, map[string]interface{}{"added": ok, "failed": failed})
}

// HandleList GET /api/offline/list FR-30。
func (o *offlineAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	list, err := o.eng.OfflineList(r.Context())
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	out := make([]offlineTaskView, 0, len(list))
	for _, t := range list {
		out = append(out, newOfflineTaskView(t))
	}
	writeOK(w, map[string]interface{}{"list": out, "total": len(out)})
}

// HandleQuery GET /api/offline/query?ids=1,2 FR-30(≤100/次)。
func (o *offlineAPI) HandleQuery(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("ids"))
	if raw == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "缺少 ids 参数(逗号分隔的任务 ID)")
		return
	}
	var ids []int64
	for _, s := range strings.Split(raw, ",") {
		id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, http.StatusBadRequest, "INTERNAL", "ids 须为逗号分隔的正整数")
			return
		}
		ids = append(ids, id)
	}
	if len(ids) > 100 {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "单次最多查询 100 个任务")
		return
	}
	list, err := o.eng.OfflineQuery(r.Context(), ids)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	out := make([]offlineTaskView, 0, len(list))
	for _, t := range list {
		out = append(out, newOfflineTaskView(t))
	}
	writeOK(w, map[string]interface{}{"list": out, "total": len(out)})
}

// offlineTaskRequest POST /api/offline/{cancel,delete} 请求体(FR-30)。
type offlineTaskRequest struct {
	TaskID int64 `json:"task_id"`
	All    bool  `json:"all"` // delete 专用: 清空全部记录(前端须输入"清空"确认)
}

// HandleCancel POST /api/offline/cancel FR-30。
func (o *offlineAPI) HandleCancel(w http.ResponseWriter, r *http.Request) {
	var req offlineTaskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.TaskID <= 0 {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"task_id\":123}")
		return
	}
	if err := o.eng.OfflineCancel(r.Context(), req.TaskID); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "task_id": req.TaskID})
}

// HandleDelete POST /api/offline/delete FR-30(all=true 清空, 需前端"清空"确认把守)。
func (o *offlineAPI) HandleDelete(w http.ResponseWriter, r *http.Request) {
	var req offlineTaskRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"task_id\":123} 或 {\"all\":true}")
		return
	}
	if !req.All && req.TaskID <= 0 {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "task_id 须为正整数(all=true 时可省略)")
		return
	}
	if err := o.eng.OfflineDelete(r.Context(), req.TaskID, req.All); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true})
}
