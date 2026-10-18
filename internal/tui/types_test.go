package tui

import "testing"

func TestUserFacingNamesAreChinese(t *testing.T) {
	if got := ModuleWorkspace.DisplayName(); got != "工作区" {
		t.Fatalf("工作区显示名称 = %q", got)
	}
	if got := ActionMerge.DisplayName(); got != "合并" {
		t.Fatalf("合并操作显示名称 = %q", got)
	}
	if got := Action("invalid").DisplayName(); got != "未知操作" {
		t.Fatalf("未知操作显示名称 = %q", got)
	}
}
