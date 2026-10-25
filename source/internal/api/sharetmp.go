// sharetmp.go W8: FR-40 直连下载 REST(§4)。
//   POST /api/share/preview {link, passcode} → 浏览分享内容(列表+share_id+job_id)
//   POST /api/share/dl       {job_id, fs_ids} → 启动直连下载作业(临时转存+流式回传)
//   GET  /api/share/dl?job_id= → 作业状态列表(Phase 展示"短暂占用配额"提示用)
//   POST /api/share/dl/cancel {job_id} → 取消作业(终态后仍删临时目录)
// 错误语义: 提取码错/禁止转存/配额不足均透传编排器中文文案(§4 错误体)。

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"baidupcs-web/internal/sharetmp"
)

// sharetmpAPI FR-40 handler。
type sharetmpAPI struct {
	orc sharetmp.Orchestrator
}

// NewShareTmp 构造 FR-40 handlers。
func NewShareTmp(orc sharetmp.Orchestrator) *sharetmpAPI { return &sharetmpAPI{orc: orc} }

// previewItem 响应视图(补 JSON 标签)。
type previewItem struct {
	FsID     string `json:"fs_id"`
	Filename string `json:"filename"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Isdir    bool   `json:"isdir"`
}

// HandlePreview POST /api/share/preview。
func (s *sharetmpAPI) HandlePreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Link     string `json:"link"`
		Passcode string `json:"passcode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Link) == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 JSON 且 link 必填")
		return
	}
	job, err := s.orc.Preview(strings.TrimSpace(req.Link), req.Passcode)
	if err != nil {
		// 提取码错/链接失效/禁止转存: 中文文案透传(400 业务错)。
		writeErr(w, http.StatusBadRequest, "TRANSFER_FORBIDDEN", err.Error())
		return
	}
	// 勾选候选清单(非接口方法, 经可选接口取; 桩 Orchestrator 缺省时空清单)。
	type itemsGetter interface{ PreviewItems(string) []sharetmp.Item }
	items := []previewItem{}
	if g, ok := s.orc.(itemsGetter); ok {
		for _, it := range g.PreviewItems(job.ID) {
			items = append(items, previewItem{FsID: it.FsID, Filename: it.Filename, Path: it.Path, Size: it.Size})
		}
	}
	writeOK(w, map[string]interface{}{
		"job_id":   job.ID,
		"share_id": job.ShareID,
		"phase":    job.Phase,
		"items":    items,
	})
}

// HandleStartDL POST /api/share/dl: {job_id, fs_ids, items?} → 启动。
// 勾选候选由前端回传(preview 时暂存), 服务端仅按 fs_ids 过滤; items 供配额预检算需求空间。
func (s *sharetmpAPI) HandleStartDL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID string   `json:"job_id"`
		FsIDs []string `json:"fs_ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.JobID == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 JSON 且 job_id 必填")
		return
	}
	if err := s.orc.StartDownload(req.JobID, req.FsIDs); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"ok": true,
		"notice": "直连下载经临时转存实现, 会短暂占用网盘配额, 下载完成后自动删除临时目录",
	})
}

// HandleListDL GET /api/share/dl: 作业列表(Phase/错误展示)。
// B5: 每个作业附 files(path/filename, 转存后清单), 前端逐个 /api/share/dl/url 签名直下。
func (s *sharetmpAPI) HandleListDL(w http.ResponseWriter, r *http.Request) {
	jobs := s.orc.List()
	type fileView struct {
		Path     string `json:"path"`
		Filename string `json:"filename"`
	}
	type jobView struct {
		sharetmp.Job
		Notice string     `json:"notice,omitempty"`
		Files  []fileView `json:"files"`
	}
	out := make([]jobView, 0, len(jobs))
	for _, j := range jobs {
		v := jobView{Job: j, Files: []fileView{}}
		for _, f := range j.Files {
			v.Files = append(v.Files, fileView{Path: f.Path, Filename: f.Filename})
		}
		if j.Phase == sharetmp.PhaseTransferring || j.Phase == sharetmp.PhaseDownloading {
			v.Notice = "短暂占用网盘配额, 完成后自动清理"
		}
		out = append(out, v)
	}
	writeOK(w, map[string]interface{}{"total": len(out), "list": out})
}

// HandleCancelDL POST /api/share/dl/cancel。
func (s *sharetmpAPI) HandleCancelDL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.JobID == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 JSON 且 job_id 必填")
		return
	}
	if err := s.orc.Cancel(req.JobID); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}
