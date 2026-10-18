package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"svntui/internal/domain"
	"svntui/internal/safepoint"
	"svntui/internal/svn"
)

// 先确定真正执行的目标，再交给界面确认；此方法不读取网络或修改文件。
func (a Actions) NormalizeAction(r domain.ActionRequest) (domain.ActionRequest, error) {
	r.Targets = slices.Clone(r.Targets)
	r.Arguments = slices.Clone(r.Arguments)
	if r.WorkingCopy == "" {
		return r, errors.New("未指定工作副本")
	}
	switch r.Action {
	case domain.ActionMove:
		if len(r.Arguments) == 1 && len(r.Targets) == 1 {
			r.Arguments = []string{r.Targets[0], r.Arguments[0]}
		}
		if len(r.Arguments) != 2 {
			return r, errors.New("移动需要一个选中源路径及目标，或明确指定源和目标")
		}
		r.Targets = slices.Clone(r.Arguments)
	case domain.ActionProperty:
		if len(r.Arguments) < 2 || len(r.Arguments) > 3 {
			return r, errors.New("用法: property <名称> <值> [目标]")
		}
		if len(r.Arguments) == 3 {
			r.Targets = []string{r.Arguments[2]}
			r.Arguments = r.Arguments[:2]
		}
		if len(r.Targets) == 0 {
			return r, errors.New("设置属性必须明确选择目标；根目录使用 .")
		}
	case domain.ActionPropDelete:
		if len(r.Arguments) != 1 || len(r.Targets) == 0 {
			return r, errors.New("删除属性需要名称和明确目标")
		}
	case domain.ActionChangelist:
		if len(r.Arguments) != 1 || strings.TrimSpace(r.Arguments[0]) == "" || len(r.Targets) == 0 {
			return r, errors.New("用法: changelist <分组名|--remove>，请先选择文件")
		}
	case domain.ActionIgnore:
		if len(r.Arguments) < 1 || len(r.Arguments) > 2 {
			return r, errors.New("用法: ignore <模式> [目录]")
		}
		target := "."
		if len(r.Arguments) == 2 {
			target = r.Arguments[1]
		}
		r.Targets = []string{target}
		r.Arguments = []string{r.Arguments[0], target}
	case domain.ActionCopy:
		if len(r.Arguments) < 4 {
			return r, errors.New("用法: copy <源URL> <目标URL> <revision|HEAD> <说明>")
		}
		r.Targets = nil
	case domain.ActionSwitch:
		if len(r.Arguments) != 1 {
			return r, errors.New("用法: switch <目标URL>")
		}
		r.Targets = nil
	case domain.ActionMerge:
		if len(r.Arguments) == 0 {
			return r, errors.New("用法: merge <源URL> [revision] [--reverse] [--apply]")
		}
		r.Targets = nil
	case domain.ActionPatch:
		if len(r.Arguments) < 1 || len(r.Arguments) > 2 || len(r.Arguments) == 2 && r.Arguments[1] != "--apply" {
			return r, errors.New("用法: patch <补丁文件> [--apply]")
		}
		r.Targets = []string{"."}
	case domain.ActionUpdate:
		apply := false
		for _, arg := range r.Arguments {
			if arg != "--apply" || apply {
				return r, errors.New("用法: update [--apply]")
			}
			apply = true
		}
		if len(r.Targets) == 0 {
			r.Targets = []string{"."}
		}
		if r.Depth != "" && !slices.Contains([]string{"empty", "files", "immediates", "infinity", "exclude"}, r.Depth) {
			return r, errors.New("无效的检出深度")
		}
	case domain.ActionProperties, domain.ActionExternals:
		if len(r.Targets) == 0 {
			r.Targets = []string{"."}
		}
	case domain.ActionCleanup:
		r.Targets = []string{"."}
	case domain.ActionAdd, domain.ActionDelete, domain.ActionCommit, domain.ActionRevert, domain.ActionResolve, domain.ActionLock, domain.ActionUnlock:
		if err := requireTargets(r.Targets); err != nil {
			return r, err
		}
	default:
		return r, fmt.Errorf("不支持的操作: %s", r.Action)
	}
	// 规范化绝对路径，避免校验与状态查找使用不同形式。
	for i, target := range r.Targets {
		if (r.Action == domain.ActionProperties || r.Action == domain.ActionExternals) && strings.Contains(target, "://") {
			continue
		}
		if err := validateTargets(r.WorkingCopy, []string{target}); err != nil {
			return r, err
		}
		relative, _, err := resolveWorkspaceTarget(r.WorkingCopy, target)
		if err != nil {
			return r, err
		}
		r.Targets[i] = relative
	}
	r.Targets = slices.Compact(r.Targets)
	if r.Action == domain.ActionMove {
		r.Arguments = slices.Clone(r.Targets)
		if len(r.Arguments) != 2 {
			return r, errors.New("移动源和目标不能相同")
		}
	}
	return r, nil
}

