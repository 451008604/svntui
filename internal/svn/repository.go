package svn

import (
	"context"
)

// List 读取仓库 URL 下的目录成员。
func (c Client) List(ctx context.Context, repositoryURL, revision, depth string) ([]ListEntry, error) {
	args := []string{"list", "--xml", "--non-interactive"}
	args = appendRevisionAndDepth(args, revision, depth)
	args = append(args, "--", LiteralTarget(repositoryURL))
	result, err := c.Run(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	return parseList([]byte(result.Stdout))
}

// Blame 读取目标文件各行的最后修改来源。
func (c Client) Blame(ctx context.Context, target, revision string) ([]BlameLine, error) {
	args := []string{"blame", "--xml", "--non-interactive"}
	if revision != "" {
		args = append(args, "-r", revision)
	}
	args = append(args, "--", LiteralTarget(target))
	result, err := c.Run(ctx, "", args...)
	if err != nil {
		return nil, err
	}
	return parseBlame([]byte(result.Stdout))
}

// Checkout 创建指向仓库 URL 的工作副本。
func (c Client) Checkout(ctx context.Context, repositoryURL, destination, revision, depth string) (Result, error) {
	args := []string{"checkout", "--non-interactive"}
	args = appendRevisionAndDepth(args, revision, depth)
	args = append(args, "--", repositoryURL, destination)
	return c.Run(ctx, "", args...)
}

// Export 导出不包含 SVN 管理数据的目录或文件。
func (c Client) Export(ctx context.Context, source, destination, revision, depth string) (Result, error) {
	args := []string{"export", "--non-interactive"}
	args = appendRevisionAndDepth(args, revision, depth)
	args = append(args, "--", LiteralTarget(source), LiteralTarget(destination))
	return c.Run(ctx, "", args...)
}

// RemoteMkdir 在仓库中创建目录并立即提交。
func (c Client) RemoteMkdir(ctx context.Context, targetURL, message string) (Result, error) {
	return c.Run(ctx, "", "mkdir", "--non-interactive", "-m", message, "--", targetURL)
}

// RemoteDelete 删除仓库路径并立即提交。
func (c Client) RemoteDelete(ctx context.Context, targetURL, message string) (Result, error) {
	return c.Run(ctx, "", "delete", "--non-interactive", "-m", message, "--", targetURL)
}

// RemoteMove 移动仓库路径并立即提交。
func (c Client) RemoteMove(ctx context.Context, sourceURL, targetURL, message string) (Result, error) {
	return c.Run(ctx, "", "move", "--non-interactive", "-m", message, "--", sourceURL, targetURL)
}

// RemoteCopy 复制仓库路径并立即提交。
func (c Client) RemoteCopy(ctx context.Context, sourceURL, targetURL, revision, message string) (Result, error) {
	return c.Copy(ctx, sourceURL, targetURL, revision, message)
}

// Compare 对比两个任意仓库 URL；summary 只输出 changed paths。
func (c Client) Compare(ctx context.Context, leftURL, rightURL string, summary bool) (Result, error) {
	args := []string{"diff", "--non-interactive"}
	if summary {
		args = append(args, "--summarize")
	}
	args = append(args, "--", LiteralTarget(leftURL), LiteralTarget(rightURL))
	return c.Run(ctx, "", args...)
}

func appendRevisionAndDepth(args []string, revision, depth string) []string {
	if revision != "" {
		args = append(args, "-r", revision)
	}
	if depth != "" {
		args = append(args, "--depth", depth)
	}
	return args
}
