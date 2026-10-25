// proxy.go W5: 流式下载代理实现(FR-11, §5.1, 决策 D2)。
// 数据面: /api/dl?path&token(签名校验) → ResolveDlinkFunc 拿直链 → NetdiskUA + 剥 Referer
// 请求百度 → 透传 Range → io.Copy(32MB 缓冲上限 + 限速钩子)回写浏览器。
// 断流续取: 上游读中断且浏览器连接仍在 → 按已发字节对百度重发 Range 续写(对浏览器透明),
// 最多 resumeRetries 次; 失败时若响应未提交回 502, 已提交则强制断开(浏览器自行按 Range 重试)。
package xfer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	"baidupcs-web/internal/xengine"
	"strings"
	"sync"
	"time"

	"baidupcs-web/internal/session"
)

// 代理行为常量(§5.1/§3)。
const (
	// copyBufLimit io.Copy 缓冲上限(32MB, NFR-04; 实际分配 min(剩余, 64KB) 级内核缓冲,
	// 上限约束的是限速/续取路径中的用户态缓冲峰值)。
	copyBufLimit = RingBufferSize
	// resumeRetries 断流续取最大重试次数(§7)。
	resumeRetries = 3
	// upstreamTimeout 单次对百度请求超时(§0.3 panHTTPClient 同款 6min)。
	upstreamTimeout = 6 * time.Minute
	// dlTokenTTL 下载签名 token 有效期(FR-39: 30min)。
	dlTokenTTL = 30 * time.Minute
)

// DlinkResolver 直链解析器: 生产注入 Engine.ResolveDlink, 冒烟注入桩函数(不覆写方法)。
type DlinkResolver func(ctx context.Context, pcspath string) (dlinkURL, ua string, size int64, err error)

// ProxyConfig Proxy 构造参数。
type ProxyConfig struct {
	// SessionKey 下载签名 token 密钥(与 session 同源)。
	SessionKey []byte
	// Resolve 直链解析器(必填)。
	Resolve DlinkResolver
	// ResolveFallback 直链解析兜底(可选; 冒烟脚本注入本地桩源, 生产为 nil)。
	ResolveFallback DlinkResolver
	// MaxParallel 下载并发上限(config.MaxDownloadLoad, 默认 3)。
	MaxParallel int
	// Rate 全局限速钩子(nil=不限速; FR-14/34)。
	Rate RateLimiter
	// Now 单测注入时钟(nil 则 time.Now)。
	Now func() time.Time
	// Client 单测注入 http.Client(nil 则内置 6min 超时客户端)。
	Client *http.Client
	// UploadEngine W6: 上传引擎(nil=上传通道未装配, EnqueueUpload 返回未就绪)。
	UploadEngine UploadEngine
	// MaxUploadLoad W6: 同时上传文件数(≤0 默认 2)。
	MaxUploadLoad int
	// RapidUploadProbe W6: 秒传探测钩子(单测/冒烟桩注入; nil=依赖引擎内部秒传)。
	RapidUploadProbe RapidProbe
	// UploadFinishHook W6: 上传任务终态钩子(FR-17 tempmgr 延迟删; server 装配注入)。
	UploadFinishHook FinishHook
}

// UploadEngine 上传引擎最小接口(xengine.Engine 满足; 独立类型便于桩测)。
type UploadEngine interface {
	Upload(ctx context.Context, param xengine.UploadParam) (xengine.UploadResult, error)
}

// proxy Proxy 接口实现(见 download.go 契约)。
type proxy struct {
	cfg  ProxyConfig
	q    *queue
	mu   sync.Mutex
	http *http.Client
	now  func() time.Time
}

