package api

// recycle.go W7: 回收站域 REST handlers(FR-31~33, §4 接口表)。
//   GET  /api/recycle/list?page       FR-31(上游页大小 100)
//   POST /api/recycle/restore         FR-32 {fs_ids:[...]} 批量还原(二次确认由前端把守)
//   POST /api/recycle/delete          FR-33 {fs_ids:[...]} 或 {all:true} 清空(前端须输入"清空"确认)

import (
	"encoding/json"
	"net/http"
	"strings"

	"baidupcs-web/internal/xengine"
)

// recycleAPI 回收站域 handler 集合, 由 server 装配。
type recycleAPI struct {
	eng xengine.Engine
}

// NewRecycle 构造回收站域 handlers。
func NewRecycle(eng xengine.Engine) *recycleAPI { return &recycleAPI{eng: eng} }

// recycleView /api/recycle/list 响应行(FR-31)。
type recycleView struct {
	FsID     int64  `json:"fs_id"`
	Path     string `json:"path"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	IsDir    bool   `json:"isDir"`
	LeftTime int    `json:"left_time"` // 剩余保留天数(百度侧 10 天)
	Mtime    int64  `json:"mtime"`
}

// HandleList GET /api/recycle/list?page FR-31。
func (rc *recycleAPI) HandleList(w http.ResponseWriter, r *http.Request) {
	page, _ := pageArgs(r.URL.Query())
	if page <= 0 {
		page = 1
	}
	list, err := rc.eng.RecycleList(r.Context(), page)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	out := make([]recycleView, 0, len(list))
	for _, it := range list {
		out = append(out, recycleView{
			FsID:     it.FsID,
			Path:     it.Path,
			Name:     it.Filename,
			Size:     it.Size,
			IsDir:    it.Isdir == 1,
			LeftTime: it.LeftTime,
			Mtime:    it.Mtime,
		})
	}
	writeOK(w, map[string]interface{}{"list": out, "total": len(out)})
}

// recycleRequest POST /api/recycle/{restore,delete} 请求体(FR-32/33)。
type recycleRequest struct {
	FsIDs []int64 `json:"fs_ids"`
	All   bool    `json:"all"` // delete 专用: 清空(前端须输入"清空"确认)
}

// parseFsIDs 去空/去重/去非法, 至少一条有效。
func parseFsIDs(raw []int64) ([]int64, bool) {
	out := make([]int64, 0, len(raw))
	seen := make(map[int64]struct{}, len(raw))
	for _, id := range raw {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, len(out) > 0
}

// HandleRestore POST /api/recycle/restore FR-32 批量还原。
func (rc *recycleAPI) HandleRestore(w http.ResponseWriter, r *http.Request) {
	var req recycleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		if strings.Contains(err.Error(), "too large") || strings.Contains(err.Error(), "request body too large") {
			writeErr(w, http.StatusRequestEntityTooLarge, "INTERNAL", "请求体过大")
			return
		}
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"fs_ids\":[123]}")
		return
	}
	ids, ok := parseFsIDs(req.FsIDs)
	if !ok {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "fs_ids 须为非空的正整数数组")
		return
	}
	if err := rc.eng.RecycleRestore(r.Context(), ids); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "restored": ids})
}

// HandleDelete POST /api/recycle/delete FR-33(all=true 清空, 需前端"清空"确认把守)。
func (rc *recycleAPI) HandleDelete(w http.ResponseWriter, r *http.Request) {
	var req recycleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"fs_ids\":[123]} 或 {\"all\":true}")
		return
	}
	if req.All {
		if err := rc.eng.RecycleDelete(r.Context(), nil, true); err != nil {
			writeEngineErr(w, err)
			return
		}
		writeOK(w, map[string]interface{}{"ok": true, "all": true})
		return
	}
	ids, ok := parseFsIDs(req.FsIDs)
	if !ok {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "fs_ids 须为非空的正整数数组(all=true 时可省略)")
		return
	}
	if err := rc.eng.RecycleDelete(r.Context(), ids, false); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "deleted": ids})
}
