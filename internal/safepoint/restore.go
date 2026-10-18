package safepoint

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"svntui/internal/svn"
	"svntui/internal/wclock"
)

func (m *Manager) Validate(ctx context.Context, id, workingCopy string) (Validation, error) {
	storeLock, err := m.acquireStoreLock("校验")
	if err != nil {
		return Validation{}, err
	}
	defer storeLock.Release()
	m.mu.Lock()
	defer m.mu.Unlock()
	manifest, err := m.inspectUnlocked(id)
	if err != nil {
		return Validation{}, err
	}
	info, err := m.client.Info(ctx, workingCopy)
	if err != nil {
		return Validation{}, fmt.Errorf("读取恢复目标信息: %w", err)
	}
	if info.WCAbsolute != "" {
		info, err = m.client.Info(ctx, info.WCAbsolute)
		if err != nil {
			return Validation{}, fmt.Errorf("读取恢复目标根目录信息: %w", err)
		}
	}
	validation := validateManifest(manifest, info)
	if err := m.validateEntryRevisions(ctx, info.WCAbsolute, manifest, &validation); err != nil {
		return Validation{}, err
	}
	validation.Valid = len(validation.Problems) == 0
	return validation, nil
}

func validateManifest(manifest Manifest, info svn.Info) Validation {
	validation := Validation{
		CurrentUUID:     info.UUID,
		CurrentRootURL:  info.RootURL,
		CurrentURL:      info.URL,
		CurrentRevision: info.Revision,
	}
	if manifest.RepositoryUUID != info.UUID {
		validation.Problems = append(validation.Problems, ProblemRepositoryUUID)
	}
	if normalizeURL(manifest.RootURL) != normalizeURL(info.RootURL) {
		validation.Problems = append(validation.Problems, ProblemRootURL)
	}
	if normalizeURL(manifest.URL) != normalizeURL(info.URL) {
		validation.Problems = append(validation.Problems, ProblemURL)
	}
	if manifest.BaseRevision != info.Revision {
		validation.Problems = append(validation.Problems, ProblemBaseRevision)
	}
	if manifest.Version < manifestVersion {
		validation.Problems = append(validation.Problems, ProblemManifest)
	}
	validation.Valid = len(validation.Problems) == 0
	return validation
}

func (m *Manager) validateEntryRevisions(ctx context.Context, workingCopy string, manifest Manifest, validation *Validation) error {
	if manifest.Version < manifestVersion {
		return nil
	}
	for _, entry := range manifest.Entries {
		newPath := entry.Unversioned || entry.Status == "added"
		if !newPath && entry.BaseRevision <= 0 {
			continue
		}
		path, err := normalizeWorkingCopyPath(workingCopy, filepath.FromSlash(entry.Path))
		if err != nil {
			return fmt.Errorf("校验条目 %q 的 revision: %w", entry.Path, err)
		}
		info, err := m.client.InfoAt(ctx, filepath.Join(workingCopy, path), "BASE")
		mismatch := err != nil || info.Revision != entry.BaseRevision || normalizeURL(info.URL) != normalizeURL(entry.BaseURL) || info.UUID != entry.RepositoryUUID
		if newPath {
			// 新增/未版本化快照不能覆盖后来已提交的同名节点。
			mismatch = err == nil && info.Revision > 0
			for _, parent := range manifest.Entries {
				if parent.Status == "replaced" && pathContains(filepath.FromSlash(parent.Path), path) {
					mismatch = false
					break
				}
			}
		}
		if mismatch {
			if !slices.Contains(validation.Problems, ProblemEntryRevision) {
				validation.Problems = append(validation.Problems, ProblemEntryRevision)
			}
			validation.EntryPaths = append(validation.EntryPaths, entry.Path)
		}
	}
	return nil
}

func normalizeURL(value string) string { return strings.TrimRight(value, "/") }

