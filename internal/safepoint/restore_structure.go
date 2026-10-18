package safepoint

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"svntui/internal/svn"
)

// 先建立节点类型和替换状态，避免属性补丁将新增目录创建成文件。
func (m *Manager) prepareStructuralEntries(ctx context.Context, snapshotDirectory, workingCopy string, entries []Entry) error {
	ordered := slices.Clone(entries)
	slices.SortFunc(ordered, func(a, b Entry) int {
		if depth := strings.Count(a.Path, "/") - strings.Count(b.Path, "/"); depth != 0 {
			return depth
		}
		return strings.Compare(a.Path, b.Path)
	})
	for _, entry := range ordered {
		if entry.Unversioned || entry.Status != "replaced" && (entry.Status != "added" || entry.Kind != EntryDir) {
			continue
		}
		path, err := normalizeWorkingCopyPath(workingCopy, filepath.FromSlash(entry.Path))
		if err != nil {
			return err
		}
		if entry.Status == "replaced" && entry.Kind != EntryDir {
			kind, _, _, exists, err := pathMetadata(filepath.Join(workingCopy, path))
			if err != nil {
				return err
			}
			// 同类型文件的完整替换补丁先删除旧内容，必须保留 BASE 供其匹配。
			if exists && kind == entry.Kind {
				continue
			}
		}
		var properties map[string]string
		if entry.Status == "replaced" {
			// 为属性补丁保留旧 BASE 值，同时重建新节点内容。
			properties, err = m.client.Properties(ctx, workingCopy, path)
			if err != nil {
				return fmt.Errorf("读取替换路径 %q 的基础属性: %w", entry.Path, err)
			}
			if _, err := m.client.Run(ctx, workingCopy, "delete", "--force", "--", svn.LiteralTarget(path)); err != nil {
				return fmt.Errorf("准备替换路径 %q: %w", entry.Path, err)
			}
		}
		var source string
		if entry.Payload != "" {
			source, err = artifactPath(snapshotDirectory, entry.Payload)
			if err != nil {
				return err
			}
		}
		if err := replacePath(source, filepath.Join(workingCopy, path), entry.Kind, entry.LinkTarget, fs.FileMode(entry.Mode)); err != nil {
			return fmt.Errorf("准备节点 %q: %w", entry.Path, err)
		}
		if _, err := m.client.Run(ctx, workingCopy, "add", "--force", "--parents", "--depth", "empty", "--", svn.LiteralTarget(path)); err != nil {
			return fmt.Errorf("准备节点 %q 的版本状态: %w", entry.Path, err)
		}
		if entry.Status == "replaced" {
			// 节点类型变化时，旧类型专属属性不能写入新节点。
			for name := range properties {
				if !propertyAppliesToKind(name, entry.Kind) {
					delete(properties, name)
				}
			}
			if err := m.restoreProperties(ctx, workingCopy, path, properties); err != nil {
				return err
			}
		}
	}
	return nil
}

func propertyAppliesToKind(name string, kind EntryKind) bool {
	if kind == EntryDir {
		switch name {
		case "svn:mime-type", "svn:executable", "svn:eol-style", "svn:keywords", "svn:needs-lock", "svn:special":
			return false
		}
	} else {
		switch name {
		case "svn:ignore", "svn:global-ignores", "svn:externals":
			return false
		}
	}
	return true
}

// JSON 中用字节切片保存属性，保留二进制值中的任意字节。
func propertyBytes(properties map[string]string) map[string][]byte {
	values := make(map[string][]byte, len(properties))
	for name, value := range properties {
		values[name] = []byte(value)
	}
	return values
}

func propertyStrings(properties map[string][]byte) map[string]string {
	values := make(map[string]string, len(properties))
	for name, value := range properties {
		values[name] = string(value)
	}
	return values
}

func (m *Manager) restoreProperties(ctx context.Context, workingCopy, path string, properties map[string]string) error {
	current, err := m.client.Properties(ctx, workingCopy, path)
	if err != nil {
		return err
	}
	for name := range current {
		if _, exists := properties[name]; !exists {
			if _, err := m.client.DeleteProperty(ctx, workingCopy, name, []string{path}); err != nil {
				return err
			}
		}
	}
	for name, value := range properties {
		if _, err := m.client.PropSet(ctx, workingCopy, name, value, path); err != nil {
			return fmt.Errorf("还原路径 %q 的属性 %q: %w", path, name, err)
		}
	}
	return nil
}
