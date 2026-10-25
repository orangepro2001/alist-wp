// smoke_all_srv.go — W9 全量回归冒烟辅助: 真实 server 装配 + 全功能桩 Engine。
// 桩行为(注入数据驱动, 与 W4~W8 桩模式同源):
//   Quota:  1TB/10GB
//   List:   "/" → 照片(目录)+冒烟文件.bin(65536B); 其他目录 NOT_FOUND
//   ResolveDlink: http://127.0.0.1:18361/dl/<path>(脚本起的桩源, Range/206)
//   Upload: 直接成功(savePath 原样返回)
//   其余:   成功/未实现按冒烟面裁剪
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"baidupcs-web/internal/config"
	"baidupcs-web/internal/server"
	"baidupcs-web/internal/xengine"
)

type stubEngine struct{}

func errNI() error { return errors.New("冒烟桩未实现") }

func (stubEngine) Upload(_ context.Context, p xengine.UploadParam) (xengine.UploadResult, error) {
	return xengine.UploadResult{SavePath: p.SavePath}, nil
}
func (stubEngine) Quota(context.Context) (int64, int64, error) { return 1 << 40, 10 << 30, nil }
func (stubEngine) List(_ context.Context, dir string, _, _ int, _, _ string) (xengine.FileDirectoryList, error) {
	if dir != "/" && dir != "" {
		return nil, &xengine.Error{Code: xengine.ErrNotFound, Message: "目录不存在"}
	}
	return xengine.FileDirectoryList{
		{FsID: 1, Path: "/照片", Filename: "照片", Isdir: true},
		{FsID: 2, Path: "/冒烟文件.bin", Filename: "冒烟文件.bin", Size: 65536},
	}, nil
}
func (stubEngine) Meta(_ context.Context, p string) (*xengine.FileDirectory, error) {
	return &xengine.FileDirectory{Path: p, Filename: strings.TrimPrefix(p, "/"), Size: 65536}, nil
}
func (stubEngine) ResolveDlink(_ context.Context, p string) (*xengine.DownloadLink, error) {
	return &xengine.DownloadLink{
		URL:  "http://127.0.0.1:18361/dl/" + strings.TrimPrefix(p, "/"),
		UA:   "netdisk", Size: 65536,
	}, nil
}
func (stubEngine) Accounts() []xengine.Account { return []xengine.Account{{UID: 9, Name: "smoke-w9"}} }
func (stubEngine) ActiveAccount() (xengine.Account, bool) {
	return xengine.Account{UID: 9, Name: "smoke-w9"}, true
}
func (stubEngine) Search(context.Context, string, string, bool) (xengine.FileDirectoryList, error) {
	return nil, nil
}
func (stubEngine) Match(context.Context, string) ([]string, error) { return nil, nil }
func (stubEngine) Export(context.Context, string) (*xengine.RapidLink, error) { return nil, errNI() }
func (stubEngine) Mkdir(context.Context, string) error                        { return nil }
func (stubEngine) Remove(context.Context, []string) error                     { return nil }
func (stubEngine) Copy(context.Context, []string, string) error               { return nil }
func (stubEngine) Move(context.Context, []string, string) error                 { return errNI() }
func (stubEngine) Rename(context.Context, string, string) error               { return errNI() }
func (stubEngine) Tree(context.Context, string, int) (xengine.FileDirectoryList, error) {
	return nil, nil
}
func (stubEngine) AddAccountByCookie(string) (*xengine.Account, error) { return nil, errNI() }
func (stubEngine) AddAccountByBDUSS(string, string) (*xengine.Account, error) {
	return nil, errNI()
}
func (stubEngine) SwitchAccount(uint64) error  { return nil }
func (stubEngine) RemoveAccount(uint64) error  { return errNI() }
func (stubEngine) ShareSet(context.Context, []string, string, int) (*xengine.Shared, error) {
	return nil, errNI()
}
func (stubEngine) ShareList(context.Context, int) (xengine.ShareRecordList, error) {
	return nil, errNI()
}
func (stubEngine) ShareCancel(context.Context, []int64) error { return errNI() }
func (stubEngine) ParseShareLink(string) (string, string, error) {
	return "", "", errNI()
}
func (stubEngine) SharePreview(context.Context, string, string) (xengine.SharePreviewResult, error) {
	return xengine.SharePreviewResult{}, errNI()
}
func (stubEngine) Transfer(context.Context, string, string, string, []string) error {
	return errNI()
}
func (stubEngine) TransferToTmp(context.Context, string, string, []string) (*xengine.TmpTransfer, error) {
	return nil, errNI()
}
func (stubEngine) OfflineAdd(context.Context, []string, string) ([]xengine.OfflineTask, []error) {
	return nil, []error{errNI()}
}
func (stubEngine) OfflineList(context.Context) (xengine.OfflineTaskList, error) {
	return nil, errNI()
}
func (stubEngine) OfflineQuery(context.Context, []int64) (xengine.OfflineTaskList, error) {
	return nil, errNI()
}
func (stubEngine) OfflineCancel(context.Context, int64) error       { return errNI() }
func (stubEngine) OfflineDelete(context.Context, int64, bool) error { return errNI() }
func (stubEngine) RecycleList(context.Context, int) (xengine.RecycleInfoList, error) {
	return nil, errNI()
}
func (stubEngine) RecycleRestore(context.Context, []int64) error { return errNI() }
func (stubEngine) RecycleDelete(context.Context, []int64, bool) error { return errNI() }

func main() {
	listen := flag.String("listen", "127.0.0.1:18360", "监听地址")
	dataDir := flag.String("data", "", "数据目录")
	tmpDir := flag.String("tmp", "", "临时目录")
	_ = flag.String("password", "", "已废弃: 口令鉴权已移除, 仅为兼容旧脚本参数")
	flag.Parse()
	cfg, err := config.Load(config.Options{Listen: *listen, DataDir: *dataDir, TmpDir: *tmpDir})
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	srv, err := server.NewWithEngine(server.Options{Config: cfg, Version: "smoke-w9"}, stubEngine{})
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
