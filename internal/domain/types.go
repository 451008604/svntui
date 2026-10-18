// 提供界面与命令行共享的操作和查询契约。
package domain

import (
	"context"
	"errors"

	"svntui/internal/svn"
)

// Module 标识应用的一级页面。
type Module uint8

const (
	ModuleWorkspace Module = iota
	ModuleRepository
	ModuleHistory
	ModuleBranches
	ModuleConflicts
	ModuleAutomations
	ModuleSafePoints
	ModuleOperations
	ModuleProjects
)

// Modules 固定一级页面的显示顺序和数字快捷键。
var Modules = [...]Module{
	ModuleWorkspace,
	ModuleRepository,
	ModuleHistory,
	ModuleBranches,
	ModuleConflicts,
	ModuleAutomations,
	ModuleSafePoints,
	ModuleOperations,
	ModuleProjects,
}

func (m Module) String() string {
	switch m {
	case ModuleWorkspace:
		return "Workspace"
	case ModuleRepository:
		return "Repository"
	case ModuleHistory:
		return "History"
	case ModuleBranches:
		return "Branches"
	case ModuleConflicts:
		return "Conflicts"
	case ModuleAutomations:
		return "Automations"
	case ModuleSafePoints:
		return "SafePoints"
	case ModuleProjects:
		return "Projects"
	case ModuleOperations:
		return "Operations"
	default:
		return "Unknown"
	}
}

// DisplayName 返回面向用户的中文模块名称。
func (m Module) DisplayName() string {
	switch m {
	case ModuleWorkspace:
		return "工作区"
	case ModuleRepository:
		return "版本库"
	case ModuleHistory:
		return "历史"
	case ModuleBranches:
		return "分支"
	case ModuleConflicts:
		return "冲突"
	case ModuleAutomations:
		return "自动任务"
	case ModuleSafePoints:
		return "安全快照"
	case ModuleProjects:
		return "项目"
	case ModuleOperations:
		return "操作记录"
	default:
		return "未知模块"
	}
}

// Action 标识命令面板请求的操作，最终安全校验由 Actions 负责。
type Action string

const (
	ActionUpdate     Action = "update"
	ActionAdd        Action = "add"
	ActionDelete     Action = "delete"
	ActionMove       Action = "move"
	ActionCommit     Action = "commit"
	ActionRevert     Action = "revert"
	ActionCleanup    Action = "cleanup"
	ActionResolve    Action = "resolve"
	ActionLock       Action = "lock"
	ActionUnlock     Action = "unlock"
	ActionIgnore     Action = "ignore"
	ActionProperty   Action = "property"
	ActionCopy       Action = "copy"
	ActionSwitch     Action = "switch"
	ActionMerge      Action = "merge"
	ActionChangelist Action = "changelist"
	ActionProperties Action = "properties"
	ActionPropDelete Action = "propdelete"
	ActionExternals  Action = "externals"
	ActionPatch      Action = "patch"
)

// DisplayName 返回面向用户的中文操作名称。
func (a Action) DisplayName() string {
	switch a {
	case ActionUpdate:
		return "更新"
	case ActionAdd:
		return "添加"
	case ActionDelete:
		return "删除"
	case ActionMove:
		return "移动"
	case ActionCommit:
		return "提交"
	case ActionRevert:
		return "还原"
	case ActionCleanup:
		return "清理"
	case ActionResolve:
		return "解决冲突"
	case ActionLock:
		return "加锁"
	case ActionUnlock:
		return "解锁"
	case ActionIgnore:
		return "忽略"
	case ActionProperty:
		return "编辑属性"
	case ActionCopy:
		return "复制分支或标签"
	case ActionSwitch:
		return "切换分支"
	case ActionMerge:
		return "合并"
	case ActionChangelist:
		return "变更分组"
	case ActionProperties:
		return "查看属性"
	case ActionPropDelete:
		return "删除属性"
	case ActionExternals:
		return "外部依赖"
	case ActionPatch:
		return "应用补丁"
	default:
		return "未知操作"
	}
}

