package api

// share.go W7: 分享/转存域 REST handlers(FR-25~28, §4 接口表)。
//   GET  /api/share/list?page        FR-26 分享列表
//   POST /api/share/set              FR-25 创建分享 {paths, password, period}
//   POST /api/share/cancel           FR-27 取消分享 {share_ids:[...]}(二次确认由前端把守)
//   POST /api/transfer               FR-28 手动转存 {link, passcode, dir?, fs_ids?}
//                                    dir 缺省="/"(FR-28: 转存到当前目录并保留文件;
//                                    编排 1:1 照抄上游 RunShareTransfer, 见 xengine)。

import (
	"encoding/json"
	"net/http"
	"strings"

	"baidupcs-web/internal/xengine"
)

// shareAPI 分享域 handler 集合, 由 server 装配。
type shareAPI struct {
	eng xengine.Engine
}

// NewShare 构造分享域 handlers。
func NewShare(eng xengine.Engine) *shareAPI { return &shareAPI{eng: eng} }

// shareSetRequest POST /api/share/set 请求体(FR-25)。
type shareSetRequest struct {
	Paths    []string `json:"paths"`
	Password string   `json:"password"` // 空=百度侧自动随机 4 位(上游 ShareOption 语义)
	Period   int      `json:"period"`   // 0=永久; 1/7/30 天(以百度接口实际允许为准)
}

// HandleShareSet POST /api/share/set FR-25。
func (s *shareAPI) HandleShareSet(w http.ResponseWriter, r *http.Request) {
	var req shareSetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"paths\":[...]}")
		return
	}
	paths, ok := parsePaths(req.Paths)
	if !ok {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "paths 须为非空的绝对路径数组")
		return
	}
	shared, err := s.eng.ShareSet(r.Context(), paths, strings.TrimSpace(req.Password), req.Period)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{
		"share_id": shared.ShareID,
		"link":     shared.Link,
		"password": shared.Pwd,
	})
}

// HandleShareList GET /api/share/list?page FR-26。
func (s *shareAPI) HandleShareList(w http.ResponseWriter, r *http.Request) {
	page, _ := pageArgs(r.URL.Query())
	if page <= 0 {
		page = 1
	}
	records, err := s.eng.ShareList(r.Context(), page)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	out := make([]map[string]interface{}, 0, len(records))
	for _, rec := range records {
		out = append(out, map[string]interface{}{
			"share_id":    rec.ShareID,
			"typical_path": rec.TypicalPath,
			"shortlink":   rec.Shortlink,
			"status":      rec.Status,
			"expire_type": rec.ExpireType, // -1=已过期
			"expire_time": rec.ExpireTime, // 相对秒数(0=永久)
			"view_count":  rec.ViewCount,
		})
	}
	writeOK(w, map[string]interface{}{"list": out, "total": len(out)})
}

// shareCancelRequest POST /api/share/cancel 请求体(FR-27)。
type shareCancelRequest struct {
	ShareIDs []int64 `json:"share_ids"`
}

// HandleShareCancel POST /api/share/cancel FR-27(批量取消; 二次确认在前端)。
func (s *shareAPI) HandleShareCancel(w http.ResponseWriter, r *http.Request) {
	var req shareCancelRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"share_ids\":[123]}")
		return
	}
	ids := make([]int64, 0, len(req.ShareIDs))
	seen := make(map[int64]struct{}, len(req.ShareIDs))
	for _, id := range req.ShareIDs {
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "share_ids 须为非空的正整数数组")
		return
	}
	if err := s.eng.ShareCancel(r.Context(), ids); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "cancelled": ids})
}

// transferRequest POST /api/transfer 请求体(FR-28)。
type transferRequest struct {
	Link     string   `json:"link"`     // 完整链/短链/?pwd=/口令文本(引擎 ParseShareLink 归一)
	Passcode string   `json:"passcode"` // 可空: ?pwd=/口令文本已携带时自动提取
	Dir      string   `json:"dir"`      // 可空="/": FR-28 转存到当前目录
	FsIDs    []string `json:"fs_ids"`   // 可空=全量
}

// HandleTransfer POST /api/transfer FR-28: 手动转存(目标目录保留文件;
// 8 步编排 1:1 照抄上游 RunShareTransfer, 不自创顺序, 实现见 xengine)。
func (s *shareAPI) HandleTransfer(w http.ResponseWriter, r *http.Request) {
	var req transferRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil || strings.TrimSpace(req.Link) == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"link\":\"...\",\"passcode\":\"...\"}")
		return
	}
	dir := strings.TrimSpace(req.Dir)
	if dir == "" {
		dir = "/"
	}
	if !strings.HasPrefix(dir, "/") {
		dir = "/" + dir
	}
	link, passcode, err := s.eng.ParseShareLink(req.Link)
	if err != nil {
		// 解析失败属客户端输入问题(非法文本/无链接): 400 而非引擎 500 映射
		writeErr(w, http.StatusBadRequest, "INTERNAL", err.Error())
		return
	}
	if passcode == "" {
		passcode = strings.TrimSpace(req.Passcode)
	}
	if err := s.eng.Transfer(r.Context(), link, passcode, dir, req.FsIDs); err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "dir": dir})
}
