// Package xengine 内部实现: 上游 baidupcs 适配(唯一 import 上游库的文件集)。
//
// 拆分: pcs.go = Engine 接口实现主体; accounts = 凭证 JSON 持久化(accounts.json 0600);
// upload.go = 上传引擎组装; transfer.go = FR-28/FR-40 分享转存编排(W7 分享域对接时补齐)。
package xengine

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	baidupcs "github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
	"github.com/qjfoidnh/BaiduPCS-Go/requester"
)

// pcsEngine Engine 接口实现。
// 线程安全: accounts/pool 由 mu 保护; BaiduPCS 实例并发安全性以"每 uid 独立实例"隔离。
type pcsEngine struct {
	mu        sync.RWMutex
	store     *accountStore
	pool      map[uint64]*baidupcs.BaiduPCS // 凭证池 map[uid]*BaiduPCS (§0.6)
	activeUID uint64
	cfg       TransConfig
}

// TransConfig 构造 BaiduPCS 时逐字段 Set* 应用的传输配置(§0.5, 来自本项目 config 白名单子集)。
type TransConfig struct {
	AppID             int // 默认 DefaultAppID(266719)
	EnableHTTPS       bool
	UserAgent         string // 空则跟随上游 requester.UserAgent 默认
	MaxUploadParallel int
	MaxUploadRate     int64
	UPolicy           string // skip|overwrite|rsync
}

// defaultTransConfig 对齐上游 pcsconfig.InitDefaultConfig(§0.5)。
func defaultTransConfig() TransConfig {
	return TransConfig{
		AppID:             DefaultAppID,
		EnableHTTPS:       true,
		MaxUploadParallel: 4,
		UPolicy:           baidupcs.SkipPolicy,
	}
}

// New 创建引擎; dataDir 为数据目录(accounts.json 落位处); cfg 可为零值(取默认)。
func New(dataDir string, cfg TransConfig) (Engine, error) {
	if cfg.AppID == 0 {
		cfg.AppID = DefaultAppID
	}
	if cfg.UPolicy == "" {
		cfg.UPolicy = baidupcs.SkipPolicy
	}
	if cfg.MaxUploadParallel <= 0 {
		cfg.MaxUploadParallel = 4
	}
	store, err := loadAccountStore(dataDir)
	if err != nil {
		return nil, err
	}
	e := &pcsEngine{
		store: store,
		pool:  make(map[uint64]*baidupcs.BaiduPCS),
		cfg:   cfg,
	}
	if len(store.list) > 0 {
		// 优先恢复持久化的 activeUID; 失效则取首个账号
		ok := false
		for _, a := range store.list {
			if a.UID == store.activeUID {
				ok = true
				break
			}
		}
		if ok {
			e.activeUID = store.activeUID
		} else {
			e.activeUID = store.list[0].UID
		}
	}
	return e, nil
}

// NewWithStore 注入已有凭证存储(测试桩用)。
func NewWithStore(store *accountStore, cfg TransConfig) *pcsEngine {
	if cfg.AppID == 0 {
		cfg.AppID = DefaultAppID
	}
	if cfg.UPolicy == "" {
		cfg.UPolicy = baidupcs.SkipPolicy
	}
	return &pcsEngine{store: store, pool: make(map[uint64]*baidupcs.BaiduPCS), cfg: cfg}
}

