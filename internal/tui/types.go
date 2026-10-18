package tui

import "svntui/internal/domain"

type Module = domain.Module
type Action = domain.Action
type ActionRequest = domain.ActionRequest
type ActionResult = domain.ActionResult
type Actions = domain.Actions
type ActionPreviewer = domain.ActionPreviewer
type ModuleRequest = domain.ModuleRequest
type ModuleRow = domain.ModuleRow
type ModuleData = domain.ModuleData
type ModuleLoader = domain.ModuleLoader
type RevisionDiffLoader = domain.RevisionDiffLoader
type Options = domain.Options
type ExternalRequest = domain.ExternalRequest
type ActionNormalizer = domain.ActionNormalizer
type ProjectSwitcher = domain.ProjectSwitcher
type MergeInfoData = domain.MergeInfoData
type MergeInfoLoader = domain.MergeInfoLoader
type HistoryInspector = domain.HistoryInspector

const (
	ModuleWorkspace   = domain.ModuleWorkspace
	ModuleRepository  = domain.ModuleRepository
	ModuleHistory     = domain.ModuleHistory
	ModuleBranches    = domain.ModuleBranches
	ModuleConflicts   = domain.ModuleConflicts
	ModuleAutomations = domain.ModuleAutomations
	ModuleSafePoints  = domain.ModuleSafePoints
	ModuleOperations  = domain.ModuleOperations
	ModuleProjects    = domain.ModuleProjects
	ActionUpdate      = domain.ActionUpdate
	ActionAdd         = domain.ActionAdd
	ActionDelete      = domain.ActionDelete
	ActionMove        = domain.ActionMove
	ActionCommit      = domain.ActionCommit
	ActionRevert      = domain.ActionRevert
	ActionCleanup     = domain.ActionCleanup
	ActionResolve     = domain.ActionResolve
	ActionLock        = domain.ActionLock
	ActionUnlock      = domain.ActionUnlock
	ActionIgnore      = domain.ActionIgnore
	ActionProperty    = domain.ActionProperty
	ActionCopy        = domain.ActionCopy
	ActionSwitch      = domain.ActionSwitch
	ActionMerge       = domain.ActionMerge
	ActionChangelist  = domain.ActionChangelist
	ActionProperties  = domain.ActionProperties
	ActionPropDelete  = domain.ActionPropDelete
	ActionExternals   = domain.ActionExternals
	ActionPatch       = domain.ActionPatch
)

var Modules = domain.Modules
var ErrActionsRequired = domain.ErrActionsRequired
