package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"svntui/internal/svn"
)

type taskKind uint8

const (
	taskStatus taskKind = iota
	taskDiff
	taskRevisionDiff
	taskModule
	taskAction
	taskCommitDiff
	taskProject
	taskMergeInfo
	taskHistoryInspect
)

type taskResultEvent struct {
	id           uint64
	kind         taskKind
	module       Module
	entries      []svn.StatusEntry
	moduleData   ModuleData
	text         string
	title        string
	actionResult ActionResult
	err          error
	request      ActionRequest
	mergeInfo    MergeInfoData
	mergeSource  string
}

type application struct {
	ctx                  context.Context
	options              Options
	terminal             terminal
	renderer             renderer
	model                model
	events               chan any
	taskCancel           context.CancelFunc
	wait                 sync.WaitGroup
	inputCancel          context.CancelFunc
	inputDone            chan struct{}
	refreshAfterExternal bool
}

// Run 打开当前平台的控制台并进入原始输入和备用屏幕，退出时始终恢复终端状态。
func Run(ctx context.Context, options Options) (err error) {
	if options.Actions == nil {
		return ErrActionsRequired
	}
	term, err := openTTY()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, term.Close()) }()
	return runWithTerminal(ctx, options, term)
}

func runWithTerminal(ctx context.Context, options Options, term terminal) error {
	runCtx, cancel := context.WithCancel(ctx)
	app := application{
		ctx:      runCtx,
		options:  options,
		terminal: term,
		renderer: renderer{color: !options.NoColor},
		model:    newModel(options.WorkingCopy),
		events:   make(chan any, 64),
	}
	defer func() {
		cancel()
		if app.taskCancel != nil {
			app.taskCancel()
		}
		if app.inputCancel != nil {
			app.inputCancel()
		}
		interruptTerminalRead(term)
		app.wait.Wait()
	}()

	if width, height, err := term.Size(); err == nil {
		app.model.setSize(width, height)
	}

	resize, stopResize := watchTerminalResize(runCtx, term)
	defer stopResize()
	app.startInput()

	if options.WorkingCopy != "" {
		app.startStatus()
	} else {
		app.model.message = "未指定工作副本；按 9 打开项目页并选择项目"
	}
	if err := app.draw(); err != nil {
		return err
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
		case <-ctx.Done():
			return ctx.Err()
		case <-resize:
			if width, height, err := term.Size(); err == nil {
				app.model.setSize(width, height)
			}
		case event := <-app.events:
			quit, err := app.handleEvent(event)
			if err != nil {
				return err
			}
			if quit {
				return nil
			}
		}
		if err := app.draw(); err != nil {
			return err
		}
	}
}

func (a *application) handleEvent(event any) (bool, error) {
	switch value := event.(type) {
	case inputEvent:
		return a.handleKey(value.key), nil
	case inputErrorEvent:
		return false, fmt.Errorf("读取终端输入: %w", value.err)
	case taskResultEvent:
		a.handleTaskResult(value)
	}
	return false, nil
}

