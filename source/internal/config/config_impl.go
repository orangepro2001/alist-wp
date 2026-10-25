package config

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func load(opt Options) (*Config, error) {
	dataDir := opt.DataDir
	if dataDir == "" {
		d, err := dataDirDefault()
		if err != nil {
			return nil, err
		}
		dataDir = d
	}
	// §5.6: 目录 0700(config.json 0600 同理, NFR-08 凭证/配置目录收紧)。
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录 %s: %w", dataDir, err)
	}
	tmpDir := opt.TmpDir
	if tmpDir == "" {
		tmpDir = dataDirDefaultTmp()
	}
	path := filepath.Join(dataDir, "config.json")

	c, err := readConfigFile(path)
	if err != nil {
		return nil, err
	}
	if c == nil { // 首次运行: 建默认配置
		c = &Config{
			MaxParallel:       1,
			MaxDownloadLoad:   3,
			MaxUploadParallel: 4,
			MaxUploadLoad:     2,
			UPolicy:           "skip",
		}
		if key, err := randomSessionKey(); err != nil {
			return nil, fmt.Errorf("生成会话密钥: %w", err)
		} else {
			c.SessionKey = key
		}
	}

	// opt 覆盖(启动参数优先于持久化值)。
	if opt.Listen != "" {
		c.Listen = opt.Listen
	}
	c.DataDir = dataDir
	c.TmpDir = tmpDir
	c.path = path

	if err := c.save(); err != nil {
		return nil, err
	}
	return c, nil
}

// readConfigFile 读取 config.json; 文件不存在返回 (nil, nil) 表示首次运行。
func readConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	c := &Config{}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	return c, nil
}

// randomSessionKey 生成 32B 随机会话签名密钥(§5.5, 首次启动生成并持久化)。
func randomSessionKey() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

// dataDirDefaultTmp 默认临时目录(决策 D4)。
func dataDirDefaultTmp() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/baidupcs-web-tmp-fallback" // 无 HOME 的兜底; 运行期会告警
	}
	return home + "/.cache/baidupcs-web/tmp"
}

func (c *Config) save() error {
	// §3/§5.6 写盘纪律: 临时文件+rename 原子写, fsync, 权限 0600(NFR-08)。
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(c.path)
	tmp, err := os.CreateTemp(dir, ".config-*.json.tmp")
	if err != nil {
		return fmt.Errorf("创建临时配置文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后为无害 no-op
	// CreateTemp 已按 0600 创建, 此处显式 Chmod 兜底 umask 放宽的情形。
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("收紧配置文件权限: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写临时配置文件: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync 配置: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时配置文件: %w", err)
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return fmt.Errorf("原子替换配置 %s: %w", c.path, err)
	}
	return nil
}
