package webui

// fs_assets_test.go W4: embed 资源完整性(handler 路由行为在 server 包冒烟覆盖)。

import (
	"io/fs"
	"testing"
)

func TestEmbedIndexNonEmpty(t *testing.T) {
	data, err := fsIndex()
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("index.html empty")
	}
}

// 静态资源齐全(app.js/pv.js/style.css 随 embed 进二进制)。
func TestEmbedAssets(t *testing.T) {
	for _, name := range []string{"app.js", "pv.js", "style.css"} {
		if _, err := fs.Stat(Files, "dist/"+name); err != nil {
			t.Errorf("dist/%s 缺失: %v", name, err)
		}
	}
}

// fsSub 剥离 dist/ 前缀后可按名读取。
func TestFSSubStrip(t *testing.T) {
	sub, err := fsSub()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.ReadFile(sub, "app.js"); err != nil {
		t.Fatalf("sub FS 读 app.js: %v", err)
	}
}