func (a Actions) showProperties(ctx context.Context, r domain.ActionRequest) (svn.Result, string, bool, error) {
	var text strings.Builder
	for _, target := range r.Targets {
		props, err := a.Client.Properties(ctx, r.WorkingCopy, target)
		if err != nil {
			return svn.Result{}, "", false, err
		}
		fmt.Fprintf(&text, "路径: %s\n", target)
		if r.Action == domain.ActionExternals {
			value := props["svn:externals"]
			if value == "" {
				text.WriteString("未定义外部依赖。\n")
			} else {
				text.WriteString("定义（保留原始 URL、-r 和 @peg 固定版本）:\n" + value + "\n")
			}
		} else {
			names := make([]string, 0, len(props))
			for name := range props {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				fmt.Fprintf(&text, "%s = %s\n", name, props[name])
			}
			if len(names) == 0 {
				text.WriteString("没有显式属性。\n")
			}
		}
	}
	return svn.Result{Stdout: text.String()}, "属性读取完成", false, nil
}

func (a Actions) patch(ctx context.Context, r domain.ActionRequest) (svn.Result, string, bool, error) {
	path, err := filepath.Abs(r.Arguments[0])
	if err != nil {
		return svn.Result{}, "", false, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return svn.Result{}, "", false, err
	}
	if !info.Mode().IsRegular() {
		return svn.Result{}, "", false, errors.New("补丁必须是普通文件")
	}
	if r.IsReadOnly() {
		result, err := a.Client.Patch(ctx, r.WorkingCopy, path, true)
		return result, "补丁预演完成，追加 --apply 执行", false, err
	}
	// 先预演并保护当前修改，失败时保留完整输出供复查。
	preview, err := a.Client.Patch(ctx, r.WorkingCopy, path, true)
	if err != nil {
		return preview, "补丁预演失败", false, err
	}
	if patchIncomplete(preview.Stdout + preview.Stderr) {
		return preview, "补丁存在无法应用的内容", false, errors.New("补丁预演存在跳过、拒绝或冲突；请先处理")
	}
	if a.SafePoints == nil {
		return preview, "", false, errors.New("SafePoint 服务不可用，已阻止补丁写入")
	}
	entries, err := a.Client.Status(ctx, r.WorkingCopy, false, true)
	if err != nil {
		return preview, "", false, err
	}
	var included []string
	for _, entry := range entries {
		if entry.Item == "unversioned" || entry.Item == "ignored" {
			included = append(included, entry.Path)
		}
	}
	if _, err := a.SafePoints.CreateLocked(ctx, r.WorkingCopy, safepoint.CreateOptions{Reason: "应用补丁前自动保护", IncludeUnversioned: included}); err != nil {
		return preview, "", false, err
	}
	result, err := a.Client.Patch(ctx, r.WorkingCopy, path, false)
	if err == nil && patchIncomplete(result.Stdout+result.Stderr) {
		err = errors.New("补丁未完整应用，请复查冲突和拒绝项")
	}
	return result, "补丁已应用，请检查差异后手动提交", true, err
}

func patchIncomplete(output string) bool {
	return svn.PatchHasProblems(output)
}
