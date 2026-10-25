package api

// tools.go W9: 工具域 REST handlers(FR-35/FR-13 + 上游 tool 加解密), §4 接口表:
//   POST /api/match        → FR-35 通配符测试 {pattern} → {list:[...]}
//   POST /api/export       → FR-13 秒传导出 {path} → {md5,slice_md5,crc32,length,...,warning}
//   POST /api/tool/cipher  → 上游同款加解密(pcsutil.EncryptFile/DecryptFile)
//
// 上游对应函数(§0.3): MatchPathByShellPattern / ExportByFileInfo;
// cipher: 上游 `tool enc/dec` 命令底层 pcsutil.EncryptFile/DecryptFile(aes-*-{ctr,cfb,ofb},
// key 修剪/补零、默认 gzip, 1:1 透传语义)。

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"baidupcs-web/internal/xengine"
)

// toolsAPI 工具域 handler 集合, 由 server 装配。
type toolsAPI struct {
	eng xengine.Engine
}

// NewTools 构造工具域 handlers。
func NewTools(eng xengine.Engine) *toolsAPI { return &toolsAPI{eng: eng} }

// matchRequest POST /api/match 请求体(FR-35)。
type matchRequest struct {
	Pattern string `json:"pattern"`
}

// HandleMatch POST /api/match: 通配符测试(只读调试), pattern 须绝对路径(上游语义)。
func (t *toolsAPI) HandleMatch(w http.ResponseWriter, r *http.Request) {
	var req matchRequest
	if err := decodeBody(w, r, &req, 16<<10); err != nil {
		return
	}
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" || !strings.HasPrefix(pattern, "/") {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "pattern 须为绝对路径表达式, 如 /我的资源/*.mp4")
		return
	}
	paths, err := t.eng.Match(r.Context(), pattern)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	if paths == nil {
		paths = []string{}
	}
	writeOK(w, map[string]interface{}{"pattern": pattern, "total": len(paths), "list": paths})
}

// exportRequest POST /api/export 请求体(FR-13)。
type exportRequest struct {
	Path string `json:"path"`
}

// HandleExport POST /api/export: 秒传信息导出(FR-13)。
// 响应带 warning: 秒传三元组(md5/长度/CRC)等价于文件本身, 勿外传。
func (t *toolsAPI) HandleExport(w http.ResponseWriter, r *http.Request) {
	var req exportRequest
	if err := decodeBody(w, r, &req, 16<<10); err != nil {
		return
	}
	p := strings.TrimSpace(req.Path)
	if p == "" || !strings.HasPrefix(p, "/") {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "缺少 path 参数(须为网盘绝对路径)")
		return
	}
	link, err := t.eng.Export(r.Context(), p)
	if err != nil {
		writeEngineErr(w, err)
		return
	}
	writeOK(w, map[string]interface{}{
		"path":      p,
		"filename":  link.Filename,
		"length":    link.Length,
		"md5":       link.ContentMD5,
		"slice_md5": link.SliceMD5,
		"crc32":     link.Crc32,
		"warning":   "秒传信息等价于文件本身, 且直链含凭证, 勿外传",
	})
}

// cipherMethodSupport 透传上游 CryptoMethodSupport(aes-*-{ctr,cfb,ofb}, 经 xengine 封装)。
func cipherMethodSupport(method string) bool { return xengine.CipherMethodSupport(method) }

// cipherRequest POST /api/tool/cipher 请求体(上游 `tool enc/dec` 参数对齐)。
type cipherRequest struct {
	Mode   string `json:"mode"`   // encrypt | decrypt
	Method string `json:"method"` // aes-128-ctr 等(上游 CryptoMethodSupport 全集)
	Key    string `json:"key"`    // 对称密钥(16/24/32 字节, 不足补 \0 超长裁剪, 上游语义)
	Path   string `json:"path"`   // 服务器本地待处理文件绝对路径
	Gzip   *bool  `json:"gzip"`   // 缺省 true(上游 disable-gzip 反向; 加密前压缩/解密后解压)
}

// errCipher 统一 400。
func errCipher(msg string) error { return &errString{msg} }

// HandleToolCipher POST /api/tool/cipher: 上游同款文件加解密。
// 语义 1:1 对齐上游 pcsutil.EncryptFile/DecryptFile: 输出 <path>.encrypt;
// decrypt 输出去 .encrypt 后缀; gzip 默认启用。限制: path 必须存在且为普通文件。
func (t *toolsAPI) HandleToolCipher(w http.ResponseWriter, r *http.Request) {
	var req cipherRequest
	if err := decodeBody(w, r, &req, 64<<10); err != nil {
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode != "encrypt" && mode != "decrypt" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "mode 须为 encrypt|decrypt")
		return
	}
	method := strings.TrimSpace(req.Method)
	if !cipherMethodSupport(method) {
		writeErr(w, http.StatusBadRequest, "INTERNAL",
			"method 仅支持 aes-128-ctr/aes-192-ctr/aes-256-ctr/aes-128-cfb/aes-192-cfb/aes-256-cfb/aes-128-ofb/aes-192-ofb/aes-256-ofb")
		return
	}
	if strings.TrimSpace(req.Key) == "" {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "key 不可为空")
		return
	}
	p := filepath.Clean(strings.TrimSpace(req.Path))
	if !filepath.IsAbs(p) {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "path 须为服务器本地绝对路径")
		return
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "path 不存在或不是普通文件")
		return
	}
	gzip := true
	if req.Gzip != nil {
		gzip = *req.Gzip
	}
	key := []byte(req.Key)
	var out string
	switch mode {
	case "encrypt":
		out, err = xengine.CipherEncryptFile(method, key, p, gzip)
	default:
		out, err = xengine.CipherDecryptFile(method, key, p, gzip)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL",
			"加解密失败: "+err.Error())
		return
	}
	var outSize int64
	if fi, err := os.Stat(out); err == nil {
		outSize = fi.Size()
	}
	writeOK(w, map[string]interface{}{
		"ok": true, "mode": mode, "method": method, "output": out, "output_size": outSize,
	})
}

// decodeBody 请求体 JSON 解码(带大小上限); 失败时已写 400 响应。
func decodeBody(w http.ResponseWriter, r *http.Request, v interface{}, limit int64) error {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "INTERNAL", "请求体 JSON 非法")
		return err
	}
	return nil
}
