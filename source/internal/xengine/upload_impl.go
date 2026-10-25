package xengine

// upload_impl.go 上传引擎组装(§5.2): 仿上游 pcsupload.UploadTaskUnit/PCSUpload 的
// 秒传(precreate)→分片(superfile2)流程, 不引入其任务框架与 pcsconfig 依赖。

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	baidupcs "github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs/pcserror"
	"github.com/qjfoidnh/BaiduPCS-Go/requester"
	"github.com/qjfoidnh/BaiduPCS-Go/requester/multipartreader"
	"github.com/qjfoidnh/BaiduPCS-Go/requester/rio"
	"github.com/qjfoidnh/BaiduPCS-Go/requester/uploader"
)

const defaultContentSize = 4 << 10 // 4KB, 上游 DefaultContentSize

// fileChecksums 流式计算整文件 MD5 与首 256KB slice-MD5(上游 SliceMD5Size)。
// ≤256KB 时 slice-md5 == content-md5(上游语义)。
func fileChecksums(f *os.File, size int64) (md5Hex, sliceMD5 string, err error) {
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	h := md5.New()
	sliceLen := int64(baidupcs.SliceMD5Size)
	if size < sliceLen {
		sliceLen = size
	}
	if size <= sliceLen {
		if _, err = io.Copy(h, f); err != nil {
			return "", "", err
		}
		sum := hex.EncodeToString(h.Sum(nil))
		return sum, sum, nil
	}
	sh := md5.New()
	if _, err = io.CopyN(io.MultiWriter(h, sh), f, sliceLen); err != nil {
		return "", "", err
	}
	if _, err = io.Copy(h, f); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(h.Sum(nil)), hex.EncodeToString(sh.Sum(nil)), nil
}

// readSlice 读取文件 [offset, offset+n) 字节(秒传 dataContent 用)。
func readSlice(p string, offset, n int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:got], nil
}

// blockSizeFor 分片尺寸随文件大小升档(仿上游 getBlockSize)。
func blockSizeFor(size int64) int64 {
	switch {
	case size >= baidupcs.MaxUploadThreshold:
		return baidupcs.MaxUploadBlockSize
	case size >= baidupcs.MiddleUploadThreshold:
		return baidupcs.MiddleUploadBlockSize
	default:
		return baidupcs.MinUploadBlockSize
	}
}

// dataOffset 仿上游 pcsupload.creaetDataOffset(秒传抽样偏移)。
func dataOffset(contentMD5 string, uk, dataTime, fileSize, subSize int64) (int64, error) {
	h := md5.New()
	_, _ = fmt.Fprintf(h, "%d%s%d", uk, contentMD5, dataTime)
	mixed := hex.EncodeToString(h.Sum(nil))
	raw, err := strconvParseHex(mixed[:8])
	if err != nil {
		return 0, err
	}
	if fileSize-subSize+1 <= 1 {
		return 0, nil
	}
	return raw % (fileSize - subSize + 1), nil
}

