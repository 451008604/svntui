//go:build !linux && !windows

package main

import (
	"context"
	"errors"
	"io"
)

func runService(context.Context, environment, []string, io.Writer) error {
	return errors.New("当前系统不支持自动安装后台服务；请直接托管 svntui automation run")
}
