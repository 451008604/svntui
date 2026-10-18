package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"svntui/internal/domain"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

func TestLiteralArgumentsRemainReadOnly(t *testing.T) {
	for _, action := range []domain.Action{domain.ActionPatch, domain.ActionMerge} {
		request := domain.ActionRequest{Action: action, Arguments: []string{"--apply"}}
		if !request.IsReadOnly() {
			t.Fatalf("literal operand enabled mutation: %+v", request)
		}
		request.Arguments = append(request.Arguments, "--apply")
		if request.IsReadOnly() {
			t.Fatalf("explicit apply ignored: %+v", request)
		}
	}
}

func TestCLIReviewLiteralValuesAndRelativeHistory(t *testing.T) {
	wc, _ := cliFixture(t)
	cliOutput(t, "property", "set", "--path", wc, "--yes", "test:literal", "--json", "untouched.txt", "second file.txt")
	for _, name := range []string{"untouched.txt", "second file.txt"} {
		if got := cliOutput(t, "property", "get", "--path", wc, "test:literal", name); got != "--json" {
			t.Fatalf("property literal changed: %q", got)
		}
	}
	var history []svn.LogEntry
	if err := json.Unmarshal([]byte(cliOutput(t, "history", "log", "--path", wc, "untouched.txt")), &history); err != nil || len(history) != 1 {
		t.Fatalf("relative history: %+v %v", history, err)
	}
	var lines []svn.BlameLine
	if err := json.Unmarshal([]byte(cliOutput(t, "history", "blame", "--path", wc, "untouched.txt")), &lines); err != nil || len(lines) != 1 || lines[0].Content != "base" {
		t.Fatalf("relative blame: %+v %v", lines, err)
	}
	// 与选项同名的补丁只能作为文件读取，预演不得写入。
	t.Chdir(t.TempDir())
	writeCLIFile(t, ".", "--apply", "Index: untouched.txt\n===================================================================\n--- untouched.txt\t(revision 1)\n+++ untouched.txt\t(working copy)\n@@ -1 +1 @@\n-base\n+changed\n")
	cliOutput(t, "patch", "apply", "--path", wc, "--yes", "--", "--apply")
	if got := readCLIFile(t, wc, "untouched.txt"); got != "base\n" {
		t.Fatalf("preview wrote file: %q", got)
	}
}

func TestConflictToolSharesRootLock(t *testing.T) {
	wc, _ := cliFixture(t)
	runCommand(t, wc, "svn", "mkdir", "sub")
	writeCLIFile(t, filepath.Join(wc, "sub"), "file.txt", "base\n")
	env, err := newEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := wclock.Acquire(filepath.Join(env.dirs.State, "working-copy-locks"), wc, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	var output bytes.Buffer
	err = runConflict(t.Context(), env, []string{"edit", "--path", filepath.Join(wc, "sub"), "file.txt"}, &output, &output)
	if !errors.Is(err, wclock.ErrBusy) {
		t.Fatalf("subdirectory external tool bypassed root lock: %v", err)
	}
}