// ApplyConfig 传输配置热更新: 清空实例池, 下次调用按新配置重建(§5.6 进行中任务不受影响)。
func (e *pcsEngine) ApplyConfig(cfg TransConfig) {
	if cfg.AppID == 0 {
		cfg.AppID = DefaultAppID
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
	e.pool = make(map[uint64]*baidupcs.BaiduPCS)
}

// buildPCS 按凭证构造 BaiduPCS(仿上游 pcsconfig.Baidu.BaiduPCS(), §0.2/§0.6)。
func (e *pcsEngine) buildPCS(a *Account) *baidupcs.BaiduPCS {
	var pcs *baidupcs.BaiduPCS
	switch {
	case a.Cookie != "":
		pcs = baidupcs.NewPCSWithCookieStr(e.cfg.AppID, a.Cookie)
	default:
		pcs = baidupcs.NewPCS(e.cfg.AppID, a.BDUSS)
		if a.STOKEN != "" {
			pcs.SetStoken(a.STOKEN)
		}
	}
	pcs.SetHTTPS(e.cfg.EnableHTTPS)
	if e.cfg.UserAgent != "" {
		pcs.SetPCSUserAgent(e.cfg.UserAgent)
		pcs.SetPanUserAgent(e.cfg.UserAgent)
	}
	pcs.SetUID(a.UID)
	applyV4FirstDial(pcs)
	return pcs
}

// v4FirstDial 环境自愈: 上游 requester/dial.go 的 resolveTCPHost 只取 DNS 结果
// addrs[0] 拨号, 而 DNS 常把 IPv6 排前; 若本机 IPv6 到百度实际不通(有地址无路由/
// 防火墙丢包), 每次百度调用都会死等到超时, 表现为"网络错误、列表加载不出"。
// 此处包装上游 Transport 的 DialContext: 先探测 IPv6 可用性(一次性, 3s 预算),
// 不可用则强制 IPv4 优先; 也可经 BPW_FORCE_V4=1 强制。
// 不改上游库; 对 IPv6 正常的环境零影响。
var v4FirstOnce sync.Once

func applyV4FirstDial(pcs *baidupcs.BaiduPCS) {
	v4FirstOnce.Do(func() {
		if os.Getenv("BPW_FORCE_V4") == "1" {
			forceV4Dial = true
		} else {
			forceV4Dial = !probeV6Reachable("pan.baidu.com")
		}
		if forceV4Dial {
			// 用上游官方钩子预填 DNS 缓存(requester.SetTCPHostBind → tcpCache),
			// 使 dialContext 直取 IPv4, 绕过"addrs[0]=v6 不通死等"问题。
			baiduHosts := []string{"pan.baidu.com", "pcs.baidu.com", "d.pcs.baidu.com", "baidu.com"}
			for _, host := range baiduHosts {
				if ip := lookupV4(host); ip != "" {
					requester.SetTCPHostBind(host, ip)
				}
			}
			log.Println("[INFO] IPv6 到百度不可达, 已强制 IPv4 直连(BPW_FORCE_V4=1 可显式指定)")
		}
	})
	_ = pcs
}

var forceV4Dial bool

func probeV6Reachable(host string) bool {
	addrs, err := net.LookupHost(host)
	if err != nil {
		return false
	}
	var v6 string
	for _, a := range addrs {
		if strings.Contains(a, ":") {
			v6 = a
			break
		}
	}
	if v6 == "" {
		return false // 无 v6 记录, 无需特殊处理
	}
	conn, err := net.DialTimeout("tcp6", net.JoinHostPort(v6, "443"), 3*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// lookupV4 返回 host 的首个 A 记录, 失败返回空串。
func lookupV4(host string) string {
	ctx, cxl := context.WithTimeout(context.Background(), 5*time.Second)
	defer cxl()
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return ""
	}
	for _, ip := range ips {
		if ip.IP.To4() != nil {
			return ip.IP.String()
		}
	}
	return ""
}

// pcsFor 取(或建)uid 对应的 BaiduPCS 实例。调用方持 e.mu.RLock 时不可重入加写锁,
// 故统一用全锁简化; 调用频度低(每账号一次构造)。
func (e *pcsEngine) pcsFor(uid uint64) (*baidupcs.BaiduPCS, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pcsForLocked(uid)
}

func (e *pcsEngine) pcsForLocked(uid uint64) (*baidupcs.BaiduPCS, error) {
	a := e.store.get(uid)
	if a == nil {
		return nil, &Error{Code: ErrNotFound, Message: "账号不存在"}
	}
	if pcs, ok := e.pool[uid]; ok {
		return pcs, nil
	}
	pcs := e.buildPCS(a)
	e.pool[uid] = pcs
	return pcs, nil
}

// active 获取当前活跃账号的 pcs 实例; 无账号时报错。
// 独立成方法便于测试桩覆写(stubEngine.active)。
func (e *pcsEngine) active() (*baidupcs.BaiduPCS, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeUID == 0 {
		return nil, &Error{Code: ErrAuth, Message: "尚未登录任何百度账号"}
	}
	// B1: 经 pcsForLocked 走 store.get 完整账号路径构造(空凭证实例会导致请求无登录态)。
	return e.pcsForLocked(e.activeUID)
}

// mapErr pcserror.Error → *Error(§0.4 映射表, 单向映射集中此处)。
func mapErr(pcsError pcserror.Error) error {
	if pcsError == nil {
		return nil
	}
	e := &Error{Err: pcsError, Message: pcsError.Error()}
	switch pcsError.GetErrType() {
	case pcserror.ErrTypeNetError:
		e.Code = ErrNet
		e.Message = "网络错误, 请稍后重试"
	case pcserror.ErrTypeRemoteError:
		e.Code = ErrRemote
		e.RemoteCode = pcsError.GetRemoteErrCode()
		switch e.RemoteCode {
		case 4, -6:
			e.Code = ErrAuth
			e.Message = "登录凭证已失效, 请重新登录"
		case 31066, 112:
			e.Code = ErrNotFound
			e.Message = "文件或目录不存在"
		case 31112:
			e.Code = ErrQuota
			e.Message = "网盘容量已满"
		case -30:
			e.Code = ErrForbiddenTransfer
			e.Message = "目标目录已存在同名文件, 转存被拒"
		case -9:
			e.Code = ErrBadPasscode
			e.Message = "提取码错误"
		case 105:
			e.Code = ErrNotFound
			e.Message = "分享链接错误或已失效"
		case -7, -21:
			e.Code = ErrNotFound
			e.Message = "分享已删除、取消或无效"
		case 31061, 114514, 1919810:
			// 策略语义(重名/skip 命中/rsync 命中), 保留原文交上层按策略处理
			e.Code = ErrRemote
			e.Message = pcsError.Error()
		case 31363:
			e.Code = ErrRemote
			e.Message = "上传状态过期, 可重试"
		case 31626:
			e.Code = ErrRemote
			e.Message = "下载请求被拒绝(UA 校验), 可重试"
		default:
			// 远端未知错误: 原文记日志用, 不透出内部细节之外的内容
			e.Message = pcsError.Error()
		}
	default:
		// Internal/JSONParse/Others → ErrInternal(原文记日志, 不透传浏览器)
		e.Code = ErrInternal
	}
	return e
}

func wrapErr(code ErrCode, format string, a ...interface{}) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// ---------------------------------------------------------------------------
// 账号管理 FR-01~04
// ---------------------------------------------------------------------------

func (e *pcsEngine) AddAccountByCookie(cookie string) (*Account, error) {
	if strings.TrimSpace(cookie) == "" {
		return nil, wrapErr(ErrInternal, "Cookie 不能为空")
	}
	// uid 优先取 Cookie 内 BDUSS 关联值; 无网络探测时以 Cookie 串稳定哈希为 uid 兜底,
	// 保证同凭证重复添加幂等(§0.6 NewUserInfoByInput 兜底思路的离线等价)。
	uid, name := deriveUIDFromCookie(cookie)
	return e.putAccount(&Account{UID: uid, Name: name, Cookie: cookie, Workdir: "/"})
}

func (e *pcsEngine) AddAccountByBDUSS(bduss, stoken string) (*Account, error) {
	if strings.TrimSpace(bduss) == "" {
		return nil, wrapErr(ErrInternal, "BDUSS 不能为空")
	}
	if strings.TrimSpace(stoken) == "" {
		// FR-01: BDUSS 方式 stoken 必填(分享/秒传需要, §0.2)
		return nil, wrapErr(ErrInternal, "STOKEN 必填(BDUSS 方式登录)")
	}
	uid := deriveUIDFromBDUSS(bduss)
	return e.putAccount(&Account{UID: uid, Name: fmt.Sprintf("baidu_%d", uid), BDUSS: bduss, STOKEN: stoken, Workdir: "/"})
}

func (e *pcsEngine) putAccount(a *Account) (*Account, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if old := e.store.get(a.UID); old != nil {
		a.Workdir = old.Workdir // 保留既有工作目录
	}
	if err := e.store.upsert(a); err != nil {
		return nil, wrapErr(ErrInternal, "保存账号凭证: %v", err)
	}
	e.activeUID = a.UID // 新增账号即切换为活跃(与上游 SetupUserByBDUSS 行为一致)
	delete(e.pool, a.UID)
	return a, nil
}

func (e *pcsEngine) Accounts() []Account {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Account, 0, len(e.store.list))
	for _, a := range e.store.list {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UID < out[j].UID })
	return out
}

