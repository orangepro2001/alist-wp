// smoke_upload_srv.go — W6 上传冒烟辅助(仅本地测试用): 起真实 server 装配 + 桩 Engine。
// 桩 Engine 上传行为(注入函数, 不覆写真实引擎方法):
//   - Upload: 若 savePath 含 "rapid" 且内容校验命中(文件名含 .rapid.)→ 秒传命中; 否则模拟分片耗时后成功
//   - 其余方法返回未实现(上传冒烟只触达上传域)
// 输出每次 Upload 调用行 "STUB_UPLOAD <savePath> rapid=<bool>" 到 stdout 供脚本断言。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"baidupcs-web/internal/config"
	"baidupcs-web/internal/server"
	"baidupcs-web/internal/xengine"
)

// stubEngine 上传冒烟桩(实现 xengine.Engine 的上传面; 其余未实现)。
type stubEngine struct {
	delay time.Duration
}

func (s *stubEngine) Upload(_ context.Context, param xengine.UploadParam) (xengine.UploadResult, error) {
	time.Sleep(s.delay)
	rapid := strings.Contains(param.SavePath, "rapid")
	fmt.Printf("STUB_UPLOAD %s rapid=%v\n", param.SavePath, rapid)
	return xengine.UploadResult{RapidUpload: rapid, SavePath: param.SavePath}, nil
}

func (s *stubEngine) Quota(context.Context) (int64, int64, error) { return 100 << 30, 10 << 30, nil }

func errNotImpl() error { return errors.New("冒烟桩未实现该方法") }

func (s *stubEngine) List(context.Context, string, int, int, string, string) (xengine.FileDirectoryList, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) Meta(_ context.Context, p string) (*xengine.FileDirectory, error) {
	return &xengine.FileDirectory{Path: p, Filename: strings.TrimPrefix(p, "/")}, nil
}
func (s *stubEngine) Search(context.Context, string, string, bool) (xengine.FileDirectoryList, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) Mkdir(context.Context, string) error                      { return errNotImpl() }
func (s *stubEngine) Remove(context.Context, []string) error                   { return errNotImpl() }
func (s *stubEngine) Copy(context.Context, []string, string) error             { return errNotImpl() }
func (s *stubEngine) Move(context.Context, []string, string) error             { return errNotImpl() }
func (s *stubEngine) Rename(context.Context, string, string) error             { return errNotImpl() }
func (s *stubEngine) Tree(context.Context, string, int) (xengine.FileDirectoryList, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) Match(context.Context, string) ([]string, error) { return nil, errNotImpl() }
func (s *stubEngine) ResolveDlink(context.Context, string) (*xengine.DownloadLink, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) Export(context.Context, string) (*xengine.RapidLink, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) Accounts() []xengine.Account {
	return []xengine.Account{{UID: 7, Name: "smoke-stub"}}
}
func (s *stubEngine) ActiveAccount() (xengine.Account, bool) {
	return xengine.Account{UID: 7, Name: "smoke-stub"}, true
}
func (s *stubEngine) AddAccountByCookie(string) (*xengine.Account, error)  { return nil, errNotImpl() }
func (s *stubEngine) AddAccountByBDUSS(string, string) (*xengine.Account, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) SwitchAccount(uint64) error                    { return nil }
func (s *stubEngine) RemoveAccount(uint64) error                    { return errNotImpl() }
func (s *stubEngine) ShareSet(context.Context, []string, string, int) (*xengine.Shared, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) ShareList(context.Context, int) (xengine.ShareRecordList, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) ShareCancel(context.Context, []int64) error { return errNotImpl() }
func (s *stubEngine) ParseShareLink(string) (string, string, error) {
	return "", "", errNotImpl()
}
func (s *stubEngine) SharePreview(context.Context, string, string) (xengine.SharePreviewResult, error) {
	return xengine.SharePreviewResult{}, errNotImpl()
}
func (s *stubEngine) Transfer(context.Context, string, string, string, []string) error {
	return errNotImpl()
}
func (s *stubEngine) TransferToTmp(context.Context, string, string, []string) (*xengine.TmpTransfer, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) OfflineAdd(context.Context, []string, string) ([]xengine.OfflineTask, []error) {
	return nil, []error{errNotImpl()}
}
func (s *stubEngine) OfflineList(context.Context) (xengine.OfflineTaskList, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) OfflineQuery(context.Context, []int64) (xengine.OfflineTaskList, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) OfflineCancel(context.Context, int64) error  { return errNotImpl() }
func (s *stubEngine) OfflineDelete(context.Context, int64, bool) error { return errNotImpl() }
func (s *stubEngine) RecycleList(context.Context, int) (xengine.RecycleInfoList, error) {
	return nil, errNotImpl()
}
func (s *stubEngine) RecycleRestore(context.Context, []int64) error { return errNotImpl() }
func (s *stubEngine) RecycleDelete(context.Context, []int64, bool) error { return errNotImpl() }

func main() {
	listen := flag.String("listen", "127.0.0.1:18331", "监听地址")
	dataDir := flag.String("data", "", "数据目录")
	tmpDir := flag.String("tmp", "", "上传临时目录(冒烟断言用, 脚本传入)")
	_ = flag.String("password", "", "已废弃: 口令鉴权已移除, 仅为兼容旧脚本参数")
	delayMs := flag.Int("delay-ms", 0, "桩 Upload 耗时(毫秒)")
	flag.Parse()

	cfg, err := config.Load(config.Options{
		Listen: *listen, DataDir: *dataDir, TmpDir: *tmpDir,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	srv, err := server.NewWithEngine(server.Options{Config: cfg, Version: "smoke-w6"}, &stubEngine{
		delay: time.Duration(*delayMs) * time.Millisecond,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
