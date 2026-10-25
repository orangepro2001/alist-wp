// Package davfs 把百度网盘(xengine.Engine)暴露为 WebDAV, 供 OpenList/Alist 的 WebDAV 驱动挂载。
// 元数据操作(PROPFIND/MKCOL/DELETE/MOVE)走 webdav.FileSystem; 内容传输(GET/HEAD/PUT/COPY)
// 在 handler.go 里拦截, 分别走 xfer 流式下载代理、tempmgr+xfer 上传队列与 Engine.Copy。
package davfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/webdav"

	"baidupcs-web/internal/xengine"
)

// Engine davfs 依赖的网盘能力子集(xengine.Engine 满足)。
type Engine interface {
	Meta(ctx context.Context, path string) (*xengine.FileDirectory, error)
	List(ctx context.Context, dir string, page, pageSize int, orderBy, order string) (xengine.FileDirectoryList, error)
	Mkdir(ctx context.Context, path string) error
	Remove(ctx context.Context, paths []string) error
	Copy(ctx context.Context, from []string, toDir string) error
	Move(ctx context.Context, from []string, toDir string) error
	Rename(ctx context.Context, from, to string) error
}

// fileInfo 实现 os.FileInfo, 并带 webdav.ContentTyper/ETager, 避免 x/net/webdav
// 为猜 Content-Type 而打开文件读 512 字节(那会触发一次真实下载)。
type fileInfo struct {
	name  string
	size  int64
	mtime time.Time
	dir   bool
	md5   string
}

func (i *fileInfo) Name() string { return i.name }
func (i *fileInfo) Size() int64  { return i.size }
func (i *fileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}
func (i *fileInfo) ModTime() time.Time { return i.mtime }
func (i *fileInfo) IsDir() bool        { return i.dir }
func (i *fileInfo) Sys() any           { return nil }

// ContentType 按扩展名给类型, 未知一律 octet-stream(不读内容)。
func (i *fileInfo) ContentType(context.Context) (string, error) {
	if t := mime.TypeByExtension(path.Ext(i.name)); t != "" {
		return t, nil
	}
	return "application/octet-stream", nil
}

// ETag 有 MD5 用 MD5, 否则用 mtime+size。
func (i *fileInfo) ETag(context.Context) (string, error) {
	if i.md5 != "" {
		return `"` + i.md5 + `"`, nil
	}
	return `"` + strconv.FormatInt(i.mtime.UnixNano(), 16) + strconv.FormatInt(i.size, 16) + `"`, nil
}

var (
	_ webdav.ContentTyper = (*fileInfo)(nil)
	_ webdav.ETager       = (*fileInfo)(nil)
)

func infoFromFD(fd *xengine.FileDirectory) *fileInfo {
	name := fd.Filename
	if name == "" {
		name = path.Base(fd.Path)
	}
	return &fileInfo{name: name, size: fd.Size, mtime: time.Unix(fd.Mtime, 0), dir: fd.Isdir, md5: fd.MD5}
}

// mapErr 把适配层错误转成 webdav 能识别的 os 错误。
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var xe *xengine.Error
	if errors.As(err, &xe) && xe.Code == xengine.ErrNotFound {
		return os.ErrNotExist
	}
	return err
}

type dirCache struct {
	infos map[string]*fileInfo
	order []*fileInfo
	exp   time.Time
}

// FS 实现 webdav.FileSystem(只含元数据操作; 内容读写见 handler.go)。
type FS struct {
	eng Engine
	ttl time.Duration
	now func() time.Time

	mu   sync.Mutex
	accs map[uint64]*accCache // 按百度账号隔离: 不同账号同一路径内容不同
}

// accCache 单个账号的目录/单项缓存。
type accCache struct {
	cache map[string]*dirCache
	meta  map[string]*metaEntry // 单项 Meta 缓存(同一请求内 Stat/OpenFile 会重复查同一路径)
}

type metaEntry struct {
	info *fileInfo
	exp  time.Time
}

// NewFS 构造; ttl 是目录列表缓存时长(≤0 取 10s)。PROPFIND depth=1 之后 x/net/webdav
// 会对每个子项再 Stat 一次, 没有缓存就会对百度发 N+1 次请求。
func NewFS(eng Engine, ttl time.Duration) *FS {
	if ttl <= 0 {
		ttl = 10 * time.Second
	}
	return &FS{eng: eng, ttl: ttl, now: time.Now, accs: map[uint64]*accCache{}}
}

// acc 取(或建)ctx 所属账号的缓存; 调用方须持 f.mu。ctx 无账号时用 0 号(单账号/桩路径)。
func (f *FS) acc(ctx context.Context) *accCache {
	uid, _ := xengine.AccountFrom(ctx)
	a := f.accs[uid]
	if a == nil {
		a = &accCache{cache: map[string]*dirCache{}, meta: map[string]*metaEntry{}}
		f.accs[uid] = a
	}
	return a
}

func clean(name string) string {
	return path.Clean("/" + strings.TrimPrefix(name, "/"))
}

var rootInfo = &fileInfo{name: "/", dir: true, mtime: time.Unix(0, 0)}

// invalidate 丢弃这些路径自身及其父目录的列表缓存。
func (f *FS) invalidate(ctx context.Context, paths ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.acc(ctx)
	for _, p := range paths {
		p = clean(p)
		delete(a.cache, p)
		delete(a.cache, path.Dir(p))
		for k := range a.meta { // 目录被删/移动时, 其下所有单项缓存一并失效
			if k == p || strings.HasPrefix(k, p+"/") {
				delete(a.meta, k)
			}
		}
	}
}

