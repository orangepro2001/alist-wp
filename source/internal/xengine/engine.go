// Package xengine 是 baidupcs 适配层: 唯一允许 import 上游库的包,
// 隔离上游接口变动(NFR-10), 对上层暴露稳定的最小调用面。
//
// 上游调研结论(2026-10-24, v4.0.2 tag):
//   - 核心库位于上游仓库根目录 baidupcs/(非 internal/), 可直接 import:
//     github.com/qjfoidnh/BaiduPCS-Go/baidupcs
//     github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror
//   - internal/pcsconfig 受 Go internal 可见性规则限制, 外部模块不可 import,
//     多账号/配置必须在本包内自行组织(凭证 JSON 持久化 + BaiduPCS 实例池)。
//   - 入口: baidupcs.NewPCS(appID, bduss) / NewPCSWithCookieStr(appID, cookieStr);
//     SetStoken/SetUID/SetHTTPS/SetPCSUserAgent 完成凭证与 UA 装配。
//   - 下载直链: LocateDownload(pcspath) → URLInfo.SingleURL(https);
//     请求 dlink 必须携带 NetdiskUA("netdisk;P2SP;3.0.0.8;...")。
//   - 错误模型: 所有调用返回 pcserror.Error(GetErrType/GetRemoteErrCode/GetRemoteErrMsg)。
package xengine

import (
	"context"
	"time"
)

// PCS 实例池常量。
const (
	// DefaultAppID 对应上游 pcsconfig.InitDefaultConfig 的 AppID=266719。
	DefaultAppID = 266719
	// NetdiskUA 别名引用上游 baidupcs.NetdiskUA(实现阶段 const 指向上游常量)。
	NetdiskUA = "netdisk;P2SP;3.0.0.8;netdisk;11.12.3;ANG-AN00;android-android;10.0;JSbridge4.4.0;jointBridge;1.1.0;"
)

// Account 一条百度账号凭证(镜像上游 pcsconfig.Baidu 字段, 自行持久化)。
type Account struct {
	UID    uint64 `json:"uid"`
	Name   string `json:"name"`
	BDUSS  string `json:"bduss"`
	STOKEN string `json:"stoken"`
	Cookie string `json:"cookie,omitempty"` // 整串 Cookie 方式登录时保留
	// Workdir 每账号工作目录(FR-07 服务器侧语义放在 session, 此处为上游兼容字段)
	Workdir string `json:"workdir"`
}

// ErrCode 适配层错误码(向上映射 pcserror, 服务层据此转中文提示)。
type ErrCode int

const (
	ErrNet        ErrCode = iota // 网络错误 pcserror.ErrTypeNetError
	ErrRemote                    // 百度远端错误(携带 RemoteErrCode)
	ErrAuth                      // 凭证失效(errno 4/-6 等, 需引导重新登录)
	ErrNotFound                  // 路径不存在(errno 31066/112)
	ErrQuota                     // 配额不足(errno 31112)
	ErrForbiddenTransfer         // 分享禁止转存(FR-40 异常分支)
	ErrBadPasscode               // 提取码错误(errno -9)
	ErrInternal                  // 其他内部错误
)

// Error 适配层统一错误。
type Error struct {
	Code       ErrCode
	RemoteCode int    // 百度 errno(ErrRemote 时有效)
	Message    string // 已脱敏的中文消息(FR-38: 不含凭证)
	Err        error  // 原始错误
}

func (e *Error) Error() string { return e.Message }

// Engine 对上层(服务层)暴露的稳定接口。实现持有上游 *baidupcs.BaiduPCS 实例池。
// 线程安全: 全部方法可并发调用; 不阻塞信号量; 禁止在信号处理函数中调用。
type Engine interface {
	// Accounts 账号管理(FR-01~04)
	AddAccountByCookie(cookie string) (*Account, error)            // FR-01
	AddAccountByBDUSS(bduss, stoken string) (*Account, error)      // FR-01; stoken 必填
	Accounts() []Account                                           // FR-02(凭证字段由服务层脱敏后输出)
	ActiveAccount() (Account, bool)                                // FR-02
	SwitchAccount(uid uint64) error                                // FR-03
	RemoveAccount(uid uint64) error                                // FR-04(进行中任务由服务层拦截)

	// 配额(FR-05)
	Quota(ctx context.Context) (quota, used int64, err error)

	// 文件(FR-06~10, 21~24)
	List(ctx context.Context, dir string, page, pageSize int, orderBy, order string) (FileDirectoryList, error)
	Meta(ctx context.Context, path string) (*FileDirectory, error)
	Search(ctx context.Context, dir, keyword string, recursive bool) (FileDirectoryList, error)
	Mkdir(ctx context.Context, path string) error
	Remove(ctx context.Context, paths []string) error
	Copy(ctx context.Context, from []string, toDir string) error
	Move(ctx context.Context, from []string, toDir string) error
	Rename(ctx context.Context, from, to string) error
	Tree(ctx context.Context, dir string, maxDepth int) (FileDirectoryList, error) // FR-08
	Match(ctx context.Context, pattern string) ([]string, error)                   // FR-35

	// 下载(FR-11)
	// ResolveDlink 获取 dlink; 返回的 dlink 必须以 NetdiskUA 请求(见 DownloadLink 结构)。
	ResolveDlink(ctx context.Context, pcspath string) (*DownloadLink, error)
	Export(ctx context.Context, pcspath string) (*RapidLink, error) // FR-13

	// 上传(FR-15/16): 直接喂服务器临时文件路径, 复用上游引擎
	Upload(ctx context.Context, param UploadParam) (UploadResult, error)

	// 分享/转存(FR-25~28, FR-40)
	ShareSet(ctx context.Context, paths []string, password string, period int) (*Shared, error)
	ShareList(ctx context.Context, page int) (ShareRecordList, error)
	ShareCancel(ctx context.Context, shareIDs []int64) error
	ParseShareLink(rawText string) (link, passcode string, err error) // FR-28 口令文本抽取
	SharePreview(ctx context.Context, link, passcode string) (SharePreviewResult, error)      // FR-40 浏览分享内容
	Transfer(ctx context.Context, link, passcode, targetDir string, fsids []string) error     // FR-28
	TransferToTmp(ctx context.Context, link, passcode string, fsids []string) (*TmpTransfer, error) // FR-40

	// 离线(FR-29/30)
	OfflineAdd(ctx context.Context, sourceURLs []string, savePath string) ([]OfflineTask, []error)
	OfflineList(ctx context.Context) (OfflineTaskList, error)
	OfflineQuery(ctx context.Context, taskIDs []int64) (OfflineTaskList, error)
	OfflineCancel(ctx context.Context, taskID int64) error
	OfflineDelete(ctx context.Context, taskID int64, all bool) error

	// 回收站(FR-31~33)
	RecycleList(ctx context.Context, page int) (RecycleInfoList, error)
	RecycleRestore(ctx context.Context, fsids []int64) error
	RecycleDelete(ctx context.Context, fsids []int64, all bool) error
}