// NewProxy 构造下载代理(含任务表/信号量/事件总线)。
func NewProxy(cfg ProxyConfig) *proxy {
	// B10: 上传并发槽取 cfg.MaxUploadLoad(≤0 默认 2), 不再硬编码。
	p := &proxy{cfg: cfg, q: newQueueWithUploadLoad(cfg.MaxParallel, cfg.MaxUploadLoad), now: cfg.Now}
	if p.now == nil {
		p.now = time.Now
	}
	p.http = cfg.Client
	if p.http == nil {
		p.http = &http.Client{Timeout: upstreamTimeout}
	}
	return p
}

// Close 停止事件总线(服务退出调用)。
func (p *proxy) Close() { p.q.close() }

// Queue 内部队列(任务 API 复用)。
func (p *proxy) Queue() *queue { return p.q }

// ResolveForURL 实现 Proxy: 生成 /api/dl?path=&token=&exp= 签名 URL(30min)。
func (p *proxy) ResolveForURL(pcspath string) (string, error) {
	exp := p.now().Add(dlTokenTTL)
	fp := "" // 会话指纹: 浏览器 <a> 直连无法带自定义头, 放空并绑定 path+exp(§4)
	token := session.SignToken(p.cfg.SessionKey, pcspath, exp, fp)
	return "/api/dl?path=" + url.QueryEscape(pcspath) +
		"&exp=" + fmt.Sprint(exp.Unix()) + "&token=" + token, nil
}

// ErrBadToken 签名/过期错误(403)。
// ErrBadToken 签名/过期错误(403 DLINK_EXPIRED)。
var ErrBadToken = errors.New("下载签名无效或已过期")

// verify 按请求参数校验签名 token(path+exp+fp 绑定, 过期拒绝)。
func (p *proxy) verify(pcspath, expStr, token string) error {
	expUnix, err := parseUnix(expStr)
	if err != nil {
		return ErrBadToken
	}
	exp := time.Unix(expUnix, 0)
	if !p.now().Before(exp) {
		return ErrBadToken // 过期拒绝(FR-39)
	}
	if !session.VerifyToken(p.cfg.SessionKey, pcspath, exp, "", token) {
		return ErrBadToken
	}
	return nil
}

// ServeDownload 实现 Proxy(download.go 契约): 鉴权 → 信号量 → 透传。
func (p *proxy) ServeDownload(w http.ResponseWriter, r *http.Request, pcspath string) error {
	if pcspath == "" {
		pcspath = r.URL.Query().Get("path")
	}
	pcspath = strings.TrimSpace(pcspath)
	if pcspath == "" || !strings.HasPrefix(pcspath, "/") {
		return badRequest("缺少 path 参数")
	}
	if err := p.verify(pcspath, r.URL.Query().Get("exp"), r.URL.Query().Get("token")); err != nil {
		return err
	}

	// 任务行 + 并发信号量(排队可见)
	name := path.Base(pcspath)
	task, release, err := p.q.acquire(r.Context(), pcspath, name, 0)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return err // 排队中被取消
		}
		return errInternal("建立下载任务失败: " + err.Error())
	}
	defer release()
	defer p.watchCancel(task)

	return p.stream(w, r, task)
}

// watchCancel running 任务: ctx 结束但任务未终态时补终态(浏览器断开)。
func (p *proxy) watchCancel(task *dlTask) {
	select {
	case <-task.done:
	default:
		p.q.finish(task.row.ID, "cancelled", "连接断开")
	}
}

