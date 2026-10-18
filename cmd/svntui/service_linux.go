//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	systemdunit "svntui/systemd"
)

func runService(ctx context.Context, env environment, args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return errors.New("用法: svntui automation service <print|install|uninstall>")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取当前程序路径: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("解析当前程序路径: %w", err)
	}
	unit, err := systemdunit.UserService(executable)
	if err != nil {
		return err
	}
	unitPath := filepath.Join(filepath.Dir(env.dirs.Config), "systemd", "user", "svntui-automation.service")
	switch args[0] {
	case "print":
		_, err = io.WriteString(stdout, unit)
		return err
	case "install":
		if err := os.MkdirAll(filepath.Dir(unitPath), 0o700); err != nil {
			return fmt.Errorf("创建 systemd 用户目录: %w", err)
		}
		if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
			return fmt.Errorf("写入 systemd 服务文件: %w", err)
		}
		if err := runSystemctl(ctx, "daemon-reload"); err != nil {
			return fmt.Errorf("服务文件已写入 %s，但重新加载失败: %w", unitPath, err)
		}
		if err := runSystemctl(ctx, "enable", "--now", "svntui-automation.service"); err != nil {
			return fmt.Errorf("服务文件已写入 %s，但启用失败: %w", unitPath, err)
		}
		fmt.Fprintln(stdout, unitPath)
		return nil
	case "uninstall":
		if err := runSystemctl(ctx, "disable", "--now", "svntui-automation.service"); err != nil {
			return fmt.Errorf("停止服务失败，已保留服务文件: %w", err)
		}
		if err := os.Remove(unitPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除 systemd 服务文件: %w", err)
		}
		return runSystemctl(ctx, "daemon-reload")
	default:
		return errors.New("service 子命令: print, install, uninstall")
	}
}

func runSystemctl(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("执行 systemctl %s 失败: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
