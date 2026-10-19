package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"svntui/internal/domain"
	"svntui/internal/safepoint"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

// Actions 把 TUI 的结构化请求映射为经过校验的 SVN 操作。
type Actions struct {
	Client     svn.Client
	SafePoints *safepoint.Manager
	LockDir    string
	ExpertMode bool
}

func (a Actions) WorkspaceStatus(ctx context.Context, workingCopy string) ([]svn.StatusEntry, error) {
	return a.Client.Status(ctx, workingCopy, false, true)
}

func (a Actions) WorkspaceDiff(ctx context.Context, workingCopy string, targets []string) (string, error) {
	if err := validateTargets(workingCopy, targets); err != nil {
		return "", err
	}
	if len(targets) == 0 {
		return a.Client.Diff(ctx, workingCopy, nil)
	}
	entries, err := a.Client.Status(ctx, workingCopy, false, true)
	if err != nil {
		return "", err
	}
	statusByPath := make(map[string]svn.StatusEntry, len(entries))
	for _, entry := range entries {
		statusByPath[filepath.Clean(entry.Path)] = entry
	}

	versioned := make([]string, 0, len(targets))
	sections := make([]string, 0, len(targets)+1)
	for _, target := range targets {
		relative, absolute, err := resolveWorkspaceTarget(workingCopy, target)
		if err != nil {
			return "", err
		}
		entry, found := statusByPath[relative]
		if !found {
			versioned = append(versioned, target)
			continue
		}
		switch entry.Item {
		case "unversioned", "ignored":
			preview, err := previewUnversionedFile(relative, absolute, entry.Item)
			if err != nil {
				return "", err
			}
			sections = append(sections, preview)
		case "external":
			sections = append(sections, fmt.Sprintf("路径：%s\n这是独立的 external 工作副本，请进入该目录单独查看差异。", relative))
		default:
			versioned = append(versioned, target)
		}
	}
	if len(versioned) > 0 {
		diff, err := a.Client.Diff(ctx, workingCopy, versioned)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(diff) != "" {
			sections = append([]string{strings.TrimRight(diff, "\n")}, sections...)
		}
	}
	return strings.Join(sections, "\n\n"), nil
}

const unversionedPreviewLimit = 512 * 1024

func resolveWorkspaceTarget(workingCopy, target string) (relative, absolute string, err error) {
	root, err := filepath.Abs(workingCopy)
	if err != nil {
		return "", "", fmt.Errorf("解析工作副本路径: %w", err)
	}
	absolute = target
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, absolute)
	}
	absolute, err = filepath.Abs(absolute)
	if err != nil {
		return "", "", fmt.Errorf("解析目标路径: %w", err)
	}
	relative, err = filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("目标路径超出工作副本: %s", target)
	}
	return filepath.Clean(relative), absolute, nil
}

func previewUnversionedFile(relative, absolute, item string) (string, error) {
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("读取未版本化路径 %q: %w", relative, err)
	}
	header := fmt.Sprintf("Index: %s\n状态：%s（尚无版本库基线）", filepath.ToSlash(relative), item)
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return header + "\n这是符号链接，为避免读取工作副本之外的内容，不展开预览。", nil
	case info.IsDir():
		return header + "\n这是目录；请进入目录选择具体文件查看内容。", nil
	case !info.Mode().IsRegular():
		return header + "\n这不是普通文件，无法显示文本内容。", nil
	case info.Size() > unversionedPreviewLimit:
		return fmt.Sprintf("%s\n文件大小为 %d 字节，超过 512 KiB 预览上限。", header, info.Size()), nil
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", fmt.Errorf("打开未版本化文件 %q: %w", relative, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, unversionedPreviewLimit+1))
	if err != nil {
		return "", fmt.Errorf("读取未版本化文件 %q: %w", relative, err)
	}
	if len(data) > unversionedPreviewLimit {
		return header + "\n文件读取期间超过 512 KiB 预览上限。", nil
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return fmt.Sprintf("%s\n二进制文件，大小 %d 字节；不显示文本内容。", header, len(data)), nil
	}
	return header + "\n" + formatNewFilePatch(data), nil
}

