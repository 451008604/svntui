package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type revisionSelection struct {
	source                     string
	reverse                    bool
	rows                       []int64
	selected                   map[int64]bool
	cursor                     int
	eligibleCount, mergedCount int
}

func (a *application) loadMergeRevisions() {
	if a.model.busy {
		a.model.message = "请等待或按 Ctrl-C 取消读取任务"
		return
	}
	source := strings.TrimSpace(string(a.model.form.fields[0].value))
	if source == "" {
		a.model.message = "请先填写合并来源 URL"
		return
	}
	loader, ok := a.options.ModuleLoader.(MergeInfoLoader)
	if !ok {
		loader, ok = a.options.Actions.(MergeInfoLoader)
	}
	if !ok {
		a.model.message = "当前服务不支持修订列表；可直接填写修订号"
		return
	}
	wc := a.options.WorkingCopy
	a.startTask(taskMergeInfo, a.model.module, "读取可合并修订", true, func(ctx context.Context) taskResultEvent {
		data, err := loader.LoadMergeInfo(ctx, wc, source)
		return taskResultEvent{mergeInfo: data, mergeSource: source, err: err}
	})
}

func (a *application) acceptMergeInfo(source string, data MergeInfoData) {
	// 来源已改变时丢弃旧查询，避免将修订带到另一条分支。
	if a.model.form.action != ActionMerge || source != strings.TrimSpace(string(a.model.form.fields[0].value)) {
		a.model.message = "来源已改变，请重新读取修订列表"
		return
	}
	reverse := string(a.model.form.fields[2].value) == "是"
	rows := data.Eligible
	if reverse {
		rows = data.Merged
	}
	selected := make(map[int64]bool)
	for item := range strings.SplitSeq(string(a.model.form.fields[1].value), ",") {
		if number, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64); err == nil && slices.Contains(rows, number) {
			selected[number] = true
		}
	}
	rows = slices.Clone(rows)
	slices.Sort(rows)
	slices.Reverse(rows)
	rows = slices.Compact(rows)
	a.model.revisions = revisionSelection{source: source, reverse: reverse, rows: rows, selected: selected, eligibleCount: len(data.Eligible), mergedCount: len(data.Merged)}
	a.model.overlay = overlayRevisions
	a.model.message = "Space 勾选修订 · A 全选 · X 清空 · Enter 使用选择 · Esc 返回"
}

func (a *application) handleRevisionKey(pressed key) bool {
	selection := &a.model.revisions
	switch pressed.kind {
	case keyEscape:
		a.model.overlay = overlayForm
		return false
	case keyUp:
		selection.cursor = max(selection.cursor-1, 0)
	case keyDown:
		selection.cursor = min(selection.cursor+1, max(len(selection.rows)-1, 0))
	case keyHome:
		selection.cursor = 0
	case keyEnd:
		selection.cursor = max(len(selection.rows)-1, 0)
	case keyPageUp:
		selection.cursor = max(selection.cursor-max(a.model.height-8, 1), 0)
	case keyPageDown:
		selection.cursor = min(selection.cursor+max(a.model.height-8, 1), max(len(selection.rows)-1, 0))
	case keyEnter:
		if len(selection.selected) == 0 {
			a.model.message = "请选择修订，或 Esc 返回手动输入"
			return false
		}
		numbers := make([]int64, 0, len(selection.selected))
		for number := range selection.selected {
			numbers = append(numbers, number)
		}
		slices.Sort(numbers)
		values := make([]string, len(numbers))
		for i, number := range numbers {
			values[i] = strconv.FormatInt(number, 10)
		}
		field := &a.model.form.fields[1]
		field.value = []rune(strings.Join(values, ","))
		field.position = len(field.value)
		a.model.overlay = overlayForm
		a.model.form.active = len(a.model.form.fields)
		a.model.message = "修订已选择；Ctrl-S 预演，预演成功后显式应用"
	case keyRune:
		switch pressed.rune {
		case ' ':
			if len(selection.rows) > 0 {
				revision := selection.rows[selection.cursor]
				if selection.selected[revision] {
					delete(selection.selected, revision)
				} else {
					selection.selected[revision] = true
				}
			}
		case 'A':
			for _, revision := range selection.rows {
				selection.selected[revision] = true
			}
		case 'X':
			clear(selection.selected)
		}
	}
	return false
}

func (r renderer) renderRevisions(m model, width, height int) []string {
	selection := m.revisions
	title := "可合并修订"
	if selection.reverse {
		title = "可反向合并的已合并修订"
	}
	rows := []string{padLine(" "+title, width), padLine(fmt.Sprintf(" 可合并 %d · 已合并 %d · 已选 %d", selection.eligibleCount, selection.mergedCount, len(selection.selected)), width)}
	if len(selection.rows) == 0 {
		rows = append(rows, padLine(" 无可选修订；Esc 返回检查来源或反向选项。", width))
	}
	available := max(height-len(rows), 0)
	start := max(selection.cursor-available+1, 0)
	for i := start; i < len(selection.rows) && len(rows) < height; i++ {
		cursor, checked := " ", " "
		if i == selection.cursor {
			cursor = ">"
		}
		if selection.selected[selection.rows[i]] {
			checked = "x"
		}
		rows = append(rows, padLine(fmt.Sprintf("%s [%s] r%d", cursor, checked, selection.rows[i]), width))
	}
	return fillRows(rows, width, height)
}

func (a *application) startHistoryInspection(kind string, values []string) {
	loader, ok := a.options.ModuleLoader.(HistoryInspector)
	if !ok {
		loader, ok = a.options.Actions.(HistoryInspector)
	}
	if !ok {
		a.model.message = "当前服务不支持历史源码和修订比较"
		return
	}
	if kind == "blame" && strings.TrimSpace(values[0]) == "" {
		a.model.message = "逐行追溯需要指定文件"
		return
	}
	if strings.TrimSpace(values[1]) == "" || kind == "compare" && strings.TrimSpace(values[2]) == "" {
		a.model.message = "请填写修订号或 HEAD"
		return
	}
	wc := a.options.WorkingCopy
	target := values[0]
	if target == "" {
		target = "."
	}
	a.model.overlay = overlayNone
	title := "逐行追溯与源码"
	if kind == "compare" {
		title = "修订比较：" + values[1] + " → " + values[2]
	}
	a.startTask(taskHistoryInspect, a.model.module, title, true, func(ctx context.Context) taskResultEvent {
		var text string
		var err error
		if kind == "blame" {
			text, err = loader.LoadBlame(ctx, wc, target, values[1])
		} else {
			text, err = loader.CompareRevisions(ctx, wc, target, values[1], values[2])
		}
		return taskResultEvent{text: text, title: title, err: err}
	})
}
