// Package safepoint 创建并恢复 Subversion 工作副本的可恢复快照。
package safepoint

import (
	"errors"
	"fmt"
	"time"
)

const manifestVersion = 4

var (
	ErrInvalidID          = errors.New("无效的 SafePoint ID")
	ErrNotFound           = errors.New("找不到 SafePoint")
	ErrRepositoryMismatch = errors.New("工作副本仓库与 SafePoint 不匹配")
	ErrRevisionMismatch   = errors.New("工作副本基础 revision 与 SafePoint 不匹配")
	ErrDirtyWorkingCopy   = errors.New("工作副本包含本地修改")
	ErrCapacity           = errors.New("不删除受保护快照便无法满足 SafePoint 容量限制")
)

type EntryKind string

const (
	EntryFile    EntryKind = "file"
	EntryDir     EntryKind = "directory"
	EntrySymlink EntryKind = "symlink"
)

// Manifest 保存仓库身份、本地变更及快照内的相对制品路径。
type Manifest struct {
	Version        int       `json:"version"`
	ID             string    `json:"id"`
	CreatedAt      time.Time `json:"created_at"`
	Reason         string    `json:"reason,omitempty"`
	WorkingCopy    string    `json:"working_copy"`
	RepositoryUUID string    `json:"repository_uuid"`
	RootURL        string    `json:"root_url"`
	URL            string    `json:"url"`
	BaseRevision   int64     `json:"base_revision"`
	Patch          string    `json:"patch,omitempty"`
	Entries        []Entry   `json:"entries,omitempty"`
	SizeBytes      int64     `json:"size_bytes,omitzero"`
}

// Entry 记录版本化变更或用户明确选择的未版本化路径。
type Entry struct {
	Path               string            `json:"path"`
	Status             string            `json:"status"`
	PropertyState      string            `json:"property_state,omitempty"`
	BaseRevision       int64             `json:"base_revision,omitzero"`
	BaseURL            string            `json:"base_url,omitempty"`
	RepositoryUUID     string            `json:"repository_uuid,omitempty"`
	Kind               EntryKind         `json:"kind"`
	Payload            string            `json:"payload,omitempty"`
	Mode               uint32            `json:"mode,omitzero"`
	LinkTarget         string            `json:"link_target,omitempty"`
	Copied             bool              `json:"copied,omitzero"`
	MovedFrom          string            `json:"moved_from,omitempty"`
	MovedTo            string            `json:"moved_to,omitempty"`
	Changelist         string            `json:"changelist,omitempty"`
	Unversioned        bool              `json:"unversioned,omitzero"`
	Binary             bool              `json:"binary,omitzero"`
	Properties         map[string][]byte `json:"properties,omitempty"`
	PropertiesCaptured bool              `json:"properties_captured,omitzero"`
}

type CreateOptions struct {
	Reason             string
	IncludeUnversioned []string
}

type RestoreOptions struct {
	// CreateBackup 在恢复前保护当前变更；默认拒绝覆盖脏工作副本。
	CreateBackup bool
	BackupReason string
	// BackupUnversioned 额外保护指定未版本化路径；会被覆盖的路径会自动加入。
	BackupUnversioned []string
}

type RestoreResult struct {
	Manifest     Manifest
	BackupID     string
	AppliedPatch bool
	Warnings     []string
}

type ValidationProblem string

const (
	ProblemRepositoryUUID ValidationProblem = "repository_uuid"
	ProblemRootURL        ValidationProblem = "repository_root_url"
	ProblemURL            ValidationProblem = "working_copy_url"
	ProblemBaseRevision   ValidationProblem = "base_revision"
	ProblemEntryRevision  ValidationProblem = "entry_revision"
	ProblemManifest       ValidationProblem = "manifest_version"
)

type Validation struct {
	Valid           bool
	Problems        []ValidationProblem
	CurrentUUID     string
	CurrentRootURL  string
	CurrentURL      string
	CurrentRevision int64
	EntryPaths      []string
}

type RetentionPolicy struct {
	MaxAge   time.Duration
	MaxBytes int64
}

func DefaultRetentionPolicy() RetentionPolicy {
	return RetentionPolicy{MaxAge: 30 * 24 * time.Hour, MaxBytes: 2 << 30}
}

type CleanupResult struct {
	DeletedIDs       []string
	FreedBytes       int64
	RemainingBytes   int64
	CapacityExceeded bool
}

type Option func(*Manager)

func WithRetentionPolicy(policy RetentionPolicy) Option {
	return func(manager *Manager) {
		manager.policy = policy
	}
}

type ValidationError struct {
	Validation Validation
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("SafePoint 校验失败: %v", e.Validation.Problems)
}

func (e *ValidationError) Is(target error) bool {
	if target == ErrRepositoryMismatch {
		for _, problem := range e.Validation.Problems {
			if problem != ProblemBaseRevision && problem != ProblemEntryRevision && problem != ProblemManifest {
				return true
			}
		}
	}
	if target == ErrRevisionMismatch {
		for _, problem := range e.Validation.Problems {
			if problem == ProblemBaseRevision || problem == ProblemEntryRevision || problem == ProblemManifest {
				return true
			}
		}
	}
	return false
}
