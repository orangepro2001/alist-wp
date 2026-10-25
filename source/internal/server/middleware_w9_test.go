package server

// middleware_w9_test.go W9: 凭证脱敏(FR-38)与日志分级单测。

import (
	"strings"
	"testing"
)

// TestMaskSecret FR-38: 前 4 后 4; 短值全掩码; 空值不变。
func TestMaskSecret(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "****"},
		{"12345678901", "****"},            // 11 字符(<12)全掩码
		{"ABCDEFGHIJKLMNOP", "ABCD****MNOP"}, // 16 字符前4后4
	}
	for _, c := range cases {
		if got := MaskSecret(c.in); got != c.want {
			t.Errorf("MaskSecret(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeLogText FR-38: 日志中 BDUSS/STOKEN/Cookie/passcode 不出现明文。
func TestSanitizeLogText(t *testing.T) {
	const bduss = "ABCDEFGH1234567890IJKL"
	const stoken = "ZXCVBNM0987654321QWER"
	cases := []struct{ name, in string }{
		{"kv 形态", "login ok BDUSS=" + bduss + " stoken=" + stoken},
		{"JSON 形态", `{"bduss":"` + bduss + `","stoken":"` + stoken + `"}`},
		{"URL pwd 形态", "https://pan.baidu.com/s/1abc?pwd=998877665544"},
	}
	for _, c := range cases {
		out := SanitizeLogText(c.in)
		if strings.Contains(out, bduss) || strings.Contains(out, stoken) {
			t.Errorf("%s: 明文残留: %s", c.name, out)
		}
	}
	// URL pwd 值脱敏
	out := SanitizeLogText("https://pan.baidu.com/s/1abc?pwd=998877665544")
	if strings.Contains(out, "998877665544") {
		t.Errorf("pwd 明文残留: %s", out)
	}
	// 普通文本不受影响
	if got := SanitizeLogText("GET /api/files 200"); got != "GET /api/files 200" {
		t.Errorf("普通文本被误改: %s", got)
	}
}

// TestSetupLogLevel 分级过滤: ERROR 级阈值下 INFO 被丢弃(捕获 stdout 不便, 只验阈值语义)。
func TestSetupLogLevel(t *testing.T) {
	old := logLevelThreshold
	defer func() { logLevelThreshold = old }()
	SetupLogLevel("error")
	if logLevelThreshold != 3 {
		t.Fatalf("threshold = %d, want 3", logLevelThreshold)
	}
	SetupLogLevel("bogus")
	if logLevelThreshold != 1 {
		t.Fatalf("非法级别应回落 INFO(1), got %d", logLevelThreshold)
	}
	// LogAt 低级别被吞(不 panic 即可)
	LogAt("debug", "quiet %d", 1)
	LogAt("ERROR", "loud %d", 1)
}
