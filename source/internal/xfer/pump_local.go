// pump_local.go W8: FR-40 服务器侧拉流(分享直连下载的下载环节)。
// 数据面同 ServeDownload(Resolve→netdisk UA→Range 续取), 但无浏览器响应:
// 流式读至 EOF(io.Discard), 只为"逐文件到终态"驱动 FR-40 状态机;
// 浏览器侧下载走 /api/share/dl/url 签名直链并行(B5); ctx 取消即中断(协作取消)。

package xfer

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// pumpRetries 断流续取次数(与下载代理 resumeRetries 一致)。
const pumpRetries = resumeRetries

// PumpLocal 服务器侧把单个网盘文件拉到终态(内容丢弃, 只保证链路与字节完整)。
// 返回 nil=文件完整拉完; 直链解析失败/续取耗尽/取消返回错误。
// B6: fetch 返回 cancel, Body 关闭后调用; 续取先 Close 旧 Body。
func (p *proxy) PumpLocal(ctx context.Context, pcspath string) error {
	pcspath = strings.TrimSpace(pcspath)
	if pcspath == "" || !strings.HasPrefix(pcspath, "/") {
		return badRequest("缺少 path 参数")
	}
	var (
		sent   int64
		resp   *http.Response
		cancel context.CancelFunc
		err    error
	)
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
		if cancel != nil {
			cancel()
		}
	}()
	for attempt := 0; ; attempt++ {
		dlink, ua, _, rerr := p.cfg.Resolve(ctx, pcspath)
		if rerr != nil {
			if attempt == 0 && p.cfg.ResolveFallback != nil {
				if fu, fua, _, ferr := p.cfg.ResolveFallback(ctx, pcspath); ferr == nil {
					dlink, ua = fu, fua
				}
			}
			if dlink == "" {
				return errInternal("解析下载直链失败: " + rerr.Error())
			}
		}
		resp, cancel, err = p.fetch(ctx, dlink, ua, "", sent)
		if err == nil {
			break
		}
		if attempt >= pumpRetries {
			return fmt.Errorf("连接下载源失败: %w", err)
		}
	}

	buf := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			sent += int64(n)
			if p.cfg.Rate != nil {
				if werr := p.cfg.Rate.WaitN(int64(n)); werr != nil {
					return werr
				}
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			// 断流续取: 按已收字节重发 Range, 最多 pumpRetries 次(§5.1)。
			resumed := false
			for a := 0; a < pumpRetries; a++ {
				nd, nua, _, e2 := p.cfg.Resolve(ctx, pcspath)
				if e2 != nil {
					continue
				}
				nr, nc, e3 := p.fetch(ctx, nd, nua, "", sent)
				if e3 != nil {
					continue
				}
				// B7: 上游对续取 Range 回 200 → 无法按偏移续写, 放弃本次续取
				if nr.StatusCode != http.StatusPartialContent && sent > 0 {
					nr.Body.Close()
					nc()
					continue
				}
				resp.Body.Close() // B6: 旧 Body 先关
				if cancel != nil {
					cancel()
				}
				resp = nr
				cancel = nc
				resumed = true
				break
			}
			if !resumed {
				return fmt.Errorf("下载中断于 %d 字节: %w", sent, rerr)
			}
		}
	}
}
