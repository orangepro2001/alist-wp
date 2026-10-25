// smoke_sharetmp_srv.go — W8 FR-40 直连下载冒烟辅助: 真实 server 装配 + 可编程桩 Engine。
// 桩行为(注入数据驱动, 不覆写装配方法):
//   SharePreview: passcode=="0000" → 提取码错误; savePath 含 "noban" 除外; link 含 "ban" → 禁止转存
//   Quota:        link 含 "poor" → 剩余 100B(配额不足); 否则 1TB
//   TransferToTmp: 登记转存目录(内存), 返回勾选文件清单
//   Remove:       登记"网盘删除"调用到日志(STUB_REMOVE <path>), 供脚本断言 .tmp- 目录被回删
//   List:         返回已转存未删目录(模拟重启孤儿扫)
// 每次桩调用输出一行日志供脚本断言。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"

	"baidupcs-web/internal/config"
	"baidupcs-web/internal/server"
	"baidupcs-web/internal/xengine"
)

type stubEngine struct {
	mu        sync.Mutex
	poorQuota bool
	tmpDirs   map[string]bool // shareID → 目录存在
}

func newStub() *stubEngine { return &stubEngine{tmpDirs: map[string]bool{}} }

func (s *stubEngine) logf(f string, a ...interface{}) { fmt.Printf("STUB "+f+"\n", a...) }

func (s *stubEngine) SharePreview(_ context.Context, link, passcode string) (xengine.SharePreviewResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if passcode == "0000" {
		return xengine.SharePreviewResult{}, errors.New("提取码错误或链接失效")
	}
	if strings.Contains(link, "ban") {
		return xengine.SharePreviewResult{}, errors.New("该分享不支持转存, 无法直连下载")
	}
	items := make([]xengine.SharePreviewItem, 0, 2)
	for i, sz := range []int64{1024, 2048} {
		items = append(items, xengine.SharePreviewItem{FsID: fmt.Sprint(i + 1), Filename: fmt.Sprintf("文件%d.bin", i+1), Size: sz})
	}
	return xengine.SharePreviewResult{ShareID: "smoke-share-1", Items: items}, nil
}

func (s *stubEngine) Quota(context.Context) (int64, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poorQuota {
		return 1000, 900, nil // 剩余 100B < 需求 1024B → 配额不足
	}
	return 1 << 40, 0, nil
}

func (s *stubEngine) TransferToTmp(_ context.Context, link, passcode string, fsids []string) (*xengine.TmpTransfer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if passcode == "0000" {
		return nil, errors.New("提取码错误或链接失效")
	}
	if strings.Contains(link, "ban") {
		return nil, errors.New("该分享不支持转存, 无法直连下载")
	}
	dir := "/来自分享/.tmp-smoke-share-1"
	s.tmpDirs[dir] = true
	files := make([]xengine.SharePreviewItem, 0, len(fsids))
	for _, id := range fsids {
		files = append(files, xengine.SharePreviewItem{
			FsID: id, Path: dir + "/文件" + id + ".bin", Filename: "文件" + id + ".bin", Size: 1024,
		})
	}
	s.logf("TRANSFER_TMP %s fsids=%v", dir, fsids)
	return &xengine.TmpTransfer{ShareID: "smoke-share-1", TargetDir: dir, Files: files}, nil
}

func (s *stubEngine) Remove(_ context.Context, paths []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range paths {
		delete(s.tmpDirs, p)
		s.logf("REMOVE %s", p)
	}
	return nil
}

func (s *stubEngine) List(_ context.Context, dir string, _, _ int, _, _ string) (xengine.FileDirectoryList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := xengine.FileDirectoryList{}
	for d := range s.tmpDirs {
		if strings.HasPrefix(d, dir) {
			out = append(out, &xengine.FileDirectory{Path: d, Filename: strings.TrimPrefix(d, dir+"/"), Isdir: true})
		}
	}
	return out, nil
}

func (s *stubEngine) ResolveDlink(_ context.Context, p string) (*xengine.DownloadLink, error) {
	// 桩直链: 本地 http 服务的桩文件由脚本另行准备; 冒烟下载环节走本地桩源。
	return &xengine.DownloadLink{URL: strings.Replace(p, "/来自分享/.tmp-", "http://127.0.0.1:18341/dl/", 1), UA: "netdisk", Size: 1024}, nil
}

