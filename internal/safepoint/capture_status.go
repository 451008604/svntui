package safepoint

import (
	"fmt"
	"path/filepath"
)

// 冲突元数据尚不能忠实恢复，必须在创建保护快照前拒绝。
func validateCaptureStatuses(statuses []statusEntry) error {
	for _, status := range statuses {
		if status.WC.TreeConflicted || status.WC.Item == "conflicted" || status.WC.Properties == "conflicted" {
			return fmt.Errorf("路径 %q 包含冲突；SafePoint 尚不能完整保存冲突元数据，请先解决冲突", status.Path)
		}
		switch status.WC.Item {
		case "obstructed", "incomplete":
			return fmt.Errorf("路径 %q 的 %s 状态无法完整保存到 SafePoint", status.Path, status.WC.Item)
		}
	}
	return nil
}

func copiedDirectoryPaths(workingCopy string, statuses []statusEntry) ([]string, error) {
	var paths []string
	for _, status := range statuses {
		if !status.WC.Copied || status.WC.Item != "added" && status.WC.Item != "replaced" {
			continue
		}
		kind, _, _, _, err := pathMetadata(filepath.Join(workingCopy, status.Path))
		if err != nil {
			return nil, err
		}
		if kind == EntryDir {
			paths = append(paths, status.Path)
		}
	}
	return paths, nil
}

// 复制目录内未修改的节点同样属于新增内容，完整捕获后以等价新增恢复。
func statusForCapture(status statusEntry, copiedDirectories []string) (statusEntry, bool) {
	if !coveredBy(status.Path, copiedDirectories) {
		return status, isDirtyVersioned(status)
	}
	switch status.WC.Item {
	case "unversioned", "ignored", "external", "deleted":
		return status, false
	}
	for _, root := range copiedDirectories {
		if status.Path == root {
			return status, true
		}
	}
	status.WC.Item = "added"
	status.WC.Revision = 0
	status.WC.Copied = true
	return status, true
}
