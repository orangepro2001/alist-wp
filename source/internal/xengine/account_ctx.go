package xengine

import (
	"context"
	"github.com/qjfoidnh/BaiduPCS-Go/baidupcs"
	"strings"
)

// 请求级账号选择: WebDAV 每个挂载(OpenList 存储)用自己的百度 Cookie 登录,
// 请求进来时把账号 uid 放进 ctx, 引擎各方法据此取对应账号的 pcs 实例,
// 不再依赖全局"当前活跃账号"。ctx 里没有账号时回落到活跃账号(命令行/桩路径)。

type accountCtxKey struct{}

// WithAccount 返回携带账号 uid 的 ctx。
func WithAccount(ctx context.Context, uid uint64) context.Context {
	return context.WithValue(ctx, accountCtxKey{}, uid)
}

// AccountFrom 取出 ctx 里的账号 uid。
func AccountFrom(ctx context.Context) (uint64, bool) {
	uid, ok := ctx.Value(accountCtxKey{}).(uint64)
	return uid, ok
}

// CarryAccount 把 from 里的账号带到 to(异步任务脱离请求 ctx 后仍用同一账号)。
func CarryAccount(from, to context.Context) context.Context {
	if uid, ok := AccountFrom(from); ok {
		return WithAccount(to, uid)
	}
	return to
}

// AccountResolver 把登录凭证(整串 Cookie)解析成账号 uid, 必要时登记账号。
// 不改变全局活跃账号。*pcsEngine 实现它; davfs 据此决定是否要求 Basic 认证。
type AccountResolver interface {
	EnsureAccount(cookie string) (uint64, error)
}

var _ AccountResolver = (*pcsEngine)(nil)

// EnsureAccount 实现 AccountResolver: uid 由 Cookie 内 BDUSS 稳定哈希得出(离线, 与
// AddAccountByCookie 一致); 账号不存在或 Cookie 变了就更新凭证并丢弃旧 pcs 实例。
func (e *pcsEngine) EnsureAccount(cookie string) (uint64, error) {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return 0, &Error{Code: ErrAuth, Message: "Cookie 不能为空"}
	}
	uid, name := deriveUIDFromCookie(cookie)
	e.mu.Lock()
	defer e.mu.Unlock()
	if old := e.store.get(uid); old != nil && old.Cookie == cookie {
		return uid, nil
	}
	a := &Account{UID: uid, Name: name, Cookie: cookie, Workdir: "/"}
	if old := e.store.get(uid); old != nil {
		a.Workdir = old.Workdir
	}
	if err := e.store.upsert(a); err != nil {
		return 0, wrapErr(ErrInternal, "保存账号凭证: %v", err)
	}
	delete(e.pool, uid)
	return uid, nil
}

// activeFor 取本次请求应使用的 pcs 实例: ctx 指定了账号就用它, 否则用活跃账号。
func (e *pcsEngine) activeFor(ctx context.Context) (*baidupcs.BaiduPCS, error) {
	if uid, ok := AccountFrom(ctx); ok {
		e.mu.Lock()
		defer e.mu.Unlock()
		pcs, err := e.pcsForLocked(uid)
		if err != nil {
			return nil, &Error{Code: ErrAuth, Message: "百度账号不存在, 请重新填写 Cookie"}
		}
		return pcs, nil
	}
	return e.active()
}