func (e *pcsEngine) ActiveAccount() (Account, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if a := e.store.get(e.activeUID); a != nil {
		return *a, true
	}
	return Account{}, false
}

func (e *pcsEngine) SwitchAccount(uid uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.store.get(uid) == nil {
		return wrapErr(ErrNotFound, "账号 %d 不存在", uid)
	}
	e.activeUID = uid
	return e.store.setActive(uid)
}

func (e *pcsEngine) RemoveAccount(uid uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.store.get(uid) == nil {
		return wrapErr(ErrNotFound, "账号 %d 不存在", uid)
	}
	if err := e.store.remove(uid); err != nil {
		return wrapErr(ErrInternal, "删除账号凭证: %v", err)
	}
	delete(e.pool, uid)
	if e.activeUID == uid {
		e.activeUID = 0
		if len(e.store.list) > 0 {
			e.activeUID = e.store.list[0].UID
			_ = e.store.setActive(e.activeUID)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// 配额 FR-05
// ---------------------------------------------------------------------------

func (e *pcsEngine) Quota(ctx context.Context) (quota, used int64, err error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return 0, 0, err
	}
	done := make(chan struct{})
	var pcsErr pcserror.Error
	go func() {
		defer close(done)
		quota, used, pcsErr = pcs.QuotaInfo()
	}()
	select {
	case <-ctx.Done():
		return 0, 0, &Error{Code: ErrNet, Message: "请求配额超时或被取消", Err: ctx.Err()}
	case <-done:
	}
	if pcsErr != nil {
		return 0, 0, mapErr(pcsErr)
	}
	return quota, used, nil
}

// ---------------------------------------------------------------------------
// 文件域 FR-06~10, 21~24, 35
// ---------------------------------------------------------------------------

func (e *pcsEngine) List(ctx context.Context, dir string, page, pageSize int, orderBy, order string) (FileDirectoryList, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	opts := orderOptions(orderBy, order)
	// 上游 v4.0.2 FilesDirectoriesList 内置整目录循环分页(§0.3);
	// Web 端 page/pageSize 在本层做内存切片(目录通常 ≤1000 条, 一次拉取即可)。
	fdl, pcsErr := pcs.FilesDirectoriesList(dir, opts)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	return sliceFDList(fdl, page, pageSize), nil
}

// orderOptions 排序参数映射(非法值回落 name/asc, 与上游 DefaultOrderOptions 一致)。
func orderOptions(by, order string) *baidupcs.OrderOptions {
	opts := &baidupcs.OrderOptions{By: baidupcs.OrderByName, Order: baidupcs.OrderAsc}
	switch baidupcs.OrderBy(by) {
	case baidupcs.OrderByName:
		opts.By = baidupcs.OrderByName
	case baidupcs.OrderByTime:
		opts.By = baidupcs.OrderByTime
	case baidupcs.OrderBySize:
		opts.By = baidupcs.OrderBySize
	}
	if baidupcs.Order(order) == baidupcs.OrderDesc {
		opts.Order = baidupcs.OrderDesc
	}
	return opts
}

// sliceFDList 上游 FileDirectoryList → 镜像类型 + page/pageSize 切片(page 从 1 起, ≤0 取全量)。
func sliceFDList(fdl baidupcs.FileDirectoryList, page, pageSize int) FileDirectoryList {
	mirrored := make(FileDirectoryList, 0, len(fdl))
	for _, fd := range fdl {
		mirrored = append(mirrored, mirrorFD(fd))
	}
	if page <= 0 || pageSize <= 0 {
		return mirrored
	}
	start := (page - 1) * pageSize
	if start >= len(mirrored) {
		return FileDirectoryList{}
	}
	end := start + pageSize
	if end > len(mirrored) {
		end = len(mirrored)
	}
	return mirrored[start:end]
}

func mirrorFD(fd *baidupcs.FileDirectory) *FileDirectory {
	return &FileDirectory{
		FsID:     fd.FsID,
		Path:     fd.Path,
		Filename: fd.Filename,
		Ctime:    fd.Ctime,
		Mtime:    fd.Mtime,
		MD5:      fd.MD5,
		Size:     fd.Size,
		Isdir:    fd.Isdir,
		HasSub:   fd.Ifhassubdir,
	}
}

func (e *pcsEngine) Meta(ctx context.Context, p string) (*FileDirectory, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	fd, pcsErr := pcs.FilesDirectoriesMeta(p)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	return mirrorFD(fd), nil
}

func (e *pcsEngine) Search(ctx context.Context, dir, keyword string, recursive bool) (FileDirectoryList, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	fdl, pcsErr := pcs.Search(dir, keyword, recursive)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	out := make(FileDirectoryList, 0, len(fdl))
	for _, fd := range fdl {
		out = append(out, mirrorFD(fd))
	}
	return out, nil
}

func (e *pcsEngine) Mkdir(ctx context.Context, p string) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	return mapErr(pcs.Mkdir(p))
}

func (e *pcsEngine) Remove(ctx context.Context, paths []string) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	return mapErr(pcs.Remove(paths...))
}

