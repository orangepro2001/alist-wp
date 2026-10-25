// upload.go W6: /api/upload 三段式 REST(FR-15, §4)。
//   POST /api/upload/init    {filename, rel_path, target_dir, size, policy, no_rapid} → Session
//   POST /api/upload/{id}    分片体: multipart(file 字段, ?seq=N) 或原始 body(X-Chunk-Seq 头)
//   POST /api/upload/{id}/complete  → {task_id}(任务入 xfer 队列, 传输页 SSE 可见)
//   POST /api/upload/{id}/abort     → 清理半成品(浏览器断链兜底, 前端 beforeunload 也调)
// 水位不足 → 507 DISK_LOW; 分片乱序 → 409 UPLOAD_CHUNK_MISMATCH; 会话不存在 → 404。

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"baidupcs-web/internal/uploader"
)

// uploadAPI 上传域 handler。
type uploadAPI struct {
	rec uploader.Receiver
}

// NewUpload 构造上传域 handlers。
func NewUpload(rec uploader.Receiver) *uploadAPI { return &uploadAPI{rec: rec} }

// HandleInit POST /api/upload/init。
func (u *uploadAPI) HandleInit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filename  string `json:"filename"`
		RelPath   string `json:"rel_path"`
		TargetDir string `json:"target_dir"`
		Size      int64  `json:"size"`
		Policy    string `json:"policy"`
		NoRapid   bool   `json:"no_rapid"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || req.Filename == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 JSON 且 filename 必填")
		return
	}
	if req.TargetDir == "" {
		req.TargetDir = "/"
	}
	sess, err := u.rec.Init(req.Filename, req.RelPath, req.TargetDir, req.Size, req.Policy, req.NoRapid)
	if err != nil {
		writeUploadErr(w, err)
		return
	}
	writeOK(w, sess)
}

// HandleChunk POST /api/upload/{id}(?seq=N 或 X-Chunk-Seq 头)。
// multipart(file 字段)与原始 body 均可(§4); 断链(客户端中断)自动 Abort 清理。
func (u *uploadAPI) HandleChunk(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	seq, err := strconv.Atoi(firstNonEmptyStr(r.URL.Query().Get("seq"), r.Header.Get("X-Chunk-Seq")))
	if err != nil || seq < 0 {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "分片序号非法(须为非负整数)")
		return
	}
	var body io.Reader = r.Body
	if mr, merr := r.MultipartReader(); merr == nil {
		for {
			part, perr := mr.NextPart()
			if perr != nil {
				body = nil
				break
			}
			if part.FormName() == "file" {
				body = part
				break
			}
		}
		if body == nil {
			writeErr(w, http.StatusBadRequest, "INTERNAL", "multipart 缺少 file 字段")
			return
		}
	}
	received, err := u.rec.Chunk(id, seq, body)
	if err != nil {
		// 浏览器断链: 请求 ctx 已取消 → 清理半成品(FR-15 异常分支)。
		if r.Context().Err() != nil {
			_ = u.rec.Abort(id)
			return // 连接已断, 响应写不出去
		}
		writeUploadErr(w, err)
		return
	}
	writeOK(w, map[string]int64{"received": received})
}

// HandleComplete POST /api/upload/{id}/complete。
func (u *uploadAPI) HandleComplete(w http.ResponseWriter, r *http.Request) {
	taskID, err := u.rec.Complete(r.PathValue("id"))
	if err != nil {
		writeUploadErr(w, err)
		return
	}
	writeOK(w, map[string]string{"task_id": taskID})
}

// HandleAbort POST /api/upload/{id}/abort。
func (u *uploadAPI) HandleAbort(w http.ResponseWriter, r *http.Request) {
	if err := u.rec.Abort(r.PathValue("id")); err != nil {
		writeUploadErr(w, err)
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// writeUploadErr 上传域错误 → HTTP 状态码+机器码(§4)。
func writeUploadErr(w http.ResponseWriter, err error) {
	var wm *uploader.ErrBelowWatermark
	switch {
	case errors.As(err, &wm):
		writeErr(w, http.StatusInsufficientStorage, "DISK_LOW", err.Error())
	case errors.Is(err, uploader.ErrChunkMismatch):
		writeErr(w, http.StatusConflict, "UPLOAD_CHUNK_MISMATCH", err.Error())
	default:
		writeErr(w, http.StatusBadRequest, "INTERNAL", err.Error())
	}
}

// firstNonEmptyStr 首个非空字符串。
func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
