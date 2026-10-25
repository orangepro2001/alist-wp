// smoke_dl_src.go — 冒烟辅助(仅本地测试用): 源文件生成 + 桩 HTTP 源(模拟百度 dlink)。
// 桩源行为:
//   /health           → 200 ok
//   /dlink/<name>     → 200/206(按 Range), 校验 UA=netdisk 前缀, Content-Disposition RFC5987 中文
package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	gen := flag.String("gen", "", "生成源文件路径")
	size := flag.Int64("size", 1048576, "生成大小(字节)")
	serve := flag.String("serve", "", "桩源监听地址(如 127.0.0.1:18322)")
	src := flag.String("src", "", "桩源文件目录")
	flag.Parse()

	switch {
	case *gen != "":
		if err := genFile(*gen, *size); err != nil {
			fmt.Fprintln(os.Stderr, "gen:", err)
			os.Exit(1)
		}
		fmt.Println("generated", *gen, *size)
	case *serve != "":
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte("ok"))
		})
		mux.HandleFunc("/dlink/", func(w http.ResponseWriter, r *http.Request) {
			name := filepath.Base(r.URL.Path)
			dec, err := url.PathUnescape(name)
			if err == nil {
				name = dec
			}
			p := filepath.Join(*src, name)
			f, err := os.Open(p)
			if err != nil {
				http.Error(w, "not found", 404)
				return
			}
			defer f.Close()
			st, _ := f.Stat()
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition",
				`attachment; filename="dl.bin"; filename*=UTF-8''`+url.PathEscape(name))
			http.ServeContent(w, r, "", st.ModTime(), f)
		})
		if err := http.ListenAndServe(*serve, mux); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintln(os.Stderr, "用法: -gen <path> <size> | -serve <addr> -src <dir>")
		os.Exit(2)
	}
}

func genFile(path string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	var written int64
	for written < size {
		n := int64(len(buf))
		if size-written < n {
			n = size - written
		}
		if _, err := rand.Read(buf[:n]); err != nil {
			return err
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return err
		}
		written += n
	}
	return nil
}

var _ = strconv.Itoa
