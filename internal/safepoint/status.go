package safepoint

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"svntui/internal/svn"
)

type statusEntry struct {
	Path       string
	Changelist string
	WC         struct {
		Item           string
		Properties     string
		Revision       int64
		Copied         bool
		TreeConflicted bool
		MovedFrom      string
		MovedTo        string
	}
}

func parseStatus(root string, data []byte) ([]statusEntry, error) {
	parsed, err := svn.ParseStatus(data)
	if err != nil {
		return nil, fmt.Errorf("为 SafePoint 解析 svn status: %w", err)
	}
	entries := make([]statusEntry, 0, len(parsed))
	for _, raw := range parsed {
		path, err := normalizeWorkingCopyPath(root, raw.Path)
		if err != nil {
			return nil, fmt.Errorf("校验状态路径 %q: %w", raw.Path, err)
		}
		entry := statusEntry{Path: path, Changelist: raw.Changelist}
		entry.WC.Item = raw.Item
		entry.WC.Properties = raw.Properties
		entry.WC.Revision = raw.Revision
		entry.WC.Copied = raw.Copied
		entry.WC.TreeConflicted = raw.TreeConflicted
		if raw.MovedFrom != "" {
			entry.WC.MovedFrom, err = normalizeWorkingCopyPath(root, raw.MovedFrom)
			if err != nil {
				return nil, fmt.Errorf("校验移动源路径: %w", err)
			}
		}
		if raw.MovedTo != "" {
			entry.WC.MovedTo, err = normalizeWorkingCopyPath(root, raw.MovedTo)
			if err != nil {
				return nil, fmt.Errorf("校验移动目标路径: %w", err)
			}
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b statusEntry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, nil
}

func normalizeWorkingCopyPath(root, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("路径不能为空")
	}
	absolute := path
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, path)
	}
	absolute, err := filepath.Abs(absolute)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("路径 %q 位于工作副本外", path)
	}
	if relative == "." || relative == "" {
		return ".", nil
	}
	if err := rejectSymlinkParents(root, relative, false); err != nil {
		return "", err
	}
	for part := range strings.SplitSeq(filepath.ToSlash(relative), "/") {
		if part == ".svn" {
			return "", fmt.Errorf("路径 %q 进入了 Subversion 元数据目录", path)
		}
	}
	return filepath.Clean(relative), nil
}

func rejectSymlinkParents(root, relative string, includeFinal bool) error {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("解析工作副本根目录: %w", err)
	}
	parts := strings.Split(filepath.Clean(relative), string(filepath.Separator))
	limit := len(parts) - 1
	if includeFinal {
		limit = len(parts)
	}
	current := resolvedRoot
	for _, part := range parts[:max(limit, 0)] {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("路径在 %q 处穿过符号链接", current)
		}
	}
	return nil
}

func isDirtyVersioned(entry statusEntry) bool {
	if entry.WC.TreeConflicted || entry.WC.Properties == "modified" || entry.WC.Properties == "conflicted" {
		return true
	}
	switch entry.WC.Item {
	case "", "normal", "none", "unversioned", "ignored", "external":
		return false
	default:
		return true
	}
}

func statusByPath(entries []statusEntry) map[string]statusEntry {
	result := make(map[string]statusEntry, len(entries))
	for _, entry := range entries {
		result[entry.Path] = entry
	}
	return result
}