func (s *stubEngine) Export(context.Context, string) (*xengine.RapidLink, error) {
	return nil, errors.New("未实现")
}

func (s *stubEngine) Meta(_ context.Context, p string) (*xengine.FileDirectory, error) {
	return &xengine.FileDirectory{Path: p, Filename: strings.TrimPrefix(p, dirOf(p))}, nil
}

func dirOf(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i+1]
	}
	return "/"
}

// 其余 Engine 方法未实现(冒烟只触达上传外的分享直连面)。
func errNI() error { return errors.New("冒烟桩未实现该方法") }

func (s *stubEngine) Accounts() []xengine.Account { return []xengine.Account{{UID: 8, Name: "smoke-w8"}} }
func (s *stubEngine) ActiveAccount() (xengine.Account, bool) {
	return xengine.Account{UID: 8, Name: "smoke-w8"}, true
}
func (s *stubEngine) Search(context.Context, string, string, bool) (xengine.FileDirectoryList, error) {
	return nil, errNI()
}
func (s *stubEngine) Mkdir(context.Context, string) error                          { return errNI() }
func (s *stubEngine) Copy(context.Context, []string, string) error                 { return errNI() }
func (s *stubEngine) Move(context.Context, []string, string) error                 { return errNI() }
func (s *stubEngine) Rename(context.Context, string, string) error                 { return errNI() }
func (s *stubEngine) Tree(context.Context, string, int) (xengine.FileDirectoryList, error) {
	return nil, errNI()
}
func (s *stubEngine) Match(context.Context, string) ([]string, error) { return nil, errNI() }
func (s *stubEngine) Upload(context.Context, xengine.UploadParam) (xengine.UploadResult, error) {
	return xengine.UploadResult{}, errNI()
}
func (s *stubEngine) AddAccountByCookie(string) (*xengine.Account, error)  { return nil, errNI() }
func (s *stubEngine) AddAccountByBDUSS(string, string) (*xengine.Account, error) {
	return nil, errNI()
}
func (s *stubEngine) SwitchAccount(uint64) error  { return nil }
func (s *stubEngine) RemoveAccount(uint64) error  { return errNI() }
func (s *stubEngine) ShareSet(context.Context, []string, string, int) (*xengine.Shared, error) {
	return nil, errNI()
}
func (s *stubEngine) ShareList(context.Context, int) (xengine.ShareRecordList, error) {
	return nil, errNI()
}
func (s *stubEngine) ShareCancel(context.Context, []int64) error { return errNI() }
func (s *stubEngine) ParseShareLink(string) (string, string, error) {
	return "", "", errNI()
}
func (s *stubEngine) Transfer(context.Context, string, string, string, []string) error {
	return errNI()
}
func (s *stubEngine) OfflineAdd(context.Context, []string, string) ([]xengine.OfflineTask, []error) {
	return nil, []error{errNI()}
}
func (s *stubEngine) OfflineList(context.Context) (xengine.OfflineTaskList, error) {
	return nil, errNI()
}
func (s *stubEngine) OfflineQuery(context.Context, []int64) (xengine.OfflineTaskList, error) {
	return nil, errNI()
}
func (s *stubEngine) OfflineCancel(context.Context, int64) error       { return errNI() }
func (s *stubEngine) OfflineDelete(context.Context, int64, bool) error { return errNI() }
func (s *stubEngine) RecycleList(context.Context, int) (xengine.RecycleInfoList, error) {
	return nil, errNI()
}
func (s *stubEngine) RecycleRestore(context.Context, []int64) error { return errNI() }
func (s *stubEngine) RecycleDelete(context.Context, []int64, bool) error {
	return errNI()
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18340", "监听地址")
	dataDir := flag.String("data", "", "数据目录")
	tmpDir := flag.String("tmp", "", "上传临时目录")
	_ = flag.String("password", "", "已废弃: 口令鉴权已移除, 仅为兼容旧脚本参数")
	poor := flag.Bool("poor", false, "配额不足模式")
	flag.Parse()


	cfg, err := config.Load(config.Options{Listen: *listen, DataDir: *dataDir, TmpDir: *tmpDir})
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	eng := newStub()
	eng.poorQuota = *poor
	srv, err := server.NewWithEngine(server.Options{Config: cfg, Version: "smoke-w8"}, eng)
	if err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
	fmt.Println("STUB_READY")
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
