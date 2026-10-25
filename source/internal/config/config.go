// Package config 定义 Web 服务自身配置(区别于上游 pcsconfig 的百度传输配置)。
// 存储格式 JSON, 路径 <DataDir>/config.json, 文件权限 0600(NFR-08)。
package config

import "os"

// Options 命令行/环境变量注入的启动选项。
type Options struct {
	Listen   string // HTTP 监听地址, 默认 0.0.0.0:8080
	DataDir  string // 数据目录; 空则取 ~/.local/share/baidupcs-web
	TmpDir   string // 上传临时目录(D4); 空则取 ~/.cache/baidupcs-web/tmp
	BasePath string // URL 路径前缀(如 /baidu), 空=挂在根路径; 运行期参数, 不落盘
}

// Config 服务运行时配置(持久化部分 + 运行期派生字段)。
type Config struct {
	Listen string `json:"listen"` // 监听地址

	// 下载直链签名密钥, 首次启动随机生成并持久化(重启后已签发的直链在有效期内仍有效)
	SessionKey []byte `json:"session_key"`

	BasePath string `json:"-"` // URL 路径前缀(规范化后形如 /baidu, 空=根), 来自启动参数

	DataDir string `json:"data_dir"` // 数据目录(凭证/任务库/配置)
	TmpDir  string `json:"tmp_dir"`  // 上传临时目录(D4 默认 ~/.cache/baidupcs-web/tmp)

	// 传输默认值(映射上游 pcsconfig 字段, FR-34)
	MaxParallel       int    `json:"max_parallel"`         // 下载分片并发, 默认 1(决策 D6, 非 SVIP 调大有限速风险)
	MaxDownloadLoad   int    `json:"max_download_load"`    // 同时下载文件数, 默认 3
	MaxUploadParallel int    `json:"max_upload_parallel"`  // 上传单文件分片并发, 默认 4
	MaxUploadLoad     int    `json:"max_upload_load"`      // 同时上传文件数, 默认 2
	MaxDownloadRate   int64  `json:"max_download_rate"`    // 下载限速 B/s, 0 不限
	MaxUploadRate     int64  `json:"max_upload_rate"`      // 上传限速 B/s, 0 不限
	UPolicy           string `json:"u_policy"`             // skip | overwrite | rsync(上游 baidupcs.SkipPolicy 等)
	UserAgent         string `json:"user_agent,omitempty"` // 高级项(折叠), 默认空=跟随上游

	path string // config.json 绝对路径(运行期字段, 不序列化)
}

// Load 读取(或初始化)配置文件并应用启动选项覆盖; 保证目录存在。
func Load(opt Options) (*Config, error) { return load(opt) }

// Save 原子落盘(临时文件+rename), 权限 0600。
func (c *Config) Save() error { return c.save() }

// dataDirDefault 返回默认数据目录; 供 load 使用, 独立成函数便于测试。
func dataDirDefault() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return home + "/.local/share/baidupcs-web", nil
}