func (a *application) handleKey(pressed key) bool {
	if pressed.kind == keyCtrlC {
		if a.model.busy && !a.model.cancelable {
			a.model.message = "写操作运行中，不能退出"
			return false
		}
		if a.model.busy && a.model.cancelable {
			a.cancelReadTask()
			return false
		}
		return true
	}
	if a.model.overlay == overlayRevisions {
		return a.handleRevisionKey(pressed)
	}
	if a.model.overlay == overlayCommit {
		return a.handleCommitKey(pressed)
	}
	if a.model.overlay == overlayForm {
		return a.handleFormKey(pressed)
	}
	if a.model.overlay == overlayContext {
		return a.handleContextKey(pressed)
	}
	if a.model.overlay == overlayCommand {
		return a.handleCommandKey(pressed)
	}
	if a.model.overlay == overlayConfirm {
		return a.handleConfirmKey(pressed)
	}
	if pressed.kind == keyEscape {
		if a.model.detailSearching {
			a.model.detailSearching = false
			return false
		}
		a.model.overlay = a.model.detailReturn
		a.model.detailReturn = overlayNone
		a.model.detailSearching = false
		a.model.overlayOffset = 0
		return false
	}
	if a.model.overlay == overlayHelp || a.model.overlay == overlayDetail {
		return a.handleDocumentKey(pressed)
	}

	if pressed.kind == keyRune && pressed.rune >= '1' && pressed.rune <= '9' {
		a.switchModule(Modules[int(pressed.rune-'1')])
		return false
	}
	if pressed.kind == keyTab || pressed.kind == keyBackTab {
		delta := 1
		if pressed.kind == keyBackTab {
			delta = -1
		}
		index := int(a.model.module) + delta
		if index < 0 {
			index = len(Modules) - 1
		}
		a.switchModule(Modules[index%len(Modules)])
		return false
	}

	if pressed.kind == keyRune {
		switch pressed.rune {
		case 'C':
			a.openCommit()
			return false
		case 'b', 'v':
			if a.model.module == ModuleHistory || a.model.module == ModuleWorkspace {
				kind := "blame"
				if pressed.rune == 'v' {
					kind = "compare"
				}
				a.openForm(kind, "")
			}
			return false
		case 'a':
			a.model.contextCursor = 0
			a.model.overlay = overlayContext
			return false
		case 'A':
			if a.model.module == ModuleWorkspace {
				a.model.selectVisible(false)
			}
			return false
		case 'I':
			if a.model.module == ModuleWorkspace {
				a.model.selectVisible(true)
			}
			return false
		case 'X':
			if a.model.module == ModuleWorkspace {
				clear(a.model.selected)
			}
			return false
		case '/':
			if a.model.module == ModuleWorkspace {
				a.openForm("workspace-filter", "")
			} else if a.model.module == ModuleHistory {
				a.openForm("history-filter", "")
			}
			return false
		case ']':
			if a.model.module == ModuleHistory {
				a.historyPage(true)
			}
			return false
		case '[':
			if a.model.module == ModuleHistory {
				a.historyPage(false)
			}
			return false
		case 'e':
			if a.model.module == ModuleConflicts {
				a.runConflictExternal("edit")
			}
			return false
		case 'm':
			if a.model.module == ModuleConflicts {
				a.runConflictExternal("merge-tool")
			} else if a.model.module == ModuleBranches {
				a.openForm("action", ActionMerge)
			}
			return false
		}
	}
	if pressed.kind == keyBackspace && a.model.module == ModuleRepository && !a.model.busy {
		if parent := a.model.moduleData[ModuleRepository].Parent; parent != "" {
			a.model.repositoryTarget = parent
			a.model.moduleCursor[ModuleRepository] = 0
			a.startModuleLoad(ModuleRepository)
		}
		return false
	}
	switch {
	case pressed.kind == keyUp || (pressed.kind == keyRune && pressed.rune == 'k'):
		a.model.moveCursor(-1)
	case pressed.kind == keyDown || (pressed.kind == keyRune && pressed.rune == 'j'):
		a.model.moveCursor(1)
	case pressed.kind == keyPageUp:
		a.model.moveCursor(-max(a.model.height-6, 1))
	case pressed.kind == keyPageDown:
		a.model.moveCursor(max(a.model.height-6, 1))
	case pressed.kind == keyHome || (pressed.kind == keyRune && pressed.rune == 'g'):
		if a.model.module == ModuleWorkspace {
			a.model.setCursor(0)
		} else {
			a.model.setModuleCursor(0)
		}
	case pressed.kind == keyEnd || (pressed.kind == keyRune && pressed.rune == 'G'):
		if a.model.module == ModuleWorkspace {
			a.model.setCursor(len(a.model.entries) - 1)
		} else {
			a.model.setModuleCursor(len(a.model.moduleData[a.model.module].Rows) - 1)
		}
	case pressed.kind == keyRune && pressed.rune == ' ':
		a.model.toggleSelection()
	case pressed.kind == keyRune && pressed.rune == ':':
		a.model.command = nil
		a.model.commandCursor = 0
		a.model.commandEditing = false
		a.model.overlay = overlayCommand
	case pressed.kind == keyRune && pressed.rune == '?':
		a.showHelp()
	case pressed.kind == keyRune && pressed.rune == 'r':
		a.refreshCurrentModule()
	case pressed.kind == keyRune && pressed.rune == 'd':
		a.startCurrentDiff()
	case pressed.kind == keyRune && pressed.rune == 'h':
		a.openCurrentFileHistory()
	case pressed.kind == keyEnter:
		a.openCurrentItem()
	case pressed.kind == keyRune && pressed.rune == 'c':
		a.cancelReadTask()
	case pressed.kind == keyRune && pressed.rune == 'q':
		if a.model.busy && !a.model.cancelable {
			a.model.message = "写操作运行中，不能退出"
			return false
		}
		return true
	}
	return false
}

