package svn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const DefaultMaxOutputBytes int64 = 8 << 20

var ErrOutputLimit = errors.New("SVN 输出超过内存限制")

// 内存只保留前缀；超限标准输出完整落盘供调用方读取和删除。
type boundedOutput struct {
	limit    int64
	buffer   bytes.Buffer
	overflow bool
	spill    bool
	file     *os.File
}

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.file != nil {
		return b.file.Write(data)
	}
	count := len(data)
	remaining := b.limit - int64(b.buffer.Len())
	if int64(count) <= remaining {
		return b.buffer.Write(data)
	}
	b.overflow = true
	prefix := int(max(remaining, 0))
	if prefix > 0 {
		_, _ = b.buffer.Write(data[:prefix])
	}
	if !b.spill {
		return count, nil
	}
	file, err := os.CreateTemp("", "svntui-output-*")
	if err != nil {
		return prefix, fmt.Errorf("保存完整 SVN 输出: %w", err)
	}
	b.file = file
	if _, err := file.Write(b.buffer.Bytes()); err != nil {
		return prefix, err
	}
	written, err := file.Write(data[prefix:])
	return prefix + written, err
}

func (c Client) Run(ctx context.Context, dir string, args ...string) (Result, error) {
	return c.runWithOutput(ctx, dir, nil, "", args...)
}

func (c Client) runWithOutput(ctx context.Context, dir string, output io.Writer, outputPath string, args ...string) (Result, error) {
	binary := c.Binary
	if binary == "" {
		binary = "svn"
	}
	limit := c.MaxOutputBytes
	if limit <= 0 {
		limit = DefaultMaxOutputBytes
	}
	command := Command{Dir: dir, Args: slices.Clone(args)}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	configureSVNProcess(cmd)
	// 固定消息语言，保留终端字符集。
	cmd.Env = append(os.Environ(), "LC_MESSAGES=C")
	stdout := boundedOutput{limit: limit, spill: true}
	stderr := boundedOutput{limit: limit}
	cmd.Stdout = &stdout
	if output != nil {
		cmd.Stdout = output
	}
	cmd.Stderr = &stderr
	started := time.Now()
	runErr := cmd.Run()
	if stdout.file != nil {
		outputPath = stdout.file.Name()
		runErr = errors.Join(runErr, stdout.file.Close())
	}
	result := Result{
		Stdout: stdout.buffer.String(), Stderr: redactURLCredentials(strings.TrimSpace(stderr.buffer.String())),
		Started: started.UTC(), Duration: time.Since(started), OutputPath: outputPath,
		OutputTruncated: stdout.overflow || stderr.overflow,
	}
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		} else {
			result.ExitCode = -1
		}
	}
	// 超限是明确错误，结构化解析器不得解析截断 XML。
	kind := classifyError(ctx, result)
	if result.OutputTruncated {
		detail := fmt.Sprintf("%v（每个输出流上限 %d 字节）", ErrOutputLimit, limit)
		if stdout.file != nil {
			detail += "; 完整标准输出: " + outputPath
		}
		if stderr.overflow {
			detail += "; 标准错误输出已截断"
		}
		result.Stderr = strings.TrimSpace(result.Stderr + "\n" + detail)
		runErr = errors.Join(runErr, fmt.Errorf("%w: %s", ErrOutputLimit, detail))
		if ctx.Err() == nil {
			kind = ErrorOutputLimit
		}
	}
	if runErr != nil {
		commandErr := &CommandError{Kind: kind, Result: result, Err: runErr}
		if c.Observer != nil {
			c.Observer(command, result, commandErr)
		}
		return result, commandErr
	}
	if c.Observer != nil {
		c.Observer(command, result, nil)
	}
	return result, nil
}
