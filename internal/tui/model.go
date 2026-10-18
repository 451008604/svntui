package tui

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"svntui/internal/svn"
)

type overlayKind uint8

const (
	overlayNone overlayKind = iota
	overlayCommand
	overlayHelp
	overlayDetail
	overlayConfirm
	overlayCommit
	overlayForm
	overlayContext
	overlayRevisions
)

type confirmation struct {
	request   ActionRequest
	dangerous bool
	typed     []rune
	preview   string
	offset    int
	returnTo  overlayKind
}

type model struct {
	workingCopy      string
	module           Module
	entries          []svn.StatusEntry
	allEntries       []svn.StatusEntry
	filter           workspaceFilter
	repositoryTarget string
	historyRequest   ModuleRequest
	historyPages     []int64
	drafts           map[string]string
	commit           commitState
	form             formState
	contextCursor    int
	commandPosition  int
	commandHistory   []string
	historyPosition  int
	commandSaved     string
	detailSearch     []rune
	detailSearching  bool
	detailReturn     overlayKind
	mergePreview     *ActionRequest
	busySince        time.Time
	lastElapsed      time.Duration
	revisions        revisionSelection
	cursor           int
	selected         map[string]bool
	moduleData       map[Module]ModuleData
	moduleCursor     map[Module]int
	historyTarget    string
	width            int
	height           int

	overlay        overlayKind
	overlayOffset  int
	command        []rune
	commandCursor  int
	commandEditing bool
	detailTitle    string
	detailContent  string
	confirm        confirmation

	busy       bool
	busyLabel  string
	cancelable bool
	message    string
	taskID     uint64
}

func newModel(workingCopy string) model {
	return model{
		workingCopy:    workingCopy,
		module:         ModuleWorkspace,
		selected:       make(map[string]bool),
		drafts:         make(map[string]string),
		historyRequest: ModuleRequest{Limit: 100},
		moduleData:     make(map[Module]ModuleData),
		moduleCursor:   make(map[Module]int),
		width:          80,
		height:         24,
		message:        "正在读取工作副本状态…",
	}
}

func (m *model) setSize(width, height int) {
	m.width = max(width, 20)
	m.height = max(height, 6)
}

func (m *model) setModule(module Module) {
	m.module = module
	m.overlay = overlayNone
	m.overlayOffset = 0
	m.message = moduleHint(module)
}

func (m *model) moveCursor(delta int) {
	if m.overlay != overlayNone {
		return
	}
	if m.module == ModuleWorkspace {
		if len(m.entries) == 0 {
			return
		}
		m.cursor = min(max(m.cursor+delta, 0), len(m.entries)-1)
		return
	}
	data := m.moduleData[m.module]
	if len(data.Rows) == 0 {
		return
	}
	m.moduleCursor[m.module] = min(max(m.moduleCursor[m.module]+delta, 0), len(data.Rows)-1)
}

func (m *model) setCursor(index int) {
	if len(m.entries) == 0 {
		m.cursor = 0
		return
	}
	m.cursor = min(max(index, 0), len(m.entries)-1)
}

func (m *model) setModuleCursor(index int) {
	data := m.moduleData[m.module]
	if len(data.Rows) == 0 {
		m.moduleCursor[m.module] = 0
		return
	}
	m.moduleCursor[m.module] = min(max(index, 0), len(data.Rows)-1)
}

func (m *model) toggleSelection() {
	if m.module != ModuleWorkspace || m.overlay != overlayNone || len(m.entries) == 0 {
		return
	}
	path := m.entries[m.cursor].Path
	if m.selected[path] {
		delete(m.selected, path)
	} else {
		m.selected[path] = true
	}
}

func (m *model) currentModuleRow() (ModuleRow, bool) {
	data, ok := m.moduleData[m.module]
	if !ok || len(data.Rows) == 0 {
		return ModuleRow{}, false
	}
	index := min(m.moduleCursor[m.module], len(data.Rows)-1)
	return data.Rows[index], true
}

func (m *model) actionTargets() []string {
	if m.module == ModuleWorkspace {
		return m.targets()
	}
	row, ok := m.currentModuleRow()
	if !ok || (row.ID == "" && row.Target == "") {
		return nil
	}
	return []string{cmp.Or(row.Target, row.ID)}
}