func (e *pcsEngine) Copy(ctx context.Context, from []string, toDir string) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	jsons := make([]*baidupcs.CpMvJSON, 0, len(from))
	for _, f := range from {
		jsons = append(jsons, &baidupcs.CpMvJSON{From: f, To: path.Join(toDir, path.Base(f))})
	}
	return mapErr(pcs.Copy(jsons...))
}

func (e *pcsEngine) Move(ctx context.Context, from []string, toDir string) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	jsons := make([]*baidupcs.CpMvJSON, 0, len(from))
	for _, f := range from {
		jsons = append(jsons, &baidupcs.CpMvJSON{From: f, To: path.Join(toDir, path.Base(f))})
	}
	return mapErr(pcs.Move(jsons...))
}

func (e *pcsEngine) Rename(ctx context.Context, from, to string) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	return mapErr(pcs.Rename(from, to))
}

func (e *pcsEngine) Tree(ctx context.Context, dir string, maxDepth int) (FileDirectoryList, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	opts := &baidupcs.OrderOptions{By: baidupcs.OrderByName, Order: baidupcs.OrderAsc}
	var out FileDirectoryList
	_ = pcs.FilesDirectoriesRecurseList(dir, opts, func(depth int, fdPath string, fd *baidupcs.FileDirectory, pcsError pcserror.Error) bool {
		if pcsError != nil {
			return false // 出错即停(简化: 树视图不吞错)
		}
		if fd != nil && fd.Path != dir {
			out = append(out, mirrorFD(fd))
		}
		if maxDepth > 0 && depth >= maxDepth {
			return false
		}
		return true
	})
	return out, nil
}

func (e *pcsEngine) Match(ctx context.Context, pattern string) ([]string, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	paths, pcsErr := pcs.MatchPathByShellPattern(pattern)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	return paths, nil
}

// ---------------------------------------------------------------------------
// 下载 FR-11 / 秒传导出 FR-13
// ---------------------------------------------------------------------------

func (e *pcsEngine) ResolveDlink(ctx context.Context, pcspath string) (*DownloadLink, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	info, pcsErr := pcs.LocateDownload(pcspath)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	u := info.SingleURL(e.cfg.EnableHTTPS)
	if u == nil {
		return nil, wrapErr(ErrInternal, "未获取到可用下载链接")
	}
	size := int64(0)
	if fd, err := pcs.FilesDirectoriesMeta(pcspath); err == nil && fd != nil {
		size = fd.Size
	}
	return &DownloadLink{
		URL:    u.String(),
		UA:     baidupcs.NetdiskUA, // dlink 必须带 netdisk UA(§0.2/§5.1)
		Expire: time.Now().Add(8 * time.Hour),
		Size:   size,
	}, nil
}