// stream 主流程: 解析直链 → 建上游请求(透传 Range) → 续取循环回写。
func (p *proxy) stream(w http.ResponseWriter, r *http.Request, task *dlTask) error {
	pcspath := task.row.Path
	rng := strings.TrimSpace(r.Header.Get("Range"))
	startOffset := int64(0)
	if rng != "" {
		if s, ok := parseRangeStart(rng); ok {
			startOffset = s
		}
	}
	// 任务基线: Done 从浏览器请求的偏移起算
	p.q.update(task.row.ID, func(row *TaskRow) { row.Done = startOffset })

	var (
		resp   *http.Response
		cancel context.CancelFunc
		status int
		err    error
	)
	// B7: 上游对 Range 请求可能回 200(不支持 Range/直链降级)——此时须按全量重写,
	// 偏移重置为 0, 对浏览器回 200 + 完整 Content-Length, 否则数据错位。
	bodyOffset := startOffset
	for attempt := 0; ; attempt++ {
		// 直链解析(断流续取时重解一次, §5.1)
		dlink, ua, size, rerr := p.cfg.Resolve(r.Context(), pcspath)
		if rerr != nil {
			if attempt == 0 && p.cfg.ResolveFallback != nil {
				// 冒烟/本地桩源兜底(生产 ResolveFallback=nil 不生效)
				if fu, fua, fs, ferr := p.cfg.ResolveFallback(r.Context(), pcspath); ferr == nil {
					dlink, ua, size = fu, fua, fs
				}
			}
			if dlink == "" {
				if attempt == 0 {
					return errInternal("解析下载直链失败: " + rerr.Error())
				}
				return p.abortOr502(w, task, "重取直链失败: "+rerr.Error())
			}
		}
		p.q.update(task.row.ID, func(row *TaskRow) {
			if size > row.Total {
				row.Total = size
			}
		})

		resp, cancel, err = p.fetch(r.Context(), dlink, ua, rng, startOffset)
		if err == nil {
			break
		}
		if attempt >= resumeRetries {
			return p.abortOr502(w, task, "连接下载源失败: "+err.Error())
		}
	}
	// B7: 上游 200(非 206)→ 忽略请求偏移, 全量转发
	if resp.StatusCode != http.StatusPartialContent {
		bodyOffset = 0
	}

	// 首响应头提交(200 或 206)
	status = http.StatusOK
	if bodyOffset > 0 || resp.StatusCode == http.StatusPartialContent {
		status = http.StatusPartialContent
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Accept-Ranges", "bytes")
	h.Set("Content-Disposition", rfc5987Disposition(task.row.Name))
	if resp.ContentLength > 0 {
		h.Set("Content-Length", fmt.Sprint(resp.ContentLength))
	} else if size := p.totalOf(task); size > bodyOffset {
		h.Set("Content-Length", fmt.Sprint(size-bodyOffset))
	}
	w.WriteHeader(status)

	// io.Copy: 32MB 缓冲上限 + 限速钩子 + 断流续取
	sent := bodyOffset
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
		if cancel != nil {
			cancel() // B6: Body 关闭后再取消 fetch 的超时 ctx
		}
	}()
	copyBuf := make([]byte, 64<<10)
	if copyBufLimit < len(copyBuf) {
		copyBuf = copyBuf[:copyBufLimit]
	}
	// 取消传导: 用户 cancel → ctx 取消 → 拷贝循环退出(先于首读建立)
	copyCtx, copyCxl := context.WithCancel(r.Context())
	defer copyCxl()
	go func() {
		select {
		case <-task.done:
			copyCxl()
		case <-copyCtx.Done():
		}
	}()
	// 首块读取前即检查取消(小文件一次 Read 即 EOF, 循环内检查不够)
	if err := copyCtx.Err(); err != nil {
		p.q.finish(task.row.ID, "cancelled", "已取消")
		return err
	}
	for {
		if p.totalOf(task) > 0 {
			p.q.update(task.row.ID, func(row *TaskRow) { row.Done = sent })
		}
		n, copyErr := p.copyChunk(copyCtx, w, resp.Body, copyBuf, task)
		sent += n
		if copyErr == nil {
			p.q.finish(task.row.ID, "done", "")
			return nil
		}
		// 用户取消/浏览器断开: 置 cancelled 并返回
		if errors.Is(copyErr, context.Canceled) {
			p.q.finish(task.row.ID, "cancelled", "已取消")
			return copyErr
		}
		if werr, isW := copyErr.(*ProxyError); isW && werr.clientGone {
			p.q.finish(task.row.ID, "cancelled", "连接断开")
			return copyErr
		}
		// 上游断流 → 续取(bytes=sent-)
		if copyErr == errUpstreamReset || (n > 0 && copyErr != errClientGone) {
			if werr := p.resume(w, r, task, &sent, &resp, &cancel); werr == nil {
				continue
			} else {
				return werr
			}
		}
		return p.abortOr502(w, task, "下载中断: "+copyErr.Error())
	}
}