// DownloadLink 一次直链解析结果。UA 必须使用 NetdiskUA; dlink 有时效(约 8h, 实测为准)。
type DownloadLink struct {
	URL    string    // dlink 原始 URL(SingleURL 产物)
	UA     string    // 恒为 NetdiskUA, 服务端代理请求时原样设置
	Expire time.Time // 预估过期时间, 供缓存判断
	Size   int64     // 文件大小(Content-Length 校验用)
}

// UploadParam 上传参数(上游引擎: 秒传→precreate→superfile2 分片→create)。
type UploadParam struct {
	LocalPath  string // 服务器临时文件绝对路径(D3: 临时落盘后的文件)
	SavePath   string // 网盘目标绝对路径
	Policy     string // skip|overwrite|rsync
	NoRapid    bool   // 跳过秒传(FR-16 开关)
	Parallel   int    // 分片并发(默认 4)
	NoCheckDir bool   // 上游 CheckIsdir 行为透传占位
}

// UploadResult 上传结果。
type UploadResult struct {
	RapidUpload bool   // 是否秒传命中(FR-16 展示)
	SavePath    string // 网盘最终路径(策略 rename 等可能变化)
	MD5         string
}

// 轻量数据镜像(避免上层直接依赖上游类型; 字段语义与上游 FileDirectory 等一致)。

type FileDirectory struct {
	FsID     int64
	Path     string
	Filename string
	Ctime    int64
	Mtime    int64
	MD5      string
	Size     int64
	Isdir    bool
	HasSub   bool
}

type FileDirectoryList []*FileDirectory

type RapidLink struct {
	ContentMD5 string
	SliceMD5   string
	Crc32      string
	Length     int64
	Filename   string
}

type Shared struct {
	Link    string
	Pwd     string
	ShareID int64
}

type ShareRecord struct {
	ShareID   int64
	TypicalPath string
	Shortlink string
	Status    int
	ExpireType int   // 上游语义: -1=已过期
	ExpireTime int64 // 相对秒数(0=永久); 绝对时间=now+ExpireTime
	ViewCount  int
}

type ShareRecordList []*ShareRecord

type SharePreviewItem struct {
	FsID      string
	Path      string
	Filename  string
	Size      int64
	// SizeKnown 上游 /share/list 接口只回 server_filename/fs_id, 不吐文件大小:
	// false 时 Size 为保守值 0(配额预检按 0 计, 不伪造), 如实注明(FR-40 B4)。
	SizeKnown bool
	Isdir     bool
}

type SharePreviewResult struct {
	ShareID string
	UK      string
	Items   []SharePreviewItem
}

// TmpTransfer FR-40 临时转存结果。
type TmpTransfer struct {
	ShareID   string   // 用于拼 /来自分享/.tmp-<shareid>
	TargetDir string   // 网盘临时目录绝对路径
	Files     []SharePreviewItem // 转存后的文件清单(供逐个下载)
}

type OfflineTask struct {
	TaskID       int64
	Status       int    // 上游语义: 0成功 1进行中 2系统错误 3资源不存在 4超时 5失败 6空间不足 7已取消
	StatusText   string
	FileSize     int64
	FinishedSize int64
	SavePath     string
	SourceURL    string
	TaskName     string
}

type OfflineTaskList []*OfflineTask

type RecycleInfo struct {
	FsID     int64
	Path     string
	Filename string
	Size     int64
	Isdir    int
	LeftTime int
	Mtime    int64
}

type RecycleInfoList []*RecycleInfo