func (e *pcsEngine) Export(ctx context.Context, pcspath string) (*RapidLink, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	fd, pcsErr := pcs.FilesDirectoriesMeta(pcspath)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	rinfo, pcsErr := pcs.ExportByFileInfo(fd)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	return &RapidLink{
		ContentMD5: rinfo.ContentMD5,
		SliceMD5:   rinfo.SliceMD5,
		Crc32:      rinfo.ContentCrc32,
		Length:     rinfo.ContentLength,
		Filename:   rinfo.Filename,
	}, nil
}

// ---------------------------------------------------------------------------
// 上传 FR-15/16(实现在 upload_impl.go)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// 分享/转存 FR-25~28, FR-40
// ---------------------------------------------------------------------------

func (e *pcsEngine) ShareSet(ctx context.Context, paths []string, password string, period int) (*Shared, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	s, pcsErr := pcs.ShareSet(paths, &baidupcs.ShareOption{Password: password, Period: period, IsCombined: false})
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	return &Shared{Link: s.Link, Pwd: s.Pwd, ShareID: s.ShareID}, nil
}

func (e *pcsEngine) ShareList(ctx context.Context, page int) (ShareRecordList, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	records, pcsErr := pcs.ShareList(page)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	out := make(ShareRecordList, 0, len(records))
	for _, r := range records {
		out = append(out, &ShareRecord{
			ShareID:     r.ShareID,
			TypicalPath: r.TypicalPath,
			Shortlink:   r.Shortlink,
			Status:      r.Status,
			ExpireType:  r.ExpireType,
			ExpireTime:  r.ExpireTime,
			ViewCount:   r.ViewCount,
		})
	}
	return out, nil
}

func (e *pcsEngine) ShareCancel(ctx context.Context, shareIDs []int64) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	return mapErr(pcs.ShareCancel(shareIDs))
}

// ParseShareLink FR-28: 从口令文本抽取链接与提取码(正则 1:1 上游 RunShareTransfer)。
var shareLinkRe = regexp.MustCompile(`(https?://pan\.baidu\.com/s/[0-9A-Za-z_-]+)(?:\?pwd=([0-9A-Za-z]{4}))?`)

func (e *pcsEngine) ParseShareLink(rawText string) (link, passcode string, err error) {
	m := shareLinkRe.FindStringSubmatch(rawText)
	if m == nil {
		return "", "", wrapErr(ErrInternal, "未找到合法的百度网盘分享链接")
	}
	link = m[1]
	if len(m) >= 3 {
		passcode = m[2]
	}
	// 文本中显式标注的提取码(如 "提取码: ab12")优先级低于 ?pwd=, 高于空值
	if passcode == "" {
		if pc := extractPasscodeText(rawText); pc != "" {
			passcode = pc
		}
	}
	return link, passcode, nil
}

var passcodeTextRe = regexp.MustCompile(`(?:提取码|密码|访问码)[:：\s]*([0-9A-Za-z]{4})`)

func extractPasscodeText(s string) string {
	if m := passcodeTextRe.FindStringSubmatch(s); len(m) >= 2 {
		return m[1]
	}
	return ""
}

// shareFeature 抽 featureStr(路径 base; 短链 "init" 时取 "1"+surl)。
func shareFeature(link string) (feature string, err error) {
	u, uerr := url.Parse(link)
	if uerr != nil {
		return "", wrapErr(ErrInternal, "链接格式非法")
	}
	feature = path.Base(strings.TrimSuffix(u.Path, "/"))
	if feature == "init" {
		feature = "1" + u.Query().Get("surl")
	}
	if len(feature) > 23 || !strings.HasPrefix(feature, "1") {
		return "", wrapErr(ErrInternal, "链接地址非法")
	}
	return feature, nil
}