func (m *Manager) Restore(ctx context.Context, id, workingCopy string, options RestoreOptions) (RestoreResult, error) {
	rootInfo, err := m.client.Info(ctx, workingCopy)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("读取恢复目标信息: %w", err)
	}
	root := rootInfo.WCAbsolute
	if root == "" {
		root = workingCopy
	}
	workingCopyLock, err := wclock.Acquire(m.lockDir, root, "恢复 SafePoint")
	if err != nil {
		return RestoreResult{}, err
	}
	defer workingCopyLock.Release()
	storeLock, err := m.acquireStoreLock("恢复")
	if err != nil {
		return RestoreResult{}, err
	}
	defer storeLock.Release()
	m.mu.Lock()
	defer m.mu.Unlock()

	manifest, err := m.inspectUnlocked(id)
	if err != nil {
		return RestoreResult{}, err
	}
	result := RestoreResult{Manifest: manifest}
	for _, entry := range manifest.Entries {
		if entry.Status == "conflicted" || entry.PropertyState == "conflicted" || entry.Status == "obstructed" || entry.Status == "incomplete" {
			return result, fmt.Errorf("SafePoint 中路径 %q 的 %s/%s 状态无法完整恢复", entry.Path, entry.Status, entry.PropertyState)
		}
	}
	info, statuses, err := m.workingCopyState(ctx, workingCopy)
	if err != nil {
		return result, err
	}
	validation := validateManifest(manifest, info)
	if err := m.validateEntryRevisions(ctx, info.WCAbsolute, manifest, &validation); err != nil {
		return result, err
	}
	validation.Valid = len(validation.Problems) == 0
	if !validation.Valid {
		return result, &ValidationError{Validation: validation}
	}
	workingCopy = info.WCAbsolute
	snapshotDirectory, err := m.snapshotDirectory(id)
	if err != nil {
		return result, err
	}
	if err := verifySnapshotArtifacts(snapshotDirectory, workingCopy, manifest); err != nil {
		return result, err
	}

	dirty := false
	for _, status := range statuses {
		if isDirtyVersioned(status) {
			dirty = true
			break
		}
	}
	collisions, err := unversionedCollisions(workingCopy, statuses, manifest.Entries)
	if err != nil {
		return result, err
	}
	if (dirty || len(collisions) > 0) && !options.CreateBackup {
		return result, ErrDirtyWorkingCopy
	}
	if dirty || len(collisions) > 0 {
		reason := options.BackupReason
		if reason == "" {
			reason = "恢复 SafePoint " + id + " 前自动保护"
		}
		included := append(slices.Clone(options.BackupUnversioned), collisions...)
		backup, err := m.createUnlocked(ctx, workingCopy, CreateOptions{
			Reason:             reason,
			IncludeUnversioned: included,
		}, id)
		if err != nil {
			return result, fmt.Errorf("创建恢复前保护 SafePoint: %w", err)
		}
		result.BackupID = backup.ID
		for _, collision := range collisions {
			if err := os.RemoveAll(filepath.Join(workingCopy, collision)); err != nil {
				return result, fmt.Errorf("清理已备份的未版本化路径 %q: %w", collision, err)
			}
		}
	}
	if dirty {
		if _, err := m.client.Run(ctx, workingCopy, "revert", "--depth", "infinity", "--remove-added", "--", "."); err != nil {
			return result, fmt.Errorf("清理恢复目标: %w", err)
		}
		if err := removeRevertedAddedPaths(workingCopy, statuses); err != nil {
			return result, err
		}
	}

	if err := m.prepareStructuralEntries(ctx, snapshotDirectory, workingCopy, manifest.Entries); err != nil {
		return result, err
	}

	if manifest.Patch != "" {
		patchPath, err := artifactPath(snapshotDirectory, manifest.Patch)
		if err != nil {
			return result, err
		}
		patchInfo, err := os.Stat(patchPath)
		if err != nil {
			return result, fmt.Errorf("读取 SafePoint 补丁: %w", err)
		}
		if patchInfo.Size() > 0 {
			patchResult, err := m.client.Run(ctx, workingCopy, "patch", "--", svn.LiteralTarget(patchPath), svn.LiteralTarget("."))
			if err != nil {
				return result, fmt.Errorf("应用 SafePoint 补丁: %w", err)
			}
			if svn.PatchHasProblems(patchResult.Stdout + "\n" + patchResult.Stderr) {
				return result, errors.New("SafePoint 补丁未能完整应用")
			}
			result.AppliedPatch = true
		}
	}

	if err := m.restoreEntries(ctx, snapshotDirectory, workingCopy, manifest.Entries, &result); err != nil {
		return result, err
	}
	if err := m.verifyRestoredEntries(ctx, snapshotDirectory, workingCopy, manifest.Entries); err != nil {
		return result, err
	}
	return result, nil
}

