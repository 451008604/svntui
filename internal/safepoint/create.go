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

	"svntui/internal/storage"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

func (m *Manager) Create(ctx context.Context, workingCopy string, options CreateOptions) (Manifest, error) {
	info, err := m.client.Info(ctx, workingCopy)
	if err != nil {
		return Manifest{}, fmt.Errorf("读取工作副本信息: %w", err)
	}
	root := info.WCAbsolute
	if root == "" {
		root = workingCopy
	}
	workingCopyLock, err := wclock.Acquire(m.lockDir, root, "创建 SafePoint")
	if err != nil {
		return Manifest{}, err
	}
	defer workingCopyLock.Release()
	return m.CreateLocked(ctx, root, options)
}

// CreateLocked 用于已持有同一工作副本写锁的调用方，避免重复加锁。
func (m *Manager) CreateLocked(ctx context.Context, workingCopy string, options CreateOptions) (Manifest, error) {
	storeLock, err := m.acquireStoreLock("创建")
	if err != nil {
		return Manifest{}, err
	}
	defer storeLock.Release()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createUnlocked(ctx, workingCopy, options)
}

func (m *Manager) createUnlocked(ctx context.Context, workingCopy string, options CreateOptions, protectedIDs ...string) (Manifest, error) {
	info, statuses, err := m.workingCopyState(ctx, workingCopy)
	if err != nil {
		return Manifest{}, err
	}
	workingCopy = info.WCAbsolute
	if err := validateCaptureStatuses(statuses); err != nil {
		return Manifest{}, err
	}
	copiedDirectories, err := copiedDirectoryPaths(workingCopy, statuses)
	if err != nil {
		return Manifest{}, err
	}
	patch, err := m.readDiff(ctx, workingCopy)
	if err != nil {
		return Manifest{}, fmt.Errorf("创建 SafePoint diff: %w", err)
	}

	now := m.now().UTC()
	id, err := newID(now)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		Version:        manifestVersion,
		ID:             id,
		CreatedAt:      now,
		Reason:         options.Reason,
		WorkingCopy:    workingCopy,
		RepositoryUUID: info.UUID,
		RootURL:        info.RootURL,
		URL:            info.URL,
		BaseRevision:   info.Revision,
	}

	if err := os.MkdirAll(m.root, 0o700); err != nil {
		return Manifest{}, fmt.Errorf("创建 SafePoint 根目录: %w", err)
	}
	temporary, err := os.MkdirTemp(m.root, ".pending-")
	if err != nil {
		return Manifest{}, fmt.Errorf("创建临时 SafePoint: %w", err)
	}
	if err := os.Chmod(temporary, 0o700); err != nil {
		_ = os.RemoveAll(temporary)
		return Manifest{}, fmt.Errorf("保护临时 SafePoint: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()

	if patch != "" {
		manifest.Patch = patchFilename
		if err := os.WriteFile(filepath.Join(temporary, patchFilename), []byte(patch), 0o600); err != nil {
			return Manifest{}, fmt.Errorf("写入 SafePoint 补丁: %w", err)
		}
	}
	for _, status := range statuses {
		capturedStatus, capture := statusForCapture(status, copiedDirectories)
		if !capture {
			continue
		}
		entry, err := captureEntry(temporary, workingCopy, capturedStatus, false)
		if err != nil {
			return Manifest{}, err
		}
		if entry.BaseRevision > 0 || entry.Status == "replaced" {
			entryInfo, err := m.client.InfoAt(ctx, filepath.Join(workingCopy, status.Path), "BASE")
			if err != nil {
				return Manifest{}, fmt.Errorf("读取 %q 的基础身份: %w", status.Path, err)
			}
			entry.BaseRevision = entryInfo.Revision
			entry.BaseURL = entryInfo.URL
			entry.RepositoryUUID = entryInfo.UUID
		}
		if entry.Status != "deleted" && entry.Status != "missing" {
			properties, err := m.client.Properties(ctx, workingCopy, status.Path)
			if err != nil {
				return Manifest{}, fmt.Errorf("捕获 %q 的完整属性: %w", status.Path, err)
			}
			entry.Properties = propertyBytes(properties)
			entry.PropertiesCaptured = true
		}
		manifest.Entries = append(manifest.Entries, entry)
	}

	selected, err := selectedUnversioned(workingCopy, statuses, options.IncludeUnversioned)
	if err != nil {
		return Manifest{}, err
	}
	for _, path := range selected {
		status := statusEntry{Path: path}
		status.WC.Item = "unversioned"
		entry, err := captureEntry(temporary, workingCopy, status, true)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Entries = append(manifest.Entries, entry)
	}
	slices.SortFunc(manifest.Entries, func(a, b Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	if err := m.verifyCaptureStable(ctx, temporary, workingCopy, info, statuses, patch, manifest.Entries); err != nil {
		return Manifest{}, err
	}

	if err := storage.SaveJSON(filepath.Join(temporary, manifestFilename), manifest); err != nil {
		return Manifest{}, err
	}
	manifest.SizeBytes, err = directorySize(temporary)
	if err != nil {
		return Manifest{}, fmt.Errorf("计算 SafePoint 大小: %w", err)
	}
	if err := storage.SaveJSON(filepath.Join(temporary, manifestFilename), manifest); err != nil {
		return Manifest{}, err
	}
	measured, err := directorySize(temporary)
	if err != nil {
		return Manifest{}, fmt.Errorf("计算 SafePoint 大小: %w", err)
	}
	if measured != manifest.SizeBytes {
		manifest.SizeBytes = measured
		if err := storage.SaveJSON(filepath.Join(temporary, manifestFilename), manifest); err != nil {
			return Manifest{}, err
		}
	}

	if _, err := m.makeRoomUnlocked(manifest, protectedIDs...); err != nil {
		return Manifest{}, err
	}
	target, err := m.snapshotDirectory(id)
	if err != nil {
		return Manifest{}, err
	}
	if err := os.Rename(temporary, target); err != nil {
		return Manifest{}, fmt.Errorf("提交 SafePoint 文件: %w", err)
	}
	committed = true
	return manifest, nil
}

func (m *Manager) verifyCaptureStable(ctx context.Context, snapshotRoot, workingCopy string, beforeInfo svn.Info, beforeStatus []statusEntry, beforePatch string, entries []Entry) error {
	afterInfo, afterStatus, err := m.workingCopyState(ctx, workingCopy)
	if err != nil {
		return err
	}
	if beforeInfo.UUID != afterInfo.UUID || normalizeURL(beforeInfo.URL) != normalizeURL(afterInfo.URL) || beforeInfo.Revision != afterInfo.Revision || !slices.Equal(beforeStatus, afterStatus) {
		return errors.New("创建 SafePoint 期间工作副本发生了变化")
	}
	diff, err := m.readDiff(ctx, workingCopy)
	if err != nil {
		return fmt.Errorf("校验 SafePoint diff: %w", err)
	}
	if diff != beforePatch {
		return errors.New("创建 SafePoint 期间工作副本 diff 发生了变化")
	}
	for _, entry := range entries {
		if entry.PropertiesCaptured {
			properties, err := m.client.Properties(ctx, workingCopy, filepath.FromSlash(entry.Path))
			if err != nil {
				return err
			}
			if !maps.Equal(properties, propertyStrings(entry.Properties)) {
				return fmt.Errorf("创建 SafePoint 期间工作副本路径 %q 的属性发生了变化", entry.Path)
			}
		}
		if entry.Payload == "" {
			continue
		}
		source := filepath.Join(workingCopy, filepath.FromSlash(entry.Path))
		payload := filepath.Join(snapshotRoot, filepath.FromSlash(entry.Payload))
		equal, err := samePathContent(source, payload)
		if err != nil {
			return fmt.Errorf("校验已捕获路径 %q: %w", entry.Path, err)
		}
		if !equal {
			return fmt.Errorf("创建 SafePoint 期间工作副本路径 %q 发生了变化", entry.Path)
		}
	}
	return nil
}

func captureEntry(snapshotRoot, workingCopy string, status statusEntry, unversioned bool) (Entry, error) {
	source := filepath.Join(workingCopy, status.Path)
	kind, mode, linkTarget, exists, err := pathMetadata(source)
	if err != nil {
		return Entry{}, fmt.Errorf("检查 SafePoint 路径 %q: %w", status.Path, err)
	}
	entry := Entry{
		Path:          filepath.ToSlash(status.Path),
		Status:        status.WC.Item,
		PropertyState: status.WC.Properties,
		BaseRevision:  status.WC.Revision,
		Kind:          kind,
		Mode:          uint32(mode.Perm()),
		LinkTarget:    linkTarget,
		Copied:        status.WC.Copied,
		MovedFrom:     filepath.ToSlash(status.WC.MovedFrom),
		MovedTo:       filepath.ToSlash(status.WC.MovedTo),
		Changelist:    status.Changelist,
		Unversioned:   unversioned,
	}
	if !exists {
		return entry, nil
	}
	entry.Binary = kind == EntryFile && looksBinary(source)
	shouldCapture := unversioned || kind == EntrySymlink
	switch status.WC.Item {
	case "added", "modified", "replaced":
		shouldCapture = kind != EntryDir
	}
	if !shouldCapture {
		return entry, nil
	}
	entry.Payload = filepath.ToSlash(filepath.Join(payloadDirectory, status.Path))
	target := filepath.Join(snapshotRoot, filepath.FromSlash(entry.Payload))
	if err := ensureWithinRoot(snapshotRoot, target); err != nil {
		return Entry{}, err
	}
	if err := copyPath(source, target); err != nil {
		return Entry{}, fmt.Errorf("捕获 SafePoint 路径 %q: %w", status.Path, err)
	}
	return entry, nil
}

func selectedUnversioned(workingCopy string, statuses []statusEntry, requested []string) ([]string, error) {
	var available []string
	for _, status := range statuses {
		if status.WC.Item == "unversioned" || status.WC.Item == "ignored" {
			available = append(available, status.Path)
		}
	}
	selected := make([]string, 0, len(requested))
	for _, requestedPath := range requested {
		path, err := normalizeWorkingCopyPath(workingCopy, requestedPath)
		if err != nil {
			return nil, fmt.Errorf("校验所选未版本化路径 %q: %w", requestedPath, err)
		}
		if path == "." {
			return nil, fmt.Errorf("不能将工作副本根目录选作未版本化路径")
		}
		if !coveredBy(path, available) {
			return nil, fmt.Errorf("所选路径 %q 不是未版本化路径", requestedPath)
		}
		if _, err := os.Lstat(filepath.Join(workingCopy, path)); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("所选未版本化路径 %q 已不存在", requestedPath)
			}
			return nil, err
		}
		if coveredBy(path, selected) {
			continue
		}
		for index := len(selected) - 1; index >= 0; index-- {
			if pathContains(path, selected[index]) {
				selected = slices.Delete(selected, index, index+1)
			}
		}
		selected = append(selected, path)
	}
	slices.Sort(selected)
	return selected, nil
}

func coveredBy(path string, candidates []string) bool {
	for _, candidate := range candidates {
		if pathContains(candidate, path) {
			return true
		}
	}
	return false
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

// 将完整补丁落盘，避免命令输出上限截断快照。
func (m *Manager) readDiff(ctx context.Context, workingCopy string) (string, error) {
	directory, err := os.MkdirTemp("", "svntui-safepoint-diff-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, patchFilename)
	if err := m.client.NativeDiffToFile(ctx, workingCopy, []string{"."}, path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	return string(data), err
}