func (a *application) handleCommandKey(pressed key) bool {
	if a.model.commandEditing {
		return a.handleCommandEditKey(pressed)
	}
	entries := filteredPaletteEntries(string(a.model.command))
	switch pressed.kind {
	case keyPaste:
		a.model.commandEditing = true
		a.model.commandPosition = len(a.model.command)
		a.model.historyPosition = len(a.model.commandHistory)
		editRunes(&a.model.command, &a.model.commandPosition, pressed, false)
	case keyEscape:
		a.model.overlay = overlayNone
		a.model.command = nil
		a.model.commandCursor = 0
	case keyBackspace, keyDelete:
		if len(a.model.command) > 0 {
			a.model.command = a.model.command[:len(a.model.command)-1]
		}
		a.model.commandCursor = 0
	case keyUp:
		if len(entries) > 0 {
			a.model.commandCursor = max(a.model.commandCursor-1, 0)
		}
	case keyDown:
		if len(entries) > 0 {
			a.model.commandCursor = min(a.model.commandCursor+1, len(entries)-1)
		}
	case keyEnter:
		if len(entries) == 0 {
			return false
		}
		entry := entries[min(a.model.commandCursor, len(entries)-1)]
		if entry.Edit {
			a.model.command = []rune(entry.Template)
			a.model.commandEditing = true
			a.model.commandPosition = len(a.model.command)
			a.model.historyPosition = len(a.model.commandHistory)
			return false
		}
		return a.runPaletteLine(entry.Command)
	case keyRune:
		if pressed.rune >= ' ' {
			a.model.command = append(a.model.command, pressed.rune)
			a.model.commandCursor = 0
		}
	}
	return false
}

func (a *application) handleCommandEditKey(pressed key) bool {
	switch pressed.kind {
	case keyEscape:
		a.model.command = nil
		a.model.commandCursor = 0
		a.model.commandEditing = false
	case keyEnter:
		return a.runPaletteLine(string(a.model.command))
	case keyUp, keyDown:
		if len(a.model.commandHistory) == 0 {
			return false
		}
		if a.model.historyPosition == len(a.model.commandHistory) {
			a.model.commandSaved = string(a.model.command)
		}
		delta := -1
		if pressed.kind == keyDown {
			delta = 1
		}
		a.model.historyPosition = min(max(a.model.historyPosition+delta, 0), len(a.model.commandHistory))
		if a.model.historyPosition == len(a.model.commandHistory) {
			a.model.command = []rune(a.model.commandSaved)
		} else {
			a.model.command = []rune(a.model.commandHistory[a.model.historyPosition])
		}
		a.model.commandPosition = len(a.model.command)
	case keyTab:
		prefix := string(a.model.command[:min(a.model.commandPosition, len(a.model.command))])
		for _, entry := range paletteEntries {
			template := entry.Template
			if template == "" {
				template = entry.Command
			}
			if strings.HasPrefix(template, prefix) {
				a.model.command = []rune(template)
				a.model.commandPosition = len(a.model.command)
				break
			}
		}
	default:
		editRunes(&a.model.command, &a.model.commandPosition, pressed, false)
	}
	return false
}

