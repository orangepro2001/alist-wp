package xengine

// accounts.go 凭证 JSON 持久化(§0.6): accounts.json 0600, 原子写, 目录 0700。
// 镜像上游 pcsconfig.Baidu 的必要字段; 禁止任何明文凭证进日志(FR-38 由上层负责)。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// accountsFile 凭证文件名。
const accountsFile = "accounts.json"

// accountDoc accounts.json 文档结构。
type accountDoc struct {
	ActiveUID uint64     `json:"active_uid"`
	Accounts  []*Account `json:"accounts"`
}

// accountStore 内存态账号表 + 落盘。
type accountStore struct {
	mu        sync.Mutex
	path      string // accounts.json 绝对路径
	activeUID uint64
	list      []*Account
}

// loadAccountStore 读取(或初始化)dataDir/accounts.json。
func loadAccountStore(dataDir string) (*accountStore, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录 %s: %w", dataDir, err)
	}
	s := &accountStore{path: filepath.Join(dataDir, accountsFile)}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil // 首次运行, 空表
	}
	if err != nil {
		return nil, fmt.Errorf("读取凭证 %s: %w", s.path, err)
	}
	doc := &accountDoc{}
	if err := json.Unmarshal(data, doc); err != nil {
		return nil, fmt.Errorf("解析凭证 %s: %w", s.path, err)
	}
	s.activeUID = doc.ActiveUID
	s.list = doc.Accounts
	return s, nil
}

// get 读取账号(不复制, 调用方不得改写字段; 引擎内均按只读使用)。
func (s *accountStore) get(uid uint64) *Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.list {
		if a.UID == uid {
			return a
		}
	}
	return nil
}

// upsert 新增或覆盖账号并落盘。
func (s *accountStore) upsert(a *Account) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	replaced := false
	for i, old := range s.list {
		if old.UID == a.UID {
			s.list[i] = a
			replaced = true
			break
		}
	}
	if !replaced {
		s.list = append(s.list, a)
	}
	return s.saveLocked()
}

// remove 删除账号并落盘。
func (s *accountStore) remove(uid uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.list[:0]
	for _, a := range s.list {
		if a.UID != uid {
			out = append(out, a)
		}
	}
	s.list = out
	if s.activeUID == uid {
		s.activeUID = 0
	}
	return s.saveLocked()
}

// setActive 持久化活跃账号。
func (s *accountStore) setActive(uid uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeUID = uid
	return s.saveLocked()
}

// saveLocked 原子写盘(临时文件+rename+fsync, 0600)。调用方须持 s.mu。
func (s *accountStore) saveLocked() error {
	doc := accountDoc{ActiveUID: s.activeUID, Accounts: s.list}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化凭证: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".accounts-*.json.tmp")
	if err != nil {
		return fmt.Errorf("创建临时凭证文件: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil { // 显式 0600 兜底 umask
		tmp.Close()
		return fmt.Errorf("收紧凭证文件权限: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写临时凭证文件: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fsync 凭证: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时凭证文件: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("原子替换凭证 %s: %w", s.path, err)
	}
	// 目录权限 0700 兜底 umask(用户主目录默认 0755 时 MkdirAll 不会收紧)
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("收紧凭证目录权限: %w", err)
	}
	return nil
}