func formatNewFilePatch(data []byte) string {
	if len(data) == 0 {
		return "新增空文件，无文本内容。"
	}
	content := strings.ReplaceAll(string(data), "\r\n", "\n")
	hasFinalNewline := strings.HasSuffix(content, "\n")
	content = strings.TrimSuffix(content, "\n")
	lines := strings.Split(content, "\n")
	var patch strings.Builder
	fmt.Fprintf(&patch, "--- /dev/null\n+++ 工作副本\n@@ -0,0 +1,%d @@\n", len(lines))
	for _, line := range lines {
		patch.WriteByte('+')
		patch.WriteString(line)
		patch.WriteByte('\n')
	}
	if !hasFinalNewline {
		patch.WriteString("\\ No newline at end of file\n")
	}
	return strings.TrimRight(patch.String(), "\n")
}

func (a Actions) Execute(ctx context.Context, request domain.ActionRequest) (domain.ActionResult, error) {
	return a.ExecuteWithPrecondition(ctx, request, nil)
}

// 在共享写锁内检查前置条件，避免确认与实际写入之间发生其他操作。
func (a Actions) ExecuteWithPrecondition(ctx context.Context, request domain.ActionRequest, check func() error) (domain.ActionResult, error) {
	// 确认与执行共用规范化目标，执行前再次校验。
	request, err := a.NormalizeAction(request)
	if err != nil {
		return domain.ActionResult{}, err
	}
	if !request.IsReadOnly() {
		// 子目录启动与根目录启动共享同一写锁，命令仍在原目录执行。
		info, err := a.Client.Info(ctx, request.WorkingCopy)
		if err != nil {
			return domain.ActionResult{}, err
		}
		lockRoot := info.WCAbsolute
		if lockRoot == "" {
			lockRoot = request.WorkingCopy
		}
		lock, err := wclock.Acquire(a.LockDir, lockRoot, string(request.Action))
		if err != nil {
			return domain.ActionResult{}, err
		}
		defer lock.Release()
	}
	if check != nil {
		if err := check(); err != nil {
			return domain.ActionResult{}, err
		}
	}
	result, summary, refresh, err := a.executeLocked(ctx, request)
	output := strings.TrimSpace(result.Stdout)
	if result.Stderr != "" {
		output = strings.TrimSpace(output + "\n" + result.Stderr)
	}
	if err != nil && !request.IsReadOnly() {
		refresh = true
		summary = "操作未完整完成，请检查刷新后的状态"
	}
	return domain.ActionResult{Summary: summary, Output: output, RefreshWorkspace: refresh}, err
}