// shareVerifyAndList FR-40 编排 ①~⑦(1:1 上游 RunShareTransfer 时序, 不自创顺序):
// AccessSharePage → verify(提取码) → UpdatePCSCookies(true) → 再次 AccessSharePage → list。
func (e *pcsEngine) shareVerifyAndList(link, passcode string) (metas map[string]string, feature string, err error) {
	pcs, err := e.active()
	if err != nil {
		return nil, "", err
	}
	feature, err = shareFeature(link)
	if err != nil {
		return nil, "", err
	}
	if len(passcode) != 4 {
		return nil, "", wrapErr(ErrBadPasscode, "提取码非法(须为 4 位)")
	}

	tokens := pcs.AccessSharePage(feature, true)
	if tokens["ErrMsg"] != "0" {
		return nil, "", wrapErr(ErrNotFound, "分享页访问失败: %s", tokens["ErrMsg"])
	}

	verifyURL := pcs.GenerateShareQueryURL("verify", map[string]string{
		"shareid":    tokens["shareid"],
		"time":       strconv.FormatInt(time.Now().UnixMilli(), 10),
		"clienttype": "1",
		"uk":         tokens["share_uk"],
	}).String()
	res := pcs.PostShareQuery(verifyURL, link, map[string]string{
		"pwd":       passcode,
		"vcode":     "null",
		"vcode_str": "null",
		"bdstoken":  tokens["bdstoken"],
	})
	if res["ErrMsg"] != "0" {
		if strings.Contains(res["ErrMsg"], "提取码错误") {
			return nil, "", wrapErr(ErrBadPasscode, "提取码错误")
		}
		return nil, "", wrapErr(ErrForbiddenTransfer, "分享校验失败: %s", res["ErrMsg"])
	}

	pcs.UpdatePCSCookies(true)

	tokens = pcs.AccessSharePage(feature, false)
	if tokens["ErrMsg"] != "0" {
		return nil, "", wrapErr(ErrNotFound, "分享页二次访问失败: %s", tokens["ErrMsg"])
	}
	queryURL := pcs.GenerateShareQueryURL("list", map[string]string{
		"bdstoken": tokens["bdstoken"],
		"root":     "1",
		"web":      "5",
		"app_id":   baidupcs.PanAppID,
		"shorturl": feature[1:],
		"channel":  "chunlei",
	}).String()
	metas = pcs.ExtractShareInfo(queryURL, tokens["shareid"], tokens["share_uk"], tokens["bdstoken"])
	if metas["ErrMsg"] != "success" {
		return nil, "", wrapErr(ErrNotFound, "获取分享内容失败: %s", metas["ErrMsg"])
	}
	return metas, feature, nil
}

// shareMetaItem share/list 单条元数据(gjson 抽取)。
type shareMetaItem struct {
	FsID      string
	Filename  string
	Size      int64
	Isdir     bool
	SizeKnown bool
}

// shareListMetas 按上游 /share/list 接口(root=0, 单 fs_id)逐个补 server_filename:
// ExtractShareInfo 只回 list.0.server_filename 与 fs_id 数组, 单文件分享可从中取文件名;
// 多文件时对每个 fs_id 单独查询(该接口 root=0+fs_id 过滤), 失败即跳过回退 fs_id 占位。
// 注意: 上游分享域接口不回文件大小, Size 恒 0 且 SizeKnown=false(不伪造, FR-40/B4)。
func (e *pcsEngine) shareListMetas(pcs *baidupcs.BaiduPCS, tokens map[string]string, feature, fsIDs string) []shareMetaItem {
	out := make([]shareMetaItem, 0, 8)
	for _, id := range strings.Split(strings.Trim(fsIDs, "[]"), ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		queryURL := pcs.GenerateShareQueryURL("list", map[string]string{
			"bdstoken": tokens["bdstoken"],
			"root":     "0",
			"web":      "5",
			"app_id":   baidupcs.PanAppID,
			"shorturl": feature[1:],
			"fs_id":    id,
			"channel":  "chunlei",
		}).String()
		res := pcs.ExtractShareInfo(queryURL, tokens["shareid"], tokens["share_uk"], tokens["bdstoken"])
		if res["ErrMsg"] != "success" || res["filename"] == "" {
			continue
		}
		out = append(out, shareMetaItem{FsID: id, Filename: res["filename"], SizeKnown: false})
	}
	return out
}

// SharePreview FR-40 浏览分享内容(复用编排 ①~⑦)。
// 元数据来源: 上游 ExtractShareInfo 只回 list.0.server_filename 与 fs_id 数组——
// 单文件分享直接取该 server_filename; 多文件时逐 fs_id 再查 /share/list(root=0)
// 取 server_filename。大小: 上游分享域接口均不回文件大小, Size 恒 0 + SizeKnown=false
// (配额预检按 0 保守计, 如实注明不伪造, FR-40/B4)。
func (e *pcsEngine) SharePreview(ctx context.Context, link, passcode string) (SharePreviewResult, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return SharePreviewResult{}, err
	}
	metas, feature, err := e.shareVerifyAndList(link, passcode)
	if err != nil {
		return SharePreviewResult{}, err
	}
	out := SharePreviewResult{ShareID: metas["shareid"], UK: metas["from"]}
	// 上游 ExtractShareInfo 顺带回填的 filename(list.0.server_filename, 单文件有效)
	single := metas["filename"]
	fsids := strings.Split(strings.Trim(metas["fs_id"], "[]"), ",")
	out.Items = make([]SharePreviewItem, 0, len(fsids))
	named := make(map[string]string, len(fsids))
	if len(fsids) == 1 && single != "" {
		named[strings.TrimSpace(fsids[0])] = single
	} else if len(fsids) > 1 {
		toks := map[string]string{"shareid": metas["shareid"], "share_uk": metas["from"], "bdstoken": metas["bdstoken"]}
		for _, m := range e.shareListMetas(pcs, toks, feature, metas["fs_id"]) {
			named[m.FsID] = m.Filename
		}
	}
	for _, id := range fsids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		it := SharePreviewItem{FsID: id, SizeKnown: false} // 大小: 上游不吐, 恒 0(不伪造)
		if name, ok := named[id]; ok && name != "" {
			it.Filename = name
		} else {
			it.Filename = id // 上游不吐名称时以 fs_id 占位(不伪造)
		}
		// B5: preview 阶段文件尚未转存, 网盘路径不存在, Path 保持留空;
		// 转存后路径见 TransferToTmp(经 /api/share/dl 响应 files 回传前端签名直下)。
		out.Items = append(out.Items, it)
	}
	_ = feature
	return out, nil
}

