package davfs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/webdav"

	"baidupcs-web/internal/tempmgr"
	"baidupcs-web/internal/xengine"
	"baidupcs-web/internal/xfer"
)

// Uploader 上传队列(xfer.Manager 满足)。
type Uploader interface {
	EnqueueUploadWithRename(ctx context.Context, tempPath, savePath, policy string, noRapid bool, renamer func(taskID string) (string, error)) (string, error)
	RenameTemp(taskID, tempPath string) (string, error)
	Row(id string) (xfer.TaskRow, bool)
}

// Temp 上传临时文件管理(tempmgr.Manager 满足)。
type Temp interface {
	NewTaskFile(taskID, filename string) (string, error)
	DeleteTaskFile(taskID string) error
	DiskState() (tempmgr.DiskState, error)
}

// Downloader 流式下载代理(xfer.Proxy 满足)。
type Downloader interface {
	ServeDownloadTrusted(w http.ResponseWriter, r *http.Request, pcspath string) error
}

// Config WebDAV 装配参数。
type Config struct {
	Engine   Engine
	Uploads  Uploader
	Temp     Temp
	Download Downloader
	Prefix   string        // URL 前缀, 如 "/dav"
	Policy   string        // 上传同名策略, 空取 overwrite(WebDAV PUT 语义=覆盖)
	CacheTTL time.Duration // 目录缓存, ≤0 取 10s
	Poll     time.Duration // 等待上传终态的轮询间隔, ≤0 取 200ms
}

type handler struct {
	cfg  Config
	fs   *FS
	dav  *webdav.Handler
	poll time.Duration
}

// New 构造 WebDAV http.Handler: GET/HEAD/PUT/COPY 自行处理, 其余交给 x/net/webdav。
func New(cfg Config) http.Handler {
	if cfg.Policy == "" {
		cfg.Policy = "overwrite"
	}
	if cfg.Poll <= 0 {
		cfg.Poll = 200 * time.Millisecond
	}
	cfg.Prefix = strings.TrimRight(cfg.Prefix, "/")
	f := NewFS(cfg.Engine, cfg.CacheTTL)
	h := &handler{cfg: cfg, fs: f, poll: cfg.Poll}
	h.dav = &webdav.Handler{
		Prefix:     cfg.Prefix,
		FileSystem: f,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("[WARN] [webdav] %s %s: %v", r.Method, r.URL.Path, err)
			}
		},
	}
	return h
}

func (h *handler) reqPath(r *http.Request) string {
	return clean(strings.TrimPrefix(r.URL.Path, h.cfg.Prefix))
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.get(w, r)
	case http.MethodHead:
		h.head(w, r)
	case http.MethodPut:
		h.put(w, r)
	case "COPY":
		h.copy(w, r)
	default:
		h.dav.ServeHTTP(w, r)
	}
}

func (h *handler) head(w http.ResponseWriter, r *http.Request) {
	in, err := h.fs.stat(r.Context(), h.reqPath(r))
	if err != nil {
		h.fail(w, err)
		return
	}
	if in.dir {
		h.dav.ServeHTTP(w, r)
		return
	}
	hd := w.Header()
	ct, _ := in.ContentType(r.Context())
	etag, _ := in.ETag(r.Context())
	hd.Set("Content-Type", ct)
	hd.Set("Content-Length", strconv.FormatInt(in.size, 10))
	hd.Set("Accept-Ranges", "bytes")
	hd.Set("Last-Modified", in.mtime.UTC().Format(http.TimeFormat))
	hd.Set("ETag", etag)
	w.WriteHeader(http.StatusOK)
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	p := h.reqPath(r)
	in, err := h.fs.stat(r.Context(), p)
	if err != nil {
		h.fail(w, err)
		return
	}
	if in.dir {
		h.dav.ServeHTTP(w, r) // 目录 GET: x/net/webdav 回 405
		return
	}
	err = h.cfg.Download.ServeDownloadTrusted(w, r, p)
	if err == nil {
		return
	}
	var pe *xfer.ProxyError
	if errors.As(err, &pe) {
		http.Error(w, pe.Msg, pe.Status)
		return
	}
	select {
	case <-r.Context().Done(): // 客户端已断开, 响应已提交或连接已断
	default:
		http.Error(w, "下载中断: "+err.Error(), http.StatusBadGateway)
	}
}

