package tui

import (
	"slices"
	"strings"
	"testing"

	"svntui/internal/svn"
)

func TestRendererShowsWorkspaceAndSanitizesData(t *testing.T) {
	model := newModel("/tmp/wc")
	model.setSize(100, 16)
	model.replaceEntries([]svn.StatusEntry{
		{Path: "safe\x1b[31m.go", Item: "conflicted", TreeConflicted: true},
	})
	output := (renderer{color: false}).render(model)
	if strings.Contains(output, "\x1b[31m.go") {
		t.Fatal("repository data preserved an ANSI escape sequence")
	}
	for _, expected := range []string{"工作区", "safe.go", "树冲突"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("rendered output does not contain %q", expected)
		}
	}
}

func TestRendererShowsInjectedModuleData(t *testing.T) {
	model := newModel("/tmp/wc")
	model.setSize(100, 16)
	model.setModule(ModuleHistory)
	model.replaceModuleData(ModuleHistory, ModuleData{
		Title:   "最近修订",
		Columns: []string{"Revision", "Author"},
		Rows:    []ModuleRow{{ID: "42", Cells: []string{"r42", "alice"}}},
	})
	output := (renderer{color: false}).render(model)
	for _, expected := range []string{"最近修订", "Revision", "r42", "alice"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("rendered output does not contain %q", expected)
		}
	}
}

func TestRenderModuleDataAlignsChineseColumnsAndTruncatesLongCells(t *testing.T) {
	model := newModel("/tmp/wc")
	model.setModule(ModuleAutomations)
	data := ModuleData{
		Title:   "自动更新任务",
		Columns: []string{"名称", "状态", "工作副本"},
		Rows: []ModuleRow{
			{Cells: []string{"中文任务", "scheduled", "/very/long/working-copy/path/with/source"}},
			{Cells: []string{"api\n服务", "失败", "/short"}},
		},
	}
	const width = 38
	rows := (renderer{color: false}).renderModuleData(model, data, width, 6)
	if len(rows) != 6 {
		t.Fatalf("渲染行数 = %d，期望 6", len(rows))
	}
	wantSeparators := separatorColumns(rows[1])
	if len(wantSeparators) != 2 {
		t.Fatalf("表头分隔符位置 = %v", wantSeparators)
	}
	for _, row := range rows[2:4] {
		if got := separatorColumns(row); !slices.Equal(got, wantSeparators) {
			t.Fatalf("列未对齐：表头 %v，数据行 %v\n%s", wantSeparators, got, row)
		}
		if got := displayWidth(stripANSI(row)); got != width {
			t.Fatalf("数据行显示宽度 = %d，期望 %d", got, width)
		}
	}
	if !strings.Contains(rows[2], "…") {
		t.Fatalf("超长单元格未截断：%s", rows[2])
	}
	if !strings.Contains(rows[3], "api 服务") {
		t.Fatalf("单元格换行未被安全折叠：%s", rows[3])
	}
}

func TestFitTableCellUsesTerminalDisplayWidth(t *testing.T) {
	if got := fitTableCell("中文", 5); got != "中文 " || displayWidth(got) != 5 {
		t.Fatalf("中文单元格 = %q，显示宽度 %d", got, displayWidth(got))
	}
	if got := fitTableCell("abcdef", 4); got != "abc…" || displayWidth(got) != 4 {
		t.Fatalf("截断单元格 = %q，显示宽度 %d", got, displayWidth(got))
	}
}

func TestRendererShowsSearchableChineseCommandPalette(t *testing.T) {
	model := newModel("/tmp/wc")
	model.setSize(100, 18)
	model.overlay = overlayCommand
	model.command = []rune("提交")
	output := (renderer{color: false}).render(model)
	for _, expected := range []string{"命令面板", "commit <提交说明>", "提交临时选择集中的文件", "搜索：提交"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("命令面板缺少 %q\n%s", expected, output)
		}
	}
}

func separatorColumns(value string) []int {
	var columns []int
	width := 0
	for _, decoded := range stripANSI(value) {
		if decoded == '│' {
			columns = append(columns, width)
		}
		width += displayRuneWidth(decoded)
	}
	return columns
}
