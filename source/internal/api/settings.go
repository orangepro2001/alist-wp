package api

// settings.go W9: 配置白名单读写(FR-34)+ 服务状态(FR-36), §4 接口表:
//   GET  /api/config        → {writable:{...}, readonly:{...}}
//   POST /api/config/set    → 白名单键可写; 触碰只读键 → 403(FR-34 验收②)
//   GET  /api/status        → 版本/上游基线/运行时长/临时目录占用/磁盘剩余(FR-36)

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"baidupcs-web/internal/config"
)

// settingsAPI 配置域 handler 集合, 由 server 装配。
type settingsAPI struct {
	cfg     *config.Config
	version string
	startAt time.Time
	tmpDir  string
}

// NewSettings 构造配置域 handlers。
func NewSettings(cfg *config.Config, version string, startAt time.Time, tmpDir string) *settingsAPI {
	return &settingsAPI{cfg: cfg, version: version, startAt: startAt, tmpDir: tmpDir}
}

// writableKeys 可写键白名单(§0.5 FR-34); 仅这些键允许经 POST /api/config/set 修改。
var writableKeys = map[string]bool{
	"max_download_rate":   true,
	"max_upload_rate":     true,
	"max_parallel":        true,
	"max_download_load":   true,
	"max_upload_load":     true,
	"max_upload_parallel": true,
	"u_policy":            true,
	"tmp_dir":             true,
	"user_agent":          true,
}

// readonlyKeys 只读键(风控敏感, 改动即 403 风险, §0.5)。
var readonlyKeys = map[string]bool{
	"appid":         true,
	"pcs_ua":        true,
	"pan_ua":        true,
	"pcs_addr":      true,
	"fix_pcs_addr":  true,
	"enable_https":  true,
	"proxy":         true,
	"local_addrs":   true,
	"download_mode": true,
	"savedir":       true,
}

// intOf 取配置整型字段(白名单键 → 值)。
func (s *settingsAPI) intOf(key string) int64 {
	switch key {
	case "max_download_rate":
		return s.cfg.MaxDownloadRate
	case "max_upload_rate":
		return s.cfg.MaxUploadRate
	case "max_parallel":
		return int64(s.cfg.MaxParallel)
	case "max_download_load":
		return int64(s.cfg.MaxDownloadLoad)
	case "max_upload_load":
		return int64(s.cfg.MaxUploadLoad)
	case "max_upload_parallel":
		return int64(s.cfg.MaxUploadParallel)
	}
	return 0
}

// strOf 取配置字符串字段(白名单键 → 值)。
func (s *settingsAPI) strOf(key string) string {
	switch key {
	case "u_policy":
		return s.cfg.UPolicy
	case "tmp_dir":
		return s.cfg.TmpDir
	case "user_agent":
		return s.cfg.UserAgent
	}
	return ""
}

// writableBody GET /api/config 的 writable 区。
func (s *settingsAPI) writableBody() map[string]interface{} {
	m := map[string]interface{}{}
	for k := range writableKeys {
		if v := s.intOf(k); v != 0 || k == "max_download_rate" || k == "max_upload_rate" ||
			k == "max_parallel" || k == "max_download_load" || k == "max_upload_load" || k == "max_upload_parallel" {
			m[k] = v
			continue
		}
		m[k] = s.strOf(k)
	}
	return m
}

// readonlyBody GET /api/config 的 readonly 区(风控敏感项, 仅展示)。
func (s *settingsAPI) readonlyBody() map[string]interface{} {
	return map[string]interface{}{
		"appid":         266719,
		"pcs_ua":        "(跟随上游 v4.0.2)",
		"pan_ua":        "(跟随上游 v4.0.2)",
		"pcs_addr":      "(默认官方地址)",
		"fix_pcs_addr":  false,
		"enable_https":  true,
		"proxy":         "",
		"local_addrs":   "",
		"download_mode": "locate",
		"savedir":       "(Web 下载不落盘)",
	}
}

// HandleConfig GET /api/config: 返回 {writable, readonly}(§5.6)。
func (s *settingsAPI) HandleConfig(w http.ResponseWriter, _ *http.Request) {
	writeOK(w, map[string]interface{}{
		"writable": s.writableBody(),
		"readonly": s.readonlyBody(),
	})
}

// configSetRequest POST /api/config/set 请求体: {"key":"...","value":...}。
type configSetRequest struct {
	Key   string      `json:"key"`
	Value interface{} `json:"value"`
}

