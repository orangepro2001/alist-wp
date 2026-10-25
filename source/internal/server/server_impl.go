package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"baidupcs-web/internal/api"
	"baidupcs-web/internal/davfs"
	"baidupcs-web/internal/tempmgr"
	"baidupcs-web/internal/xengine"
	"baidupcs-web/internal/xfer"
)

// fileDlinkResolver 本地直链映射兜底(<DataDir>/dlink.map, 每行 "网盘路径<TAB>URL"):
// 冒烟/本地演示用(脚本写入桩源 URL); 无映射文件时返回恒失败解析器(生产零影响)。
func fileDlinkResolver(dataDir string) xfer.DlinkResolver {
	return func(_ context.Context, pcspath string) (string, string, int64, error) {
		data, err := os.ReadFile(filepath.Join(dataDir, "dlink.map"))
		if err != nil {
			return "", "", 0, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			if i := strings.IndexByte(line, '\t'); i > 0 && line[:i] == pcspath {
				u := strings.TrimSpace(line[i+1:])
				if u != "" {
					return u, xengine.NetdiskUA, 0, nil
				}
			}
		}
		return "", "", 0, errors.New("dlink.map 无映射")
	}
}

// resolveViaEngine 生产直链解析器: Engine.ResolveDlink → xfer.DlinkResolver(§5.1)。
// 返回 (dlink, NetdiskUA, size, err); 冒烟脚本经 engineOverride 注入桩 Engine 同样生效。
func resolveViaEngine(eng xengine.Engine) xfer.DlinkResolver {
	return func(ctx context.Context, pcspath string) (string, string, int64, error) {
		link, err := eng.ResolveDlink(ctx, pcspath)
		if err != nil {
			return "", "", 0, err
		}
		ua := link.UA
		if ua == "" {
			ua = xengine.NetdiskUA
		}
		return link.URL, ua, link.Size, nil
	}
}

// newServer 装配: 只有三组路由 — /api/health(探活)、/api/accounts*(供命令行账号子命令, 仅回环)、
// /dav/(WebDAV, 给 OpenList/Alist 挂载)。无应用层鉴权, 无 Web 页面: 服务只监听回环地址,
// 对外入口只有 tailscale serve 暴露的 OpenList。
func newServer(opt Options) (*Server, error) {
	setupLogLevelFromEnv()
	mux := http.NewServeMux()

	version := opt.Version
	if version == "" {
		version = "dev"
	}
	mux.HandleFunc("GET /api/health", api.NewHealth(version))

	// Engine 无账号时各调用返回 ErrAuth(409 BAIDU_NOT_LINKED / WebDAV 409)。
	eng := engineOverride
	if eng == nil {
		var err error
		eng, err = xengine.New(opt.Config.DataDir, xengine.TransConfig{
			AppID:             xengine.DefaultAppID,
			EnableHTTPS:       true,
			MaxUploadParallel: opt.Config.MaxUploadParallel,
			MaxUploadRate:     opt.Config.MaxUploadRate,
			UPolicy:           opt.Config.UPolicy,
			UserAgent:         opt.Config.UserAgent,
		})
		if err != nil {
			return nil, err
		}
	}
	accounts := api.NewAccounts(eng)
	mux.HandleFunc("GET /api/accounts", accounts.HandleList)
	mux.HandleFunc("POST /api/accounts/add", accounts.HandleAdd)
	mux.HandleFunc("POST /api/accounts/{uid}/switch", accounts.HandleSwitch)
	mux.HandleFunc("DELETE /api/accounts/{uid}", accounts.HandleRemove)

	// 传输通道: 下载并发取 MaxDownloadLoad(默认 3)、限速取 MaxDownloadRate(0 不限);
	// 上传并发取 MaxUploadLoad(默认 2)。
	maxDL := opt.Config.MaxDownloadLoad
	if maxDL <= 0 {
		maxDL = 3
	}
	uploadLoad := opt.Config.MaxUploadLoad
	if uploadLoad <= 0 {
		uploadLoad = 2
	}
	var rate xfer.RateLimiter
	if opt.Config.MaxDownloadRate > 0 {
		rate = xfer.NewRateLimiter(opt.Config.MaxDownloadRate)
	}
	tmpDir := opt.Config.TmpDir
	if tmpDir == "" {
		tmpDir = filepath.Join(os.TempDir(), "baidupcs-web-tmp")
	}
	tm, err := tempmgr.New(tmpDir)
	if err != nil {
		return nil, err
	}
	xf := xfer.NewManager(xfer.ProxyConfig{
		SessionKey:       opt.Config.SessionKey,
		Resolve:          resolveViaEngine(eng),
		ResolveFallback:  fileDlinkResolver(opt.Config.DataDir),
		MaxParallel:      maxDL,
		Rate:             rate,
		UploadEngine:     eng,
		MaxUploadLoad:    uploadLoad,
		UploadFinishHook: func(taskID string) { _ = tm.DeleteTaskFile(taskID) },
	})
	xf.SetUploadEngine(eng)
	// FR-17 孤儿扫描: 引用集=xfer 活跃任务(上传临时文件持有者); 启动全扫+每 5min。
	sweepCancel := make(chan struct{})
	tm.StartOrphanSweep(func(taskIDs map[string]struct{}) {
		for _, row := range xf.Rows() {
			if row.State == "queued" || row.State == "running" {
				taskIDs[row.ID] = struct{}{}
			}
		}
	}, sweepCancel)

	// WebDAV: 下载走流式代理(免签名, 仅回环可达), 上传落 tempmgr 临时文件后入同一上传队列
	// (秒传优先)。PUT 同名一律覆盖(WebDAV 语义)。
	mux.Handle("/dav/", davfs.New(davfs.Config{
		Engine:   eng,
		Uploads:  xf,
		Temp:     tm,
		Download: xf.Proxy(),
		Prefix:   "/dav",
	}))

	// 中间件链: recover → 脱敏分级日志。
	root := chain(mux, recoverMiddleware, logMiddleware)
	srv := &Server{mux: mux, root: root}
	addr := opt.Config.Listen
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	srv.httpSrv = &http.Server{Addr: addr, Handler: root}
	return srv, nil
}