func (a *application) runPaletteLine(line string) bool {
	if strings.TrimSpace(line) != "" {
		a.model.commandHistory = append(a.model.commandHistory, line)
	}
	a.model.command = nil
	a.model.commandCursor = 0
	a.model.commandEditing = false
	a.model.overlay = overlayNone
	command, err := parsePaletteCommand(line)
	if err != nil {
		a.showDetail("命令错误", err.Error())
		return false
	}
	return a.executePaletteCommand(command)
}

func (a *application) handleConfirmKey(pressed key) bool {
	a.model.confirm.offset = scrollOffset(a.model.confirm.offset, pressed, max(len(wrapText(confirmationContent(a.model.confirm), max(a.model.width-2, 1)))-max(a.model.height-5, 1), 0), max(a.model.height-6, 1))
	if pressed.kind == keyUp || pressed.kind == keyDown || pressed.kind == keyPageUp || pressed.kind == keyPageDown || pressed.kind == keyHome || pressed.kind == keyEnd {
		return false
	}
	if pressed.kind == keyEscape || (!a.model.confirm.dangerous && pressed.kind == keyRune && (pressed.rune == 'n' || pressed.rune == 'N')) {
		a.model.overlay = a.model.confirm.returnTo
		a.model.message = "已取消操作"
		return false
	}
	if a.model.confirm.dangerous {
		switch pressed.kind {
		case keyBackspace, keyDelete:
			if len(a.model.confirm.typed) > 0 {
				a.model.confirm.typed = a.model.confirm.typed[:len(a.model.confirm.typed)-1]
			}
		case keyRune:
			if pressed.rune >= ' ' {
				a.model.confirm.typed = append(a.model.confirm.typed, pressed.rune)
			}
		case keyEnter:
			if string(a.model.confirm.typed) != "YES" {
				a.model.message = "高风险操作必须输入 YES"
				return false
			}
			request := a.model.confirm.request
			a.model.overlay = overlayNone
			a.startAction(request)
		}
		return false
	}
	if pressed.kind == keyRune && (pressed.rune == 'y' || pressed.rune == 'Y') {
		request := a.model.confirm.request
		a.model.overlay = overlayNone
		a.startAction(request)
	}
	return false
}

func (a *application) executePaletteCommand(command paletteCommand) bool {
	switch command.kind {
	case commandNone:
	case commandHelp:
		a.showHelp()
	case commandQuit:
		if a.model.busy && !a.model.cancelable {
			a.model.message = "写操作运行中，不能退出"
			return false
		}
		return true
	case commandRefresh:
		a.refreshCurrentModule()
	case commandDiff:
		a.startCurrentDiff()
	case commandModule:
		a.switchModule(command.module)
	case commandAction:
		if a.model.busy {
			a.model.message = "已有任务运行，请等待或取消读取任务"
			return false
		}
		request := ActionRequest{
			Action:      command.action,
			Module:      a.model.module,
			WorkingCopy: a.options.WorkingCopy,
			Targets:     a.model.actionTargets(),
			Arguments:   slices.Clone(command.arguments),
		}
		if command.action == ActionCommit {
			request.Targets = a.model.selectedTargets()
		}
		if command.action == ActionCommit && len(command.arguments) == 0 {
			a.openCommit()
			return false
		}
		a.prepareAction(request, command.dangerous, overlayNone)
	}
	return false
}

