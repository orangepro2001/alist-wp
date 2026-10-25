package api

// tools_w9_test.go W9: FR-35 match / FR-13 export / tool cipher 单测。
// Engine 桩注入(独立类型, 不覆写); cipher 走真实上游算法(1KB 文件加解密回环, FR-35 验收)。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"baidupcs-web/internal/xengine"
)

// stubToolsEngine 工具域桩: 仅 Match/Export 可用(其余方法由 stubToolsEmbed 兜底)。
type stubToolsEngine struct {
	stubToolsEmbed
	matchPaths []string
	matchErr   error
	rapid      *xengine.RapidLink
	exportErr  error
}

// stubToolsEmbed 兜底未实现方法(独立内嵌类型, 非 struct 覆写)。
type stubToolsEmbed struct{}

func errToolsNI() error { return errors.New("工具域桩未实现该方法") }

func (stubToolsEmbed) Accounts() []xengine.Account { return nil }
func (stubToolsEmbed) ActiveAccount() (xengine.Account, bool) {
	return xengine.Account{}, false
}
func (stubToolsEmbed) List(context.Context, string, int, int, string, string) (xengine.FileDirectoryList, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) Meta(context.Context, string) (*xengine.FileDirectory, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) Search(context.Context, string, string, bool) (xengine.FileDirectoryList, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) Quota(context.Context) (int64, int64, error) { return 0, 0, errToolsNI() }
func (stubToolsEmbed) Mkdir(context.Context, string) error                        { return errToolsNI() }
func (stubToolsEmbed) Remove(context.Context, []string) error                     { return errToolsNI() }
func (stubToolsEmbed) Copy(context.Context, []string, string) error               { return errToolsNI() }
func (stubToolsEmbed) Move(context.Context, []string, string) error               { return errToolsNI() }
func (stubToolsEmbed) Rename(context.Context, string, string) error               { return errToolsNI() }
func (stubToolsEmbed) Tree(context.Context, string, int) (xengine.FileDirectoryList, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) ResolveDlink(context.Context, string) (*xengine.DownloadLink, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) Upload(context.Context, xengine.UploadParam) (xengine.UploadResult, error) {
	return xengine.UploadResult{}, errToolsNI()
}
func (stubToolsEmbed) AddAccountByCookie(string) (*xengine.Account, error) { return nil, errToolsNI() }
func (stubToolsEmbed) AddAccountByBDUSS(string, string) (*xengine.Account, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) SwitchAccount(uint64) error  { return nil }
func (stubToolsEmbed) RemoveAccount(uint64) error  { return errToolsNI() }
func (stubToolsEmbed) ShareSet(context.Context, []string, string, int) (*xengine.Shared, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) ShareList(context.Context, int) (xengine.ShareRecordList, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) ShareCancel(context.Context, []int64) error { return errToolsNI() }
func (stubToolsEmbed) ParseShareLink(string) (string, string, error) {
	return "", "", errToolsNI()
}
func (stubToolsEmbed) SharePreview(context.Context, string, string) (xengine.SharePreviewResult, error) {
	return xengine.SharePreviewResult{}, errToolsNI()
}
func (stubToolsEmbed) Transfer(context.Context, string, string, string, []string) error {
	return errToolsNI()
}
func (stubToolsEmbed) TransferToTmp(context.Context, string, string, []string) (*xengine.TmpTransfer, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) OfflineAdd(context.Context, []string, string) ([]xengine.OfflineTask, []error) {
	return nil, []error{errToolsNI()}
}
func (stubToolsEmbed) OfflineList(context.Context) (xengine.OfflineTaskList, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) OfflineQuery(context.Context, []int64) (xengine.OfflineTaskList, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) OfflineCancel(context.Context, int64) error { return errToolsNI() }
func (stubToolsEmbed) OfflineDelete(context.Context, int64, bool) error {
	return errToolsNI()
}
func (stubToolsEmbed) RecycleList(context.Context, int) (xengine.RecycleInfoList, error) {
	return nil, errToolsNI()
}
func (stubToolsEmbed) RecycleRestore(context.Context, []int64) error { return errToolsNI() }
func (stubToolsEmbed) RecycleDelete(context.Context, []int64, bool) error {
	return errToolsNI()
}

func (s *stubToolsEngine) Match(context.Context, string) ([]string, error) {
	return s.matchPaths, s.matchErr
}
func (s *stubToolsEngine) Export(context.Context, string) (*xengine.RapidLink, error) {
	return s.rapid, s.exportErr
}

