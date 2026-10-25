package api

// tasks.go W5: /api/tasks 任务列表与操作(FR-18~20, §4)。
// GET /api/tasks 全量任务行; POST /api/tasks/{id}/cancel|retry|purge。

import (
	"net/http"

	"baidupcs-web/internal/xfer"
)

// tasksAPI 任务域 handler。
type tasksAPI struct {
	m xfer.Manager
}

// NewTasks 构造任务域 handlers。
func NewTasks(m xfer.Manager) *tasksAPI { return &tasksAPI{m: m} }

// taskView 任务行响应视图(进度字段并入)。
type taskView struct {
	xfer.TaskRow
	Percent float64 `json:"percent"`
}

// HandleList GET /api/tasks: 全量任务行(queued 排队可见)。
func (t *tasksAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	type rowLister interface{ Rows() []xfer.TaskRow }
	rows := []xfer.TaskRow{}
	if rl, ok := t.m.(rowLister); ok {
		rows = rl.Rows()
	}
	out := make([]taskView, 0, len(rows))
	for _, row := range rows {
		out = append(out, taskView{
			TaskRow: row,
			Percent: percentOf(row.Done, row.Total),
		})
	}
	writeOK(w, map[string]interface{}{"total": len(out), "list": out})
}

// percentOf 本地百分比(避免依赖 xfer 私有函数)。
func percentOf(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total) * 100
	if p > 100 {
		p = 100
	}
	return p
}

// HandleCancel POST /api/tasks/{id}/cancel。
func (t *tasksAPI) HandleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := t.m.Cancel(id); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// HandleRetry POST /api/tasks/{id}/retry。
func (t *tasksAPI) HandleRetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := t.m.Retry(id); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// HandlePurge POST /api/tasks/{id}/purge。
func (t *tasksAPI) HandlePurge(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := t.m.Purge(id); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}