// listDir 返回目录内容(带缓存)。
func (f *FS) listDir(ctx context.Context, dir string) (*dirCache, error) {
	f.mu.Lock()
	if c, ok := f.acc(ctx).cache[dir]; ok && f.now().Before(c.exp) {
		f.mu.Unlock()
		return c, nil
	}
	f.mu.Unlock()
	list, err := f.eng.List(ctx, dir, 0, 0, "name", "asc")
	if err != nil {
		return nil, mapErr(err)
	}
	c := &dirCache{infos: make(map[string]*fileInfo, len(list)), exp: f.now().Add(f.ttl)}
	for _, fd := range list {
		in := infoFromFD(fd)
		c.infos[in.name] = in
		c.order = append(c.order, in)
	}
	f.mu.Lock()
	f.acc(ctx).cache[dir] = c
	f.mu.Unlock()
	return c, nil
}

// Stat 实现 webdav.FileSystem。
func (f *FS) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	return f.stat(ctx, name)
}

func (f *FS) stat(ctx context.Context, name string) (*fileInfo, error) {
	p := clean(name)
	if p == "/" {
		return rootInfo, nil
	}
	// 父目录列表已缓存时只信缓存(命中/不存在都不再打百度)。
	f.mu.Lock()
	c, ok := f.acc(ctx).cache[path.Dir(p)]
	fresh := ok && f.now().Before(c.exp)
	f.mu.Unlock()
	if fresh {
		if in, ok := c.infos[path.Base(p)]; ok {
			return in, nil
		}
		return nil, os.ErrNotExist
	}
	f.mu.Lock()
	if m, ok := f.acc(ctx).meta[p]; ok && f.now().Before(m.exp) {
		f.mu.Unlock()
		return m.info, nil
	}
	f.mu.Unlock()
	fd, err := f.eng.Meta(ctx, p)
	if err != nil {
		return nil, mapErr(err)
	}
	in := infoFromFD(fd)
	f.mu.Lock()
	f.acc(ctx).meta[p] = &metaEntry{info: in, exp: f.now().Add(f.ttl)}
	f.mu.Unlock()
	return in, nil
}

// Mkdir 实现 webdav.FileSystem。
func (f *FS) Mkdir(ctx context.Context, name string, _ os.FileMode) error {
	p := clean(name)
	if _, err := f.stat(ctx, p); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent, err := f.stat(ctx, path.Dir(p))
	if err != nil {
		return err // 父目录不存在 → 409
	}
	if !parent.dir {
		return os.ErrNotExist
	}
	if err := mapErr(f.eng.Mkdir(ctx, p)); err != nil {
		return err
	}
	f.invalidate(ctx, p)
	return nil
}

// RemoveAll 实现 webdav.FileSystem。
func (f *FS) RemoveAll(ctx context.Context, name string) error {
	p := clean(name)
	if p == "/" {
		return os.ErrPermission
	}
	if err := mapErr(f.eng.Remove(ctx, []string{p})); err != nil {
		return err
	}
	f.invalidate(ctx, p)
	return nil
}

// Rename 实现 webdav.FileSystem(MOVE): 同目录=改名, 跨目录=移动, 必要时再改名。
func (f *FS) Rename(ctx context.Context, oldName, newName string) error {
	from, to := clean(oldName), clean(newName)
	if from == "/" || to == "/" {
		return os.ErrPermission
	}
	if from == to {
		return nil
	}
	if path.Dir(from) == path.Dir(to) {
		if err := mapErr(f.eng.Rename(ctx, from, to)); err != nil {
			return err
		}
	} else {
		toDir := path.Dir(to)
		if err := mapErr(f.eng.Move(ctx, []string{from}, toDir)); err != nil {
			return err
		}
		if moved := path.Join(toDir, path.Base(from)); moved != to {
			if err := mapErr(f.eng.Rename(ctx, moved, to)); err != nil {
				return err
			}
		}
	}
	f.invalidate(ctx, from, to)
	return nil
}

// OpenFile 实现 webdav.FileSystem: 只支持只读打开(目录列举/属性查询);
// 写入由 handler.go 拦截 PUT, 不会走到这里。
func (f *FS) OpenFile(ctx context.Context, name string, flag int, _ os.FileMode) (webdav.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, os.ErrPermission
	}
	in, err := f.stat(ctx, name)
	if err != nil {
		return nil, err
	}
	return &davFile{fs: f, ctx: ctx, path: clean(name), info: in}, nil
}

// davFile 只读 webdav.File: 目录支持 Readdir, 文件不提供内容(GET 由代理承担)。
type davFile struct {
	fs     *FS
	ctx    context.Context
	path   string
	info   *fileInfo
	offset int
}

func (d *davFile) Close() error               { return nil }
func (d *davFile) Stat() (os.FileInfo, error) { return d.info, nil }
func (d *davFile) Write([]byte) (int, error)  { return 0, os.ErrPermission }
func (d *davFile) Read([]byte) (int, error) {
	return 0, errors.New("davfs: 文件内容仅经 GET 代理提供")
}
func (d *davFile) Seek(int64, int) (int64, error) { return 0, errors.New("davfs: 不支持 Seek") }

func (d *davFile) Readdir(count int) ([]fs.FileInfo, error) {
	if !d.info.dir {
		return nil, errors.New("davfs: 不是目录")
	}
	c, err := d.fs.listDir(d.ctx, d.path)
	if err != nil {
		return nil, err
	}
	rest := c.order[d.offset:]
	if count > 0 {
		if len(rest) == 0 {
			return nil, io.EOF
		}
		if count < len(rest) {
			rest = rest[:count]
		}
	}
	d.offset += len(rest)
	out := make([]fs.FileInfo, len(rest))
	for i, in := range rest {
		out[i] = in
	}
	return out, nil
}
