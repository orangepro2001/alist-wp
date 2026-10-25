package session

// impl.go: 下载直链 HMAC 签名(FR-39)。口令/会话鉴权已移除(访问控制交给 tailscale serve, 仅 tailnet 可达)。

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"time"
)

// SignToken 实现 session.go 契约: HMAC-SHA256 前 16 字节 hex(§4 下载 token 规格)。
// 与 MintToken 的区别: 签名范围仅 path|expire|fp, 无版本段, 供 /api/dl 直链用。
func SignToken(sessionKey []byte, path string, expire time.Time, fp string) string {
	mac := hmac.New(sha256.New, sessionKey)
	_, _ = mac.Write([]byte(path))
	_, _ = mac.Write([]byte{'|'})
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(expire.Unix()))
	_, _ = mac.Write(buf[:])
	_, _ = mac.Write([]byte{'|'})
	_, _ = mac.Write([]byte(fp))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

// VerifyToken 实现 session.go 契约: 与 SignToken 对应的恒定时间校验。
func VerifyToken(sessionKey []byte, path string, expire time.Time, fp, token string) bool {
	given, err := hex.DecodeString(token)
	if err != nil || len(given) != 16 {
		return false
	}
	mac := hmac.New(sha256.New, sessionKey)
	_, _ = mac.Write([]byte(path))
	_, _ = mac.Write([]byte{'|'})
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(expire.Unix()))
	_, _ = mac.Write(buf[:])
	_, _ = mac.Write([]byte{'|'})
	_, _ = mac.Write([]byte(fp))
	want := mac.Sum(nil)[:16]
	return subtle.ConstantTimeCompare(want, given) == 1
}