func (a *application) switchModule(module Module) {
	if a.model.busy && !a.model.cancelable {
		a.model.message = "写操作运行期间不能切换模块"
		return
	}
	if a.model.busy {
		a.cancelReadTask()
	}
	a.model.setModule(module)
	if module == ModuleHistory {
		a.model.historyTarget = ""
		a.model.historyRequest = ModuleRequest{Limit: 100}
		a.model.historyPages = nil
	}
	if module != ModuleWorkspace {
		a.startModuleLoad(module)
	}
}

func (a *application) openCurrentFileHistory() {
	if a.model.module != ModuleWorkspace || len(a.model.entries) == 0 {
		a.model.message = "请先在工作区选择一个文件"
		return
	}
	if a.model.busy && !a.model.cancelable {
		a.model.message = "写操作运行期间不能切换模块"
		return
	}
	if a.model.busy {
		a.cancelReadTask()
	}
	target := a.model.entries[a.model.cursor].Path
	a.model.setModule(ModuleHistory)
	a.model.historyTarget = target
	a.model.historyRequest = ModuleRequest{Limit: 100}
	a.model.historyPages = nil
	a.startModuleLoad(ModuleHistory)
}

func (a *application) refreshCurrentModule() {
	if a.model.busy {
		a.model.message = "已有任务运行，请等待或按 c 取消读取任务"
		return
	}
	if a.model.module == ModuleWorkspace {
		a.startStatus()
	} else {
		a.startModuleLoad(a.model.module)
	}
}

func (a *application) openCurrentItem() {
	if a.model.module == ModuleWorkspace {
		a.startDiff()
		return
	}
	row, ok := a.model.currentModuleRow()
	if !ok {
		return
	}
	if a.model.module == ModuleProjects {
		a.switchProject(cmpTarget(row))
		return
	}
	if a.model.module == ModuleRepository && row.Kind == "dir" && !a.model.busy {
		a.model.repositoryTarget = cmpTarget(row)
		a.model.moduleCursor[ModuleRepository] = 0
		a.startModuleLoad(ModuleRepository)
		return
	}
	content := row.Detail
	if content == "" {
		content = strings.Join(row.Cells, "\n")
	}
	a.showDetail(a.model.module.DisplayName(), content)
}

func (a *application) showHelp() {
	a.model.overlay = overlayHelp
	a.model.overlayOffset = 0
}

func (a *application) showDetail(title, content string) {
	a.model.mergePreview = nil
	a.model.detailReturn = overlayNone
	a.model.detailSearching = false
	a.model.detailTitle = title
	a.model.detailContent = content
	a.model.overlay = overlayDetail
	a.model.overlayOffset = 0
}

func (a *application) startStatus() {
	if a.options.WorkingCopy == "" {
		a.model.message = "未指定工作副本"
		return
	}
	wc := a.options.WorkingCopy
	a.startTask(taskStatus, ModuleWorkspace, "刷新工作副本状态", true, func(ctx context.Context) taskResultEvent {
		entries, err := a.options.Actions.WorkspaceStatus(ctx, wc)
		return taskResultEvent{entries: entries, err: err}
	})
}

func (a *application) startDiff() {
	if a.model.busy {
		a.model.message = "已有任务运行，请等待或按 c 取消读取任务"
		return
	}
	targets := a.model.targets()
	wc := a.options.WorkingCopy
	a.startTask(taskDiff, ModuleWorkspace, "读取差异", true, func(ctx context.Context) taskResultEvent {
		content, err := a.options.Actions.WorkspaceDiff(ctx, wc, targets)
		return taskResultEvent{text: content, err: err}
	})
}

func (a *application) startCurrentDiff() {
	switch a.model.module {
	case ModuleWorkspace:
		a.startDiff()
	case ModuleHistory:
		a.startRevisionDiff()
	default:
		a.model.message = "当前页面没有可查看的内容差异"
	}
}

