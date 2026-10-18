package tui

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"svntui/internal/svn"
)

type commitState struct {
	message    []rune
	position   int
	focus      int
	diff       string
	diffOffset int
}

type formField struct {
	label     string
	value     []rune
	position  int
	toggle    bool
	multiline bool
}
type formState struct {
	title, kind string
	action      Action
	fields      []formField
	active      int
	targets     []string
}
type contextAction struct {
	label    string
	action   Action
	external string
}

func editRunes(value *[]rune, position *int, pressed key, multiline bool) bool {
	*position = min(max(*position, 0), len(*value))
	insert := func(text string) {
		addition := []rune(text)
		*value = slices.Insert(*value, *position, addition...)
		*position += len(addition)
	}
	if multiline {
		start, end := *position, *position
		for start > 0 && (*value)[start-1] != '\n' {
			start--
		}
		for end < len(*value) && (*value)[end] != '\n' {
			end++
		}
		switch pressed.kind {
		case keyHome:
			*position = start
			return true
		case keyEnd:
			*position = end
			return true
		case keyUp:
			if start > 0 {
				previous := start - 1
				for previous > 0 && (*value)[previous-1] != '\n' {
					previous--
				}
				*position = previous + min(*position-start, start-1-previous)
			}
			return true
		case keyDown:
			if end < len(*value) {
				nextEnd := end + 1
				for nextEnd < len(*value) && (*value)[nextEnd] != '\n' {
					nextEnd++
				}
				*position = end + 1 + min(*position-start, nextEnd-end-1)
			}
			return true
		}
	}
	switch pressed.kind {
	case keyLeft:
		*position = max(*position-1, 0)
	case keyRight:
		*position = min(*position+1, len(*value))
	case keyHome:
		*position = 0
	case keyEnd:
		*position = len(*value)
	case keyBackspace:
		if *position > 0 {
			*value = slices.Delete(*value, *position-1, *position)
			*position--
		}
	case keyDelete:
		if *position < len(*value) {
			*value = slices.Delete(*value, *position, *position+1)
		}
	case keyPaste:
		// Windows 控制台可把粘贴换行变成裸 CR，统一后再交给编辑器。
		text := strings.ReplaceAll(pressed.text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		if !multiline {
			text = strings.ReplaceAll(text, "\n", " ")
		}
		insert(text)
	case keyEnter:
		if multiline {
			insert("\n")
		} else {
			return false
		}
	case keyRune:
		if pressed.rune >= ' ' {
			insert(string(pressed.rune))
		}
	default:
		return false
	}
	return true
}

func (a *application) prepareAction(request ActionRequest, dangerous bool, returnTo overlayKind) {
	if a.model.busy {
		a.model.message = "已有任务运行，请等待或取消读取任务"
		return
	}
	// 先规范化，再展示实际目标；只读请求直接启动可取消任务。
	if normalizer, ok := a.options.Actions.(ActionNormalizer); ok {
		normalized, err := normalizer.NormalizeAction(request)
		if err != nil {
			a.showDetail("参数错误", err.Error())
			a.model.detailReturn = returnTo
			return
		}
		request = normalized
	} else if request.Action == ActionMove && len(request.Arguments) == 1 && len(request.Targets) != 1 {
		a.model.message = "移动操作需要选择一个源路径"
		return
	}
	if request.IsReadOnly() {
		a.model.overlay = overlayNone
		a.startAction(request)
		return
	}
	preview := ""
	if provider, ok := a.options.Actions.(ActionPreviewer); ok {
		preview = provider.PreviewAction(request)
	}
	a.model.confirm = confirmation{request: request, dangerous: dangerous, preview: preview, returnTo: returnTo}
	a.model.overlay = overlayConfirm
}

func (a *application) openCommit() {
	if a.model.busy {
		a.model.message = "请等待或取消当前读取任务"
		return
	}
	a.model.module = ModuleWorkspace
	a.model.commit = commitState{message: []rune(a.model.drafts[a.model.workingCopy])}
	a.model.commit.position = len(a.model.commit.message)
	a.model.overlay = overlayCommit
	a.model.message = "Tab 切换文件/说明/差异 · Ctrl-S 提交 · Ctrl-E 外部编辑 · Esc 保留草稿"
}

func (a *application) saveDraft() {
	if a.model.drafts == nil {
		a.model.drafts = make(map[string]string)
	}
	a.model.drafts[a.model.workingCopy] = string(a.model.commit.message)
}

func (a *application) submitCommit() {
	a.saveDraft()
	if len(a.model.selected) == 0 {
		a.model.message = "请先选择需要提交的文件"
		return
	}
	if strings.TrimSpace(string(a.model.commit.message)) == "" {
		a.model.message = "请填写提交说明"
		return
	}
	request := ActionRequest{Action: ActionCommit, Module: ModuleWorkspace, WorkingCopy: a.options.WorkingCopy, Targets: a.model.selectedTargets(), Arguments: []string{string(a.model.commit.message)}}
	a.prepareAction(request, false, overlayCommit)
}

func (a *application) handleCommitKey(pressed key) bool {
	if a.model.busy && a.model.cancelable && a.model.commit.focus != 1 && pressed.kind == keyRune && pressed.rune == 'c' {
		a.cancelReadTask()
		return false
	}
	switch pressed.kind {
	case keyEscape:
		a.saveDraft()
		if a.model.busy && a.model.cancelable {
			a.cancelReadTask()
		}
		a.model.overlay = overlayNone
		return false
	case keyCtrlS:
		a.submitCommit()
		return false
	case keyCtrlE:
		a.editCommitExternally()
		return false
	case keyTab:
		a.model.commit.focus = (a.model.commit.focus + 1) % 3
		return false
	case keyBackTab:
		a.model.commit.focus = (a.model.commit.focus + 2) % 3
		return false
	}
	if a.model.commit.focus == 1 {
		editRunes(&a.model.commit.message, &a.model.commit.position, pressed, true)
		a.saveDraft()
		return false
	}
	if a.model.commit.focus == 2 {
		a.model.commit.diffOffset = scrollOffset(a.model.commit.diffOffset, pressed, max(len(wrapText(sanitizeText(a.model.commit.diff), max(a.model.width-2, 1)))-max(a.model.height/3-1, 1), 0), max(a.model.height/3-1, 1))
		return false
	}
	switch {
	case pressed.kind == keyUp:
		a.model.setCursor(a.model.cursor - 1)
	case pressed.kind == keyDown:
		a.model.setCursor(a.model.cursor + 1)
	case pressed.kind == keyHome:
		a.model.setCursor(0)
	case pressed.kind == keyEnd:
		a.model.setCursor(len(a.model.entries) - 1)
	case pressed.kind == keyRune && pressed.rune == ' ':
		a.toggleCommitSelection()
	case pressed.kind == keyRune && pressed.rune == 'A':
		a.model.selectVisible(false)
	case pressed.kind == keyRune && pressed.rune == 'I':
		a.model.selectVisible(true)
	case pressed.kind == keyRune && pressed.rune == 'X':
		clear(a.model.selected)
	case pressed.kind == keyRune && pressed.rune == 'd' || pressed.kind == keyEnter:
		a.startCommitDiff()
	case pressed.kind == keyRune && pressed.rune == '?':
		a.model.detailReturn = overlayCommit
		a.showHelp()
	}
	return false
}

func (a *application) toggleCommitSelection() {
	if len(a.model.entries) == 0 {
		return
	}
	path := a.model.entries[a.model.cursor].Path
	if a.model.selected[path] {
		delete(a.model.selected, path)
	} else {
		a.model.selected[path] = true
	}
}

func (a *application) startCommitDiff() {
	if a.model.busy {
		return
	}
	targets := a.model.selectedTargets()
	if len(targets) == 0 {
		targets = a.model.targets()
	}
	wc := a.options.WorkingCopy
	a.startTask(taskCommitDiff, ModuleWorkspace, "读取提交差异", true, func(ctx context.Context) taskResultEvent {
		content, err := a.options.Actions.WorkspaceDiff(ctx, wc, targets)
		return taskResultEvent{text: content, err: err}
	})
}

func (a *application) openForm(kind string, action Action) {
	if a.model.busy && !a.model.cancelable {
		a.model.message = "写操作运行期间不能打开操作表单"
		return
	}
	form := formState{kind: kind, action: action, targets: a.model.actionTargets()}
	add := func(label, value string) {
		form.fields = append(form.fields, formField{label: label, value: []rune(value), position: len([]rune(value))})
	}
	switch kind {
	case "workspace-filter":
		form.title = "筛选工作区（空值表示全部）"
		add("文件名或目录", a.model.filter.path)
		add("状态（M/A/D/C 或英文）", a.model.filter.status)
		add("变更分组", a.model.filter.group)
	case "history-filter":
		form.title = "筛选历史"
		add("作者", a.model.historyRequest.Author)
		add("提交说明或路径", a.model.historyRequest.Search)
		add("开始日期 YYYY-MM-DD", a.model.historyRequest.Since)
		add("结束日期 YYYY-MM-DD", a.model.historyRequest.Until)
	case "blame", "compare":
		target := a.model.historyTarget
		if a.model.module == ModuleWorkspace {
			target = firstTarget(a.model.targets())
		}
		revision := "HEAD"
		if a.model.module == ModuleHistory {
			if row, ok := a.model.currentModuleRow(); ok {
				revision = strings.TrimPrefix(row.ID, "r")
			}
		}
		add("文件或目录", target)
		if kind == "blame" {
			form.title = "逐行追溯与源码"
			add("修订", revision)
		} else {
			form.title = "比较两个修订"
			add("起始修订", revision)
			add("结束修订", "HEAD")
		}
	default:
		form.title = action.DisplayName()
		switch action {
		case ActionMove:
			add("目标路径", "")
		case ActionProperty:
			add("属性名称", "")
			add("属性值（Enter 换行，Tab 下一项）", "")
			form.fields[1].multiline = true
		case ActionPropDelete:
			add("属性名称", "")
		case ActionMerge:
			form.title = "合并向导 · Ctrl-L 读取可选修订"
			source := ""
			if a.model.module == ModuleBranches || a.model.module == ModuleRepository {
				source = firstTarget(form.targets)
			}
			add("来源 URL", source)
			add("修订号或范围（可空）", "")
			add("反向合并（Space 切换）", "否")
			form.fields[2].toggle = true
		case ActionCopy:
			add("来源 URL", firstTarget(form.targets))
			add("目标 URL", "")
			add("修订号（必填）", "")
			add("提交说明", "")
		case ActionSwitch:
			add("目标 URL", firstTarget(form.targets))
		case ActionResolve:
			add("解决方式 working/mine-full/theirs-full/base", "working")
		case ActionIgnore:
			add("忽略模式", filepath.Base(firstTarget(form.targets)))
			add("目录", filepath.Dir(firstTarget(form.targets)))
		case ActionChangelist:
			add("分组名（--remove 移除）", "")
		case ActionPatch:
			add("补丁文件", "")
		case ActionLock:
			add("锁定说明", "")
		case ActionUpdate:
			add("修订（可空）", "")
			add("深度（可空：empty/files/immediates/infinity/exclude）", "")
			add("指定路径（留空使用所选项或整个工作副本）", "")
		}
	}
	a.model.form = form
	a.model.overlay = overlayForm
}

func firstTarget(targets []string) string {
	if len(targets) > 0 {
		return targets[0]
	}
	return ""
}

func (a *application) handleFormKey(pressed key) bool {
	f := &a.model.form
	if f.active < len(f.fields) && f.fields[f.active].multiline && (pressed.kind == keyUp || pressed.kind == keyDown) {
		field := &f.fields[f.active]
		editRunes(&field.value, &field.position, pressed, true)
		return false
	}
	switch pressed.kind {
	case keyEscape:
		a.model.overlay = overlayNone
		return false
	case keyTab, keyDown:
		f.active = (f.active + 1) % (len(f.fields) + 1)
		return false
	case keyBackTab, keyUp:
		f.active = (f.active + len(f.fields)) % (len(f.fields) + 1)
		return false
	case keyCtrlS:
		a.submitForm()
		return false
	case keyCtrlL:
		if f.action == ActionMerge {
			a.loadMergeRevisions()
		}
		return false
	case keyEnter:
		if f.active < len(f.fields) && f.fields[f.active].multiline {
			field := &f.fields[f.active]
			editRunes(&field.value, &field.position, pressed, true)
			return false
		}
		if f.active == len(f.fields) {
			a.submitForm()
		} else {
			f.active++
		}
		return false
	}
	if f.active < len(f.fields) {
		field := &f.fields[f.active]
		if field.toggle {
			if pressed.kind == keyRune && pressed.rune == ' ' {
				if string(field.value) == "是" {
					field.value = []rune("否")
				} else {
					field.value = []rune("是")
				}
			}
			return false
		}
		editRunes(&field.value, &field.position, pressed, field.multiline)
	}
	return false
}

func (a *application) submitForm() {
	if a.model.busy {
		a.model.message = "请等待或取消当前任务"
		return
	}
	f := a.model.form
	values := make([]string, len(f.fields))
	for i, field := range f.fields {
		values[i] = string(field.value)
	}
	switch f.kind {
	case "workspace-filter":
		a.model.filter = workspaceFilter{path: values[0], status: values[1], group: values[2]}
		a.model.applyWorkspaceFilter()
		a.model.overlay = overlayNone
		return
	case "history-filter":
		a.model.historyRequest = ModuleRequest{Limit: 100, Author: values[0], Search: values[1], Since: values[2], Until: values[3]}
		a.model.historyPages = nil
		a.model.overlay = overlayNone
		a.startModuleLoad(ModuleHistory)
		return
	case "blame", "compare":
		a.startHistoryInspection(f.kind, values)
		return
	}
	request := ActionRequest{Action: f.action, Module: a.model.module, WorkingCopy: a.options.WorkingCopy, Targets: slices.Clone(f.targets), Arguments: values}
	switch f.action {
	case ActionMove:
		if len(f.targets) != 1 {
			a.model.message = "移动操作需要选择一个源路径"
			return
		}
		if strings.TrimSpace(values[0]) == "" {
			a.model.message = "请填写目标路径"
			return
		}
	case ActionCopy:
		if strings.TrimSpace(values[2]) == "" {
			a.model.message = "复制分支或标签必须指定修订号"
			return
		}
	case ActionMerge:
		if strings.TrimSpace(values[0]) == "" {
			a.model.message = "请填写合并来源 URL"
			return
		}
		request.Arguments = []string{values[0]}
		if values[1] != "" {
			request.Arguments = append(request.Arguments, values[1])
		}
		if values[2] == "是" {
			if values[1] == "" {
				a.model.message = "反向合并必须指定修订号"
				return
			}
			request.Arguments = append(request.Arguments, "--reverse")
		}
	case ActionUpdate:
		request.Arguments = nil
		request.Revision = values[0]
		request.Depth = values[1]
		if len(values) > 2 && values[2] != "" {
			request.Targets = []string{values[2]}
		}
	}
	_, dangerous, _ := parseAction(string(f.action))
	a.prepareAction(request, dangerous, overlayForm)
}

func (a *application) contextActions() []contextAction {
	switch a.model.module {
	case ModuleWorkspace:
		targets := a.model.targets()
		allVersioned, allUnversioned, allConflicted, allPresent := len(targets) > 0, len(targets) > 0, len(targets) > 0, len(targets) > 0
		anyDirty, allLocked, noneLocked := false, len(targets) > 0, true
		// 只展示当前选择确实适用的动作，避免混合状态生成无效写请求。
		for _, target := range targets {
			index := slices.IndexFunc(a.model.allEntries, func(entry svn.StatusEntry) bool { return entry.Path == target })
			if index < 0 {
				allVersioned, allUnversioned, allConflicted, allPresent = false, false, false, false
				continue
			}
			entry := a.model.allEntries[index]
			allVersioned = allVersioned && !slices.Contains([]string{"unversioned", "ignored", "external", ""}, entry.Item)
			allUnversioned = allUnversioned && entry.Item == "unversioned"
			allConflicted = allConflicted && entry.IsConflict()
			allPresent = allPresent && entry.Item != "deleted" && entry.Item != "missing"
			anyDirty = anyDirty || entry.IsVersionedDirty()
			allLocked = allLocked && entry.LockOwner != ""
			noneLocked = noneLocked && entry.LockOwner == ""
		}
		var actions []contextAction
		if allVersioned && anyDirty {
			actions = append(actions, contextAction{"打开提交页", ActionCommit, ""}, contextAction{"还原", ActionRevert, ""})
		}
		if allVersioned && allPresent {
			if len(targets) == 1 && !allConflicted {
				actions = append(actions, contextAction{"移动/重命名", ActionMove, ""})
			}
			actions = append(actions, contextAction{"设置属性", ActionProperty, ""}, contextAction{"查看属性", ActionProperties, ""}, contextAction{"删除属性", ActionPropDelete, ""}, contextAction{"设置变更分组", ActionChangelist, ""}, contextAction{"删除", ActionDelete, ""})
			if noneLocked {
				actions = append(actions, contextAction{"加锁", ActionLock, ""})
			}
			if allLocked {
				actions = append(actions, contextAction{"解锁", ActionUnlock, ""})
			}
		}
		if allUnversioned {
			actions = append(actions, contextAction{"添加", ActionAdd, ""})
			if len(targets) == 1 {
				actions = append(actions, contextAction{"忽略", ActionIgnore, ""})
			}
		}
		if allConflicted {
			actions = append(actions, contextAction{"标记解决", ActionResolve, ""})
		}
		return append(actions, contextAction{"更新预览", ActionUpdate, ""}, contextAction{"查看外部依赖", ActionExternals, ""}, contextAction{"补丁预览", ActionPatch, ""})
	case ModuleConflicts:
		if _, ok := a.model.currentModuleRow(); !ok {
			return nil
		}
		return []contextAction{{"编辑冲突文件", "", "edit"}, {"调用合并工具", "", "merge-tool"}, {"标记解决", ActionResolve, ""}, {"查看属性", ActionProperties, ""}}
	case ModuleBranches, ModuleRepository:
		if _, ok := a.model.currentModuleRow(); !ok {
			return nil
		}
		return []contextAction{{"合并向导", ActionMerge, ""}, {"复制分支/标签", ActionCopy, ""}, {"切换分支", ActionSwitch, ""}, {"查看属性", ActionProperties, ""}}
	default:
		return nil
	}
}

func (a *application) handleContextKey(pressed key) bool {
	items := a.contextActions()
	if len(items) == 0 {
		a.model.overlay = overlayNone
		return false
	}
	switch pressed.kind {
	case keyEscape:
		a.model.overlay = overlayNone
	case keyUp:
		a.model.contextCursor = max(a.model.contextCursor-1, 0)
	case keyDown:
		a.model.contextCursor = min(a.model.contextCursor+1, len(items)-1)
	case keyEnter:
		item := items[min(a.model.contextCursor, len(items)-1)]
		if item.external != "" {
			a.runConflictExternal(item.external)
			return false
		}
		if item.action == ActionCommit {
			a.openCommit()
			return false
		}
		switch item.action {
		case ActionAdd, ActionDelete, ActionRevert, ActionUnlock, ActionProperties, ActionExternals:
			_, dangerous, _ := parseAction(string(item.action))
			a.prepareAction(ActionRequest{Action: item.action, Module: a.model.module, WorkingCopy: a.options.WorkingCopy, Targets: a.model.actionTargets()}, dangerous, overlayContext)
		default:
			a.openForm("action", item.action)
		}
	}
	return false
}

func scrollOffset(offset int, pressed key, limit, page int) int {
	switch {
	case pressed.kind == keyHome:
		offset = 0
	case pressed.kind == keyEnd:
		offset = limit
	case pressed.kind == keyUp || pressed.kind == keyRune && pressed.rune == 'k':
		offset--
	case pressed.kind == keyDown || pressed.kind == keyRune && pressed.rune == 'j':
		offset++
	case pressed.kind == keyPageUp:
		offset -= page
	case pressed.kind == keyPageDown:
		offset += page
	}
	return min(max(offset, 0), limit)
}

func (a *application) handleDocumentKey(pressed key) bool {
	content := a.model.detailContent
	if a.model.overlay == overlayHelp {
		content = helpText
	}
	lines := wrapText(sanitizeText(content), max(a.model.width-2, 1))
	limit := max(len(lines)-max(a.model.height-5, 1), 0)
	if a.model.detailSearching {
		switch pressed.kind {
		case keyEscape:
			a.model.detailSearching = false
		case keyEnter:
			a.model.detailSearching = false
			a.findDetail(lines, false)
		case keyBackspace:
			if len(a.model.detailSearch) > 0 {
				a.model.detailSearch = a.model.detailSearch[:len(a.model.detailSearch)-1]
			}
		case keyRune:
			a.model.detailSearch = append(a.model.detailSearch, pressed.rune)
		case keyPaste:
			a.model.detailSearch = append(a.model.detailSearch, []rune(strings.ReplaceAll(pressed.text, "\n", " "))...)
		}
		return false
	}
	if pressed.kind == keyRune {
		switch pressed.rune {
		case 'c':
			a.cancelReadTask()
			return false
		case '/':
			a.model.detailSearching = true
			a.model.detailSearch = nil
			return false
		case 'n':
			a.findDetail(lines, false)
			return false
		case 'N':
			a.findDetail(lines, true)
			return false
		case '[', ']':
			delta := 1
			if pressed.rune == '[' {
				delta = -1
			}
			for i := a.model.overlayOffset + delta; i >= 0 && i < len(lines); i += delta {
				if strings.HasPrefix(lines[i], "@@") {
					a.model.overlayOffset = min(i, limit)
					break
				}
			}
			return false
		case 'a':
			if a.model.mergePreview != nil {
				request := *a.model.mergePreview
				request.Arguments = append(slices.Clone(request.Arguments), "--apply")
				a.prepareAction(request, request.Action == ActionMerge, overlayDetail)
			}
			return false
		case 'd':
			if a.model.module == ModuleWorkspace {
				a.model.overlay = overlayNone
				a.startDiff()
			}
			return false
		case 'C':
			a.openCommit()
			return false
		case 'q':
			if a.model.busy && !a.model.cancelable {
				a.model.message = "写操作运行中，不能退出"
				return false
			}
			return true
		}
	}
	a.model.overlayOffset = scrollOffset(a.model.overlayOffset, pressed, limit, max(a.model.height-6, 1))
	return false
}

func (a *application) findDetail(lines []string, reverse bool) {
	query := strings.ToLower(string(a.model.detailSearch))
	if query == "" {
		return
	}
	step := 1
	if reverse {
		step = -1
	}
	for n := 1; n <= len(lines); n++ {
		i := (a.model.overlayOffset + step*n + len(lines)) % len(lines)
		if strings.Contains(strings.ToLower(lines[i]), query) {
			a.model.overlayOffset = min(i, max(len(lines)-max(a.model.height-5, 1), 0))
			a.model.message = "搜索：" + string(a.model.detailSearch)
			return
		}
	}
	a.model.message = "没有匹配内容"
}

func (a *application) historyPage(next bool) {
	if a.model.busy {
		return
	}
	if next {
		data := a.model.moduleData[ModuleHistory]
		if !data.HasMore {
			a.model.message = "已到历史末页"
			return
		}
		a.model.historyPages = append(a.model.historyPages, a.model.historyRequest.BeforeRevision)
		a.model.historyRequest.BeforeRevision = data.NextRevision
	} else {
		if len(a.model.historyPages) == 0 {
			return
		}
		last := len(a.model.historyPages) - 1
		a.model.historyRequest.BeforeRevision = a.model.historyPages[last]
		a.model.historyPages = a.model.historyPages[:last]
	}
	a.model.moduleCursor[ModuleHistory] = 0
	a.startModuleLoad(ModuleHistory)
}

func (a *application) switchProject(path string) {
	if a.model.busy && !a.model.cancelable {
		a.model.message = "写操作运行期间不能切换项目"
		return
	}
	if a.model.busy {
		a.cancelReadTask()
	}
	switcher, ok := a.options.Actions.(ProjectSwitcher)
	if !ok {
		switcher, ok = a.options.ModuleLoader.(ProjectSwitcher)
	}
	if !ok {
		a.model.message = "当前服务不支持切换项目"
		return
	}
	a.startTask(taskProject, ModuleProjects, "切换项目", false, func(ctx context.Context) taskResultEvent {
		wc, err := switcher.SwitchProject(ctx, path)
		return taskResultEvent{text: wc, err: err}
	})
}

func (a *application) runConflictExternal(kind string) {
	if a.model.busy {
		a.model.message = "请先等待或取消当前任务"
		return
	}
	target := firstTarget(a.model.actionTargets())
	if target == "" {
		a.model.message = "请选择冲突文件"
		return
	}
	err := a.withExternal(ExternalRequest{Kind: kind, WorkingCopy: a.options.WorkingCopy, Target: target})
	a.model.overlay = overlayNone
	if err != nil {
		a.showDetail("外部工具失败", err.Error())
	}
	a.refreshAfterExternal = true
	a.startStatus()
}

func (a *application) editCommitExternally() {
	if a.model.busy {
		a.model.message = "请先等待或取消当前任务"
		return
	}
	a.saveDraft()
	file, err := os.CreateTemp("", "svntui-commit-*.txt")
	if err != nil {
		a.model.message = err.Error()
		return
	}
	path := file.Name()
	defer os.Remove(path)
	_, writeErr := file.WriteString(string(a.model.commit.message))
	err = errors.Join(writeErr, file.Close())
	if err == nil {
		err = a.withExternal(ExternalRequest{Kind: "editor", WorkingCopy: a.options.WorkingCopy, Target: path})
	}
	// 即使编辑器失败也保留已写入的草稿，并且始终恢复界面。
	content, readErr := os.ReadFile(path)
	if readErr == nil {
		a.model.commit.message = []rune(string(content))
		a.model.commit.position = len(a.model.commit.message)
		a.saveDraft()
	}
	if err = errors.Join(err, readErr); err != nil {
		a.model.message = "外部编辑失败：" + err.Error()
	} else {
		a.model.message = "提交草稿已更新"
	}
}

func (a *application) withExternal(request ExternalRequest) (err error) {
	if a.model.busy {
		return errors.New("请先等待或取消当前任务")
	}
	term, ok := a.terminal.(terminalSuspender)
	if !ok {
		return errors.New("当前终端不支持外部工具挂起")
	}
	// 先停止输入读取，再恢复终端；返回时按相反顺序恢复。
	if a.inputCancel != nil {
		a.inputCancel()
		interruptTerminalRead(a.terminal)
		if a.inputDone != nil {
			<-a.inputDone
		}
	}
	defer func() {
		err = errors.Join(err, term.Resume())
		if a.ctx != nil && a.events != nil {
			a.startInput()
		}
	}()
	if err = term.Suspend(); err != nil {
		return err
	}
	if a.options.RunExternal != nil {
		return a.options.RunExternal(a.ctx, request)
	}
	if request.Kind != "editor" {
		return errors.New("未配置外部冲突工具")
	}
	editor := cmp.Or(a.options.Editor, os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		return errors.New("请先配置 editor")
	}
	command := exec.CommandContext(a.ctx, editor, request.Target)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("运行编辑器: %w", err)
	}
	return nil
}
