package session

// session_test.go: 下载签名 token(FR-39)单测。

import (
	"strings"
	"testing"
	"time"
)

var (
	testKey = []byte("test-key-0123456789abcdef0123456")
	testNow = time.Unix(1700000000, 0)
)

func TestSignVerifyTokenOK(t *testing.T) {
	exp := testNow.Add(30 * time.Minute)
	tok := SignToken(testKey, "/dir/file.txt", exp, "fp-1")
	if len(tok) != 32 { // 16 字节 hex
		t.Fatalf("token 长度 = %d, want 32", len(tok))
	}
	if !VerifyToken(testKey, "/dir/file.txt", exp, "fp-1", tok) {
		t.Fatal("正确 token 应通过")
	}
}

func TestVerifyTokenRejects(t *testing.T) {
	exp := testNow.Add(30 * time.Minute)
	tok := SignToken(testKey, "/dir/file.txt", exp, "fp-1")
	cases := []struct {
		name string
		path string
		exp  time.Time
		fp   string
		tok  string
	}{
		{"篡改路径", "/other", exp, "fp-1", tok},
		{"篡改指纹", "/dir/file.txt", exp, "fp-2", tok},
		{"篡改token", "/dir/file.txt", exp, "fp-1", strings.Repeat("0", 32)},
		{"过期时刻不同", "/dir/file.txt", exp.Add(time.Second), "fp-1", tok},
	}
	for _, c := range cases {
		if VerifyToken(testKey, c.path, c.exp, c.fp, c.tok) {
			t.Errorf("%s: 应拒绝", c.name)
		}
	}
	other := SignToken([]byte("other-key---------------------------"), "/dir/file.txt", exp, "fp-1")
	if VerifyToken(testKey, "/dir/file.txt", exp, "fp-1", other) {
		t.Error("跨 key token 应拒绝")
	}
}
