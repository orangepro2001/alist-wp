package webui

import (
	"io/fs"
	"testing"
)

// W1 骨架冒烟: embed FS 可读且包含 index.html。
func TestEmbed(t *testing.T) {
	f, err := fs.ReadFile(Files, "dist/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	if len(f) == 0 {
		t.Fatal("index.html empty")
	}
}
