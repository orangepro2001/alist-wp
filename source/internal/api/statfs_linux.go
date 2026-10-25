package api

// statfs_linux.go — diskFree 的 Linux 实现(statfs, CGO_ENABLED=0 纯 Go)。

import "syscall"

type syscallStatfs = syscall.Statfs_t

func statfs(path string, st *syscallStatfs) error { return syscall.Statfs(path, st) }
