package api

// errors.go W4: xengine.ErrCode → (HTTP 状态码, 机器码) 映射(§4 错误体契约)。
// 集中一处, 供各域 handler 的 writeEngineErr 使用。

import (
	"errors"
	"net/http"

	"baidupcs-web/internal/xengine"
)

// statusOf 引擎错误 → (HTTP 状态码, §4 机器码)。
func statusOf(err error) (int, string) {
	var e *xengine.Error
	if !errors.As(err, &e) {
		return http.StatusInternalServerError, "INTERNAL"
	}
	switch e.Code {
	case xengine.ErrNet:
		return http.StatusBadGateway, "UPSTREAM_ERROR"
	case xengine.ErrRemote:
		return http.StatusBadGateway, "UPSTREAM_ERROR"
	case xengine.ErrAuth:
		// 语义区分(修复"登录后闪回登录页"死循环): ErrAuth 这里是"百度账号未登录",
		// 不是"Web 会话未登录"。Web 会话校验在 middleware(返回 401 AUTH_REQUIRED);
		// 此处必须 409, 否则前端会把未绑百度账号误判为会话过期而踢回登录页。
		return http.StatusConflict, "BAIDU_NOT_LINKED"
	case xengine.ErrNotFound:
		return http.StatusNotFound, "NOT_FOUND"
	case xengine.ErrQuota:
		return http.StatusInsufficientStorage, "QUOTA_EXCEEDED"
	case xengine.ErrForbiddenTransfer:
		return http.StatusForbidden, "TRANSFER_FORBIDDEN"
	case xengine.ErrBadPasscode:
		return http.StatusUnauthorized, "AUTH_BAD_PASSCODE"
	default:
		return http.StatusInternalServerError, "INTERNAL"
	}
}

// writeEngineErr 输出引擎错误的统一 JSON 错误体。
func writeEngineErr(w http.ResponseWriter, err error) {
	status, code := statusOf(err)
	writeErr(w, status, code, err.Error())
}
