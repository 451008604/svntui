package tui

import (
	"reflect"
	"strings"
	"testing"
)

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name string
		line string
		want []string
	}{
		{name: "空命令", line: "   ", want: nil},
		{name: "普通参数", line: "module history", want: []string{"module", "history"}},
		{name: "引号和转义", line: `commit "fix issue 42" path\ with\ spaces`, want: []string{"commit", "fix issue 42", "path with spaces"}},
		{name: "空参数", line: `property "" value`, want: []string{"property", "", "value"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := splitCommand(test.line)
			if err != nil {
				t.Fatalf("splitCommand() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("splitCommand() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSplitCommandRejectsIncompleteInput(t *testing.T) {
	for _, line := range []string{`commit "unfinished`, `update trailing\`} {
		if _, err := splitCommand(line); err == nil {
			t.Fatalf("splitCommand(%q) should fail", line)
		}
	}
}

func TestParsePaletteCommand(t *testing.T) {
	command, err := parsePaletteCommand(`merge -c 123`)
	if err != nil {
		t.Fatal(err)
	}
	if command.kind != commandAction || command.action != ActionMerge || !command.dangerous {
		t.Fatalf("unexpected command: %#v", command)
	}
	if !reflect.DeepEqual(command.arguments, []string{"-c", "123"}) {
		t.Fatalf("arguments = %#v", command.arguments)
	}

	module, err := parsePaletteCommand("module safepoints")
	if err != nil {
		t.Fatal(err)
	}
	if module.kind != commandModule || module.module != ModuleSafePoints {
		t.Fatalf("unexpected module command: %#v", module)
	}
}

func TestParsePaletteCommandAcceptsChineseModuleName(t *testing.T) {
	command, err := parsePaletteCommand("module 安全快照")
	if err != nil {
		t.Fatal(err)
	}
	if command.kind != commandModule || command.module != ModuleSafePoints {
		t.Fatalf("中文模块命令解析结果不正确：%#v", command)
	}
}

func TestFilteredPaletteEntriesMatchCommandAndChineseDescription(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{query: "merge", want: "merge <参数>"},
		{query: "提交", want: "commit <提交说明>"},
		{query: "修订", want: "diff"},
	}
	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			entries := filteredPaletteEntries(test.query)
			if !containsPaletteCommand(entries, test.want) {
				t.Fatalf("筛选 %q 的结果为 %#v，缺少 %q", test.query, entries, test.want)
			}
		})
	}
	for _, entry := range paletteEntries {
		if strings.TrimSpace(entry.Description) == "" {
			t.Fatalf("命令 %q 缺少中文说明", entry.Command)
		}
	}
}

func containsPaletteCommand(entries []paletteEntry, command string) bool {
	for _, entry := range entries {
		if entry.Command == command {
			return true
		}
	}
	return false
}
