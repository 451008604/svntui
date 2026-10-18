//go:build !windows

package automation

import (
	"errors"
	"os"
)

func checkSecretFilePermissions(_ *os.File, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("Webhook 密钥文件不能允许用户组或其他用户访问")
	}
	return nil
}
