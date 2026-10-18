package svn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 将本地路径作为字面目标传递，URL 的显式 peg revision 保持原样。
func LiteralTarget(target string) string {
	if !isURL(target) && strings.Contains(target, "@") {
		return target + "@"
	}
	return target
}

func isURL(target string) bool { return strings.Contains(target, "://") }

func appendLiteralTargets(args, targets []string) []string {
	for _, target := range targets {
		args = append(args, LiteralTarget(target))
	}
	return args
}

// 文件缺失时从仍存在的祖先目录执行，避免把文件当作工作目录。
func commandTarget(path string) (string, string, error) {
	if isURL(path) {
		return "", path, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("解析工作副本路径: %w", err)
	}
	if info, err := os.Lstat(absolute); err == nil && info.IsDir() {
		return absolute, ".", nil
	}
	for directory := filepath.Dir(absolute); ; directory = filepath.Dir(directory) {
		if info, err := os.Stat(directory); err == nil && info.IsDir() {
			target, err := filepath.Rel(directory, absolute)
			if err != nil {
				return "", "", fmt.Errorf("解析工作副本目标: %w", err)
			}
			return directory, LiteralTarget(target), nil
		}
		if filepath.Dir(directory) == directory {
			return "", "", fmt.Errorf("找不到工作副本目标 %s 的父目录", path)
		}
	}
}
