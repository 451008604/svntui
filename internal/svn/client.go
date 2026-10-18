package svn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Observer func(Command, Result, error)

// Client 统一执行和解析系统 svn 命令。
type Client struct {
	Binary   string
	Observer Observer
	// 每个输出流的内存上限；零值使用默认值。
	MaxOutputBytes int64
}

func NewClient() Client { return Client{Binary: "svn"} }

var credentialURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^/@\s]+)@`)

func redactURLCredentials(value string) string {
	return credentialURL.ReplaceAllString(value, `${1}[REDACTED]@`)
}

func classifyError(ctx context.Context, result Result) ErrorKind {
	if ctx.Err() != nil {
		return ErrorCanceled
	}
	message := strings.ToLower(result.Stderr + "\n" + result.Stdout)
	switch {
	case containsAny(message, "authentication failed", "authorization failed", "could not authenticate", "e170001", "e215004"):
		return ErrorAuth
	case containsAny(message, "could not connect", "connection refused", "connection timed out", "name or service not known", "temporary failure", "e000001", "e170013"):
		return ErrorNetwork
	case containsAny(message, "working copy locked", "run 'svn cleanup'", "e155004"):
		return ErrorLocked
	case containsAny(message, "out of date", "e160028", "e155011"):
		return ErrorOutOfDate
	case containsAny(message, "conflict", "e155015"):
		return ErrorConflict
	case containsAny(message, "is not a working copy", "not a working copy", "e155007"):
		return ErrorNotWorkingCopy
	case containsAny(message, "permission denied", "access forbidden", "e175013"):
		return ErrorPermission
	default:
		return ErrorOther
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func (c Client) Version(ctx context.Context) (string, error) {
	result, err := c.Run(ctx, "", "--version", "--quiet")
	return strings.TrimSpace(result.Stdout), err
}

func (c Client) Info(ctx context.Context, path string) (Info, error) {
	directory, target, err := commandTarget(path)
	if err != nil {
		return Info{}, err
	}
	return c.infoTarget(ctx, directory, target, "")
}

func (c Client) infoTarget(ctx context.Context, directory, target, revision string) (Info, error) {
	args := []string{"info", "--xml", "--non-interactive"}
	if revision != "" {
		args = append(args, "-r", revision)
	}
	result, err := c.Run(ctx, directory, append(args, "--", target)...)
	if err != nil {
		return Info{}, err
	}
	return parseInfo([]byte(result.Stdout))
}

// 获取指定修订身份；替换节点的 BASE 来自原工作修订。
func (c Client) InfoAt(ctx context.Context, path, revision string) (Info, error) {
	directory, target, err := commandTarget(path)
	if err != nil {
		return Info{}, err
	}
	if !strings.EqualFold(revision, "BASE") || isURL(path) {
		return c.infoTarget(ctx, directory, target, revision)
	}
	current, err := c.infoTarget(ctx, directory, target, "")
	if err != nil {
		return Info{}, err
	}
	// 替换节点没有当前 revision，但 status 保留了被替换节点的 BASE。
	result, err := c.Run(ctx, directory, "status", "--xml", "--depth", "empty", "--", target)
	if err != nil {
		return Info{}, err
	}
	entries, err := ParseStatus([]byte(result.Stdout))
	if err != nil {
		return Info{}, err
	}
	for _, entry := range entries {
		if entry.Item == "replaced" && entry.Revision > 0 {
			rev := strconv.FormatInt(entry.Revision, 10)
			base, err := c.infoTarget(ctx, "", current.URL+"@"+rev, rev)
			if err != nil {
				return Info{}, err
			}
			base.Path, base.WCAbsolute, base.Depth = current.Path, current.WCAbsolute, current.Depth
			return base, nil
		}
		if entry.Item == "added" {
			return Info{}, fmt.Errorf("目标 %s 尚无 BASE 修订", path)
		}
	}
	return current, nil
}

func (c Client) Status(ctx context.Context, path string, remote, includeIgnored bool) ([]StatusEntry, error) {
	args := []string{"status", "--xml"}
	if remote {
		args = append(args, "--show-updates", "--non-interactive")
	}
	if includeIgnored {
		args = append(args, "--no-ignore")
	}
	args = append(args, "--", ".")
	result, err := c.Run(ctx, path, args...)
	if err != nil {
		return nil, err
	}
	entries, err := ParseStatus([]byte(result.Stdout))
	if err != nil {
		return nil, err
	}
	for index := range entries {
		if !entries[index].IsConflict() || entries[index].ConflictType != "" && entries[index].ConflictOld != "" && entries[index].ConflictNew != "" && entries[index].ConflictWorking != "" {
			continue
		}
		target := entries[index].Path
		if !filepath.IsAbs(target) {
			target = filepath.Join(path, target)
		}
		info, infoErr := c.Info(ctx, target)
		if infoErr != nil {
			continue
		}
		entries[index].ConflictOld = info.ConflictOld
		entries[index].ConflictNew = info.ConflictNew
		entries[index].ConflictWorking = info.ConflictWorking
		entries[index].ConflictType = info.ConflictType
		entries[index].ConflictOperation = info.ConflictOperation
		entries[index].ConflictLeft = info.ConflictLeft
		entries[index].ConflictRight = info.ConflictRight
	}
	return entries, nil
}

func (c Client) Log(ctx context.Context, path, revisionRange string, limit int) ([]LogEntry, error) {
	args := []string{"log", "--xml", "--verbose", "--non-interactive"}
	if limit > 0 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	if revisionRange == "" {
		revisionRange = "HEAD:0"
	}
	args = append(args, "-r", revisionRange)
	directory, target, err := commandTarget(path)
	if err != nil {
		return nil, err
	}
	args = append(args, "--", target)
	result, err := c.Run(ctx, directory, args...)
	if err != nil {
		return nil, err
	}
	return parseLog([]byte(result.Stdout))
}

func (c Client) Diff(ctx context.Context, path string, targets []string) (string, error) {
	args := []string{"diff", "--git", "--"}
	// 工作副本 diff 不解析 peg，路径保持原样。
	args = append(args, targets...)
	if len(targets) == 0 {
		args = append(args, ".")
	}
	result, err := c.Run(ctx, path, args...)
	return result.Stdout, err
}

// RevisionDiff 返回指定修订相对上一修订引入的内容变化。
func (c Client) RevisionDiff(ctx context.Context, path, target string, revision int64) (string, error) {
	if target == "" {
		target = "."
	}
	result, err := c.Run(ctx, path, "diff", "--git", "--change", strconv.FormatInt(revision, 10), "--non-interactive", "--", LiteralTarget(target))
	return result.Stdout, err
}

func (c Client) Update(ctx context.Context, path string, ignoreExternals bool) (Result, error) {
	return c.UpdateWithOptions(ctx, path, UpdateOptions{IgnoreExternals: ignoreExternals})
}

func (c Client) Commit(ctx context.Context, path string, targets []string, message string) (Result, error) {
	args := []string{"commit", "--non-interactive", "--depth", "empty", "-m", message, "--"}
	args = appendLiteralTargets(args, targets)
	return c.Run(ctx, path, args...)
}

func (c Client) Add(ctx context.Context, path string, targets []string) (Result, error) {
	args := []string{"add", "--parents", "--"}
	args = appendLiteralTargets(args, targets)
	return c.Run(ctx, path, args...)
}

func (c Client) Delete(ctx context.Context, path string, targets []string) (Result, error) {
	args := []string{"delete", "--"}
	args = appendLiteralTargets(args, targets)
	return c.Run(ctx, path, args...)
}

func (c Client) Move(ctx context.Context, path, source, target string) (Result, error) {
	return c.Run(ctx, path, "move", "--", LiteralTarget(source), target)
}

func (c Client) Revert(ctx context.Context, path string, targets []string) (Result, error) {
	args := []string{"revert", "--depth", "infinity", "--"}
	args = appendLiteralTargets(args, targets)
	return c.Run(ctx, path, args...)
}

func (c Client) Cleanup(ctx context.Context, path string) (Result, error) {
	return c.Run(ctx, path, "cleanup", "--", ".")
}

func (c Client) Lock(ctx context.Context, path string, targets []string, message string) (Result, error) {
	args := []string{"lock", "--non-interactive", "-m", message, "--"}
	args = appendLiteralTargets(args, targets)
	return c.Run(ctx, path, args...)
}

func (c Client) Unlock(ctx context.Context, path string, targets []string) (Result, error) {
	args := []string{"unlock", "--non-interactive", "--"}
	args = appendLiteralTargets(args, targets)
	return c.Run(ctx, path, args...)
}

func (c Client) Resolve(ctx context.Context, path, accept string, targets []string) (Result, error) {
	args := []string{"resolve", "--accept", accept, "--"}
	args = appendLiteralTargets(args, targets)
	return c.Run(ctx, path, args...)
}

func (c Client) Copy(ctx context.Context, source, target, revision, message string) (Result, error) {
	args := []string{"copy", "--non-interactive"}
	if revision != "" {
		args = append(args, "-r", revision)
	}
	if isURL(target) {
		args = append(args, "-m", message)
	}
	args = append(args, "--", LiteralTarget(source), LiteralTarget(target))
	return c.Run(ctx, "", args...)
}

func (c Client) Switch(ctx context.Context, workingCopy, targetURL string, ignoreExternals bool) (Result, error) {
	args := []string{"switch", "--non-interactive", "--accept", "postpone"}
	if ignoreExternals {
		args = append(args, "--ignore-externals")
	}
	args = append(args, "--", targetURL, ".")
	return c.Run(ctx, workingCopy, args...)
}

func (c Client) Merge(ctx context.Context, plan MergePlan) (Result, error) {
	args := []string{"merge", "--non-interactive", "--accept", "postpone"}
	if plan.DryRun {
		args = append(args, "--dry-run")
	}
	if plan.IgnoreAncestry {
		args = append(args, "--ignore-ancestry")
	}
	if plan.RevisionSpec != "" {
		if plan.Reverse {
			var err error
			plan.RevisionSpec, err = reverseRevisionSpec(plan.RevisionSpec)
			if err != nil {
				return Result{}, err
			}
		}
		args = append(args, "-c", plan.RevisionSpec)
	}
	args = append(args, "--", LiteralTarget(plan.Source), ".")
	return c.Run(ctx, plan.WorkingCopy, args...)
}

func reverseRevisionSpec(spec string) (string, error) {
	var revisions []string
	for token := range strings.SplitSeq(spec, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			return "", errors.New("反向合并 revision 列表包含空项")
		}
		startText, endText, isRange := strings.Cut(token, "-")
		start, err := strconv.ParseInt(startText, 10, 64)
		if err != nil || start <= 0 {
			return "", fmt.Errorf("无效 revision %q", token)
		}
		if !isRange {
			revisions = append(revisions, "-"+strconv.FormatInt(start, 10))
			continue
		}
		end, err := strconv.ParseInt(endText, 10, 64)
		if err != nil || end <= 0 {
			return "", fmt.Errorf("无效 revision range %q", token)
		}
		high, low := max(start, end), min(start, end)
		if high-low > 10_000 {
			return "", fmt.Errorf("反向合并范围过大: %s", token)
		}
		for revision := high; revision >= low; revision-- {
			revisions = append(revisions, "-"+strconv.FormatInt(revision, 10))
		}
	}
	return strings.Join(revisions, ","), nil
}

func (c Client) MergeInfo(ctx context.Context, workingCopy, source, kind string) ([]int64, error) {
	if kind != "eligible" && kind != "merged" {
		return nil, fmt.Errorf("不支持的 mergeinfo 类型: %s", kind)
	}
	result, err := c.Run(ctx, workingCopy, "mergeinfo", "--show-revs", kind, "--non-interactive", "--", LiteralTarget(source), ".")
	if err != nil {
		return nil, err
	}
	var revisions []int64
	for line := range strings.SplitSeq(result.Stdout, "\n") {
		line = strings.TrimSpace(line)
		line, ok := strings.CutPrefix(line, "r")
		if !ok {
			continue
		}
		revision, parseErr := strconv.ParseInt(line, 10, 64)
		if parseErr == nil {
			revisions = append(revisions, revision)
		}
	}
	return revisions, nil
}

func (c Client) PropGet(ctx context.Context, path, name, target string) (string, error) {
	// XML 固定使用 UTF-8，并以 base64 保留二进制属性。
	result, err := c.Run(ctx, path, "propget", "--xml", "--non-interactive", "--", name, LiteralTarget(target))
	if err != nil {
		return "", err
	}
	properties, err := parseProperties([]byte(result.Stdout))
	if err != nil {
		return "", err
	}
	value, ok := properties[name]
	if !ok {
		return "", fmt.Errorf("目标 %s 不存在属性 %s", target, name)
	}
	return value, nil
}

func (c Client) PropSet(ctx context.Context, path, name, value, target string) (Result, error) {
	return c.PropSetTargets(ctx, path, name, value, []string{target})
}

// 在一次 SVN 命令中为所有明确目标写入同一属性值。
func (c Client) PropSetTargets(ctx context.Context, path, name, value string, targets []string) (Result, error) {
	// 通过文件传递原始字节，避开 Windows 命令行编码及 NUL 限制。
	file, err := os.CreateTemp("", "svntui-property-*")
	if err != nil {
		return Result{}, fmt.Errorf("创建属性值文件: %w", err)
	}
	defer os.Remove(file.Name())
	_, writeErr := file.WriteString(value)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return Result{}, fmt.Errorf("写入属性值文件: %w", err)
	}
	args := []string{"propset", "--file", file.Name()}
	// SVN 内建属性需要显式字符集；自定义属性必须保持原始字节。
	if strings.HasPrefix(name, "svn:") {
		args = append(args, "--encoding", "UTF-8")
	}
	args = append(args, "--", name)
	return c.Run(ctx, path, appendLiteralTargets(args, targets)...)
}

var revisionLine = regexp.MustCompile(`(?i)(?:updated to|at) revision (\d+)`)

// ParseUpdateOutput 提取 update 的文件变化和最终修订号。
func ParseUpdateOutput(output string) ([]ChangedPath, int64) {
	var changed []ChangedPath
	var revision int64
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if action, path, ok := parseUpdateNotification(line); ok {
			changed = append(changed, ChangedPath{Action: action, Path: path})
		}
		if match := revisionLine.FindStringSubmatch(line); len(match) == 2 {
			revision, _ = strconv.ParseInt(match[1], 10, 64)
		}
	}
	return changed, revision
}

func parseUpdateNotification(line string) (string, string, bool) {
	if len(line) < 6 || line[4] != ' ' {
		return "", "", false
	}
	codes := line[:4]
	for index, code := range []byte(codes) {
		allowed := " ADUGCREB"
		if index == 3 {
			allowed = " C"
		}
		if !strings.ContainsRune(allowed, rune(code)) {
			return "", "", false
		}
	}
	if strings.TrimSpace(codes) == "" {
		return "", "", false
	}
	path := strings.TrimSpace(line[5:])
	if path == "" {
		return "", "", false
	}
	action := strings.ReplaceAll(codes, " ", "")
	return action, path, true
}