// HandleConfigSet POST /api/config/set: 白名单键写回 config.json(0600 原子写);
// 只读键 → 403(FR-34 验收②); 未知键 → 400。生效语义: 保存即写入, 后续任务生效(§5.6)。
func (s *settingsAPI) HandleConfigSet(w http.ResponseWriter, r *http.Request) {
	var req configSetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体须为 {\"key\":\"...\",\"value\":...}")
		return
	}
	key := strings.TrimSpace(req.Key)
	switch {
	case readonlyKeys[key]:
		writeErr(w, http.StatusForbidden, "INTERNAL", "该配置项为风控敏感只读项, 不允许修改(改动会导致百度接口 403)")
		return
	case !writableKeys[key]:
		writeErr(w, http.StatusBadRequest, "INTERNAL", "不支持的配置键: "+key)
		return
	}
	if err := s.applySet(key, req.Value); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", err.Error())
		return
	}
	if err := s.cfg.Save(); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "保存配置失败: "+err.Error())
		return
	}
	writeOK(w, map[string]interface{}{"ok": true, "key": key})
}

// applySet 校验并应用单键修改(类型/取值范围把守)。
func (s *settingsAPI) applySet(key string, value interface{}) error {
	if iv, ok := wantInt(key); ok {
		n, err := toInt64(value)
		if err != nil {
			return err
		}
		if n < 0 {
			return errBadValue(key, "不可为负数")
		}
		if iv == "rate" && n > 0 && n < 1024 {
			return errBadValue(key, "限速单位为 B/s, 过小值视为非法(0=不限速)")
		}
		switch iv {
		case "rate":
			if key == "max_download_rate" {
				s.cfg.MaxDownloadRate = n
			} else {
				s.cfg.MaxUploadRate = n
			}
		case "parallel":
			if n == 0 || n > 16 {
				return errBadValue(key, "并发数须在 1~16")
			}
			switch key {
			case "max_parallel":
				s.cfg.MaxParallel = int(n)
			case "max_download_load":
				s.cfg.MaxDownloadLoad = int(n)
			case "max_upload_load":
				s.cfg.MaxUploadLoad = int(n)
			case "max_upload_parallel":
				s.cfg.MaxUploadParallel = int(n)
			}
		}
		return nil
	}
	sv, ok := value.(string)
	if !ok {
		return errBadValue(key, "须为字符串")
	}
	sv = strings.TrimSpace(sv)
	switch key {
	case "u_policy":
		switch sv {
		case "skip", "overwrite", "rsync":
			s.cfg.UPolicy = sv
		default:
			return errBadValue(key, "仅支持 skip|overwrite|rsync")
		}
	case "tmp_dir":
		if sv == "" {
			return errBadValue(key, "临时目录不可为空")
		}
		if !filepath.IsAbs(sv) {
			return errBadValue(key, "临时目录须为绝对路径")
		}
		s.cfg.TmpDir = sv
	case "user_agent":
		s.cfg.UserAgent = sv
	}
	return nil
}

// wantInt 报告键是否整型及其语义类别(rate/parallel)。
func wantInt(key string) (string, bool) {
	switch key {
	case "max_download_rate", "max_upload_rate":
		return "rate", true
	case "max_parallel", "max_download_load", "max_upload_load", "max_upload_parallel":
		return "parallel", true
	}
	return "", false
}

// toInt64 JSON value → int64(接受 number 或数字字符串)。
func toInt64(v interface{}) (int64, error) {
	switch x := v.(type) {
	case float64:
		return int64(x), nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0, errBadValue(x, "须为整数")
		}
		return n, nil
	default:
		return 0, errBadValue("", "须为整数")
	}
}

func errBadValue(key, msg string) error {
	if key == "" {
		return &errString{"值非法: " + msg}
	}
	return &errString{"配置 " + key + " 非法: " + msg}
}

type errString struct{ s string }

func (e *errString) Error() string { return e.s }

// HandleStatus GET /api/status: 版本/上游基线/运行时长/临时目录占用/磁盘剩余(FR-36)。
func (s *settingsAPI) HandleStatus(w http.ResponseWriter, _ *http.Request) {
	usage, uerr := dirUsage(s.tmpDir)
	free, ferr := diskFree(s.tmpDir)
	writeOK(w, map[string]interface{}{
		"version":       s.version,
		"upstream":      "BaiduPCS-Go v4.0.2",
		"uptime_second": int64(time.Since(s.startAt).Seconds()),
		"tmp_dir":       s.tmpDir,
		"tmp_usage":     usage,
		"tmp_usage_err": errText(uerr),
		"disk_free":     free,
		"disk_free_err": errText(ferr),
		"start_at":      s.startAt.Unix(),
	})
}

func errText(err error) string {
	if err != nil {
		return err.Error()
	}
	return ""
}

// dirUsage 统计目录总占用字节(du 语义; 目录不存在计 0)。
func dirUsage(dir string) (int64, error) {
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 无权限/已消失的条目跳过, 不中断统计
		}
		if info != nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return total, err
	}
	return total, nil
}

// diskFree 目录所在磁盘剩余字节。
func diskFree(dir string) (int64, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	var st syscallStatfs
	if err := statfs(dir, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
