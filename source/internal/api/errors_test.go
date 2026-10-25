package api

import (
	"context"
	"net/http"
	"testing"

	"baidupcs-web/internal/xengine"
)

// xengine.Error → HTTP 状态码/机器码映射表逐项。
func TestStatusOfEngineErrMapping(t *testing.T) {
	cases := []struct {
		code    xengine.ErrCode
		status  int
		machine string
	}{
		{xengine.ErrNet, http.StatusBadGateway, "UPSTREAM_ERROR"},
		{xengine.ErrRemote, http.StatusBadGateway, "UPSTREAM_ERROR"},
		{xengine.ErrAuth, http.StatusConflict, "BAIDU_NOT_LINKED"},
		{xengine.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{xengine.ErrQuota, http.StatusInsufficientStorage, "QUOTA_EXCEEDED"},
		{xengine.ErrForbiddenTransfer, http.StatusForbidden, "TRANSFER_FORBIDDEN"},
		{xengine.ErrBadPasscode, http.StatusUnauthorized, "AUTH_BAD_PASSCODE"},
		{xengine.ErrInternal, http.StatusInternalServerError, "INTERNAL"},
	}
	for _, c := range cases {
		status, machine := statusOf(&xengine.Error{Code: c.code, Message: "x"})
		if status != c.status || machine != c.machine {
			t.Errorf("ErrCode %d → (%d,%s), want (%d,%s)", c.code, status, machine, c.status, c.machine)
		}
	}
	if s, m := statusOf(context.DeadlineExceeded); s != http.StatusInternalServerError || m != "INTERNAL" {
		t.Errorf("非引擎错误应落 INTERNAL, got (%d,%s)", s, m)
	}
}
