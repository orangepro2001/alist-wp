package api

// auth.go: 统一 JSON 错误体与成功响应助手(口令登录接口已移除)。
// 响应错误体格式见设计 §4: {"code":"<机器码>","message":"<中文>"}。

import (
	"encoding/json"
	"net/http"
)

// errBody 统一错误响应体(§4)。
type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeErr 按 error 类型映射机器码并输出 JSON。
func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errBody{Code: code, Message: message})
}

// writeOK 输出 JSON 200。
func writeOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