// filterFsIDs 指定 fsids 时过滤转存清单; 空=全量。
func filterFsIDs(all string, fsids []string) string {
	if len(fsids) == 0 {
		return all
	}
	want := make(map[string]struct{}, len(fsids))
	for _, id := range fsids {
		want[strings.TrimSpace(id)] = struct{}{}
	}
	var keep []string
	for _, id := range strings.Split(strings.Trim(all, "[]"), ",") {
		id = strings.TrimSpace(id)
		if _, ok := want[id]; ok {
			keep = append(keep, id)
		}
	}
	return "[" + strings.Join(keep, ",") + "]"
}

// transferTarget FR-28/FR-40 共用的转存第⑧步参数(W8 复用口: targetDir 与 afterAction 参数化)。
type transferTarget struct {
	// TargetDir 转存目标目录(FR-28=当前目录; FR-40=/来自分享/.tmp-<shareid>)。
	TargetDir string
	// MkdirFirst POST 前先建目录(FR-40 语义; FR-28 为 false——上游 RunShareTransfer
	// 在 item_num==1 时不建目录, 不自创步骤)。
	MkdirFirst bool
	// FilterFsIDs 指定 fs_id 清单(空=全量, 覆盖 metas["fs_id"])。
	FilterFsIDs []string
}

// postTransferShare 编排第 ⑧ 步(1:1 上游 RunShareTransfer 收尾时序):
// fs_id 覆盖 → path 赋值 → referer 拼接 → UpdatePCSCookies(true) → GenerateRequestQuery POST。
// 返回上游 resp(filenames 等字段供调用方消费)。
func (e *pcsEngine) postTransferShare(metas map[string]string, feature string, tgt transferTarget) (map[string]string, error) {
	pcs, err := e.active()
	if err != nil {
		return nil, err
	}
	if len(tgt.FilterFsIDs) > 0 {
		metas["fs_id"] = filterFsIDs(metas["fs_id"], tgt.FilterFsIDs)
	}
	if tgt.MkdirFirst {
		if err := pcs.Mkdir(tgt.TargetDir); err != nil {
			// 目录已存在(-8/31061)可复用; 其余报错交由转存步骤给出最终裁决
			_ = mapErr(err)
		}
	}
	metas["path"] = tgt.TargetDir
	metas["referer"] = "https://pan.baidu.com/s/" + feature
	pcs.UpdatePCSCookies(true)
	resp := pcs.GenerateRequestQuery("POST", metas)
	if resp["ErrNo"] != "0" {
		return nil, wrapErr(ErrForbiddenTransfer, "转存失败: %s", resp["ErrMsg"])
	}
	return resp, nil
}

// Transfer FR-28: 转存到 targetDir(编排第 ⑧ 步 GenerateRequestQuery POST)。
// targetDir 与 afterAction 已参数化(postTransferShare/transferTarget), 为 W8 直连下载留口。
func (e *pcsEngine) Transfer(ctx context.Context, link, passcode, targetDir string, fsids []string) error {
	metas, feature, err := e.shareVerifyAndList(link, passcode)
	if err != nil {
		return err
	}
	_, err = e.postTransferShare(metas, feature, transferTarget{TargetDir: targetDir, FilterFsIDs: fsids})
	return err
}

// TransferToTmp FR-40: 转存到 /来自分享/.tmp-<shareid> 临时目录。
func (e *pcsEngine) TransferToTmp(ctx context.Context, link, passcode string, fsids []string) (*TmpTransfer, error) {
	metas, feature, err := e.shareVerifyAndList(link, passcode)
	if err != nil {
		return nil, err
	}
	shareID := metas["shareid"]
	targetDir := path.Join("/来自分享", ".tmp-"+shareID)
	resp, err := e.postTransferShare(metas, feature, transferTarget{
		TargetDir:   targetDir,
		MkdirFirst:  true,
		FilterFsIDs: fsids,
	})
	if err != nil {
		// B3: MkdirFirst 可能已建 /来自分享/.tmp-<shareid>(孤儿目录风险)。
		// 返回非空 TargetDir, 编排器失败分支(sharetmp target!="" 即 RemoveFn 回删)据此清理。
		return &TmpTransfer{ShareID: shareID, TargetDir: targetDir}, err
	}
	files := make([]SharePreviewItem, 0, 4)
	for _, name := range strings.Split(resp["filenames"], ",") {
		if name = strings.TrimSpace(name); name != "" {
			files = append(files, SharePreviewItem{Path: path.Join(targetDir, name), Filename: name})
		}
	}
	return &TmpTransfer{ShareID: shareID, TargetDir: targetDir, Files: files}, nil
}

