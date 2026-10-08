// Package vaultpath 集中解析保险库与口令文件的位置。
//
// 命令行工具散落在 tools/ 下，各自硬编码 /var/lib/xgift 会让本地调试
// 和非生产部署都必须改代码。这里把默认值收在一处，需要时用环境变量覆盖。
package vaultpath

import "os"

const (
	defaultDB       = "/var/lib/xgift/vault.db"
	defaultPassword = "/var/lib/xgift/vault-password"
)

// DB 返回保险库路径。
func DB() string {
	if p := os.Getenv("XGIFT_VAULT_DB"); p != "" {
		return p
	}
	return defaultDB
}

// PasswordFile 返回保险库口令文件的路径。
//
// 注意返回的是"文件路径"而不是口令本身 —— vault.Open 要的是前者，
// 口令由它自己从文件里读。
func PasswordFile() string {
	if p := os.Getenv("XGIFT_PASSWORD_FILE"); p != "" {
		return p
	}
	return defaultPassword
}
