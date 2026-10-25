package server

import (
	"testing"

	"baidupcs-web/internal/config"
)

// W1 骨架冒烟: 装配不 panic、Handler 非 nil。W3 起带真实鉴权装配(临时数据目录)。
func TestNewServer(t *testing.T) {
	cfg, err := config.Load(config.Options{DataDir: t.TempDir(), Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	s, err := New(Options{Config: cfg, Version: "test"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if s.Handler() == nil {
		t.Fatal("Handler() = nil")
	}
}