func (a Actions) executeLocked(ctx context.Context, request domain.ActionRequest) (svn.Result, string, bool, error) {
	switch request.Action {
	case domain.ActionUpdate:
		return a.update(ctx, request)
	case domain.ActionChangelist:
		remove := request.Arguments[0] == "--remove"
		result, err := a.Client.Changelist(ctx, request.WorkingCopy, request.Arguments[0], request.Targets, remove)
		return result, "变更分组已更新", true, err
	case domain.ActionProperties, domain.ActionExternals:
		return a.showProperties(ctx, request)
	case domain.ActionPropDelete:
		result, err := a.Client.DeleteProperty(ctx, request.WorkingCopy, request.Arguments[0], request.Targets)
		return result, "属性已删除", true, err
	case domain.ActionPatch:
		return a.patch(ctx, request)
	case domain.ActionAdd:
		if err := requireTargets(request.Targets); err != nil {
			return svn.Result{}, "", false, err
		}
		result, err := a.Client.Add(ctx, request.WorkingCopy, request.Targets)
		return result, "已加入版本控制", true, err
	case domain.ActionDelete:
		if err := requireTargets(request.Targets); err != nil {
			return svn.Result{}, "", false, err
		}
		result, err := a.Client.Delete(ctx, request.WorkingCopy, request.Targets)
		return result, "已标记删除", true, err
	case domain.ActionMove:
		if len(request.Arguments) != 2 {
			return svn.Result{}, "", false, errors.New("用法: move <源路径> <目标路径>")
		}
		if err := validateTargets(request.WorkingCopy, request.Arguments); err != nil {
			return svn.Result{}, "", false, err
		}
		result, err := a.Client.Move(ctx, request.WorkingCopy, request.Arguments[0], request.Arguments[1])
		return result, "已移动路径", true, err
	case domain.ActionCommit:
		if err := requireTargets(request.Targets); err != nil {
			return svn.Result{}, "", false, errors.New("提交前请用 Space 选择文件")
		}
		message := strings.TrimSpace(strings.Join(request.Arguments, " "))
		if message == "" {
			return svn.Result{}, "", false, errors.New("用法: commit <提交说明>")
		}
		if err := a.revalidateCommit(ctx, request.WorkingCopy, request.Targets); err != nil {
			return svn.Result{}, "", false, err
		}
		result, err := a.Client.Commit(ctx, request.WorkingCopy, request.Targets, message)
		return result, "选中内容已提交", true, err
	case domain.ActionRevert:
		if err := requireTargets(request.Targets); err != nil {
			return svn.Result{}, "", false, err
		}
		if _, err := a.createSafePoint(ctx, request.WorkingCopy, "revert 前自动保护"); err != nil {
			return svn.Result{}, "", false, err
		}
		result, err := a.Client.Revert(ctx, request.WorkingCopy, request.Targets)
		return result, "已还原选中路径；操作前 SafePoint 已保留", true, err
	case domain.ActionCleanup:
		result, err := a.Client.Cleanup(ctx, request.WorkingCopy)
		return result, "工作副本清理完成", true, err
	case domain.ActionResolve:
		if err := requireTargets(request.Targets); err != nil {
			return svn.Result{}, "", false, err
		}
		accept := "working"
		if len(request.Arguments) > 0 {
			accept = request.Arguments[0]
		}
		if !slices.Contains([]string{"working", "mine-full", "theirs-full", "base"}, accept) {
			return svn.Result{}, "", false, errors.New("resolve 仅支持 working、mine-full、theirs-full 或 base")
		}
		if accept == "working" {
			entries, err := a.Client.Status(ctx, request.WorkingCopy, false, true)
			if err != nil {
				return svn.Result{}, "", false, err
			}
			for _, target := range request.Targets {
				for _, entry := range entries {
					// 树/属性冲突依赖 SVN 的节点状态，只有文本冲突才检查内容标记。
					if filepath.Clean(entry.Path) != filepath.Clean(target) || entry.Item != "conflicted" {
						continue
					}
					path := filepath.Join(request.WorkingCopy, target)
					info, err := os.Lstat(path)
					if err != nil {
						if errors.Is(err, os.ErrNotExist) {
							continue
						}
						return svn.Result{}, "", false, err
					}
					if !info.Mode().IsRegular() {
						continue
					}
					marked, err := containsConflictMarkers(path)
					if err != nil {
						return svn.Result{}, "", false, err
					}
					if marked {
						return svn.Result{}, "", false, fmt.Errorf("%s 仍包含冲突标记，已阻止标记 resolved", target)
					}
				}
			}
		}
		result, err := a.Client.Resolve(ctx, request.WorkingCopy, accept, request.Targets)
		if err == nil {
			entries, statusErr := a.Client.Status(ctx, request.WorkingCopy, false, false)
			if statusErr != nil {
				return result, "", true, fmt.Errorf("重新检查冲突状态: %w", statusErr)
			}
			for _, entry := range entries {
				if slices.Contains(request.Targets, filepath.Clean(entry.Path)) && entry.IsConflict() {
					return result, "", true, fmt.Errorf("%s 仍处于冲突状态", entry.Path)
				}
			}
		}
		return result, "冲突已按所选版本标记解决", true, err
	case domain.ActionLock:
		if err := requireTargets(request.Targets); err != nil {
			return svn.Result{}, "", false, err
		}
		result, err := a.Client.Lock(ctx, request.WorkingCopy, request.Targets, strings.Join(request.Arguments, " "))
		return result, "已锁定选中路径", true, err
	case domain.ActionUnlock:
		if err := requireTargets(request.Targets); err != nil {
			return svn.Result{}, "", false, err
		}
		result, err := a.Client.Unlock(ctx, request.WorkingCopy, request.Targets)
		return result, "已解锁选中路径", true, err
	case domain.ActionIgnore:
		return a.ignore(ctx, request)
	case domain.ActionProperty:
		return a.property(ctx, request)
	case domain.ActionCopy:
		return a.copy(ctx, request)
	case domain.ActionSwitch:
		return a.switchWorkingCopy(ctx, request)
	case domain.ActionMerge:
		return a.merge(ctx, request)
	default:
		return svn.Result{}, "", false, fmt.Errorf("不支持的操作: %s", request.Action)
	}
}