func verifySnapshotArtifacts(snapshotDirectory, workingCopy string, manifest Manifest) error {
	if manifest.Patch != "" {
		if manifest.Patch != patchFilename {
			return fmt.Errorf("无效的 SafePoint 补丁路径 %q", manifest.Patch)
		}
		path, err := artifactPath(snapshotDirectory, manifest.Patch)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("检查 SafePoint 补丁: %w", err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("SafePoint 补丁不是普通文件")
		}
	}
	for _, entry := range manifest.Entries {
		path, err := normalizeWorkingCopyPath(workingCopy, filepath.FromSlash(entry.Path))
		if err != nil {
			return fmt.Errorf("校验 SafePoint 条目 %q: %w", entry.Path, err)
		}
		if path == "." && (entry.Status != "normal" && entry.Status != "modified" || entry.Payload != "" || entry.Unversioned) {
			return fmt.Errorf("SafePoint 条目不能替换工作副本根目录结构")
		}
		if entry.Payload == "" {
			continue
		}
		cleanPayload := filepath.Clean(filepath.FromSlash(entry.Payload))
		if cleanPayload == payloadDirectory || !strings.HasPrefix(cleanPayload, payloadDirectory+string(filepath.Separator)) {
			return fmt.Errorf("无效的 SafePoint 数据副本路径 %q", entry.Payload)
		}
		payloadPath, err := artifactPath(snapshotDirectory, entry.Payload)
		if err != nil {
			return err
		}
		info, err := os.Lstat(payloadPath)
		if err != nil {
			return fmt.Errorf("检查 %q 的 SafePoint 数据副本: %w", entry.Path, err)
		}
		switch entry.Kind {
		case EntryFile:
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%q 的 SafePoint 数据副本不是普通文件", entry.Path)
			}
		case EntryDir:
			if !info.IsDir() {
				return fmt.Errorf("%q 的 SafePoint 数据副本不是目录", entry.Path)
			}
		case EntrySymlink:
			if info.Mode()&os.ModeSymlink == 0 {
				return fmt.Errorf("%q 的 SafePoint 数据副本不是符号链接", entry.Path)
			}
		default:
			return fmt.Errorf("SafePoint 条目 %q 的类型 %q 无效", entry.Path, entry.Kind)
		}
	}
	return nil
}

func removeRevertedAddedPaths(workingCopy string, statuses []statusEntry) error {
	var added []string
	for _, status := range statuses {
		if status.WC.Item == "added" {
			added = append(added, status.Path)
		}
	}
	slices.SortFunc(added, func(a, b string) int {
		depthA := strings.Count(filepath.ToSlash(a), "/")
		depthB := strings.Count(filepath.ToSlash(b), "/")
		if depthA != depthB {
			return depthB - depthA
		}
		return strings.Compare(b, a)
	})
	for _, path := range added {
		target := filepath.Join(workingCopy, path)
		if err := os.Remove(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
			if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
				continue
			}
			return fmt.Errorf("移除已还原的新增路径 %q: %w", path, err)
		}
	}
	return nil
}

// 同时覆盖补丁、数据副本与全量回滚可能删除的路径。
func unversionedCollisions(workingCopy string, statuses []statusEntry, entries []Entry) ([]string, error) {
	var destructive []string
	for _, entry := range entries {
		if entry.Payload == "" && !entry.Unversioned && entry.Status != "added" && entry.Status != "replaced" && entry.Status != "deleted" && entry.Status != "missing" {
			continue
		}
		path, err := normalizeWorkingCopyPath(workingCopy, filepath.FromSlash(entry.Path))
		if err != nil {
			return nil, fmt.Errorf("校验 SafePoint 条目 %q: %w", entry.Path, err)
		}
		destructive = append(destructive, path)
	}
	for _, status := range statuses {
		switch status.WC.Item {
		case "added", "replaced", "deleted", "missing":
			destructive = append(destructive, status.Path)
		}
	}
	var collisions []string
	for _, status := range statuses {
		if status.WC.Item != "unversioned" && status.WC.Item != "ignored" {
			continue
		}
		for _, path := range destructive {
			if pathContains(path, status.Path) || pathContains(status.Path, path) {
				if !coveredBy(status.Path, collisions) {
					collisions = append(collisions, status.Path)
				}
				break
			}
		}
	}
	slices.Sort(collisions)
	return collisions, nil
}

