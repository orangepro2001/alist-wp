// stub_engine_test.go W4: xengine.Engine 桩(文件冒烟用)与带注入钩子的装配。
// 纪律: 不用 struct 内嵌覆写; 桩为独立类型, 经 Server 注入钩子进入装配。

package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"baidupcs-web/internal/config"
	"baidupcs-web/internal/xengine"
)

// stubFilesEngine 文件域桩: 仅 List/Quota/Meta 可用。
type stubFilesEngine struct {
	quotaQuota, quotaUsed int64
}

func (s *stubFilesEngine) List(_ context.Context, dir string, _ int, _ int, _, _ string) (xengine.FileDirectoryList, error) {
	if dir != "/" {
		return nil, &xengine.Error{Code: xengine.ErrNotFound, Message: "目录不存在"}
	}
	return xengine.FileDirectoryList{
		{FsID: 1, Path: "/照片", Filename: "照片", Isdir: true, Mtime: 1700000100},
		{FsID: 2, Path: "/readme.txt", Filename: "readme.txt", Size: 2048, Mtime: 1700000200},
	}, nil
}

func (s *stubFilesEngine) Quota(context.Context) (int64, int64, error) {
	return s.quotaQuota, s.quotaUsed, nil
}

func (s *stubFilesEngine) Meta(_ context.Context, p string) (*xengine.FileDirectory, error) {
	return &xengine.FileDirectory{FsID: 2, Path: p, Filename: trimPrefixSlash(p)}, nil
}

func (s *stubFilesEngine) Search(_ context.Context, _, keyword string, _ bool) (xengine.FileDirectoryList, error) {
	return xengine.FileDirectoryList{{FsID: 9, Path: "/deep/" + keyword, Filename: keyword}}, nil
}

// trimPrefixSlash 去掉路径前导斜杠(桩内小工具)。
func trimPrefixSlash(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

func (s *stubFilesEngine) Mkdir(context.Context, string) error          { return errNotImpl }
func (s *stubFilesEngine) Remove(context.Context, []string) error       { return errNotImpl }
func (s *stubFilesEngine) Copy(context.Context, []string, string) error { return errNotImpl }
func (s *stubFilesEngine) Move(context.Context, []string, string) error { return errNotImpl }
func (s *stubFilesEngine) Rename(context.Context, string, string) error { return errNotImpl }
func (s *stubFilesEngine) Tree(context.Context, string, int) (xengine.FileDirectoryList, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) Match(context.Context, string) ([]string, error) { return nil, errNotImpl }
func (s *stubFilesEngine) ResolveDlink(context.Context, string) (*xengine.DownloadLink, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) Export(context.Context, string) (*xengine.RapidLink, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) Upload(context.Context, xengine.UploadParam) (xengine.UploadResult, error) {
	return xengine.UploadResult{}, errNotImpl
}
func (s *stubFilesEngine) Accounts() []xengine.Account { return nil }
func (s *stubFilesEngine) ActiveAccount() (xengine.Account, bool) {
	return xengine.Account{UID: 42, Name: "stub-account"}, true
}
func (s *stubFilesEngine) AddAccountByCookie(string) (*xengine.Account, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) AddAccountByBDUSS(string, string) (*xengine.Account, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) SwitchAccount(uint64) error { return nil }
func (s *stubFilesEngine) RemoveAccount(uint64) error { return nil }
func (s *stubFilesEngine) ShareSet(context.Context, []string, string, int) (*xengine.Shared, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) ShareList(context.Context, int) (xengine.ShareRecordList, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) ShareCancel(context.Context, []int64) error { return errNotImpl }
func (s *stubFilesEngine) ParseShareLink(string) (string, string, error) {
	return "", "", errNotImpl
}
func (s *stubFilesEngine) SharePreview(context.Context, string, string) (xengine.SharePreviewResult, error) {
	return xengine.SharePreviewResult{}, errNotImpl
}
func (s *stubFilesEngine) Transfer(context.Context, string, string, string, []string) error {
	return errNotImpl
}
func (s *stubFilesEngine) TransferToTmp(context.Context, string, string, []string) (*xengine.TmpTransfer, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) OfflineAdd(context.Context, []string, string) ([]xengine.OfflineTask, []error) {
	return nil, nil
}
func (s *stubFilesEngine) OfflineList(context.Context) (xengine.OfflineTaskList, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) OfflineQuery(context.Context, []int64) (xengine.OfflineTaskList, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) OfflineCancel(context.Context, int64) error       { return errNotImpl }
func (s *stubFilesEngine) OfflineDelete(context.Context, int64, bool) error { return errNotImpl }
func (s *stubFilesEngine) RecycleList(context.Context, int) (xengine.RecycleInfoList, error) {
	return nil, errNotImpl
}
func (s *stubFilesEngine) RecycleRestore(context.Context, []int64) error { return errNotImpl }
func (s *stubFilesEngine) RecycleDelete(context.Context, []int64, bool) error {
	return errNotImpl
}

var errNotImpl = errors.New("stub: 未实现")

var _ xengine.Engine = (*stubFilesEngine)(nil)

// newStubFilesEngine 构造带预置配额的桩。
func newStubFilesEngine() *stubFilesEngine {
	return &stubFilesEngine{quotaQuota: 10 * 1024 * 1024 * 1024, quotaUsed: 3 * 1024 * 1024 * 1024}
}

// newFilesSmokeServer 起装配完整的测试服务: 临时数据目录 + 注入桩 Engine。
// FilesEngine 注入钩子见 server.go(仅测试与 W4 冒烟使用)。
func newFilesSmokeServer(t *testing.T, eng xengine.Engine) *Server {
	t.Helper()
	cfg, err := config.Load(config.Options{DataDir: t.TempDir(), Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	srv, err := NewWithEngine(Options{Config: cfg, Version: "test"}, eng)
	if err != nil {
		t.Fatalf("NewWithEngine: %v", err)
	}
	return srv
}

// readAll 读取完整响应体。
func readAll(t *testing.T, r *http.Response) []byte {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("读响应体: %v", err)
	}
	return data
}