// Upload 上传本地文件到网盘(秒传优先, 失败转分片引擎; §5.2)。
func (e *pcsEngine) Upload(ctx context.Context, param UploadParam) (UploadResult, error) {
	pcs, err := e.activeFor(ctx)
	if err != nil {
		return UploadResult{}, err
	}
	f, err := os.Open(param.LocalPath)
	if err != nil {
		return UploadResult{}, wrapErr(ErrInternal, "打开待上传文件: %v", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return UploadResult{}, wrapErr(ErrInternal, "stat 待上传文件: %v", err)
	}
	size := fi.Size()
	f.Close()

	policy := param.Policy
	if policy == "" {
		policy = e.cfg.UPolicy
	}
	parallel := param.Parallel
	if parallel <= 0 {
		parallel = e.cfg.MaxUploadParallel
	}

	// FR-16: 计算 md5/slice-md5(秒传预检 + 分片引擎共用)
	f, err = os.Open(param.LocalPath)
	if err != nil {
		return UploadResult{}, wrapErr(ErrInternal, "打开待上传文件: %v", err)
	}
	md5Hex, sliceMD5, cerr := fileChecksums(f, size)
	f.Close()
	if cerr != nil {
		return UploadResult{}, wrapErr(ErrInternal, "计算文件校验: %v", cerr)
	}

	if !param.NoRapid {
		ok, err := e.tryRapidUpload(pcs, param.LocalPath, param.SavePath, policy, md5Hex, sliceMD5, size)
		if err != nil {
			return UploadResult{}, err
		}
		if ok {
			return UploadResult{RapidUpload: true, SavePath: param.SavePath, MD5: md5Hex}, nil
		}
	}

	if err := e.multiUpload(ctx, pcs, uploadJob{
		localPath: param.LocalPath,
		savePath:  param.SavePath,
		policy:    policy,
		parallel:  parallel,
		maxRate:   e.cfg.MaxUploadRate,
		noRapid:   param.NoRapid,
		size:      size,
		md5:       md5Hex,
		sliceMD5:  sliceMD5,
	}); err != nil {
		return UploadResult{}, err
	}
	return UploadResult{SavePath: param.SavePath, MD5: md5Hex}, nil
}

// tryRapidUpload 尝试秒传(RapidUpload V2, 仿上游 precreate 抽样流程); 返回是否命中。
func (e *pcsEngine) tryRapidUpload(pcs *baidupcs.BaiduPCS, localPath, targetPath, policy, contentMD5, sliceMD5 string, size int64) (bool, error) {
	uk, pcsErr := pcs.UK()
	if pcsErr != nil {
		return false, nil // UK 不可得(如缺 STOKEN)→ 直接走分片
	}
	now := time.Now().Unix()
	offset, err := dataOffset(contentMD5, uk, now, size, defaultContentSize)
	if err != nil {
		return false, nil
	}
	dataContent, err := readSlice(localPath, offset, defaultContentSize)
	if err != nil {
		return false, nil
	}
	b64 := base64.StdEncoding.EncodeToString(dataContent)
	blockList := []string{sliceMD5}
	if size > baidupcs.SliceMD5Size {
		blockList = []string{contentMD5, sliceMD5}
	}
	pcsErr, jsonData := pcs.RapidUpload(targetPath, policy, "", contentMD5, sliceMD5, b64, "0",
		offset, int64(len(dataContent)), size, now, blockList)
	if pcsErr == nil {
		return jsonData.ReturnType == 2, nil
	}
	if pe, ok := pcsErr.(pcserror.Error); ok && pe.GetErrType() == pcserror.ErrTypeRemoteError {
		if pe.GetRemoteErrCode() == 31112 { // 配额满须显式报错, 不静默转分片
			return false, mapErr(pe)
		}
	}
	return false, nil // 其余错误一律视为"秒传未命中", 转分片
}

// uploadJob 一次分片上传的全部输入。
type uploadJob struct {
	localPath string
	savePath  string
	policy    string
	parallel  int
	maxRate   int64
	noRapid   bool
	size      int64
	md5       string
	sliceMD5  string
}

// multiUpload 走上游 MultiUploader 分片引擎。
func (e *pcsEngine) multiUpload(ctx context.Context, pcs *baidupcs.BaiduPCS, job uploadJob) error {
	f, err := os.Open(job.localPath)
	if err != nil {
		return wrapErr(ErrInternal, "打开待上传文件: %v", err)
	}
	defer f.Close()

	pcsUpload := newPCSUpload(pcs, job.savePath)
	cfg := &uploader.MultiUploaderConfig{
		Parallel:  job.parallel,
		BlockSize: blockSizeFor(job.size),
		MaxRate:   job.maxRate,
		Policy:    job.policy,
	}
	muer := uploader.NewMultiUploader(pcsUpload, rio.NewFileReaderAtLen64(f), cfg, job.savePath)

	errCh := make(chan error, 1)
	muer.OnSuccess(func() { errCh <- nil })
	muer.OnError(func(err error) { errCh <- err })

	// NoRapid 时先 FakeRapidUpload 拿 uploadid(上游 prepareFile NoRapidUpload 同款时序)
	if job.noRapid {
		pcsErr, _ := pcs.FakeRapidUpload(job.savePath, job.policy, job.size)
		if pcsErr == nil {
			// 正常取得 uploadid; 引擎 precreate 会复用 pcs 侧 uploadid 流程
		}
	}

	go muer.Execute()

	select {
	case <-ctx.Done():
		muer.Cancel()
		return &Error{Code: ErrNet, Message: "上传被取消", Err: ctx.Err()}
	case err := <-errCh:
		if err != nil {
			return classifyUploadErr(err)
		}
	}
	return nil
}

// classifyUploadErr 上传引擎错误 → 适配层错误。
func classifyUploadErr(err error) error {
	if me, ok := err.(*uploader.MultiError); ok && me.Err != nil {
		if pe, ok := me.Err.(pcserror.Error); ok {
			return mapErr(pe)
		}
		return &Error{Code: ErrInternal, Message: "上传失败", Err: me.Err}
	}
	if pe, ok := err.(pcserror.Error); ok {
		return mapErr(pe)
	}
	return &Error{Code: ErrInternal, Message: "上传失败", Err: err}
}

// ---------------------------------------------------------------------------
// pcsMultiUpload: 上游 uploader.MultiUpload 适配(轻量仿 pcsupload.PCSUpload,
// 上传客户端以 pcs.GetClient() 为基, 独立超时, 与上游 getUploadClient 语义一致)。
// ---------------------------------------------------------------------------

var (
	uploadClientOnce sync.Once
	uploadClient     *requester.HTTPClient
)

func getUploadClient(jar http.CookieJar, ua string) *requester.HTTPClient {
	uploadClientOnce.Do(func() {
		uploadClient = requester.NewHTTPClient()
		uploadClient.SetHTTPSecure(true)
		if ua != "" {
			uploadClient.SetUserAgent(ua)
		}
		uploadClient.SetTimeout(200 * time.Second)
	})
	uploadClient.SetCookiejar(jar)
	return uploadClient
}

type pcsMultiUpload struct {
	pcs        *baidupcs.BaiduPCS
	targetPath string
	ua         string
}

func newPCSUpload(pcs *baidupcs.BaiduPCS, targetPath string) *pcsMultiUpload {
	return &pcsMultiUpload{pcs: pcs, targetPath: targetPath, ua: requester.DefaultUserAgent}
}

// Precreate 上传前准备, 获取本次上传用的 pcs 服务器(仿 PCSUpload.Precreate)。
func (p *pcsMultiUpload) Precreate() (string, pcserror.Error) {
	origin := p.pcs.GetPCSAddr()
	_, newHost := p.pcs.GetRandomPCSHost()
	if newHost != "" {
		p.pcs.SetPCSAddr(newHost)
	}
	return origin, nil
}

// TmpFile 上传单个分片(仿 PCSUpload.TmpFile: multipart + 取消透传 + 4xx 终止)。
func (p *pcsMultiUpload) TmpFile(ctx context.Context, uploadid, targetPath string, partseq int, partOffset int64, r rio.ReaderLen64) (string, error) {
	var respErr *uploader.MultiError
	checksum, pcsErr := p.pcs.UploadTmpFile(uploadid, targetPath, partseq, partOffset, func(uploadURL string, jar http.CookieJar) (resp *http.Response, err error) {
		mr := multipartreader.NewMultipartReader()
		mr.AddFormFile("uploadedfile", "", r)
		mr.CloseMultipart()

		doneChan := make(chan struct{}, 1)
		go func() {
			cli := getUploadClient(jar, p.ua)
			resp, err = cli.Req(http.MethodPost, uploadURL, mr, nil)
			doneChan <- struct{}{}
			if resp != nil {
				switch resp.StatusCode {
				case 400, 401, 403, 413: // 客户端非法操作, 深度重试无益 → 终止
					respErr = &uploader.MultiError{Terminated: true}
				}
			}
		}()
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case <-doneChan:
		}
		return
	})
	if respErr != nil {
		respErr.Err = pcsErr
		return checksum, respErr
	}
	return checksum, pcsErr
}

// CreateSuperFile 合并分片(恢复原 pcs 服务器后 create)。
func (p *pcsMultiUpload) CreateSuperFile(pcsHost, policy, uploadid string, fileSize int64, checksumMap map[int]string) error {
	if pcsHost != "" {
		p.pcs.SetPCSAddr(pcsHost)
	}
	return p.pcs.UploadCreateSuperFile(uploadid, policy, fileSize, p.targetPath, checksumMap)
}