func (a Actions) update(ctx context.Context, request domain.ActionRequest) (svn.Result, string, bool, error) {
	if !request.IsReadOnly() {
		result, err := a.Client.UpdateWithOptions(ctx, request.WorkingCopy, svn.UpdateOptions{Targets: request.Targets, Revision: request.Revision, Depth: request.Depth, IgnoreExternals: true})
		return result, "工作副本已更新", true, err
	}
	entries, err := a.Client.Status(ctx, request.WorkingCopy, true, false)
	if err != nil {
		return svn.Result{}, "", false, err
	}
	var preview strings.Builder
	fmt.Fprintf(&preview, "目标: %s\n", strings.Join(request.Targets, ", "))
	changed := 0
	for _, entry := range entries {
		if !pathInTargets(entry.Path, request.Targets) {
			continue
		}
		content, props := remoteChanged(entry.RepositoryItem), remoteChanged(entry.RepositoryProps)
		if !content && !props {
			continue
		}
		changed++
		fmt.Fprintf(&preview, "%s 远端内容=%s 属性=%s", entry.Path, entry.RepositoryItem, entry.RepositoryProps)
		if entry.IsVersionedDirty() || entry.IsConflict() {
			preview.WriteString(" [本地也有修改，可能冲突]")
		}
		preview.WriteByte('\n')
	}
	if changed == 0 {
		preview.WriteString("未发现远端 HEAD 的内容或属性变化。\n")
	}
	if request.Revision != "" {
		fmt.Fprintf(&preview, "请求更新到 %s；以上是相对 HEAD 的状态查询，不代表目标修订的完整差异。\n", request.Revision)
	}
	if request.Depth != "" {
		fmt.Fprintf(&preview, "将调整检出深度为 %s；缩减深度可能移除本地已版本化内容。\n", request.Depth)
	}
	preview.WriteString("确认目标后追加 --apply 执行；更新结果仍需检查。")
	return svn.Result{Stdout: preview.String()}, "远端更新预览完成", false, nil
}

func remoteChanged(value string) bool { return value != "" && value != "none" && value != "normal" }