func (a *application) startRevisionDiff() {
	if a.model.busy {
		a.model.message = "已有任务运行，请等待或按 c 取消读取任务"
		return
	}
	row, ok := a.model.currentModuleRow()
	if !ok {
		a.model.message = "请先选择一条修订记录"
		return
	}
	loader, ok := a.options.ModuleLoader.(RevisionDiffLoader)
	if !ok {
		a.model.message = "当前历史服务不支持读取修订内容差异"
		return
	}
	revision := row.ID
	target := a.model.historyTarget
	wc := a.options.WorkingCopy
	a.startTask(taskRevisionDiff, ModuleHistory, "读取 r"+revision+" 的内容差异", true, func(ctx context.Context) taskResultEvent {
		content, err := loader.LoadRevisionDiff(ctx, wc, target, revision)
		return taskResultEvent{text: content, title: "Revision r" + revision + " 内容差异", err: err}
	})
}

func (a *application) startModuleLoad(module Module) {
	if a.options.ModuleLoader == nil {
		a.model.message = moduleHint(module)
		return
	}
	request := ModuleRequest{Module: module, WorkingCopy: a.options.WorkingCopy}
	if module == ModuleHistory {
		request = a.model.historyRequest
		request.Module = module
		request.WorkingCopy = a.options.WorkingCopy
		request.Target = a.model.historyTarget
	}
	if module == ModuleRepository {
		request.Target = a.model.repositoryTarget
	}
	a.startTask(taskModule, module, "加载"+module.DisplayName(), true, func(ctx context.Context) taskResultEvent {
		data, err := a.options.ModuleLoader.LoadModule(ctx, request)
		return taskResultEvent{moduleData: data, err: err}
	})
}

func (a *application) startAction(request ActionRequest) {
	a.startTask(taskAction, a.model.module, "执行"+request.Action.DisplayName(), request.IsReadOnly(), func(ctx context.Context) taskResultEvent {
		result, err := a.options.Actions.Execute(ctx, request)
		return taskResultEvent{actionResult: result, err: err, request: request}
	})
}

func (a *application) startTask(kind taskKind, module Module, label string, cancelable bool, run func(context.Context) taskResultEvent) {
	if a.model.busy && !a.model.cancelable {
		a.model.message = "写操作运行期间不能启动其他任务"
		return
	}
	if a.taskCancel != nil {
		a.taskCancel()
	}
	taskCtx, cancel := context.WithCancel(a.ctx)
	a.taskCancel = cancel
	id := a.model.beginTask(label, cancelable)
	a.wait.Add(1)
	go func() {
		defer a.wait.Done()
		result := run(taskCtx)
		result.id = id
		result.kind = kind
		result.module = module
		sendEvent(a.ctx, a.events, result)
	}()
}

func (a *application) cancelReadTask() {
	if !a.model.busy || !a.model.cancelable || a.taskCancel == nil {
		return
	}
	a.taskCancel()
	a.model.taskID++
	a.model.busy = false
	a.model.cancelable = false
	a.model.busyLabel = ""
	a.model.message = "已取消读取任务"
}