// ---------------------------------------------------------------------------
// 离线 FR-29/30
// ---------------------------------------------------------------------------
func (e *pcsEngine) OfflineAdd(ctx context.Context, sourceURLs []string, savePath string) ([]OfflineTask, []error) {
	tasks := make([]OfflineTask, 0, len(sourceURLs))
	errs := make([]error, 0, len(sourceURLs))
	for _, u := range sourceURLs {
		task, err := func() (OfflineTask, error) {
			pcs, err := e.activeFor(ctx)
			if err != nil {
				return OfflineTask{}, err
			}
			id, pcsErr := pcs.CloudDlAddTask(u, savePath)
			if pcsErr != nil {
				return OfflineTask{SourceURL: u}, mapErr(pcsErr)
			}
			return OfflineTask{TaskID: id, SourceURL: u, SavePath: savePath}, nil
		}()
		tasks = append(tasks, task)
		errs = append(errs, err)
	}
	return tasks, errs
}

func (e *pcsEngine) OfflineList(ctx context.Context) (OfflineTaskList, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	cl, pcsErr := pcs.CloudDlListTask()
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	return mirrorCloudDl(cl), nil
}

func (e *pcsEngine) OfflineQuery(ctx context.Context, taskIDs []int64) (OfflineTaskList, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	cl, pcsErr := pcs.CloudDlQueryTask(taskIDs)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	return mirrorCloudDl(cl), nil
}

func mirrorCloudDl(cl baidupcs.CloudDlTaskList) OfflineTaskList {
	out := make(OfflineTaskList, 0, len(cl))
	for _, t := range cl {
		out = append(out, &OfflineTask{
			TaskID:       t.TaskID,
			Status:       t.Status,
			StatusText:   t.StatusText,
			FileSize:     t.FileSize,
			FinishedSize: t.FinishedSize,
			SavePath:     t.SavePath,
			SourceURL:    t.SourceURL,
			TaskName:     t.TaskName,
		})
	}
	return out
}

func (e *pcsEngine) OfflineCancel(ctx context.Context, taskID int64) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	return mapErr(pcs.CloudDlCancelTask(taskID))
}

func (e *pcsEngine) OfflineDelete(ctx context.Context, taskID int64, all bool) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	if all {
		_, pcsErr := pcs.CloudDlClearTask()
		return mapErr(pcsErr)
	}
	return mapErr(pcs.CloudDlDeleteTask(taskID))
}

// ---------------------------------------------------------------------------
// 回收站 FR-31~33
// ---------------------------------------------------------------------------

func (e *pcsEngine) RecycleList(ctx context.Context, page int) (RecycleInfoList, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return nil, err
	}
	fdl, pcsErr := pcs.RecycleList(page)
	if pcsErr != nil {
		return nil, mapErr(pcsErr)
	}
	out := make(RecycleInfoList, 0, len(fdl))
	for _, r := range fdl {
		out = append(out, &RecycleInfo{
			FsID:     r.FsID,
			Path:     r.Path,
			Filename: r.Filename,
			Size:     r.Size,
			Isdir:    r.Isdir,
			LeftTime: r.LeftTime,
			Mtime:    r.Mtime,
		})
	}
	return out, nil
}

func (e *pcsEngine) RecycleRestore(ctx context.Context, fsids []int64) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	_, pcsErr := pcs.RecycleRestore(fsids...)
	return mapErr(pcsErr)
}

func (e *pcsEngine) RecycleDelete(ctx context.Context, fsids []int64, all bool) error {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return err
	}
	if all {
		_, pcsErr := pcs.RecycleClear()
		return mapErr(pcsErr)
	}
	return mapErr(pcs.RecycleDelete(fsids...))
}

// ---------------------------------------------------------------------------
// 数据偏移与离线 uid 派生
// ---------------------------------------------------------------------------

// deriveUIDFromBDUSS 离线派生稳定 uid(BDUSS 串 FNV-64; 上游 SetUID 仅 locate/download
// 签名用途, 以稳定派生 uid 满足签名; 置高位避免与真实 uid(≤10^10)撞车)。
func deriveUIDFromBDUSS(bduss string) uint64 {
	return fnv64(bduss) | (1 << 63)
}

// deriveUIDFromCookie 从整串 Cookie 抽 BDUSS 派生 uid; 抽不到则以整串哈希。
func deriveUIDFromCookie(cookie string) (uint64, string) {
	bduss := ""
	for _, kv := range strings.Split(cookie, ";") {
		kv = strings.TrimSpace(kv)
		if strings.HasPrefix(strings.ToLower(kv), "bduss=") {
			bduss = kv[len("bduss="):]
			break
		}
	}
	if bduss == "" {
		bduss = cookie
	}
	uid := deriveUIDFromBDUSS(bduss)
	return uid, fmt.Sprintf("baidu_%d", uid)
}

func fnv64(s string) uint64 {
	const offset64 = 14695981039346656037
	const prime64 = 1099511628211
	h := uint64(offset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime64
	}
	return h
}

// compile-time 断言: *pcsEngine 实现 Engine。
var _ Engine = (*pcsEngine)(nil)
