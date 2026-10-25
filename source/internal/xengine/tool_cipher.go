package xengine

// tool_cipher.go W9: 上游 `tool enc/dec` 命令底层封装(FR-35 工具页加解密)。
// 1:1 透传上游 pcsutil.EncryptFile/DecryptFile(aes-*-{ctr,cfb,ofb},
// key 修剪/补零、gzip 默认启用), 不自创加解密实现。

import pcsutil "github.com/qjfoidnh/BaiduPCS-Go/pcsutil"

// CipherMethodSupport 报告加解密方法是否受支持(上游 CryptoMethodSupport 全集)。
func CipherMethodSupport(method string) bool { return pcsutil.CryptoMethodSupport(method) }

// CipherEncryptFile 加密本地文件, 返回输出路径(<path>.encrypt, 上游语义)。
func CipherEncryptFile(method string, key []byte, filePath string, isGzip bool) (string, error) {
	return pcsutil.EncryptFile(method, key, filePath, isGzip)
}

// CipherDecryptFile 解密本地文件, 返回输出路径(去 .encrypt 后缀, 上游语义)。
func CipherDecryptFile(method string, key []byte, filePath string, isGzip bool) (string, error) {
	return pcsutil.DecryptFile(method, key, filePath, isGzip)
}
