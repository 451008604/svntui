package svn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (c Client) UpdateWithOptions(ctx context.Context, path string, opts UpdateOptions) (Result, error) {
	args := []string{"update", "--non-interactive", "--accept", "postpone"}
	if opts.Revision != "" {
		args = append(args, "-r", opts.Revision)
	}
	if opts.Depth != "" {
		args = append(args, "--set-depth", opts.Depth)
	}
	if opts.IgnoreExternals {
		args = append(args, "--ignore-externals")
	}
	args = append(args, "--")
	args = appendLiteralTargets(args, opts.Targets)
	if len(opts.Targets) == 0 {
		args = append(args, ".")
	}
	return c.Run(ctx, path, args...)
}

func (c Client) Changelist(ctx context.Context, path, name string, targets []string, remove bool) (Result, error) {
	if len(targets) == 0 {
		return Result{}, errors.New("changelist 需要目标文件")
	}
	args := []string{"changelist"}
	if remove {
		args = append(args, "--remove", "--")
	} else {
		if name == "" {
			return Result{}, errors.New("changelist 名称不能为空")
		}
		args = append(args, "--", name)
	}
	return c.Run(ctx, path, appendLiteralTargets(args, targets)...)
}

func (c Client) Properties(ctx context.Context, path, target string) (map[string]string, error) {
	if target == "" {
		target = "."
	}
	result, err := c.Run(ctx, path, "proplist", "--xml", "--verbose", "--non-interactive", "--", LiteralTarget(target))
	if err != nil {
		return nil, err
	}
	return parseProperties([]byte(result.Stdout))
}

func (c Client) DeleteProperty(ctx context.Context, path, name string, targets []string) (Result, error) {
	args := []string{"propdel", "--", name}
	args = appendLiteralTargets(args, targets)
	if len(targets) == 0 {
		args = append(args, ".")
	}
	return c.Run(ctx, path, args...)
}

func (c Client) DiffRevisions(ctx context.Context, path, target, left, right string) (string, error) {
	if left == "" || right == "" {
		return "", errors.New("比较需要左右两个修订")
	}
	if target == "" {
		target = "."
	}
	result, err := c.Run(ctx, path, "diff", "--git", "--non-interactive", "-r", left+":"+right, "--", LiteralTarget(target))
	return result.Stdout, err
}

func (c Client) Cat(ctx context.Context, target, revision string) (string, error) {
	args := []string{"cat", "--non-interactive"}
	if revision != "" {
		args = append(args, "-r", revision)
	}
	result, err := c.Run(ctx, "", append(args, "--", LiteralTarget(target))...)
	return result.Stdout, err
}

func (c Client) BlameWithContent(ctx context.Context, target, revision string) ([]BlameLine, error) {
	// 固定一次修订，避免两次命令之间 HEAD 前进导致内容错位。
	info, err := c.InfoAt(ctx, target, revision)
	if err != nil {
		return nil, err
	}
	if info.Revision <= 0 {
		return nil, errors.New("目标尚无可追溯的修订")
	}
	resolvedRevision := fmt.Sprint(info.Revision)
	resolvedTarget := info.URL + "@" + resolvedRevision
	lines, err := c.Blame(ctx, resolvedTarget, resolvedRevision)
	if err != nil {
		return nil, err
	}
	content, err := c.Cat(ctx, resolvedTarget, resolvedRevision)
	if err != nil {
		return nil, err
	}
	content = strings.TrimSuffix(content, "\n")
	index := 0
	if content != "" || len(lines) > 0 {
		for value := range strings.SplitSeq(content, "\n") {
			if index >= len(lines) {
				return nil, errors.New("blame 与文件内容行数不一致")
			}
			lines[index].Content = strings.TrimSuffix(value, "\r")
			index++
		}
	}
	if index != len(lines) {
		return nil, errors.New("blame 与文件内容行数不一致")
	}
	return lines, nil
}

func (c Client) Patch(ctx context.Context, path, patchFile string, dryRun bool) (Result, error) {
	args := []string{"patch", "--non-interactive"}
	if dryRun {
		args = append(args, "--dry-run")
	}
	// patch 仍会解析参数中的 peg，补丁文件名也需要转义。
	return c.Run(ctx, path, append(args, "--", LiteralTarget(patchFile), ".")...)
}

// 流式写出完整补丁，命令成功后原子替换目标文件。
func (c Client) DiffToFile(ctx context.Context, path string, targets []string, destination string) error {
	return c.diffToFile(ctx, path, targets, destination, true)
}

// SafePoint 使用原生 SVN 补丁保留目录与属性状态。
func (c Client) NativeDiffToFile(ctx context.Context, path string, targets []string, destination string) error {
	return c.diffToFile(ctx, path, targets, destination, false)
}

func (c Client) diffToFile(ctx context.Context, path string, targets []string, destination string, git bool) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".svntui-diff-*")
	if err != nil {
		return fmt.Errorf("创建补丁文件: %w", err)
	}
	defer os.Remove(temporary.Name())
	args := []string{"diff"}
	if git {
		args = append(args, "--git")
	} else {
		// 复制节点必须携带完整新增内容，保证补丁可独立重建子树。
		args = append(args, "--show-copies-as-adds")
	}
	args = append(args, "--")
	args = append(args, targets...)
	if len(targets) == 0 {
		args = append(args, ".")
	}
	_, runErr := c.runWithOutput(ctx, path, temporary, destination, args...)
	closeErr := temporary.Close()
	if err := errors.Join(runErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return fmt.Errorf("保存完整补丁: %w", err)
	}
	return nil
}