func (m *model) replaceModuleData(module Module, data ModuleData) {
	data.Columns = slices.Clone(data.Columns)
	data.Rows = slices.Clone(data.Rows)
	for index := range data.Rows {
		data.Rows[index].Cells = slices.Clone(data.Rows[index].Cells)
	}
	m.moduleData[module] = data
	if len(data.Rows) == 0 {
		m.moduleCursor[module] = 0
	} else {
		m.moduleCursor[module] = min(m.moduleCursor[module], len(data.Rows)-1)
	}
}

func (m *model) targets() []string {
	if len(m.selected) > 0 {
		return slices.Sorted(maps.Keys(m.selected))
	}
	if len(m.entries) == 0 || m.cursor >= len(m.entries) {
		return nil
	}
	return []string{m.entries[m.cursor].Path}
}

func (m *model) selectedTargets() []string {
	return slices.Sorted(maps.Keys(m.selected))
}

func (m *model) replaceEntries(entries []svn.StatusEntry) {
	currentPath := ""
	if len(m.entries) > 0 && m.cursor < len(m.entries) {
		currentPath = m.entries[m.cursor].Path
	}
	m.allEntries = slices.Clone(entries)
	m.entries = slices.Clone(entries)
	slices.SortFunc(m.entries, func(left, right svn.StatusEntry) int {
		return cmp.Compare(left.Path, right.Path)
	})

	paths := make(map[string]bool, len(m.entries))
	for index, entry := range m.entries {
		paths[entry.Path] = true
		if entry.Path == currentPath {
			m.cursor = index
		}
	}
	maps.DeleteFunc(m.selected, func(path string, _ bool) bool { return !paths[path] })
	if len(m.entries) == 0 {
		m.cursor = 0
	} else {
		m.cursor = min(m.cursor, len(m.entries)-1)
	}
	m.applyWorkspaceFilter()
	for index, entry := range m.entries {
		if entry.Path == currentPath {
			m.cursor = index
			break
		}
	}
}

func (m *model) beginTask(label string, cancelable bool) uint64 {
	m.taskID++
	m.busy = true
	m.busySince = time.Now()
	m.busyLabel = label
	m.cancelable = cancelable
	m.message = label
	return m.taskID
}

func (m *model) finishTask(taskID uint64) bool {
	if taskID != m.taskID {
		return false
	}
	m.busy = false
	m.lastElapsed = time.Since(m.busySince)
	m.busyLabel = ""
	m.cancelable = false
	return true
}

func moduleHint(module Module) string {
	switch module {
	case ModuleWorkspace:
		return "Space 选择 · d 差异 · C 提交 · a 操作 · / 筛选 · h 文件历史"
	case ModuleRepository:
		return "Enter 进入目录 · Backspace 返回父目录 · a 上下文操作"
	case ModuleHistory:
		return "Enter 详情 · d 差异 · b 追溯 · v 比较 · [/] 翻页 · / 筛选"
	case ModuleBranches:
		return "a 复制/切换/合并 · m 合并向导 · Enter 详情"
	case ModuleConflicts:
		return "e 编辑文件 · m 合并工具 · a 标记解决 · Enter 冲突详情"
	case ModuleAutomations:
		return "Enter 查看计划任务详情 · r 刷新"
	case ModuleSafePoints:
		return "Enter 查看安全快照详情 · r 刷新"
	case ModuleProjects:
		return "Enter 异步切换项目；提交草稿按项目保存"
	case ModuleOperations:
		return "Enter 查看操作输出和结果 · r 刷新"
	default:
		return ""
	}
}

type workspaceFilter struct{ path, status, group string }

func (m *model) applyWorkspaceFilter() {
	m.entries = m.entries[:0]
	for _, entry := range m.allEntries {
		if m.filter.path != "" && !strings.Contains(strings.ToLower(entry.Path), strings.ToLower(m.filter.path)) {
			continue
		}
		if m.filter.status != "" && !strings.EqualFold(entry.Item, m.filter.status) && !strings.EqualFold(statusCode(entry), m.filter.status) {
			continue
		}
		if m.filter.group != "" && !strings.EqualFold(entry.Changelist, m.filter.group) {
			continue
		}
		m.entries = append(m.entries, entry)
	}
	slices.SortFunc(m.entries, func(a, b svn.StatusEntry) int {
		if n := cmp.Compare(a.Changelist, b.Changelist); n != 0 {
			return n
		}
		return cmp.Compare(a.Path, b.Path)
	})
	m.setCursor(m.cursor)
}

func (m *model) selectVisible(invert bool) {
	for _, entry := range m.entries {
		if invert && m.selected[entry.Path] {
			delete(m.selected, entry.Path)
		} else {
			m.selected[entry.Path] = true
		}
	}
}
