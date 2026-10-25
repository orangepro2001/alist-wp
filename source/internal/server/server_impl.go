package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"baidupcs-web/internal/api"
	"baidupcs-web/internal/davfs"
	"baidupcs-web/internal/sharetmp"
	"baidupcs-web/internal/tempmgr"
	"baidupcs-web/internal/uploader"
	"baidupcs-web/internal/webui"
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

// newServer 装配实现: 中间件链(§5.5)包裹路由 mux, 挂 /api/health、W3 会话域路由、
// W4 文件域/账号域/SSE 占位与前端 embed。W5~W9 各域 REST 路由在此装配。

// startAt 服务进程启动时刻(FR-36 运行时长; 包级便于测试注入)。
var startAt = time.Now()

// rateLimiters 限速器表(W9: upload 10/min/IP, FR-39②); 测试可注入短窗口。
var rateLimiters = map[string]*rateLimiter{
	"/api/upload/":    newRateLimiter(RateLimitConfig{}),
}

// setupRateLimitersForTest 重建限速器(测试注入短间隔用); 返回恢复函数。
func setupRateLimitersForTest(cfg RateLimitConfig) func() {
	old := rateLimiters
	rateLimiters = map[string]*rateLimiter{
		"/api/upload/": newRateLimiter(cfg),
	}
	return func() { rateLimiters = old }
}

// shareDLResolver B5: /api/share/dl/url 的 resolver 适配器。
// path 归属校验走 sharetmp 作业清单(HasFilePath); 签名 URL 复用 xfer Manager(同 /api/dl/url)。
type shareDLResolver struct {
	xferMgr interface {
		ResolveForURL(pcspath string) (string, error)
	}
	orc sharetmp.Orchestrator
}

// ResolveForURL 实现签名直链生成(30min token, 与 /api/dl/url 同源)。
func (s shareDLResolver) ResolveForURL(pcspath string) (string, error) { return s.xferMgr.ResolveForURL(pcspath) }

// HasFilePath 透传作业清单校验(orch 实现必有; 接口未声明时恒 true 由签名 token 兜底)。
func (s shareDLResolver) HasFilePath(jobID, path string) bool {
	type pathChecker interface{ HasFilePath(jobID, path string) bool }
	if c, ok := s.orc.(pathChecker); ok {
		return c.HasFilePath(jobID, path)
	}
	return true
}