// TestMatchHandler FR-35: pattern 校验 + 列表整形。
func TestMatchHandler(t *testing.T) {
	eng := &stubToolsEngine{matchPaths: []string{"/我的资源/a.mp4", "/我的资源/b.mp4"}}
	api := NewTools(eng)

	rec := postJSONHandler(t, api.HandleMatch, "/api/match", `{"pattern":"/我的资源/*.mp4"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "a.mp4") {
		t.Fatalf("match 失败: %d %s", rec.Code, rec.Body.String())
	}
	// 缺 pattern → 400
	rec = postJSONHandler(t, api.HandleMatch, "/api/match", `{}`)
	if rec.Code != 400 {
		t.Errorf("空 pattern 应 400, got %d", rec.Code)
	}
	// 相对路径 → 400
	rec = postJSONHandler(t, api.HandleMatch, "/api/match", `{"pattern":"a/*.mp4"}`)
	if rec.Code != 400 {
		t.Errorf("相对 pattern 应 400, got %d", rec.Code)
	}
	// 空结果 → [] 非 null
	empty := NewTools(&stubToolsEngine{})
	rec = postJSONHandler(t, empty.HandleMatch, "/api/match", `{"pattern":"/x/*.y"}`)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "null") {
		t.Errorf("空结果应为 []: %s", rec.Body.String())
	}
}

// TestExportHandler FR-13: 返回秒传三元组 + 警告文案。
func TestExportHandler(t *testing.T) {
	eng := &stubToolsEngine{rapid: &xengine.RapidLink{
		ContentMD5: "aa11", SliceMD5: "bb22", Crc32: "31323334", Length: 4096, Filename: "f.bin",
	}}
	api := NewTools(eng)
	rec := postJSONHandler(t, api.HandleExport, "/api/export", `{"path":"/f.bin"}`)
	if rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"aa11", "bb22", "31323334", "4096", "勿外传"} {
		if !strings.Contains(body, want) {
			t.Errorf("export 响应缺 %s: %s", want, body)
		}
	}
	// 引擎错误映射
	fail := NewTools(&stubToolsEngine{exportErr: errors.New("路径不存在")})
	rec = postJSONHandler(t, fail.HandleExport, "/api/export", `{"path":"/x"}`)
	if rec.Code != 500 {
		t.Errorf("引擎失败应 5xx, got %d", rec.Code)
	}
}

// TestToolCipherRoundtrip FR-35 验收: 1KB 文件加密再解密内容一致(上游同款算法)。
func TestToolCipherRoundtrip(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "case1kb.bin")
	content := strings.Repeat("百度盘加密回环测试0123456789", 32) // ≈1KB
	if err := os.WriteFile(plain, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	api := NewTools(nil)

	// 非法 method/mode/key → 400
	for _, body := range []string{
		`{"mode":"crypt","method":"aes-128-ctr","key":"k","path":"` + plain + `"}`,
		`{"mode":"encrypt","method":"aes-666-xyz","key":"k","path":"` + plain + `"}`,
		`{"mode":"encrypt","method":"aes-128-ctr","key":"","path":"` + plain + `"}`,
	} {
		rec := postJSONHandler(t, api.HandleToolCipher, "/api/tool/cipher", body)
		if rec.Code != 400 {
			t.Errorf("非法请求应 400: %s → %d", body, rec.Code)
		}
	}
	// 相对路径 → 400
	rec := postJSONHandler(t, api.HandleToolCipher, "/api/tool/cipher",
		`{"mode":"encrypt","method":"aes-128-ctr","key":"0123456789abcdef","path":"rel.bin"}`)
	if rec.Code != 400 {
		t.Errorf("相对路径应 400, got %d", rec.Code)
	}

	key := "0123456789abcdef"
	// 加密 → <path>.encrypt, 原文件被上游语义移除
	rec = postJSONHandler(t, api.HandleToolCipher, "/api/tool/cipher",
		`{"mode":"encrypt","method":"aes-128-ctr","key":"`+key+`","path":"`+plain+`"}`)
	if rec.Code != 200 {
		t.Fatalf("加密失败: %d %s", rec.Code, rec.Body.String())
	}
	encPath := plain + ".encrypt"
	if _, err := os.Stat(encPath); err != nil {
		t.Fatalf("加密产物缺失: %v", err)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Errorf("上游语义: 加密后原文件应移除, err=%v", err)
	}
	// 解密 → 回到原路径
	rec = postJSONHandler(t, api.HandleToolCipher, "/api/tool/cipher",
		`{"mode":"decrypt","method":"aes-128-ctr","key":"`+key+`","path":"`+encPath+`"}`)
	if rec.Code != 200 {
		t.Fatalf("解密失败: %d %s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(plain)
	if err != nil {
		t.Fatalf("解密产物缺失: %v", err)
	}
	if string(got) != content {
		t.Errorf("回环内容不一致: len=%d/%d", len(got), len(content))
	}
	// 残留清理断言: 无 .decrypted 中间文件
	if _, err := os.Stat(plain + ".decrypted"); !os.IsNotExist(err) {
		t.Errorf("解密中间文件残留")
	}
}
