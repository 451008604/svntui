//go:build windows

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
)

const windowsTaskName = "svntui-automation"

func runService(ctx context.Context, _ environment, args []string, stdout io.Writer) error {
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
	taskCommand := `"` + executable + `" automation run`
	switch args[0] {
	case "print":
		fmt.Fprintf(stdout, "Windows 登录任务名称: %s\n执行命令: %s\n", windowsTaskName, taskCommand)
		return nil
	case "install":
		if err := runSchtasks(ctx, "/Create", "/TN", windowsTaskName, "/SC", "ONLOGON", "/TR", taskCommand, "/F"); err != nil {
			return fmt.Errorf("安装 Windows 登录任务失败: %w", err)
		}
		if err := runSchtasks(ctx, "/Run", "/TN", windowsTaskName); err != nil {
			return fmt.Errorf("任务已安装，但首次启动失败: %w", err)
		}
		fmt.Fprintln(stdout, "Windows 自动更新登录任务已安装并启动")
		return nil
	case "uninstall":
		_ = runSchtasks(ctx, "/End", "/TN", windowsTaskName)
		if err := runSchtasks(ctx, "/Delete", "/TN", windowsTaskName, "/F"); err != nil {
			return fmt.Errorf("删除 Windows 登录任务失败: %w", err)
		}
		fmt.Fprintln(stdout, "Windows 自动更新登录任务已删除")
		return nil
	default:
		return errors.New("service 子命令: print, install, uninstall")
	}
}

func runSchtasks(ctx context.Context, args ...string) error {
	command := exec.CommandContext(ctx, "schtasks.exe", args...)
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("执行 schtasks %s 失败: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