// errUpstreamReset 上游读错误(可续取)。
var errUpstreamReset = errors.New("upstream reset")

// errClientGone 浏览器侧断开(不可续取)。
var errClientGone = errors.New("client gone")

// copyChunk 限速拷贝一段; 返回字节数与错误。
func (p *proxy) copyChunk(ctx context.Context, w io.Writer, src io.Reader, buf []byte, task *dlTask) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := src.Read(buf)
		if n > 0 {
			if p.cfg.Rate != nil {
				if werr := p.cfg.Rate.WaitN(int64(n)); werr != nil {
					_, _ = w.Write(buf[:n]) // 限速取消前已读数据仍尽力写出
					total += int64(n)
					return total, werr
				}
			}
			if _, werr := w.Write(buf[:n]); werr != nil {
				return total, &ProxyError{Status: 499, Code: "INTERNAL", Msg: "浏览器断开", clientGone: true}
			}
			total += int64(n)
			p.q.update(task.row.ID, func(row *TaskRow) { row.Done += int64(n); row.Speed = speedOf(row, int64(n)) })
		}
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, errUpstreamReset
		}
	}
}

// resume 断流续取: 重解直链 + Range: bytes=sent- 续写(浏览器无感)。失败返回错误。
// B6: 重赋值前必须 Close 旧 Body 并保存新 fetch 的 cancel(B7: 200 回退重置偏移)。
func (p *proxy) resume(w http.ResponseWriter, r *http.Request, task *dlTask, sent *int64, resp **http.Response, cancel *context.CancelFunc) error {
	for attempt := 0; attempt < resumeRetries; attempt++ {
		dlink, ua, size, err := p.cfg.Resolve(r.Context(), task.row.Path)
		if err != nil {
			continue
		}
		nr, nc, err := p.fetch(r.Context(), dlink, ua, "", *sent)
		if err != nil {
			continue
		}
		if size > p.totalOf(task) {
			p.q.update(task.row.ID, func(row *TaskRow) { row.Total = size })
		}
		// B7: 上游对续取 Range 回 200 → 已写偏移作废, 只能强制断开(数据不可错位续写)
		if nr.StatusCode != http.StatusPartialContent && *sent > 0 {
			nr.Body.Close()
			nc()
			p.q.finish(task.row.ID, "failed", "断流续取失败: 上游不支持 Range")
			return fmt.Errorf("断流续取失败: 上游不支持 Range")
		}
		// B6: 旧 Body 关闭 + 旧 cancel 释放, 再换新连接
		(*resp).Body.Close()
		if *cancel != nil {
			(*cancel)()
		}
		*resp = nr
		*cancel = nc
		return nil
	}
	// 已提交响应头, 只能强制断开(浏览器按 Range 重试, §5.1)
	p.q.finish(task.row.ID, "failed", "断流续取失败")
	return fmt.Errorf("断流续取失败")
}

