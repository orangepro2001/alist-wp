module baidupcs-web

go 1.23.0

// 上游基线(NFR-10/D9): github.com/qjfoidnh/BaiduPCS-Go v4.0.2
// 由 Wave 2 执行 `go get github.com/qjfoidnh/BaiduPCS-Go@v4.0.2 && go mod tidy` 落地;
// 骨架(W1)零第三方依赖, 纯标准库, 保证离线 `go build ./...` 可过。

require (
	github.com/qjfoidnh/BaiduPCS-Go v0.0.0-20260821135237-225bdd3b6cb2
	golang.org/x/crypto v0.36.0
)

require (
	github.com/bitly/go-simplejson v0.5.0 // indirect
	github.com/fatih/color v1.18.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kardianos/osext v0.0.0-20190222173326-2bc1f35cddc0 // indirect
	github.com/mattn/go-colorable v0.1.13 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-runewidth v0.0.9 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/oleiade/lane v1.0.1 // indirect
	github.com/olekukonko/tablewriter v0.0.4 // indirect
	github.com/qjfoidnh/Baidu-Login v1.4.1 // indirect
	github.com/qjfoidnh/baidu-tools v1.2.0 // indirect
	github.com/rs/dnscache v0.0.0-20230804202142-fc85eb664529 // indirect
	github.com/tidwall/gjson v1.18.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	golang.org/x/net v0.38.0 // indirect
	golang.org/x/sync v0.0.0-20190911185100-cd5d95a43a6e // indirect
	golang.org/x/sys v0.31.0 // indirect
	google.golang.org/protobuf v1.33.0 // indirect
)
