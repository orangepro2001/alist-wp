package api

// settings_w9_test.go W9: FR-34 白名单读写/只读 403 + FR-36 状态页单测。
// 纪律: 桩注入, 不触网。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"baidupcs-web/internal/config"
)

func newSettingsForTest(t *testing.T) (*settingsAPI, *config.Config) {
	t.Helper()
	cfg, err := config.Load(config.Options{DataDir: t.TempDir(), Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return NewSettings(cfg, "w9-test", time.Now(), t.TempDir()), cfg
}

// TestConfigGetShape GET /api/config 返回 {writable, readonly} 且无凭证字段(FR-38)。
func TestConfigGetShape(t *testing.T) {
	s, _ := newSettingsForTest(t)
	rec := httptest.NewRecorder()
	s.HandleConfig(rec, httptest.NewRequest("GET", "/api/config", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		Writable map[string]json.RawMessage `json:"writable"`
		Readonly map[string]json.RawMessage `json:"readonly"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"max_download_rate", "max_upload_rate", "max_parallel", "u_policy", "tmp_dir"} {
		if _, ok := body.Writable[k]; !ok {
			t.Errorf("writable 缺 %s", k)
		}
	}
	for _, k := range []string{"appid", "pcs_ua", "pan_ua", "pcs_addr"} {
		if _, ok := body.Readonly[k]; !ok {
			t.Errorf("readonly 缺 %s", k)
		}
	}
	if strings.Contains(rec.Body.String(), "bduss") || strings.Contains(rec.Body.String(), "stoken") {
		t.Error("配置响应不应包含凭证字段")
	}
}

// TestConfigSetWhitelistAndForbidden FR-34 验收②: 只读键 403; 白名单键生效并持久化。
func TestConfigSetWhitelistAndForbidden(t *testing.T) {
	s, cfg := newSettingsForTest(t)

	// 只读键 → 403
	for _, k := range []string{"pcs_ua", "appid", "enable_https", "proxy"} {
		rec := httptest.NewRecorder()
		s.HandleConfigSet(rec, bodyRequest(t, `/api/config/set`, `{"key":"`+k+`","value":"hack"}`))
		if rec.Code != http.StatusForbidden {
			t.Errorf("只读键 %s 应 403, got %d", k, rec.Code)
		}
	}
	// 未知键 → 400
	rec := httptest.NewRecorder()
	s.HandleConfigSet(rec, bodyRequest(t, "/api/config/set", `{"key":"nope","value":1}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("未知键应 400, got %d", rec.Code)
	}
	// 白名单键 → 200 且生效
	rec = httptest.NewRecorder()
	s.HandleConfigSet(rec, bodyRequest(t, "/api/config/set", `{"key":"max_download_rate","value":2097152}`))
	if rec.Code != 200 {
		t.Fatalf("写 max_download_rate 应 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if cfg.MaxDownloadRate != 2097152 {
		t.Fatalf("MaxDownloadRate = %d, want 2097152", cfg.MaxDownloadRate)
	}
	// 字符串数字亦可
	rec = httptest.NewRecorder()
	s.HandleConfigSet(rec, bodyRequest(t, "/api/config/set", `{"key":"u_policy","value":"overwrite"}`))
	if rec.Code != 200 || cfg.UPolicy != "overwrite" {
		t.Fatalf("u_policy 写入失败: %d %s", rec.Code, rec.Body.String())
	}
	// 非法枚举 → 400
	rec = httptest.NewRecorder()
	s.HandleConfigSet(rec, bodyRequest(t, "/api/config/set", `{"key":"u_policy","value":"bogus"}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("非法 u_policy 应 400, got %d", rec.Code)
	}
	// 并发越界 → 400
	rec = httptest.NewRecorder()
	s.HandleConfigSet(rec, bodyRequest(t, "/api/config/set", `{"key":"max_parallel","value":99}`))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("并发越界应 400, got %d", rec.Code)
	}
}

// TestConfigSetPersists FR-34 验收③: 修改后重启(Load)值保持。
func TestConfigSetPersists(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(config.Options{DataDir: dir, Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewSettings(cfg, "t", time.Now(), t.TempDir())
	rec := httptest.NewRecorder()
	s.HandleConfigSet(rec, bodyRequest(t, "/api/config/set", `{"key":"max_upload_load","value":5}`))
	if rec.Code != 200 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	cfg2, err := config.Load(config.Options{DataDir: dir, Listen: ""})
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.MaxUploadLoad != 5 {
		t.Fatalf("重启后 MaxUploadLoad = %d, want 5", cfg2.MaxUploadLoad)
	}
}

// TestStatusShape FR-36: 版本/时长/临时目录占用/磁盘剩余, 数值非负。
func TestStatusShape(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "f.bin"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	s, _ := newSettingsForTest(t)
	s.tmpDir = tmp
	rec := httptest.NewRecorder()
	s.HandleStatus(rec, httptest.NewRequest("GET", "/api/status", nil))
	if rec.Code != 200 {
		t.Fatalf("code = %d", rec.Code)
	}
	var body struct {
		Version      string `json:"version"`
		Upstream     string `json:"upstream"`
		UptimeSecond int64  `json:"uptime_second"`
		TmpUsage     int64  `json:"tmp_usage"`
		DiskFree     int64  `json:"disk_free"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Version != "w9-test" || body.Upstream == "" {
		t.Errorf("版本/上游基线缺失: %+v", body)
	}
	if body.TmpUsage < 4096 {
		t.Errorf("tmp_usage = %d, want ≥4096", body.TmpUsage)
	}
	if body.DiskFree <= 0 {
		t.Errorf("disk_free = %d, want >0", body.DiskFree)
	}
	if body.UptimeSecond < 0 {
		t.Errorf("uptime_second = %d", body.UptimeSecond)
	}
}

func bodyRequest(t *testing.T, path, body string) *http.Request {
	t.Helper()
	return httptest.NewRequest("POST", path, strings.NewReader(body))
}
