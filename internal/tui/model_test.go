package tui

import (
	"reflect"
	"testing"

	"svntui/internal/svn"
)

func TestModelWorkspaceSelectionSurvivesRefresh(t *testing.T) {
	model := newModel("/wc")
	model.replaceEntries([]svn.StatusEntry{
		{Path: "z.go", Item: "modified"},
		{Path: "a.go", Item: "added"},
	})
	if model.entries[0].Path != "a.go" {
		t.Fatalf("entries were not sorted: %#v", model.entries)
	}
	model.toggleSelection()
	model.setCursor(1)
	model.toggleSelection()
	if got := model.targets(); !reflect.DeepEqual(got, []string{"a.go", "z.go"}) {
		t.Fatalf("targets() = %#v", got)
	}

	model.replaceEntries([]svn.StatusEntry{{Path: "z.go", Item: "modified"}})
	if got := model.targets(); !reflect.DeepEqual(got, []string{"z.go"}) {
		t.Fatalf("stale selection was not removed: %#v", got)
	}
}

func TestModelModuleRowsProvideActionTarget(t *testing.T) {
	model := newModel("/wc")
	model.setModule(ModuleBranches)
	data := ModuleData{
		Columns: []string{"名称", "URL"},
		Rows: []ModuleRow{
			{ID: "branches/main", Cells: []string{"main", "svn://example/main"}},
			{ID: "branches/dev", Cells: []string{"dev", "svn://example/dev"}},
		},
	}
	model.replaceModuleData(ModuleBranches, data)
	model.moveCursor(1)
	if got := model.actionTargets(); !reflect.DeepEqual(got, []string{"branches/dev"}) {
		t.Fatalf("actionTargets() = %#v", got)
	}

	data.Rows[1].Cells[0] = "changed outside"
	row, ok := model.currentModuleRow()
	if !ok || row.Cells[0] != "dev" {
		t.Fatalf("module data was not defensively cloned: %#v", row)
	}
}
