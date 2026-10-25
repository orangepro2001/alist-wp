package main

// account.go: 百度账号管理子命令。服务没有 Web 页面, 账号经本命令调用运行中服务的
// 回环接口 /api/accounts*(不直接改 accounts.json, 避免与服务进程互相覆盖)。
//
//	baidupcs-web account list
//	baidupcs-web account add -cookie - < cookie.txt        (整串 Cookie, "-" 表示读 stdin)
//	baidupcs-web account add -bduss X -stoken Y
//	baidupcs-web account switch <uid>
//	baidupcs-web account rm <uid>
//
// 目标地址默认取 BPW_LISTEN(无则 127.0.0.1:8080), 可用 -addr 覆盖; 容器内用
// docker exec <容器> baidupcs-web account ... 即可。

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const accountUsage = `用法:
  baidupcs-web account list
  baidupcs-web account add -cookie - < cookie.txt     (整串 Cookie 经 stdin 传入, 不进进程列表)
  baidupcs-web account add -cookie-file cookie.txt
  baidupcs-web account add -bduss BDUSS -stoken STOKEN
  baidupcs-web account switch <uid>
  baidupcs-web account rm <uid>
公共选项: -addr host:port (默认取 BPW_LISTEN, 无则 127.0.0.1:8080)
`

// loopbackAddr 把监听地址变成可连接的回环地址(0.0.0.0/空主机 → 127.0.0.1)。
func loopbackAddr(listen string) string {
	if listen == "" {
		return "127.0.0.1:8080"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func runAccount(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, accountUsage)
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("account "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", loopbackAddr(os.Getenv("BPW_LISTEN")), "服务地址 host:port")
	var cookie, cookieFile, bduss, stoken *string
	if sub == "add" {
		cookie = fs.String("cookie", "", `整串 Cookie; "-" 表示从 stdin 读取`)
		cookieFile = fs.String("cookie-file", "", "从文件读取整串 Cookie")
		bduss = fs.String("bduss", "", "BDUSS")
		stoken = fs.String("stoken", "", "STOKEN(pan.baidu.com 域)")
	}
	if err := fs.Parse(rest); err != nil {
		return 2
	}
	base := "http://" + *addr + "/api/accounts"

	switch sub {
	case "list":
		var out struct {
			Accounts []struct {
				UID    uint64 `json:"uid"`
				Name   string `json:"name"`
				Active bool   `json:"active"`
			} `json:"accounts"`
		}
		if err := callJSON(http.MethodGet, base, nil, &out); err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
		if len(out.Accounts) == 0 {
			fmt.Fprintln(stdout, "(尚未绑定百度账号)")
		}
		for _, a := range out.Accounts {
			mark := " "
			if a.Active {
				mark = "*"
			}
			fmt.Fprintf(stdout, "%s %d\t%s\n", mark, a.UID, a.Name)
		}
		return 0

	case "add":
		body := map[string]string{}
		switch {
		case *cookie == "-":
			b, err := io.ReadAll(io.LimitReader(stdin, 64<<10))
			if err != nil {
				fmt.Fprintln(stderr, "读取 stdin 失败:", err)
				return 1
			}
			body["cookie"] = strings.TrimSpace(string(b))
		case *cookieFile != "":
			b, err := os.ReadFile(*cookieFile)
			if err != nil {
				fmt.Fprintln(stderr, "读取 Cookie 文件失败:", err)
				return 1
			}
			body["cookie"] = strings.TrimSpace(string(b))
		case *cookie != "":
			body["cookie"] = *cookie
		case *bduss != "":
			body["bduss"], body["stoken"] = *bduss, *stoken
		default:
			fmt.Fprint(stderr, accountUsage)
			return 2
		}
		var out struct {
			UID  uint64 `json:"uid"`
			Name string `json:"name"`
		}
		if err := callJSON(http.MethodPost, base+"/add", body, &out); err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
		fmt.Fprintf(stdout, "已绑定: %d\t%s\n", out.UID, out.Name)
		return 0

	case "switch", "rm":
		if fs.NArg() != 1 {
			fmt.Fprint(stderr, accountUsage)
			return 2
		}
		uid, err := strconv.ParseUint(fs.Arg(0), 10, 64)
		if err != nil || uid == 0 {
			fmt.Fprintln(stderr, "uid 须为正整数")
			return 2
		}
		method, url := http.MethodPost, fmt.Sprintf("%s/%d/switch", base, uid)
		if sub == "rm" {
			method, url = http.MethodDelete, fmt.Sprintf("%s/%d", base, uid)
		}
		if err := callJSON(method, url, nil, nil); err != nil {
			fmt.Fprintln(stderr, "错误:", err)
			return 1
		}
		fmt.Fprintln(stdout, "完成")
		return 0
	}
	fmt.Fprint(stderr, accountUsage)
	return 2
}

// callJSON 发一次 JSON 请求; 非 2xx 返回服务端 message。
func callJSON(method, url string, in, out any) error {
	var rd io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req) // 绑定账号要访问百度, 给足时间
	if err != nil {
		return fmt.Errorf("连接服务失败(服务是否已启动?): %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(data, &e) == nil && e.Message != "" {
			return fmt.Errorf("%s (HTTP %d)", e.Message, resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}