// ActionRequest 是界面生成的结构化写请求，Arguments 是业务参数而非 shell 命令行。
type ActionRequest struct {
	Action      Action
	Module      Module
	WorkingCopy string
	Targets     []string
	Arguments   []string
	Revision    string
	Depth       string
}

// ActionResult 是 Actions 服务返回给用户的操作结果。
type ActionResult struct {
	Summary          string `json:"summary"`
	Output           string `json:"output"`
	RefreshWorkspace bool   `json:"refresh_workspace"`
}

// Actions 隔离界面、SVN 命令和上层服务，TUI 会在渲染循环之外调用这些阻塞方法。
type Actions interface {
	WorkspaceStatus(ctx context.Context, workingCopy string) ([]svn.StatusEntry, error)
	WorkspaceDiff(ctx context.Context, workingCopy string, targets []string) (string, error)
	Execute(ctx context.Context, request ActionRequest) (ActionResult, error)
}

// ActionPreviewer 可选地为确认页提供经过脱敏的真实命令摘要。
type ActionPreviewer interface {
	PreviewAction(request ActionRequest) string
}

// ModuleRequest 标识一次非 Workspace 页面加载。
type ModuleRequest struct {
	Module         Module
	WorkingCopy    string
	Target         string
	BeforeRevision int64
	Limit          int
	Author         string
	Search         string
	Since          string
	Until          string
}

// ModuleRow 是功能服务提供的可选记录，ID 会作为操作目标回传，Detail 用于详情页。
type ModuleRow struct {
	ID     string
	Cells  []string
	Detail string
	Kind   string
	Target string
}

// ModuleData 是一级功能页面使用的通用表格数据。
type ModuleData struct {
	Title        string
	Columns      []string
	Rows         []ModuleRow
	EmptyMessage string
	NextRevision int64
	HasMore      bool
	Location     string
	Parent       string
}

// ModuleLoader 可选地提供非 Workspace 页面数据，避免 TUI 耦合具体子系统。
type ModuleLoader interface {
	LoadModule(ctx context.Context, request ModuleRequest) (ModuleData, error)
}

// RevisionDiffLoader 可选地按需读取某次修订产生的内容差异，避免历史列表预加载大量 diff。
type RevisionDiffLoader interface {
	LoadRevisionDiff(ctx context.Context, workingCopy, target, revision string) (string, error)
}

// Options 配置一次 TUI 会话。
type Options struct {
	WorkingCopy  string
	Actions      Actions
	ModuleLoader ModuleLoader
	NoColor      bool
	Editor       string
	RunExternal  func(context.Context, ExternalRequest) error
}

var ErrActionsRequired = errors.New("TUI 未配置操作服务")

// 外部工具在终端挂起期间运行，返回后重新加载真实状态。
type ExternalRequest struct{ Kind, WorkingCopy, Target string }
type ActionNormalizer interface {
	NormalizeAction(ActionRequest) (ActionRequest, error)
}
type ProjectSwitcher interface {
	SwitchProject(context.Context, string) (string, error)
}

type MergeInfoData struct {
	Eligible []int64
	Merged   []int64
}

type MergeInfoLoader interface {
	LoadMergeInfo(context.Context, string, string) (MergeInfoData, error)
}

type HistoryInspector interface {
	LoadBlame(context.Context, string, string, string) (string, error)
	CompareRevisions(context.Context, string, string, string, string) (string, error)
}

// 只读操作允许取消，不需要写入确认。
func (r ActionRequest) IsReadOnly() bool {
	switch r.Action {
	case ActionProperties, ActionExternals:
		return true
	case ActionUpdate, ActionMerge, ActionPatch:
		arguments := r.Arguments
		// 合并来源与补丁文件名是字面值，不能将它们当成执行开关。
		if r.Action != ActionUpdate && len(arguments) > 0 {
			arguments = arguments[1:]
		}
		for _, arg := range arguments {
			if arg == "--apply" {
				return false
			}
		}
		return true
	default:
		return false
	}
}