func (m *Manager) restoreEntries(ctx context.Context, snapshotDirectory, workingCopy string, entries []Entry, result *RestoreResult) error {
	validated := make([]Entry, len(entries))
	copy(validated, entries)
	for index := range validated {
		path, err := normalizeWorkingCopyPath(workingCopy, filepath.FromSlash(validated[index].Path))
		if err != nil {
			return fmt.Errorf("校验 SafePoint 条目 %q: %w", validated[index].Path, err)
		}
		validated[index].Path = path
		if validated[index].Payload != "" {
			if _, err := artifactPath(snapshotDirectory, validated[index].Payload); err != nil {
				return fmt.Errorf("校验 %q 的 SafePoint 数据副本: %w", validated[index].Path, err)
			}
		}
	}

	current, err := m.readStatus(ctx, workingCopy)
	if err != nil {
		return err
	}
	byPath := statusByPath(current)
	for _, entry := range validated {
		target := filepath.Join(workingCopy, entry.Path)
		if _, err := normalizeWorkingCopyPath(workingCopy, entry.Path); err != nil {
			return fmt.Errorf("重新校验恢复目标 %q: %w", entry.Path, err)
		}
		switch entry.Status {
		case "deleted":
			if status, exists := byPath[entry.Path]; !exists || status.WC.Item != "deleted" {
				if _, err := m.client.Run(ctx, workingCopy, "delete", "--force", "--", svn.LiteralTarget(entry.Path)); err != nil {
					return fmt.Errorf("恢复已删除路径 %q: %w", entry.Path, err)
				}
			}
		case "missing":
			if status, exists := byPath[entry.Path]; exists && status.WC.Item == "deleted" {
				if _, err := m.client.Run(ctx, workingCopy, "revert", "--", svn.LiteralTarget(entry.Path)); err != nil {
					return fmt.Errorf("恢复缺失路径 %q 的状态: %w", entry.Path, err)
				}
			}
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("恢复缺失路径 %q: %w", entry.Path, err)
			}
		case "replaced":
			if status, exists := byPath[entry.Path]; !exists || status.WC.Item != "replaced" {
				if _, err := m.client.Run(ctx, workingCopy, "delete", "--force", "--", svn.LiteralTarget(entry.Path)); err != nil {
					return fmt.Errorf("准备被替换路径 %q: %w", entry.Path, err)
				}
			}
		}
	}

	slices.SortFunc(validated, func(a, b Entry) int {
		depthA := strings.Count(filepath.ToSlash(a.Path), "/")
		depthB := strings.Count(filepath.ToSlash(b.Path), "/")
		if depthA != depthB {
			return depthA - depthB
		}
		return strings.Compare(a.Path, b.Path)
	})
	for _, entry := range validated {
		needsEmptyDirectory := entry.Kind == EntryDir && (entry.Status == "added" || entry.Status == "replaced")
		if entry.Status == "deleted" || entry.Status == "missing" || entry.Payload == "" && entry.Kind != EntrySymlink && !needsEmptyDirectory {
			continue
		}
		target := filepath.Join(workingCopy, entry.Path)
		if _, err := normalizeWorkingCopyPath(workingCopy, entry.Path); err != nil {
			return fmt.Errorf("重新校验恢复目标 %q: %w", entry.Path, err)
		}
		var source string
		if entry.Payload != "" {
			var err error
			source, err = artifactPath(snapshotDirectory, entry.Payload)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(source); err != nil {
				return fmt.Errorf("读取 %q 的数据副本: %w", entry.Path, err)
			}
		}
		if needsEmptyDirectory && source == "" {
			if info, err := os.Lstat(target); err == nil && info.IsDir() {
				continue
			}
		}
		if err := replacePath(source, target, entry.Kind, entry.LinkTarget, fs.FileMode(entry.Mode)); err != nil {
			return fmt.Errorf("恢复数据副本 %q: %w", entry.Path, err)
		}
	}
	for _, entry := range validated {
		if entry.Unversioned || entry.Status != "added" && entry.Status != "replaced" {
			continue
		}
		if _, err := os.Lstat(filepath.Join(workingCopy, entry.Path)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		if _, err := m.client.Run(ctx, workingCopy, "add", "--force", "--parents", "--depth", "empty", "--", svn.LiteralTarget(entry.Path)); err != nil {
			return fmt.Errorf("恢复新增路径 %q: %w", entry.Path, err)
		}
	}

	for _, entry := range validated {
		if entry.PropertiesCaptured && !entry.Unversioned && entry.Status != "deleted" && entry.Status != "missing" {
			if err := m.restoreProperties(ctx, workingCopy, entry.Path, propertyStrings(entry.Properties)); err != nil {
				return fmt.Errorf("恢复路径 %q 的完整属性: %w", entry.Path, err)
			}
		}
		if entry.Changelist != "" && !entry.Unversioned {
			if _, err := m.client.Run(ctx, workingCopy, "changelist", "--", entry.Changelist, svn.LiteralTarget(entry.Path)); err != nil {
				return fmt.Errorf("恢复路径 %q 的 changelist: %w", entry.Path, err)
			}
		}
	}

	refreshed, err := m.readStatus(ctx, workingCopy)
	if err != nil {
		return err
	}
	refreshedByPath := statusByPath(refreshed)
	for _, entry := range validated {
		if entry.Copied && !refreshedByPath[entry.Path].WC.Copied && entry.MovedFrom == "" && entry.MovedTo == "" {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s 的复制关系已还原为等价新增内容", filepath.ToSlash(entry.Path)))
		}
		if entry.MovedFrom == "" && entry.MovedTo == "" {
			continue
		}
		status, exists := refreshedByPath[entry.Path]
		if !exists || filepath.ToSlash(status.WC.MovedFrom) != filepath.ToSlash(entry.MovedFrom) || filepath.ToSlash(status.WC.MovedTo) != filepath.ToSlash(entry.MovedTo) {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s 的移动关系已还原为等价路径变更", filepath.ToSlash(entry.Path)))
		}
	}
	return nil
}