func newServer(opt Options) (*Server, error) {
	setupLogLevelFromEnv()
	mux := http.NewServeMux()

	// 健康检查(FR-36 子集): version 缺省 "dev"(W9 ldflags 注入)。
	version := opt.Version
	if version == "" {
		version = "dev"
	}
	mux.HandleFunc("GET /api/health", api.NewHealth(version))

	// W4: 文件域(FR-05~10, FR-21~24)与账号域(FR-01~04)。
	// Engine 无账号时返回 AUTH_REQUIRED(401), 前端据此引导到设置页添加百度账号。
	// W9: config/match/export/status/tools 已接(见下方 settings/tools 域)。
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
	files := api.NewFiles(eng)
	mux.HandleFunc("GET /api/quota", files.HandleQuota)
	mux.HandleFunc("GET /api/files", files.HandleList)
	mux.HandleFunc("GET /api/meta", files.HandleMeta)
	mux.HandleFunc("GET /api/search", files.HandleSearch)
	mux.HandleFunc("POST /api/files/mkdir", files.HandleMkdir)
	mux.HandleFunc("POST /api/files/rm", files.HandleRemove)
	mux.HandleFunc("POST /api/files/cp", files.HandleCopy)
	mux.HandleFunc("POST /api/files/mv", files.HandleMove)
	accounts := api.NewAccounts(eng)
	mux.HandleFunc("GET /api/accounts", accounts.HandleList)
	mux.HandleFunc("POST /api/accounts/add", accounts.HandleAdd)
	mux.HandleFunc("POST /api/accounts/{uid}/switch", accounts.HandleSwitch)
	mux.HandleFunc("DELETE /api/accounts/{uid}", accounts.HandleRemove)

	// W7: 分享/转存域(FR-25~28)、离线域(FR-29/30)与回收站域(FR-31~33)。
	share := api.NewShare(eng)
	mux.HandleFunc("GET /api/share/list", share.HandleShareList)
	mux.HandleFunc("POST /api/share/set", share.HandleShareSet)
	mux.HandleFunc("POST /api/share/cancel", share.HandleShareCancel)
	mux.HandleFunc("POST /api/transfer", share.HandleTransfer)
	offline := api.NewOffline(eng)
	mux.HandleFunc("POST /api/offline/add", offline.HandleAdd)
	mux.HandleFunc("GET /api/offline/list", offline.HandleList)
	mux.HandleFunc("GET /api/offline/query", offline.HandleQuery)
	mux.HandleFunc("POST /api/offline/cancel", offline.HandleCancel)
	mux.HandleFunc("POST /api/offline/delete", offline.HandleDelete)
	recycle := api.NewRecycle(eng)
	mux.HandleFunc("GET /api/recycle/list", recycle.HandleList)
	mux.HandleFunc("POST /api/recycle/restore", recycle.HandleRestore)
	mux.HandleFunc("POST /api/recycle/delete", recycle.HandleDelete)

	// W5: 下载通道(FR-11/12/14): 直链解析器注入(生产=Engine, 冒烟=桩经 engineOverride 生效);
	// 并发上限取 config.MaxDownloadLoad(默认 3), 限速钩子取 config.MaxDownloadRate(0 不限)。
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
	// W6: 上传通道(FR-15/16/17): 三段式 Receiver + tempmgr 生命周期。
	tmpDir := opt.Config.TmpDir
	if tmpDir == "" {
		tmpDir = filepath.Join(os.TempDir(), "baidupcs-web-tmp")
	}
	tm, tmErr := tempmgr.New(tmpDir)
	if tmErr != nil {
		return nil, tmErr
	}
	xf := xfer.NewManager(xfer.ProxyConfig{
		SessionKey:      opt.Config.SessionKey,
		Resolve:         resolveViaEngine(eng),
		ResolveFallback: fileDlinkResolver(opt.Config.DataDir),
		MaxParallel:     maxDL,
		Rate:            rate,
		UploadEngine:    eng,
		MaxUploadLoad:   uploadLoad,
		UploadFinishHook: func(taskID string) { _ = tm.DeleteTaskFile(taskID) },
	})
	xf.SetUploadEngine(eng)
	dl := api.NewDL(xf.Proxy())
	mux.HandleFunc("GET /api/dl", dl.HandleDL)
	mux.HandleFunc("GET /api/dl/url", api.NewDLURL(xf).HandleDLURL)
	tasks := api.NewTasks(xf)
	mux.HandleFunc("GET /api/tasks", tasks.HandleList)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", tasks.HandleCancel)
	mux.HandleFunc("POST /api/tasks/{id}/retry", tasks.HandleRetry)
	mux.HandleFunc("POST /api/tasks/{id}/purge", tasks.HandlePurge)
	// W5: SSE 进度推送(≤500ms 节流, FR-19)。
	mux.HandleFunc("GET /api/events", api.NewEvents(xf))

	// W8: FR-40 分享直连下载(编排器 Deps 注入 Engine 与下载代理解析, 不覆写方法)。
	shareOrc, err := sharetmp.New(sharetmp.Deps{
		PreviewFn: func(ctx context.Context, link, passcode string) ([]sharetmp.Item, string, error) {
			res, err := eng.SharePreview(ctx, link, passcode)
			if err != nil {
				return nil, "", err
			}
			items := make([]sharetmp.Item, 0, len(res.Items))
			for _, it := range res.Items {
				items = append(items, sharetmp.Item{FsID: it.FsID, Path: it.Path, Filename: it.Filename, Size: it.Size})
			}
			return items, res.ShareID, nil
		},
		FreeBytesFn: func(ctx context.Context) (int64, error) {
			q, used, err := eng.Quota(ctx)
			if err != nil {
				return 0, err
			}
			return q - used, nil
		},
		TransferFn: func(ctx context.Context, link, passcode string, fsids []string) (string, []sharetmp.Item, error) {
			tt, err := eng.TransferToTmp(ctx, link, passcode, fsids)
			if err != nil {
				// B3: 失败也可能已建临时目录(TransferToTmp 返回非空 TargetDir),
				// 透传给编排器失败分支回删, 防孤儿目录。
				if tt == nil {
					return "", nil, err
				}
				return tt.TargetDir, nil, err
			}
			items := make([]sharetmp.Item, 0, len(tt.Files))
			for _, f := range tt.Files {
				items = append(items, sharetmp.Item{FsID: f.FsID, Path: f.Path, Filename: f.Filename, Size: f.Size})
			}
			return tt.TargetDir, items, nil
		},
		RemoveFn: func(ctx context.Context, paths []string) error { return eng.Remove(ctx, paths) },
		DownloadFn: func(ctx context.Context, pcspath string) error {
			// 服务器侧拉流(不含浏览器响应): 解析直链→流式丢弃至终态(内容透传给浏览器经
			// /api/share/dl/url 签名直链); 失败即作业失败, 触发回删。
			return xf.Proxy().PumpLocal(ctx, pcspath)
		},
	})
	if err != nil {
		return nil, err
	}
	st := api.NewShareTmp(shareOrc)
	mux.HandleFunc("POST /api/share/preview", st.HandlePreview)
	mux.HandleFunc("POST /api/share/dl", st.HandleStartDL)
	mux.HandleFunc("GET /api/share/dl", st.HandleListDL)
	mux.HandleFunc("POST /api/share/dl/cancel", st.HandleCancelDL)
	// B5: FR-40 浏览器直下签名直链(复用 xfer 签名 resolver; path 校验绑定作业下载清单)。
	mux.HandleFunc("GET /api/share/dl/url", api.NewShareDLURL(shareDLResolver{xferMgr: xf, orc: shareOrc}).HandleShareDLURL)
	// FR-40 异常 ③: 服务启动孤儿扫 .tmp-*(需求: 重启后 5min 内清除, 这里启动即扫)。
	go func() {
		_, _ = sharetmp.SweepPanTmp(context.Background(), sharetmp.SweepDeps{
			ListTmpDirs: func(ctx context.Context) ([]string, error) {
				list, err := eng.List(ctx, "/来自分享", 0, 0, "", "")
				if err != nil {
					return nil, err
				}
				out := make([]string, 0, len(list))
				for _, e := range list {
					out = append(out, e.Path)
				}
				return out, nil
			},
			RemoveDirs: func(ctx context.Context, paths []string) error { return eng.Remove(ctx, paths) },
			Log: func(f string, a ...interface{}) { log.Printf("sharetmp: "+f, a...) },
		})
	}()

	// W6: 上传通道(FR-15/16/17): 三段式 Receiver + tempmgr 生命周期。
	// B9: StartUpload 经 EnqueueUploadWithRename 注入"待改名"回调——引擎任务 ID
	// 确定后立即把临时文件改名挂到该 ID 命名下(worker 读最终路径), 删除只由
	// xfer 终态 FinishHook(上方 UploadFinishHook)触发。
	rec, err := uploader.New(uploader.Config{
		TempMgr: tm,
		StartUpload: func(ctx context.Context, tempPath, savePath, policy string, noRapid bool) (string, error) {
			// B9: 引擎任务 ID 确定后立即 rename(临时文件挂任务 ID 命名下, worker 读最终路径);
			// 改名失败保留原路径, 删除登记仍以任务 ID 前缀定位(tempmgr 幂等)。
			return xf.EnqueueUploadWithRename(ctx, tempPath, savePath, policy, noRapid, func(taskID string) (string, error) {
				return xf.RenameTemp(taskID, tempPath)
			})
		},
		Watermark: func() (int64, bool, error) {
			st, err := tm.DiskState()
			if err != nil {
				return 0, false, err
			}
			return st.FreeBytes, st.BelowLimit, nil
		},
	})
	if err != nil {
		return nil, err
	}
	// FR-17 孤儿扫描: 引用集=xfer 活跃任务(上传临时文件持有者); 启动全扫+每 5min。
	sweepCancel := make(chan struct{})
	tm.StartOrphanSweep(func(taskIDs map[string]struct{}) {
		for _, row := range xf.Rows() {
			if row.State == "queued" || row.State == "running" {
				taskIDs[row.ID] = struct{}{}
			}
		}
	}, sweepCancel)
	up := api.NewUpload(rec)
	mux.HandleFunc("POST /api/upload/init", up.HandleInit)
	mux.HandleFunc("POST /api/upload/{id}", up.HandleChunk)
	mux.HandleFunc("POST /api/upload/{id}/complete", up.HandleComplete)
	mux.HandleFunc("POST /api/upload/{id}/abort", up.HandleAbort)

	// W9: 配置域(FR-34 白名单读写/只读 403)与状态域(FR-36 版本/时长/临时目录占用)。
	settings := api.NewSettings(opt.Config, version, startAt, tmpDir)
	mux.HandleFunc("GET /api/config", settings.HandleConfig)
	mux.HandleFunc("POST /api/config/set", settings.HandleConfigSet)
	mux.HandleFunc("GET /api/status", settings.HandleStatus)

	// W9: 工具域(FR-35 match/FR-13 export/上游 tool 加解密)。
	tools := api.NewTools(eng)
	mux.HandleFunc("POST /api/match", tools.HandleMatch)
	mux.HandleFunc("POST /api/export", tools.HandleExport)
	mux.HandleFunc("POST /api/tool/cipher", tools.HandleToolCipher)

	// WebDAV(给 OpenList/Alist 的 WebDAV 驱动挂载): 下载走流式代理(免签名, 仅回环可达),
	// 上传落 tempmgr 临时文件后入同一上传队列(秒传优先)。PUT 同名一律覆盖(WebDAV 语义)。
	mux.Handle("/dav/", davfs.New(davfs.Config{
		Engine:   eng,
		Uploads:  xf,
		Temp:     tm,
		Download: xf.Proxy(),
		Prefix:   "/dav",
	}))

	// 前端 SPA(go:embed): /assets/* 静态文件, 其余回落 index.html(hash 路由单入口)。
	// index.html 注入 <base href=BasePath/>, 前端一律用相对路径(api/...、assets/...)。
	mux.Handle("/", webui.HandlerWithBase(opt.Config.BasePath))

	// 中间件链: recover → 限速(upload) → 脱敏分级日志。无应用层鉴权: 服务只监听回环地址,
	// 由 tailscale serve 在 tailnet 内以 HTTPS 暴露(访问控制交给 tailnet)。
	var app http.Handler = chain(mux, recoverMiddleware, rateLimitMiddleware(rateLimiters), logMiddleware)
	root := mountBasePath(app, opt.Config.BasePath)
	srv := &Server{mux: mux, root: root}
	addr := opt.Config.Listen
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	srv.httpSrv = &http.Server{Addr: addr, Handler: root}
	return srv, nil
}

// mountBasePath 把 app 挂到 base 前缀下(base 为空则原样返回)。
// /base → 301 到 /base/; /base/* → 剥前缀交给 app; 其余路径 404(端口直连根路径无内容)。
func mountBasePath(app http.Handler, base string) http.Handler {
	if base == "" {
		return app
	}
	strip := http.StripPrefix(base, app)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == base:
			target := base + "/"
			if r.URL.RawQuery != "" {
				target += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		case strings.HasPrefix(r.URL.Path, base+"/"):
			strip.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}
