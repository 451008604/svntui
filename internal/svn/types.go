package svn

import "time"

// Info 描述工作副本与仓库的绑定关系。
type Info struct {
	Path              string
	Kind              string
	Revision          int64
	URL               string
	RootURL           string
	UUID              string
	WCAbsolute        string
	WCRelative        string
	Depth             string
	ConflictOld       string
	ConflictNew       string
	ConflictWorking   string
	ConflictType      string
	ConflictOperation string
	ConflictLeft      ConflictVersion
	ConflictRight     ConflictVersion
}

type ConflictVersion struct {
	Side       string
	Kind       string
	Path       string
	Revision   int64
	Repository string
}

// StatusEntry 是 svn status 的结构化结果。
type StatusEntry struct {
	Path              string
	Changelist        string
	MovedFrom         string
	MovedTo           string
	Item              string
	Properties        string
	Revision          int64
	Copied            bool
	Switched          bool
	TreeConflicted    bool
	RepositoryItem    string
	RepositoryProps   string
	LockOwner         string
	ConflictOld       string
	ConflictNew       string
	ConflictWorking   string
	ConflictType      string
	ConflictOperation string
	ConflictLeft      ConflictVersion
	ConflictRight     ConflictVersion
}

func (s StatusEntry) IsConflict() bool {
	return s.Item == "conflicted" || s.Properties == "conflicted" || s.TreeConflicted
}

func (s StatusEntry) IsVersionedDirty() bool {
	switch s.Item {
	case "normal", "none", "unversioned", "ignored", "external":
		return s.Properties == "modified" || s.Properties == "conflicted"
	default:
		return true
	}
}

// LogEntry 描述一次仓库修订及其变更路径。
type LogEntry struct {
	Revision int64
	Author   string
	Date     time.Time
	Message  string
	Paths    []ChangedPath
}

type ChangedPath struct {
	Action       string
	Path         string
	CopyFromPath string
	CopyFromRev  int64
}

// ListEntry 描述仓库目录中的一个直接或递归成员。
type ListEntry struct {
	Name     string
	Kind     string
	Size     int64
	Revision int64
	Author   string
	Date     time.Time
}

// BlameLine 描述文件中一行内容的最后修改来源。
type BlameLine struct {
	Line           int64
	Content        string
	Revision       int64
	Author         string
	Date           time.Time
	MergedPath     string
	MergedRevision int64
}

type Command struct {
	Dir  string
	Args []string
}

type Result struct {
	Stdout          string
	OutputPath      string
	OutputTruncated bool
	Stderr          string
	ExitCode        int
	Started         time.Time
	Duration        time.Duration
}

type ErrorKind string

const (
	ErrorAuth           ErrorKind = "authentication"
	ErrorNetwork        ErrorKind = "network"
	ErrorLocked         ErrorKind = "working_copy_locked"
	ErrorOutOfDate      ErrorKind = "out_of_date"
	ErrorConflict       ErrorKind = "conflict"
	ErrorNotWorkingCopy ErrorKind = "not_working_copy"
	ErrorPermission     ErrorKind = "permission"
	ErrorCanceled       ErrorKind = "canceled"
	ErrorOther          ErrorKind = "other"
	ErrorOutputLimit    ErrorKind = "output_limit"
)

type CommandError struct {
	Kind   ErrorKind
	Result Result
	Err    error
}

func (e *CommandError) Error() string {
	if e.Result.Stderr != "" {
		return e.Result.Stderr
	}
	return e.Err.Error()
}

func (e *CommandError) Unwrap() error { return e.Err }

type MergePlan struct {
	WorkingCopy    string
	Source         string
	RevisionSpec   string
	Reverse        bool
	DryRun         bool
	IgnoreAncestry bool
}

// 控制更新目标、修订和工作副本持久深度。
type UpdateOptions struct {
	Targets         []string
	Revision        string
	Depth           string
	IgnoreExternals bool
}

// 按原生修订页读取历史，再应用显示筛选。
type LogOptions struct {
	RevisionRange string
	StartRevision int64
	Limit         int
	Author        string
	Search        string
	Since         time.Time
	Until         time.Time
}

type LogPage struct {
	Entries      []LogEntry
	NextRevision int64
	HasMore      bool
}