func pathInTargets(path string, targets []string) bool {
	path = filepath.Clean(path)
	for _, target := range targets {
		target = filepath.Clean(target)
		if target == "." || path == target || strings.HasPrefix(path, target+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func containsConflictMarkers(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("读取冲突文件 %q: %w", path, err)
	}
	defer file.Close()
	markers := [][]byte{[]byte("<<<<<<<"), []byte("======="), []byte(">>>>>>>")}
	found := make([]bool, len(markers))
	buffer := make([]byte, 64*1024)
	tail := make([]byte, 0, 8)
	for {
		count, readErr := file.Read(buffer)
		window := append(append([]byte(nil), tail...), buffer[:count]...)
		for index, marker := range markers {
			found[index] = found[index] || bytes.Contains(window, marker)
		}
		if found[0] && found[1] && found[2] {
			return true, nil
		}
		keep := min(len(window), 8)
		tail = append(tail[:0], window[len(window)-keep:]...)
		if errors.Is(readErr, io.EOF) {
			return false, nil
		}
		if readErr != nil {
			return false, fmt.Errorf("读取冲突文件 %q: %w", path, readErr)
		}
	}
}

func (a Actions) ignore(ctx context.Context, request domain.ActionRequest) (svn.Result, string, bool, error) {
	if len(request.Arguments) == 0 {
		return svn.Result{}, "", false, errors.New("用法: ignore <模式> [目录]")
	}
	target := "."
	if len(request.Arguments) > 1 {
		target = request.Arguments[1]
	}
	if err := validateTargets(request.WorkingCopy, []string{target}); err != nil {
		return svn.Result{}, "", false, err
	}
	current, _ := a.Client.PropGet(ctx, request.WorkingCopy, "svn:ignore", target)
	patterns := strings.Split(strings.TrimSpace(current), "\n")
	if !slices.Contains(patterns, request.Arguments[0]) {
		patterns = append(patterns, request.Arguments[0])
	}
	patterns = slices.DeleteFunc(patterns, func(value string) bool { return strings.TrimSpace(value) == "" })
	result, err := a.Client.PropSet(ctx, request.WorkingCopy, "svn:ignore", strings.Join(patterns, "\n")+"\n", target)
	return result, "忽略规则已更新", true, err
}

func (a Actions) property(ctx context.Context, request domain.ActionRequest) (svn.Result, string, bool, error) {
	result, err := a.Client.PropSetTargets(ctx, request.WorkingCopy, request.Arguments[0], request.Arguments[1], request.Targets)
	return result, "所选路径属性已更新", true, err
}

func (a Actions) copy(ctx context.Context, request domain.ActionRequest) (svn.Result, string, bool, error) {
	if len(request.Arguments) < 4 {
		return svn.Result{}, "", false, errors.New("用法: copy <源URL> <目标URL> <revision|HEAD> <日志>")
	}
	result, err := a.Client.Copy(ctx, request.Arguments[0], request.Arguments[1], request.Arguments[2], strings.Join(request.Arguments[3:], " "))
	return result, "远端路径复制完成", false, err
}

func (a Actions) switchWorkingCopy(ctx context.Context, request domain.ActionRequest) (svn.Result, string, bool, error) {
	if len(request.Arguments) != 1 {
		return svn.Result{}, "", false, errors.New("用法: switch <目标URL>")
	}
	dirty, unversioned, err := a.branchState(ctx, request.WorkingCopy)
	if err != nil {
		return svn.Result{}, "", false, err
	}
	if unversioned {
		return svn.Result{}, "", false, errors.New("工作副本包含未版本化路径；请先 add、ignore 或移走后再 switch")
	}
	if dirty && !a.ExpertMode {
		return svn.Result{}, "", false, errors.New("工作副本有本地修改；普通模式禁止 switch")
	}
	if dirty {
		if _, err := a.createSafePoint(ctx, request.WorkingCopy, "强制 switch 前自动保护"); err != nil {
			return svn.Result{}, "", false, err
		}
	}
	result, err := a.Client.Switch(ctx, request.WorkingCopy, request.Arguments[0], true)
	return result, "工作副本已切换", true, err
}

func (a Actions) merge(ctx context.Context, request domain.ActionRequest) (svn.Result, string, bool, error) {
	if len(request.Arguments) == 0 {
		return svn.Result{}, "", false, errors.New("用法: merge <源URL> [revision] [--reverse] [--apply]")
	}
	plan := svn.MergePlan{WorkingCopy: request.WorkingCopy, Source: request.Arguments[0], DryRun: true}
	for _, argument := range request.Arguments[1:] {
		switch argument {
		case "--reverse":
			plan.Reverse = true
		case "--apply":
			plan.DryRun = false
		default:
			if plan.RevisionSpec != "" {
				return svn.Result{}, "", false, errors.New("merge 只能指定一个 revision 或 revision 列表")
			}
			plan.RevisionSpec = argument
		}
	}
	if plan.Reverse && plan.RevisionSpec == "" {
		return svn.Result{}, "", false, errors.New("反向合并必须指定 revision")
	}
	dirty, unversioned, err := a.branchState(ctx, request.WorkingCopy)
	if err != nil {
		return svn.Result{}, "", false, err
	}
	if unversioned {
		return svn.Result{}, "", false, errors.New("工作副本包含未版本化路径；请先 add、ignore 或移走后再 merge")
	}
	if dirty && !a.ExpertMode {
		return svn.Result{}, "", false, errors.New("工作副本有本地修改；普通模式禁止 merge")
	}
	if dirty && !plan.DryRun {
		if _, err := a.createSafePoint(ctx, request.WorkingCopy, "强制 merge 前自动保护"); err != nil {
			return svn.Result{}, "", false, err
		}
	}
	mergeInfo := a.mergeInfo(ctx, request.WorkingCopy, plan.Source)
	result, err := a.Client.Merge(ctx, plan)
	result.Stdout = mergeInfo + result.Stdout
	if plan.DryRun {
		return result, "合并预演完成；确认后追加 --apply 执行", false, err
	}
	return result, "合并已应用到本地，请检查差异和冲突后手动提交", true, err
}

func (a Actions) mergeInfo(ctx context.Context, workingCopy, source string) string {
	eligible, eligibleErr := a.Client.MergeInfo(ctx, workingCopy, source, "eligible")
	merged, mergedErr := a.Client.MergeInfo(ctx, workingCopy, source, "merged")
	if eligibleErr != nil || mergedErr != nil {
		return "mergeinfo: 无法可靠判断已合并/可合并 revision，请人工核对。\n\n"
	}
	return fmt.Sprintf("mergeinfo eligible: %v\nmergeinfo merged: %v\n\n", eligible, merged)
}

func (a Actions) revalidateCommit(ctx context.Context, workingCopy string, targets []string) error {
	entries, err := a.Client.Status(ctx, workingCopy, false, true)
	if err != nil {
		return err
	}
	statusByPath := make(map[string]svn.StatusEntry, len(entries))
	for _, entry := range entries {
		statusByPath[filepath.Clean(entry.Path)] = entry
	}
	for _, target := range targets {
		entry, ok := statusByPath[filepath.Clean(target)]
		if !ok {
			return fmt.Errorf("提交前状态已变化: %s", target)
		}
		if entry.IsConflict() || entry.Item == "missing" || entry.Item == "ignored" || entry.Item == "unversioned" {
			return fmt.Errorf("%s 当前状态 %s 不可提交", target, entry.Item)
		}
	}
	return nil
}

func (a Actions) branchState(ctx context.Context, workingCopy string) (dirty, unversioned bool, err error) {
	entries, err := a.Client.Status(ctx, workingCopy, false, false)
	if err != nil {
		return false, false, err
	}
	for _, entry := range entries {
		if entry.Item == "unversioned" {
			unversioned = true
		}
		if entry.IsVersionedDirty() || entry.IsConflict() {
			dirty = true
		}
	}
	return dirty, unversioned, nil
}

func (a Actions) createSafePoint(ctx context.Context, workingCopy, reason string) (safepoint.Manifest, error) {
	if a.SafePoints == nil {
		return safepoint.Manifest{}, errors.New("SafePoint 服务不可用，已阻止危险操作")
	}
	manifest, err := a.SafePoints.CreateLocked(ctx, workingCopy, safepoint.CreateOptions{Reason: reason})
	if err != nil {
		return safepoint.Manifest{}, fmt.Errorf("创建 SafePoint: %w", err)
	}
	return manifest, nil
}

func requireTargets(targets []string) error {
	if len(targets) == 0 {
		return errors.New("请先选择至少一个路径")
	}
	return nil
}

func validateTargets(workingCopy string, targets []string) error {
	root, err := filepath.Abs(workingCopy)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if strings.TrimSpace(target) == "" {
			return errors.New("目标路径不能为空")
		}
		candidate := target
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, candidate)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("目标路径超出工作副本: %s", target)
		}
	}
	return nil
}
