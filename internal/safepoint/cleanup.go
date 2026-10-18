package safepoint

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

type snapshotRecord struct {
	manifest    Manifest
	size        int64
	prospective bool
}

func (m *Manager) Cleanup() (CleanupResult, error) {
	lock, err := m.acquireStoreLock("清理")
	if err != nil {
		return CleanupResult{}, err
	}
	defer lock.Release()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cleanupUnlocked(nil, nil)
}

func (m *Manager) makeRoomUnlocked(prospective Manifest, protectedIDs ...string) (CleanupResult, error) {
	protected := make(map[string]bool, len(protectedIDs))
	for _, id := range protectedIDs {
		protected[id] = true
	}
	return m.cleanupUnlocked(&prospective, protected)
}

func (m *Manager) cleanupUnlocked(prospective *Manifest, additionallyProtected map[string]bool) (CleanupResult, error) {
	manifests, err := m.listUnlocked()
	if err != nil {
		return CleanupResult{}, err
	}
	records := make([]snapshotRecord, 0, len(manifests)+1)
	for _, manifest := range manifests {
		directory, err := m.snapshotDirectory(manifest.ID)
		if err != nil {
			return CleanupResult{}, err
		}
		if err := ensureWithinRoot(m.root, directory); err != nil {
			return CleanupResult{}, err
		}
		size, err := directorySize(directory)
		if err != nil {
			return CleanupResult{}, fmt.Errorf("计算 SafePoint %q 的大小: %w", manifest.ID, err)
		}
		records = append(records, snapshotRecord{manifest: manifest, size: size})
	}
	if prospective != nil {
		records = append(records, snapshotRecord{
			manifest:    *prospective,
			size:        prospective.SizeBytes,
			prospective: true,
		})
	}

	protected := latestPerWorkingCopy(records)
	for id := range additionallyProtected {
		protected[id] = true
	}
	candidates := make([]snapshotRecord, 0, len(records))
	for _, record := range records {
		if record.prospective || protected[record.manifest.ID] {
			continue
		}
		candidates = append(candidates, record)
	}
	slices.SortFunc(candidates, func(a, b snapshotRecord) int {
		if order := a.manifest.CreatedAt.Compare(b.manifest.CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(a.manifest.ID, b.manifest.ID)
	})

	var total int64
	for _, record := range records {
		total += record.size
	}
	now := m.now().UTC()
	deleteSet := make(map[string]snapshotRecord)
	if m.policy.MaxAge > 0 {
		cutoff := now.Add(-m.policy.MaxAge)
		for _, record := range candidates {
			if record.manifest.CreatedAt.Before(cutoff) {
				deleteSet[record.manifest.ID] = record
				total -= record.size
			}
		}
	}
	if m.policy.MaxBytes > 0 && total > m.policy.MaxBytes {
		for _, record := range candidates {
			if total <= m.policy.MaxBytes {
				break
			}
			if _, selected := deleteSet[record.manifest.ID]; selected {
				continue
			}
			deleteSet[record.manifest.ID] = record
			total -= record.size
		}
	}

	result := CleanupResult{RemainingBytes: total}
	if m.policy.MaxBytes > 0 && total > m.policy.MaxBytes {
		result.CapacityExceeded = true
		return result, ErrCapacity
	}
	ordered := make([]snapshotRecord, 0, len(deleteSet))
	for _, record := range candidates {
		if _, selected := deleteSet[record.manifest.ID]; selected {
			ordered = append(ordered, record)
		}
	}
	for _, record := range ordered {
		directory, err := m.snapshotDirectory(record.manifest.ID)
		if err != nil {
			return result, err
		}
		if err := ensureWithinRoot(m.root, directory); err != nil {
			return result, err
		}
		if err := os.RemoveAll(directory); err != nil {
			return result, fmt.Errorf("清理 SafePoint %q: %w", record.manifest.ID, err)
		}
		result.DeletedIDs = append(result.DeletedIDs, record.manifest.ID)
		result.FreedBytes += record.size
	}
	return result, nil
}

func latestPerWorkingCopy(records []snapshotRecord) map[string]bool {
	latest := make(map[string]snapshotRecord)
	for _, record := range records {
		current, exists := latest[record.manifest.WorkingCopy]
		if !exists || record.manifest.CreatedAt.After(current.manifest.CreatedAt) ||
			record.manifest.CreatedAt.Equal(current.manifest.CreatedAt) && record.manifest.ID > current.manifest.ID {
			latest[record.manifest.WorkingCopy] = record
		}
	}
	protected := make(map[string]bool, len(latest))
	for _, record := range latest {
		protected[record.manifest.ID] = true
	}
	return protected
}
