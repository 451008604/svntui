package safepoint

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"svntui/internal/appdirs"
	"svntui/internal/storage"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

const (
	manifestFilename = "manifest.json"
	patchFilename    = "changes.patch"
	payloadDirectory = "payload"
)

type Manager struct {
	root         string
	lockDir      string
	storeLockDir string
	client       svn.Client
	policy       RetentionPolicy
	now          func() time.Time
	mu           sync.Mutex
}

func New(dirs appdirs.Dirs, client svn.Client, options ...Option) *Manager {
	manager := &Manager{
		root:         filepath.Join(dirs.State, "safepoints"),
		lockDir:      filepath.Join(dirs.State, "working-copy-locks"),
		storeLockDir: filepath.Join(dirs.State, "safepoint-locks"),
		client:       client,
		policy:       DefaultRetentionPolicy(),
		now:          time.Now,
	}
	for _, option := range options {
		option(manager)
	}
	return manager
}

func (m *Manager) Root() string { return m.root }

func (m *Manager) List() ([]Manifest, error) {
	lock, err := m.acquireStoreLock("列出")
	if err != nil {
		return nil, err
	}
	defer lock.Release()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listUnlocked()
}

func (m *Manager) listUnlocked() ([]Manifest, error) {
	directories, err := os.ReadDir(m.root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("列出 SafePoint: %w", err)
	}

	manifests := make([]Manifest, 0, len(directories))
	for _, directory := range directories {
		if !directory.IsDir() || strings.HasPrefix(directory.Name(), ".") {
			continue
		}
		if err := validateID(directory.Name()); err != nil {
			continue
		}
		manifest, err := m.inspectUnlocked(directory.Name())
		if err != nil {
			return nil, err
		}
		manifests = append(manifests, manifest)
	}
	slices.SortFunc(manifests, func(a, b Manifest) int {
		if order := b.CreatedAt.Compare(a.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(b.ID, a.ID)
	})
	return manifests, nil
}

func (m *Manager) Inspect(id string) (Manifest, error) {
	lock, err := m.acquireStoreLock("查看")
	if err != nil {
		return Manifest{}, err
	}
	defer lock.Release()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inspectUnlocked(id)
}

func (m *Manager) inspectUnlocked(id string) (Manifest, error) {
	directory, err := m.snapshotDirectory(id)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := storage.LoadJSON(filepath.Join(directory, manifestFilename), &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.ID == "" {
		return Manifest{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if manifest.ID != id || manifest.Version < 1 || manifest.Version > manifestVersion {
		return Manifest{}, fmt.Errorf("无效的 SafePoint 清单 %q", id)
	}
	return manifest, nil
}

func (m *Manager) Delete(id string) error {
	lock, err := m.acquireStoreLock("删除")
	if err != nil {
		return err
	}
	defer lock.Release()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleteUnlocked(id)
}

func (m *Manager) deleteUnlocked(id string) error {
	directory, err := m.snapshotDirectory(id)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(directory); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return fmt.Errorf("检查 SafePoint %q: %w", id, err)
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("删除 SafePoint %q: %w", id, err)
	}
	return nil
}

func (m *Manager) snapshotDirectory(id string) (string, error) {
	if err := validateID(id); err != nil {
		return "", err
	}
	directory := filepath.Join(m.root, id)
	if err := ensureWithinRoot(m.root, directory); err != nil {
		return "", err
	}
	if info, err := os.Lstat(directory); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("SafePoint 目录 %q 是符号链接", id)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	return directory, nil
}

func validateID(id string) error {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	for _, character := range id {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return fmt.Errorf("%w: %q", ErrInvalidID, id)
		}
	}
	return nil
}

func ensureWithinRoot(root, target string) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("解析 SafePoint 根目录: %w", err)
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return fmt.Errorf("解析 SafePoint 目标路径: %w", err)
	}
	relative, err := filepath.Rel(absoluteRoot, absoluteTarget)
	if err != nil {
		return fmt.Errorf("比较 SafePoint 目标路径: %w", err)
	}
	if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("SafePoint 目标路径 %q 并未严格位于 %q 内", absoluteTarget, absoluteRoot)
	}
	if err := rejectSymlinkParents(absoluteRoot, relative, false); err != nil {
		return fmt.Errorf("SafePoint 路径穿过符号链接: %w", err)
	}
	return nil
}

func (m *Manager) acquireStoreLock(operation string) (*wclock.Lock, error) {
	return wclock.Acquire(m.storeLockDir, m.root, "SafePoint "+operation)
}

func newID(now time.Time) (string, error) {
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("生成 SafePoint ID: %w", err)
	}
	return now.UTC().Format("20060102t150405-000000000z") + "-" + hex.EncodeToString(random[:]), nil
}

func (m *Manager) workingCopyState(ctx context.Context, path string) (svn.Info, []statusEntry, error) {
	info, err := m.client.Info(ctx, path)
	if err != nil {
		return svn.Info{}, nil, fmt.Errorf("读取工作副本信息: %w", err)
	}
	root := info.WCAbsolute
	if root == "" {
		root, err = filepath.Abs(path)
		if err != nil {
			return svn.Info{}, nil, fmt.Errorf("解析工作副本路径: %w", err)
		}
	}
	info, err = m.client.Info(ctx, root)
	if err != nil {
		return svn.Info{}, nil, fmt.Errorf("读取工作副本根目录信息: %w", err)
	}
	result, err := m.client.Run(ctx, root, "status", "--xml", "--no-ignore", "--", ".")
	if err != nil {
		return svn.Info{}, nil, fmt.Errorf("读取工作副本状态: %w", err)
	}
	entries, err := parseStatus(root, []byte(result.Stdout))
	if err != nil {
		return svn.Info{}, nil, err
	}
	// 仅展开复制子树，避免普通未修改节点改变快照语义与输出规模。
	copiedDirectories, err := copiedDirectoryPaths(root, entries)
	if err != nil {
		return svn.Info{}, nil, err
	}
	if len(copiedDirectories) > 0 {
		args := []string{"status", "--xml", "--verbose", "--no-ignore", "--"}
		for _, path := range copiedDirectories {
			args = append(args, svn.LiteralTarget(path))
		}
		details, err := m.client.Run(ctx, root, args...)
		if err != nil {
			return svn.Info{}, nil, fmt.Errorf("读取复制子树状态: %w", err)
		}
		expanded, err := parseStatus(root, []byte(details.Stdout))
		if err != nil {
			return svn.Info{}, nil, err
		}
		byPath := statusByPath(entries)
		for _, entry := range expanded {
			byPath[entry.Path] = entry
		}
		entries = slices.Collect(maps.Values(byPath))
		slices.SortFunc(entries, func(a, b statusEntry) int { return strings.Compare(a.Path, b.Path) })
	}
	info.WCAbsolute = root
	return info, entries, nil
}
