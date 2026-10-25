// Command baidupcs-web 把百度网盘(BaiduPCS-Go)暴露为 WebDAV 服务, 供 OpenList/Alist 挂载。
// 职责仅限: 解析命令行、装配配置与服务、启动 HTTP 监听、账号子命令(account.go); 业务逻辑一律在 internal/*。
package main

import (
	"flag"
	"fmt"
	"os"

	"baidupcs-web/internal/config"
	"baidupcs-web/internal/server"
)

// version 由构建注入: -ldflags "-X main.version=vX.Y.Z"。
var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "account" {
		os.Exit(runAccount(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	var (
		listen   = flag.String("listen", envOr("BPW_LISTEN", "127.0.0.1:8080"), "HTTP 监听地址(默认仅回环, 经 tailscale serve 暴露; 环境变量 BPW_LISTEN 可改默认值)")
		dataDir  = flag.String("data", "", "数据目录, 默认 ~/.local/share/baidupcs-web")
		tmpDir   = flag.String("tmp", "", "上传临时目录, 默认 ~/.cache/baidupcs-web/tmp (决策 D4)")
		logLevel = flag.String("log-level", "", "日志级别 DEBUG/INFO/WARN/ERROR(NFR-07); 亦可经 BPW_LOG_LEVEL")
		showVer  = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	// W9 日志分级(NFR-07): flag 优先, 环境变量 BPW_LOG_LEVEL 兜底, 默认 INFO。
	server.SetupLogLevel(firstNonEmpty(*logLevel, os.Getenv("BPW_LOG_LEVEL")))

	if *showVer {
		fmt.Println("baidupcs-web", version)
		return
	}

	cfg, err := config.Load(config.Options{
		Listen:  *listen,
		DataDir: *dataDir,
		TmpDir:  *tmpDir,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置加载失败:", err)
		os.Exit(1)
	}
	srv, err := server.New(server.Options{Config: cfg, Version: version})
	if err != nil {
		fmt.Fprintln(os.Stderr, "服务初始化失败:", err)
		os.Exit(1)
	}
	// NFR-06: 监听失败(含端口被占)必须报错退出, 禁止静默换端口。
	fmt.Fprintf(os.Stderr, "baidupcs-web %s 监听 %s (WebDAV: /dav/)\n", version, cfg.Listen)
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "服务退出:", err)
		os.Exit(1)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