func (m *Manager) readStatus(ctx context.Context, workingCopy string) ([]statusEntry, error) {
	command, err := m.client.Run(ctx, workingCopy, "status", "--xml", "--no-ignore", "--", ".")
	if err != nil {
		return nil, fmt.Errorf("刷新工作副本状态: %w", err)
	}
	return parseStatus(workingCopy, []byte(command.Stdout))
}

func artifactPath(snapshotDirectory, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", fmt.Errorf("SafePoint 制品路径不能是绝对路径: %q", relative)
	}
	path := filepath.Join(snapshotDirectory, filepath.FromSlash(relative))
	if err := ensureWithinRoot(snapshotDirectory, path); err != nil {
		return "", err
	}
	return path, nil
}

// 数据写回后再检查内容和状态，二进制差异不依赖文本补丁。
func (m *Manager) verifyRestoredEntries(ctx context.Context, snapshotDirectory, workingCopy string, entries []Entry) error {
	statuses, err := m.readStatus(ctx, workingCopy)
	if err != nil {
		return err
	}
	byPath := statusByPath(statuses)
	for _, entry := range entries {
		path := filepath.FromSlash(entry.Path)
		if entry.Payload != "" {
			payload, err := artifactPath(snapshotDirectory, entry.Payload)
			if err != nil {
				return err
			}
			equal, err := samePathContent(filepath.Join(workingCopy, path), payload)
			if err != nil || !equal {
				return fmt.Errorf("恢复后路径 %q 与 SafePoint 数据副本不一致: %w", entry.Path, errors.Join(err, errors.New("内容校验失败")))
			}
		}
		if entry.Unversioned {
			continue
		}
		if entry.PropertiesCaptured {
			properties, err := m.client.Properties(ctx, workingCopy, path)
			if err != nil || !maps.Equal(properties, propertyStrings(entry.Properties)) {
				return fmt.Errorf("恢复后路径 %q 的属性与 SafePoint 不一致: %w", entry.Path, errors.Join(err, errors.New("属性校验失败")))
			}
		}
		status, exists := byPath[path]
		if !exists || status.WC.Item != entry.Status || entry.PropertyState == "modified" && status.WC.Properties != "modified" || entry.Changelist != "" && status.Changelist != entry.Changelist {
			return fmt.Errorf("恢复后路径 %q 状态不匹配: 期望 %s/%s，实际 %s/%s", entry.Path, entry.Status, entry.PropertyState, status.WC.Item, status.WC.Properties)
		}
	}
	return nil
}