// fetch 用 NetdiskUA + 剥 Referer 请求直链(§0.3 上游事实)。
// rangeHeader 首请求透传浏览器原始 Range(§5.1); 续取传 "" 由 offset 拼构造。
// B6: 返回 cancel 供调用方在 Body 关闭后调用——若在此 defer cancel(),
// 函数返回即取消 ctx, 大文件 Body 读取中途报 context canceled。
func (p *proxy) fetch(ctx context.Context, dlink, ua, rangeHeader string, offset int64) (*http.Response, context.CancelFunc, error) {
	ctx, cxl := context.WithTimeout(ctx, upstreamTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlink, nil)
	if err != nil {
		cxl()
		return nil, nil, err
	}
	if ua == "" {
		ua = "netdisk" // 兜底: 直链必须 netdisk 系 UA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Del("Referer")
	rh := strings.TrimSpace(rangeHeader)
	if rh == "" && offset > 0 {
		rh = fmt.Sprintf("bytes=%d-", offset)
	}
	if rh != "" {
		req.Header.Set("Range", rh)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		cxl()
		return nil, nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		cxl()
		return nil, nil, fmt.Errorf("下载源 HTTP %d", resp.StatusCode)
	}
	return resp, cxl, nil
}

// abortOr502 收尾: 响应未提交回 502; 已提交只标记失败(连接交由浏览器重试)。
func (p *proxy) abortOr502(w http.ResponseWriter, task *dlTask, msg string) error {
	p.q.finish(task.row.ID, "failed", msg)
	return errInternal(msg)
}

// totalOf B10: 任务行 Total 锁内快照读(直接读 task.row.Total 与并发 update 竞争)。
func (p *proxy) totalOf(task *dlTask) int64 {
	if row, ok := p.q.get(task.row.ID); ok {
		return row.Total
	}
	return 0
}

// speedOf 简易速度估计(步进均值; SSE 侧 500ms 节流, 展示足够)。
func speedOf(row *TaskRow, chunk int64) int64 {
	now := time.Now().Unix()
	if row.UpAt == now {
		return row.Speed + chunk // 同秒累加
	}
	return chunk // 换秒重置(粗粒度)
}

// errInternal / badRequest handler 层错误(sentinel 分类, transfer_dl 映射状态码)。
func errInternal(msg string) error  { return &ProxyError{Status: 502, Code: "UPSTREAM_ERROR", Msg: msg} }
func badRequest(msg string) error   { return &ProxyError{Status: 400, Code: "INTERNAL", Msg: msg} }

// ProxyError 代理层错误(带建议 HTTP 状态码)。
type ProxyError struct {
	Status     int
	Code       string
	Msg        string
	clientGone bool // 浏览器侧断开标记(handler 不再写头)
}

func (e *ProxyError) Error() string { return e.Msg }

// parseUnix 解析 unix 秒。
func parseUnix(s string) (int64, error) {
	var v int64
	if s == "" {
		return 0, errors.New("空")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("非数字")
		}
		v = v*10 + int64(c-'0')
	}
	return v, nil
}

// parseRangeStart 解析 "bytes=N-" 起始偏移(不支持多段/后缀语义, 返回 ok=false 时按 0 全量)。
func parseRangeStart(h string) (int64, bool) {
	h = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(h), "bytes="))
	if i := strings.IndexByte(h, ','); i >= 0 {
		h = h[:i] // 多段只取首段(浏览器不会对单文件多段)
	}
	dash := strings.IndexByte(h, '-')
	if dash < 0 {
		return 0, false
	}
	start := strings.TrimSpace(h[:dash])
	if start == "" {
		return 0, false // 后缀语义 bytes=-N: 不映射为偏移
	}
	var v int64
	for _, c := range start {
		if c < '0' || c > '9' {
			return 0, false
		}
		v = v*10 + int64(c-'0')
	}
	return v, true
}

// rfc5987Disposition Content-Disposition(FR-11 验收④):
// ASCII 名仅 filename; 含非 ASCII 时补 filename*=UTF-8''<percent-encoded>。
func rfc5987Disposition(name string) string {
	ascii := name
	hasNonASCII := false
	for i := 0; i < len(ascii); i++ {
		if ascii[i] > 0x7e || ascii[i] < 0x20 || ascii[i] == '"' || ascii[i] == '\\' {
			hasNonASCII = true
			ascii = ascii[:i] + "_" + ascii[i+1:]
		}
	}
	if ascii == "" {
		ascii = "download"
	}
	if !hasNonASCII {
		return `attachment; filename="` + ascii + `"`
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}