// put 把请求体落到 tempmgr 临时文件, 入上传队列(秒传优先), 等任务终态再回响应,
// 这样调用方(OpenList)看到的成败就是百度侧的真实结果。
func (h *handler) put(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := h.reqPath(r)
	if p == "/" {
		http.Error(w, "不能覆盖根目录", http.StatusMethodNotAllowed)
		return
	}
	parent, err := h.fs.stat(ctx, path.Dir(p))
	if err != nil || !parent.dir {
		http.Error(w, "父目录不存在", http.StatusConflict)
		return
	}
	existed := false
	if in, err := h.fs.stat(ctx, p); err == nil {
		if in.dir {
			http.Error(w, "目标是目录", http.StatusMethodNotAllowed)
			return
		}
		existed = true
	}
	if st, err := h.cfg.Temp.DiskState(); err == nil && st.BelowLimit {
		http.Error(w, tempmgr.ErrBelowWatermark{FreeBytes: st.FreeBytes}.Error(), http.StatusInsufficientStorage)
		return
	}

	id := newID()
	tmpPath, err := h.cfg.Temp.NewTaskFile(id, path.Base(p))
	if err != nil {
		http.Error(w, "创建临时文件失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	n, err := copyToFile(tmpPath, r.Body)
	if err == nil && r.ContentLength >= 0 && n != r.ContentLength {
		err = fmt.Errorf("请求体不完整: 期望 %d 字节, 实收 %d", r.ContentLength, n)
	}
	if err != nil {
		_ = h.cfg.Temp.DeleteTaskFile(id)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	taskID, err := h.cfg.Uploads.EnqueueUploadWithRename(context.Background(), tmpPath, p, h.cfg.Policy, false,
		func(taskID string) (string, error) { return h.cfg.Uploads.RenameTemp(taskID, tmpPath) })
	if err != nil {
		_ = h.cfg.Temp.DeleteTaskFile(id)
		h.fail(w, err)
		return
	}
	// 排队/上传期间客户端断开则不再等待(任务继续在后台完成, 临时文件由终态钩子清理)。
	row, err := h.waitTerminal(ctx, taskID)
	h.fs.invalidate(p)
	switch {
	case err != nil:
		http.Error(w, "等待上传结果被中断: "+err.Error(), http.StatusGatewayTimeout)
		return
	case row.State != "done":
		msg := row.Err
		if msg == "" {
			msg = row.State
		}
		http.Error(w, "上传到网盘失败: "+msg, http.StatusBadGateway)
		return
	}
	if in, err := h.fs.stat(ctx, p); err == nil {
		if etag, err := in.ETag(ctx); err == nil {
			w.Header().Set("ETag", etag)
		}
	}
	if existed {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func (h *handler) waitTerminal(ctx context.Context, taskID string) (xfer.TaskRow, error) {
	t := time.NewTicker(h.poll)
	defer t.Stop()
	for {
		row, ok := h.cfg.Uploads.Row(taskID)
		if !ok {
			return row, fmt.Errorf("任务 %s 已不存在", taskID)
		}
		switch row.State {
		case "done", "failed", "cancelled":
			return row, nil
		}
		select {
		case <-ctx.Done():
			return row, ctx.Err()
		case <-t.C:
		}
	}
}

// copy 服务端复制: 直接 Engine.Copy, 不经本机读写。仅支持同名复制(百度 cp 不改名)。
func (h *handler) copy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	src := h.reqPath(r)
	dst, status := h.destination(r)
	if status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if src == "/" || dst == "/" || src == dst {
		http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	if path.Base(src) != path.Base(dst) {
		http.Error(w, "百度网盘复制不支持同时改名", http.StatusNotImplemented)
		return
	}
	if _, err := h.fs.stat(ctx, src); err != nil {
		h.fail(w, err)
		return
	}
	if parent, err := h.fs.stat(ctx, path.Dir(dst)); err != nil || !parent.dir {
		http.Error(w, "目标父目录不存在", http.StatusConflict)
		return
	}
	existed := false
	if _, err := h.fs.stat(ctx, dst); err == nil {
		existed = true
		if strings.EqualFold(r.Header.Get("Overwrite"), "F") {
			http.Error(w, http.StatusText(http.StatusPreconditionFailed), http.StatusPreconditionFailed)
			return
		}
		if err := h.fs.RemoveAll(ctx, dst); err != nil {
			h.fail(w, err)
			return
		}
	}
	if err := mapErr(h.cfg.Engine.Copy(ctx, []string{src}, path.Dir(dst))); err != nil {
		h.fail(w, err)
		return
	}
	h.fs.invalidate(dst)
	if existed {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

// destination 解析 Destination 头为网盘路径; 返回非 0 状态码表示请求无效。
func (h *handler) destination(r *http.Request) (string, int) {
	raw := r.Header.Get("Destination")
	if raw == "" {
		return "", http.StatusBadRequest
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", http.StatusBadRequest
	}
	if u.Host != "" && u.Host != r.Host {
		return "", http.StatusBadGateway // 不支持跨主机复制
	}
	if h.cfg.Prefix != "" && u.Path != h.cfg.Prefix && !strings.HasPrefix(u.Path, h.cfg.Prefix+"/") {
		return "", http.StatusBadGateway
	}
	return clean(strings.TrimPrefix(u.Path, h.cfg.Prefix)), 0
}

// fail 把错误映射成 HTTP 状态码。
func (h *handler) fail(w http.ResponseWriter, err error) {
	var xe *xengine.Error
	switch {
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "不存在", http.StatusNotFound)
	case errors.As(err, &xe) && xe.Code == xengine.ErrAuth:
		http.Error(w, xe.Message, http.StatusConflict)
	case errors.As(err, &xe) && xe.Code == xengine.ErrQuota:
		http.Error(w, xe.Message, http.StatusInsufficientStorage)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "dav" + hex.EncodeToString(b[:])
}

func copyToFile(p string, src io.Reader) (int64, error) {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, src)
	if serr := f.Sync(); err == nil {
		err = serr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return n, err
}
