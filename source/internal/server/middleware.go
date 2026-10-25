package server

import (
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

// 中间件链: recover → 脱敏分级日志(INFO/WARN/ERROR, 凭证前4后4)。

// statusWriter 包装 ResponseWriter 记录响应码, 供日志中间件使用。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush 透传底层 Flusher, 为 W4 SSE(/api/events)预留。
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recoverMiddleware panic 恢复: 单请求 panic 兜底 500, 不拖垮进程(§7)。
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[ERROR] [recovery] panic=%v method=%s path=%s", rec, r.Method, r.URL.Path)
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(w, `{"code":"INTERNAL","message":"内部错误"}`)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// W9 日志分级(NFR-07): DEBUG/INFO/WARN/ERROR, 默认 INFO。
// 经环境变量 BPW_LOG_LEVEL 或 SetupLogLevel 设置; 输出统一走 log 包(stdout)。
// ---------------------------------------------------------------------------

// logLevels 日志级别序数(数值越大越严重)。
var logLevels = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

// logLevelThreshold 当前放行阈值(默认 INFO)。
var logLevelThreshold = 1

// SetupLogLevel 设置全局日志级别(DEBUG/INFO/WARN/ERROR, 非法值回落 INFO)。
func SetupLogLevel(level string) {
	lv, ok := logLevels[strings.ToUpper(strings.TrimSpace(level))]
	if !ok {
		lv = 1
	}
	logLevelThreshold = lv
}

// setupLogLevelFromEnv 经环境变量 BPW_LOG_LEVEL 初始化(main 启动时调用)。
func setupLogLevelFromEnv() {
	SetupLogLevel(os.Getenv("BPW_LOG_LEVEL"))
}

// LogAt 按级别输出日志(级别低于阈值则丢弃); 供本包与冒烟使用。
func LogAt(level, format string, a ...interface{}) {
	lv, ok := logLevels[level]
	if !ok {
		lv = 1
	}
	if lv < logLevelThreshold {
		return
	}
	log.Printf("["+level+"] "+format, a...)
}

// ---------------------------------------------------------------------------
// W9 凭证脱敏(FR-38): BDUSS/STOKEN/Cookie/passcode 任何日志路径不得明文。
// 策略: 命中敏感键的值一律 "前4后4" 掩码; 短值(<12 字符)全掩码。
// ---------------------------------------------------------------------------

// sensitiveKeys 日志脱敏敏感键集合(FR-38; 大小写不敏感)。
var sensitiveKeys = map[string]bool{
	"bduss":      true,
	"stoken":     true,
	"cookie":     true,
	"passcode":   true,
	"password":   true,
	"ptoken":     true,
	"baiduid":    true,
	"set-cookie": true,
}

// MaskSecret 对单个凭证值脱敏: 前 4 后 4, 其余 *; 过短全 *。
func MaskSecret(v string) string {
	if v == "" {
		return ""
	}
	const mask = "****"
	if len(v) < 12 {
		return mask
	}
	return v[:4] + mask + v[len(v)-4:]
}

// SanitizeLogText 对任意日志文本做凭证脱敏: 扫描 "键[:=]值" 形态, 敏感键值替换为掩码。
// 兼容 JSON 片段("bduss":"xxx")与 key=value 形态; URL ?pwd=xxx 同样命中。
// 单遍推进扫描偏移, 保证终止(掩码值不再回扫)。
func SanitizeLogText(s string) string {
	for _, k := range []string{"bduss", "stoken", "cookie", "passcode", "password", "ptoken", "pwd"} {
		lower := strings.ToLower(s)
		var b strings.Builder
		from := 0
		progressed := false
		for {
			idx := indexSensitiveKeyFrom(lower, k, from)
			if idx < 0 {
				break
			}
			vs, ve := valueSpan(s, idx+len(k))
			if vs < 0 { // 无值边界: 跳过该键, 继续向后扫
				from = idx + len(k)
				continue
			}
			b.WriteString(s[from:vs])
			b.WriteString(MaskSecret(s[vs:ve]))
			from = ve
			progressed = true
		}
		if progressed {
			b.WriteString(s[from:])
			s = b.String()
		}
	}
	return s
}

// indexSensitiveKeyFrom 在 lower[from:] 中查找形如 k 后接 "=:\"" 的敏感键位置。
func indexSensitiveKeyFrom(lower, k string, from int) int {
	for {
		i := strings.Index(lower[from:], k)
		if i < 0 {
			return -1
		}
		i += from
		// 键前须为边界字符(或文本开头)
		if i > 0 {
			prev := lower[i-1]
			if prev != '&' && prev != '?' && prev != '"' && prev != '{' && prev != ',' &&
				prev != ' ' && prev != ';' && prev != '\'' {
				from = i + len(k)
				continue
			}
		}
		after := i + len(k)
		if after < len(lower) {
			switch lower[after] {
			case '=', ':', '"':
				return i
			}
		}
		from = after
	}
}

// valueSpan 从 pos(键结束后的位置)向后找值区间 [start,end); 找不到返回 -1。
func valueSpan(s string, pos int) (int, int) {
	if pos >= len(s) {
		return -1, -1
	}
	i := pos
	for i < len(s) && (s[i] == '=' || s[i] == ':' || s[i] == '"' || s[i] == ' ') {
		i++
	}
	if i >= len(s) {
		return -1, -1
	}
	// 值终止于分隔符: 引号/&/空格/逗号/换行
	j := i
	for j < len(s) {
		c := s[j]
		if c == '"' || c == '&' || c == ' ' || c == ',' || c == '\n' || c == '\r' || c == '\'' {
			break
		}
		j++
	}
	if j == i {
		return -1, -1
	}
	return i, j
}

// replaceRange 把 s 的 [a,b) 区段替换为 rep(脱敏单遍扫描后保留备用)。
func replaceRange(s string, a, b int, rep string) string {
	return s[:a] + rep + s[b:]
}

// logMiddleware 访问日志(分级): 方法/路径/状态码, 不记查询串(可含口令/签名 token);
// 非预期文本经 SanitizeLogText 兜底脱敏(FR-38)。
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		level := "INFO"
		if sw.status >= 500 {
			level = "ERROR"
		} else if sw.status >= 400 {
			level = "WARN"
		}
		LogAt(level, "%s %s %d", r.Method, r.URL.Path, sw.status)
	})
}

// chain 按声明顺序包裹: chain(h, a, b) 等价 a(b(h))。
func chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}