func (a *application) handleTaskResult(result taskResultEvent) {
	if !a.model.finishTask(result.id) {
		return
	}
	if a.taskCancel != nil {
		a.taskCancel()
	}
	a.taskCancel = nil
	if result.err != nil {
		if errors.Is(result.err, context.Canceled) {
			a.model.message = "任务已取消"
			return
		}
		content := result.err.Error()
		if strings.TrimSpace(result.actionResult.Output) != "" {
			content += "\n\n命令输出:\n" + result.actionResult.Output
		}
		a.showDetail("操作失败", content)
		if result.request.Action == ActionCommit || result.kind == taskCommitDiff {
			a.model.detailReturn = overlayCommit
		}
		if result.kind == taskMergeInfo || result.kind == taskHistoryInspect {
			a.model.detailReturn = overlayForm
		}
		a.model.message = "操作失败"
		if result.kind == taskAction && a.options.WorkingCopy != "" {
			a.startStatus()
		}
		return
	}

	switch result.kind {
	case taskMergeInfo:
		a.acceptMergeInfo(result.mergeSource, result.mergeInfo)
	case taskHistoryInspect:
		a.showDetail(result.title, result.text)
		a.model.detailReturn = overlayForm
	case taskProject:
		drafts := a.model.drafts
		width, height := a.model.width, a.model.height
		id := a.model.taskID
		a.model = newModel(result.text)
		a.model.taskID = id
		a.model.drafts = drafts
		a.model.setSize(width, height)
		a.options.WorkingCopy = result.text
		a.startStatus()
	case taskCommitDiff:
		a.model.commit.diff = result.text
		a.model.commit.diffOffset = 0
		a.model.overlay = overlayCommit
		a.model.message = "提交差异已读取"
	case taskStatus:
		a.model.replaceEntries(result.entries)
		a.model.message = statusCountMessage(result.entries)
		if a.refreshAfterExternal {
			a.refreshAfterExternal = false
			if a.model.module != ModuleWorkspace {
				a.startModuleLoad(a.model.module)
			}
		}
	case taskDiff:
		content := result.text
		if strings.TrimSpace(content) == "" {
			content = "选中目标没有差异。"
		}
		a.showDetail("工作副本差异", content)
		a.model.message = "差异读取完成"
	case taskRevisionDiff:
		content := result.text
		if strings.TrimSpace(content) == "" {
			content = "该修订没有可显示的文本内容差异。"
		}
		a.showDetail(result.title, content)
		a.model.message = "修订内容差异读取完成"
	case taskModule:
		a.model.replaceModuleData(result.module, result.moduleData)
		if result.module == ModuleRepository && result.moduleData.Location != "" {
			a.model.repositoryTarget = result.moduleData.Location
		}
		a.model.message = fmt.Sprintf("%s：%d 条记录", result.module.DisplayName(), len(result.moduleData.Rows))
	case taskAction:
		content := result.actionResult.Output
		if result.actionResult.Summary != "" {
			content = result.actionResult.Summary + "\n\n" + content
		}
		if strings.TrimSpace(content) == "" {
			content = "操作已完成。"
		}
		a.showDetail("操作完成", content)
		a.model.mergePreview = nil
		if (result.request.Action == ActionMerge || result.request.Action == ActionUpdate || result.request.Action == ActionPatch) && result.request.IsReadOnly() {
			request := result.request
			a.model.mergePreview = &request
			a.model.detailContent += "\n\n按 a 进入应用确认；Esc 返回。"
		}
		if result.request.Action == ActionCommit {
			delete(a.model.drafts, a.model.workingCopy)
			a.model.commit = commitState{}
			clear(a.model.selected)
		}
		if result.request.Action == ActionMerge && !result.request.IsReadOnly() {
			a.model.module = ModuleWorkspace
			a.model.detailContent += "\n\n检查冲突与 diff 后按 C 打开提交页；需要手动提交。"
		}
		a.model.message = "操作已完成"
		if result.actionResult.RefreshWorkspace {
			a.startStatus()
		}
	}
}

func (a *application) draw() error {
	if _, err := a.terminal.Write([]byte(a.renderer.render(a.model))); err != nil {
		return fmt.Errorf("绘制终端界面: %w", err)
	}
	return nil
}

func (a *application) startInput() {
	ctx, cancel := context.WithCancel(a.ctx)
	a.inputCancel = cancel
	done := make(chan struct{})
	a.inputDone = done
	a.wait.Add(1)
	go func() { defer a.wait.Done(); defer close(done); readInput(ctx, a.terminal, a.events) }()
}
func cmpTarget(row ModuleRow) string {
	if row.Target != "" {
		return row.Target
	}
	return row.ID
}
