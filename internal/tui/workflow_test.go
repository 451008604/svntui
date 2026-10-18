package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"svntui/internal/svn"
)

type normalizedActions struct {
	fakeActions
	normalize     func(ActionRequest) (ActionRequest, error)
	switchProject func(context.Context, string) (string, error)
}

func (f normalizedActions) NormalizeAction(r ActionRequest) (ActionRequest, error) {
	if f.normalize != nil {
		return f.normalize(r)
	}
	return r, nil
}
func (f normalizedActions) SwitchProject(ctx context.Context, path string) (string, error) {
	return f.switchProject(ctx, path)
}

func testApp(t *testing.T, actions Actions) *application {
	t.Helper()
	return &application{ctx: t.Context(), options: Options{WorkingCopy: "/wc", Actions: actions}, model: newModel("/wc"), events: make(chan any, 32)}
}
func baseActions() fakeActions {
	return fakeActions{status: func(context.Context, string) ([]svn.StatusEntry, error) {
		return []svn.StatusEntry{{Path: "file.go", Item: "modified"}}, nil
	}, diff: func(context.Context, string, []string) (string, error) { return "@@ -1 +1 @@\n-old\n+new", nil }, exec: func(context.Context, ActionRequest) (ActionResult, error) { return ActionResult{}, nil }}
}
func nextTask(t *testing.T, a *application) taskResultEvent {
	t.Helper()
	select {
	case event := <-a.events:
		result, ok := event.(taskResultEvent)
		if !ok {
			t.Fatalf("unexpected event %T", event)
		}
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("task did not finish")
		return taskResultEvent{}
	}
}

func TestNormalizeBeforeConfirmationAndReadCancellation(t *testing.T) {
	calls := make(chan ActionRequest, 2)
	actions := normalizedActions{fakeActions: baseActions(), normalize: func(r ActionRequest) (ActionRequest, error) {
		r.Targets = []string{"canonical/a", "canonical/b"}
		return r, nil
	}}
	actions.exec = func(ctx context.Context, r ActionRequest) (ActionResult, error) {
		calls <- r
		if r.IsReadOnly() {
			<-ctx.Done()
			return ActionResult{}, ctx.Err()
		}
		return ActionResult{}, nil
	}
	a := testApp(t, actions)
	a.prepareAction(ActionRequest{Action: ActionMove, Targets: []string{"a"}, Arguments: []string{"b"}}, false, overlayForm)
	if got := a.model.confirm.request.Targets; !slices.Equal(got, []string{"canonical/a", "canonical/b"}) {
		t.Fatalf("confirmation uses unnormalized targets: %v", got)
	}
	if !strings.Contains(confirmationContent(a.model.confirm), "目标数量：2") {
		t.Fatal("target count missing")
	}
	a.handleConfirmKey(key{kind: keyEscape})
	if a.model.overlay != overlayForm {
		t.Fatal("cancel did not return to form")
	}
	a.prepareAction(ActionRequest{Action: ActionProperties}, false, overlayNone)
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("read-only action requested confirmation")
	}
	if !a.model.cancelable || a.model.overlay == overlayConfirm {
		t.Fatal("read-only action not cancellable")
	}
	id := a.model.taskID
	a.cancelReadTask()
	event := nextTask(t, a)
	a.handleTaskResult(event)
	a.wait.Wait()
	if a.model.busy || a.model.taskID == id {
		t.Fatal("cancel did not invalidate task")
	}
}

func TestCommitDraftRetainedAcrossCancelAndFailedCommit(t *testing.T) {
	actions := baseActions()
	actions.exec = func(context.Context, ActionRequest) (ActionResult, error) {
		return ActionResult{}, errors.New("commit failed")
	}
	a := testApp(t, actions)
	a.model.replaceEntries([]svn.StatusEntry{{Path: "file.go", Item: "modified"}})
	a.openCommit()
	a.handleCommitKey(key{kind: keyRune, rune: ' '})
	a.handleCommitKey(key{kind: keyTab})
	a.handleCommitKey(key{kind: keyPaste, text: "title\n\nbody"})
	a.handleCommitKey(key{kind: keyEscape})
	a.openCommit()
	if string(a.model.commit.message) != "title\n\nbody" {
		t.Fatal("cancel lost draft")
	}
	a.submitCommit()
	if a.model.overlay != overlayConfirm {
		t.Fatal("commit did not confirm")
	}
	if got := a.model.confirm.request.Arguments; !reflect.DeepEqual(got, []string{"title\n\nbody"}) {
		t.Fatalf("multiline message changed: %q", got)
	}
	a.handleConfirmKey(key{kind: keyEscape})
	if a.model.overlay != overlayCommit {
		t.Fatal("confirmation cancel lost commit page")
	}
	a.submitCommit()
	a.handleConfirmKey(key{kind: keyRune, rune: 'y'})
	a.handleTaskResult(nextTask(t, a))
	a.handleTaskResult(nextTask(t, a))
	a.wait.Wait()
	if a.model.drafts["/wc"] != "title\n\nbody" || a.model.detailReturn != overlayCommit {
		t.Fatal("failed commit lost draft or return page")
	}
	a.handleKey(key{kind: keyEscape})
	if a.model.overlay != overlayCommit {
		t.Fatal("failure cannot return to draft")
	}
}

func TestCommitSuccessClearsOnlyCurrentProjectDraft(t *testing.T) {
	a := testApp(t, baseActions())
	a.model.drafts["/wc"] = "one"
	a.model.drafts["/other"] = "two"
	id := a.model.beginTask("commit", false)
	a.handleTaskResult(taskResultEvent{id: id, kind: taskAction, request: ActionRequest{Action: ActionCommit}})
	if _, ok := a.model.drafts["/wc"]; ok || a.model.drafts["/other"] != "two" {
		t.Fatal("draft clear crossed project boundary")
	}
}

func TestWorkspaceFiltersAndVisibleSelection(t *testing.T) {
	m := newModel("/wc")
	m.replaceEntries([]svn.StatusEntry{{Path: "src/a.go", Item: "modified", Changelist: "backend"}, {Path: "src/b.go", Item: "added", Changelist: "backend"}, {Path: "doc/a.md", Item: "modified", Changelist: "docs"}})
	m.filter = workspaceFilter{path: "src/", status: "M", group: "backend"}
	m.applyWorkspaceFilter()
	m.selectVisible(false)
	if len(m.entries) != 1 || !slices.Equal(m.selectedTargets(), []string{"src/a.go"}) {
		t.Fatalf("bad filter or select: %+v %v", m.entries, m.selectedTargets())
	}
	m.filter = workspaceFilter{}
	m.applyWorkspaceFilter()
	m.selectVisible(true)
	if !slices.Equal(m.selectedTargets(), []string{"doc/a.md", "src/b.go"}) {
		t.Fatalf("bad inversion: %v", m.selectedTargets())
	}
	m.filter = workspaceFilter{path: "no-match"}
	m.applyWorkspaceFilter()
	m.replaceEntries([]svn.StatusEntry{{Path: "src/b.go", Item: "added"}})
	if len(m.entries) != 0 || !slices.Equal(m.selectedTargets(), []string{"src/b.go"}) {
		t.Fatal("refresh lost filter or kept nonexistent selection")
	}
}

func TestStructuredMoveCopyAndMergeWizard(t *testing.T) {
	requests := make(chan ActionRequest, 4)
	actions := baseActions()
	actions.exec = func(_ context.Context, r ActionRequest) (ActionResult, error) {
		requests <- r
		return ActionResult{Output: "eligible r42\nmerged r41"}, nil
	}
	a := testApp(t, normalizedActions{fakeActions: actions})
	a.model.replaceEntries([]svn.StatusEntry{{Path: "old.go", Item: "modified"}})
	a.openForm("action", ActionMove)
	a.model.form.fields[0].value = []rune("new.go")
	a.submitForm()
	if !slices.Equal(a.model.confirm.request.Targets, []string{"old.go"}) || !slices.Equal(a.model.confirm.request.Arguments, []string{"new.go"}) {
		t.Fatal("single-source move form is invalid")
	}
	a.model.overlay = overlayNone
	a.openForm("action", ActionCopy)
	a.submitForm()
	if a.model.overlay != overlayForm || !strings.Contains(a.model.message, "修订号") {
		t.Fatal("copy accepted missing revision")
	}
	a.openForm("action", ActionMerge)
	a.model.form.fields[0].value = []rune("svn://host/branch")
	a.model.form.fields[1].value = []rune("42")
	a.model.form.fields[2].value = []rune("是")
	a.submitForm()
	preview := <-requests
	if !preview.IsReadOnly() || !slices.Equal(preview.Arguments, []string{"svn://host/branch", "42", "--reverse"}) {
		t.Fatalf("bad merge preview: %+v", preview)
	}
	a.handleTaskResult(nextTask(t, a))
	if !strings.Contains(a.model.detailContent, "eligible r42") || a.model.mergePreview == nil {
		t.Fatal("mergeinfo preview not shown")
	}
	a.handleKey(key{kind: keyRune, rune: 'a'})
	if a.model.overlay != overlayConfirm || a.model.confirm.request.IsReadOnly() {
		t.Fatal("apply not explicitly confirmed")
	}
	select {
	case <-requests:
		t.Fatal("merge applied without confirmation")
	default:
	}
	for _, r := range "YES" {
		a.handleConfirmKey(key{kind: keyRune, rune: r})
	}
	a.handleConfirmKey(key{kind: keyEnter})
	applied := <-requests
	if applied.Action != ActionMerge || applied.IsReadOnly() {
		t.Fatalf("unexpected apply: %+v", applied)
	}
	a.handleTaskResult(nextTask(t, a))
	a.wait.Wait()
	select {
	case r := <-requests:
		t.Fatalf("automatic followup write: %+v", r)
	default:
	}
	if !strings.Contains(a.model.detailContent, "手动提交") {
		t.Fatal("manual commit guidance missing")
	}
}

func TestDocumentScrollSearchAndHunkNavigation(t *testing.T) {
	a := testApp(t, baseActions())
	a.model.setSize(80, 10)
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	lines[8] = "@@ -1 +1 @@"
	lines[20] = "needle"
	lines[30] = "@@ -5 +5 @@"
	a.showDetail("diff", strings.Join(lines, "\n"))
	for range 200 {
		a.handleKey(key{kind: keyDown})
	}
	if a.model.overlayOffset != 35 {
		t.Fatalf("unbounded scroll: %d", a.model.overlayOffset)
	}
	a.handleKey(key{kind: keyHome})
	a.handleKey(key{kind: keyRune, rune: ']'})
	if a.model.overlayOffset != 8 {
		t.Fatal("hunk jump failed")
	}
	a.handleKey(key{kind: keyRune, rune: '/'})
	for _, r := range "needle" {
		a.handleKey(key{kind: keyRune, rune: r})
	}
	a.handleKey(key{kind: keyEnter})
	if a.model.overlayOffset != 20 {
		t.Fatal("search failed")
	}
	a.handleKey(key{kind: keyRune, rune: '/'})
	a.handleKey(key{kind: keyEscape})
	if a.model.overlay != overlayDetail {
		t.Fatal("search escape closed document")
	}
	a.handleKey(key{kind: keyEnd})
	a.handleKey(key{kind: keyUp})
	if a.model.overlayOffset != 34 {
		t.Fatal("scroll cannot move back immediately from end")
	}
}

func TestRepositoryNavigationAndHistoryPagination(t *testing.T) {
	requests := make(chan ModuleRequest, 8)
	loader := moduleLoaderFunc(func(_ context.Context, r ModuleRequest) (ModuleData, error) {
		requests <- r
		return ModuleData{Location: r.Target, Parent: "svn://host", HasMore: true, NextRevision: 100, Rows: []ModuleRow{{ID: "child", Kind: "dir", Target: "svn://host/child"}}}, nil
	})
	a := testApp(t, baseActions())
	a.options.ModuleLoader = loader
	a.switchModule(ModuleRepository)
	a.handleTaskResult(nextTask(t, a))
	<-requests
	a.openCurrentItem()
	a.handleTaskResult(nextTask(t, a))
	if r := <-requests; r.Target != "svn://host/child" {
		t.Fatalf("directory request: %+v", r)
	}
	a.handleKey(key{kind: keyBackspace})
	a.handleTaskResult(nextTask(t, a))
	if r := <-requests; r.Target != "svn://host" {
		t.Fatalf("parent request: %+v", r)
	}
	a.switchModule(ModuleHistory)
	a.handleTaskResult(nextTask(t, a))
	if r := <-requests; r.Limit != 100 {
		t.Fatal("history default limit missing")
	}
	a.historyPage(true)
	a.handleTaskResult(nextTask(t, a))
	if r := <-requests; r.BeforeRevision != 100 {
		t.Fatal("next page cursor missing")
	}
	a.historyPage(false)
	a.handleTaskResult(nextTask(t, a))
	if r := <-requests; r.BeforeRevision != 0 {
		t.Fatal("previous page cursor lost")
	}
	a.openForm("history-filter", "")
	for i, v := range []string{"alice", "fix", "2026-01-01", "2026-02-01"} {
		a.model.form.fields[i].value = []rune(v)
	}
	a.submitForm()
	a.handleTaskResult(nextTask(t, a))
	if r := <-requests; r.Author != "alice" || r.Search != "fix" || r.Since != "2026-01-01" || r.Until != "2026-02-01" {
		t.Fatalf("filter request missing values: %+v", r)
	}
	a.wait.Wait()
}

func TestProjectSwitchResetsStateAndRejectsStaleResult(t *testing.T) {
	actions := normalizedActions{fakeActions: baseActions(), switchProject: func(_ context.Context, path string) (string, error) { return path, nil }}
	a := testApp(t, actions)
	a.model.drafts["/wc"] = "old draft"
	a.model.drafts["/new"] = "new draft"
	a.model.selected["old.go"] = true
	a.model.commandHistory = []string{"old"}
	a.model.historyPages = []int64{12}
	a.model.replaceModuleData(ModuleHistory, ModuleData{Rows: []ModuleRow{{ID: "old"}}})
	oldID := a.model.taskID
	a.switchProject("/new")
	a.handleTaskResult(nextTask(t, a))
	a.handleTaskResult(nextTask(t, a))
	a.handleTaskResult(taskResultEvent{id: oldID, kind: taskStatus, entries: []svn.StatusEntry{{Path: "stale"}}})
	a.wait.Wait()
	if a.options.WorkingCopy != "/new" || len(a.model.selected) != 0 || len(a.model.commandHistory) != 0 || len(a.model.historyPages) != 0 || len(a.model.moduleData) != 0 {
		t.Fatal("project state leaked")
	}
	if a.model.entries[0].Path == "stale" {
		t.Fatal("old task overwrote new project")
	}
	a.openCommit()
	if string(a.model.commit.message) != "new draft" || a.model.drafts["/wc"] != "old draft" {
		t.Fatal("project drafts were not isolated")
	}
}

func TestCommandEditingAndBracketedPaste(t *testing.T) {
	decoder := keyDecoder{}
	if keys := decoder.feed([]byte("\x1b[200~commit title\nquit")); len(keys) != 0 {
		t.Fatalf("partial paste executed: %+v", keys)
	}
	keys := decoder.feed([]byte("\x1b[201~"))
	if len(keys) != 1 || keys[0].kind != keyPaste {
		t.Fatalf("paste not atomic: %+v", keys)
	}
	a := testApp(t, baseActions())
	a.model.overlay = overlayCommand
	a.handleCommandKey(keys[0])
	if !a.model.commandEditing || string(a.model.command) != "commit title quit" || a.model.overlay != overlayCommand {
		t.Fatal("pasted newline executed command")
	}
	a.model.command = []rune("move ac")
	a.model.commandPosition = 6
	a.handleCommandEditKey(key{kind: keyRune, rune: 'b'})
	if string(a.model.command) != "move abc" {
		t.Fatalf("insertion: %q", a.model.command)
	}
	a.handleCommandEditKey(key{kind: keyDelete})
	if string(a.model.command) != "move ab" {
		t.Fatal("delete did not use cursor")
	}
	a.model.commandHistory = []string{"refresh", "diff"}
	a.model.historyPosition = 2
	a.handleCommandEditKey(key{kind: keyUp})
	if string(a.model.command) != "diff" {
		t.Fatal("history missing")
	}
	a.handleCommandEditKey(key{kind: keyDown})
	if string(a.model.command) != "move ab" {
		t.Fatal("history lost in-progress input")
	}
	a.model.command = []rune("ref")
	a.model.commandPosition = 3
	a.handleCommandEditKey(key{kind: keyTab})
	if string(a.model.command) != "refresh" {
		t.Fatal("completion failed")
	}
}

func TestPasteNormalizesConsoleLineEndings(t *testing.T) {
	for _, separator := range []string{"\r", "\r\n", "\n"} {
		for _, multiline := range []bool{false, true} {
			value, position := []rune{}, 0
			editRunes(&value, &position, key{kind: keyPaste, text: "标题" + separator + "说明"}, multiline)
			want := "标题 说明"
			if multiline {
				want = "标题\n说明"
			}
			if string(value) != want || position != len([]rune(want)) {
				t.Fatalf("separator %q, multiline %t: got %q at %d, want %q", separator, multiline, value, position, want)
			}
		}
	}
}

func TestWindowsLiteralCommandPaths(t *testing.T) {
	for _, line := range []string{`move "C:\project files\old.go" "D:\new files\"`, `move C:\work\old.go D:\new.go`, `move \\server\share\old.go \\server\share\new.go`} {
		fields, err := splitCommand(line)
		if err != nil {
			t.Fatal(err)
		}
		if len(fields) != 3 || !strings.Contains(fields[1], `\`) || !strings.Contains(fields[2], `\`) {
			t.Fatalf("backslashes lost: %#v", fields)
		}
	}
}

type suspendTerminal struct {
	input              chan []byte
	interrupt          chan struct{}
	frames             chan string
	mutex              sync.Mutex
	output             strings.Builder
	reading            atomic.Bool
	suspended, resumed atomic.Int32
}

func newSuspendTerminal() *suspendTerminal {
	return &suspendTerminal{input: make(chan []byte, 8), interrupt: make(chan struct{}, 1), frames: make(chan string, 64)}
}
func (f *suspendTerminal) Read(b []byte) (int, error) {
	f.reading.Store(true)
	defer f.reading.Store(false)
	select {
	case input := <-f.input:
		return copy(b, input), nil
	case <-f.interrupt:
		return 0, nil
	case <-time.After(10 * time.Millisecond):
		return 0, nil
	}
}
func (f *suspendTerminal) Write(b []byte) (int, error) {
	f.mutex.Lock()
	f.output.Write(b)
	f.mutex.Unlock()
	select {
	case f.frames <- string(b):
	default:
	}
	return len(b), nil
}
func (f *suspendTerminal) Size() (int, int, error) { return 90, 24, nil }
func (f *suspendTerminal) Close() error            { return nil }
func (f *suspendTerminal) interruptRead() {
	select {
	case f.interrupt <- struct{}{}:
	default:
	}
}
func (f *suspendTerminal) Suspend() error {
	if f.reading.Load() {
		return errors.New("input reader still active")
	}
	f.suspended.Add(1)
	return nil
}
func (f *suspendTerminal) Resume() error { f.resumed.Add(1); return nil }

func TestExternalEditorFailureRestoresTerminalAndRetainsChanges(t *testing.T) {
	term := newSuspendTerminal()
	a := testApp(t, baseActions())
	a.terminal = term
	a.openCommit()
	a.startInput()
	a.options.RunExternal = func(_ context.Context, r ExternalRequest) error {
		if r.Kind != "editor" || term.suspended.Load() != 1 || term.reading.Load() {
			t.Error("editor ran before terminal suspension")
		}
		if err := os.WriteFile(r.Target, []byte("edited\nbody"), 0600); err != nil {
			t.Error(err)
		}
		return errors.New("editor failed")
	}
	a.editCommitExternally()
	a.inputCancel()
	term.interruptRead()
	a.wait.Wait()
	if term.resumed.Load() != 1 || term.suspended.Load() != 1 {
		t.Fatal("terminal was not restored after editor failure")
	}
	if a.model.drafts["/wc"] != "edited\nbody" || !strings.Contains(a.model.message, "失败") {
		t.Fatal("partial editor changes or error lost")
	}
}

func TestFakeTerminalCommitFlow(t *testing.T) {
	term := newSuspendTerminal()
	requests := make(chan ActionRequest, 1)
	actions := baseActions()
	actions.exec = func(_ context.Context, r ActionRequest) (ActionResult, error) {
		requests <- r
		return ActionResult{Summary: "提交成功"}, nil
	}
	done := make(chan error, 1)
	go func() {
		done <- runWithTerminal(t.Context(), Options{WorkingCopy: "/wc", Actions: actions, NoColor: true}, term)
	}()
	awaitFrame := func(text string) {
		t.Helper()
		for {
			select {
			case frame := <-term.frames:
				if strings.Contains(frame, text) {
					return
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("no frame containing %q", text)
			}
		}
	}
	awaitFrame("file.go")
	term.input <- []byte("C")
	awaitFrame("提交说明")
	term.input <- []byte(" \t\x1b[200~title\nbody\x1b[201~\x13")
	awaitFrame("确认操作")
	term.input <- []byte("y")
	select {
	case r := <-requests:
		if !slices.Equal(r.Targets, []string{"file.go"}) || !slices.Equal(r.Arguments, []string{"title\nbody"}) {
			t.Fatalf("bad terminal commit: %+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("commit not executed")
	}
	awaitFrame("提交成功")
	term.input <- []byte("q")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal did not quit")
	}
}

func TestNarrowTerminalKeepsActiveModuleAndConfirmationScrollable(t *testing.T) {
	m := newModel("/wc")
	m.setSize(22, 10)
	m.module = ModuleProjects
	if output := (renderer{}).render(m); !strings.Contains(output, "9 项目") {
		t.Fatal("active module clipped on narrow screen")
	}
	a := testApp(t, baseActions())
	a.model.setSize(40, 10)
	targets := make([]string, 50)
	for i := range targets {
		targets[i] = fmt.Sprintf("file-%02d", i)
	}
	a.model.confirm = confirmation{request: ActionRequest{Action: ActionDelete, Targets: targets}, dangerous: true}
	a.model.overlay = overlayConfirm
	a.handleConfirmKey(key{kind: keyEnd})
	if a.model.confirm.offset == 0 || !strings.Contains(strings.Join(renderConfirmation(a.model.confirm, 40, 6), "\n"), "file-49") {
		t.Fatal("confirmation cannot reach last target")
	}
}

func TestWriteTaskCannotBeReplacedByFilterOrExternalTool(t *testing.T) {
	a := testApp(t, baseActions())
	a.model.beginTask("write", false)
	id := a.model.taskID
	a.openForm("history-filter", "")
	a.runConflictExternal("edit")
	if a.model.taskID != id || !a.model.busy || a.model.overlay == overlayForm {
		t.Fatal("write task replaced by unrelated workflow")
	}
}

func TestContextActionsRespectTargetCountAndStatus(t *testing.T) {
	a := testApp(t, baseActions())
	has := func(action Action) bool {
		return slices.ContainsFunc(a.contextActions(), func(item contextAction) bool { return item.action == action })
	}
	if has(ActionMove) || has(ActionAdd) || has(ActionResolve) || !has(ActionUpdate) || !has(ActionPatch) {
		t.Fatal("empty selection offered invalid actions")
	}
	a.model.replaceEntries([]svn.StatusEntry{{Path: "a", Item: "modified"}, {Path: "b", Item: "modified"}})
	if !has(ActionMove) || has(ActionAdd) || has(ActionResolve) {
		t.Fatal("modified target menu is invalid")
	}
	a.model.selectVisible(false)
	if has(ActionMove) {
		t.Fatal("move offered for multiple sources")
	}
	clear(a.model.selected)
	a.model.replaceEntries([]svn.StatusEntry{{Path: "new", Item: "unversioned"}})
	if !has(ActionAdd) || has(ActionMove) || has(ActionProperty) || has(ActionResolve) {
		t.Fatal("unversioned target menu is invalid")
	}
	a.model.replaceEntries([]svn.StatusEntry{{Path: "conflict", Item: "conflicted"}})
	if !has(ActionResolve) || has(ActionMove) || has(ActionAdd) {
		t.Fatal("conflict target menu is invalid")
	}
}

func TestPropertyFormPreservesMultilinePasteAndEditing(t *testing.T) {
	a := testApp(t, normalizedActions{fakeActions: baseActions()})
	a.model.replaceEntries([]svn.StatusEntry{{Path: "dir", Item: "normal"}})
	a.openForm("action", ActionProperty)
	a.model.form.fields[0].value = []rune("svn:ignore")
	a.model.form.active = 1
	a.handleFormKey(key{kind: keyPaste, text: "*.log\n*.tmp"})
	a.handleFormKey(key{kind: keyEnter})
	a.handleFormKey(key{kind: keyPaste, text: "cache"})
	a.handleFormKey(key{kind: keyCtrlS})
	if a.model.overlay != overlayConfirm || !slices.Equal(a.model.confirm.request.Arguments, []string{"svn:ignore", "*.log\n*.tmp\ncache"}) {
		t.Fatalf("multiline property was altered: %+v", a.model.confirm.request)
	}
}

func TestUpdateFormAcceptsExplicitSparseDirectory(t *testing.T) {
	actions := baseActions()
	requests := make(chan ActionRequest, 1)
	actions.exec = func(_ context.Context, request ActionRequest) (ActionResult, error) {
		requests <- request
		return ActionResult{}, nil
	}
	a := testApp(t, actions)
	a.model.replaceEntries([]svn.StatusEntry{{Path: "unrelated.go", Item: "modified"}})
	a.openForm("action", ActionUpdate)
	a.model.form.fields[0].value = []rune("120")
	a.model.form.fields[1].value = []rune("infinity")
	a.model.form.fields[2].value = []rune("sparse/中文 目录")
	a.submitForm()
	_ = nextTask(t, a)
	request := <-requests
	if !request.IsReadOnly() || request.Revision != "120" || request.Depth != "infinity" || !slices.Equal(request.Targets, []string{"sparse/中文 目录"}) {
		t.Fatalf("update form lost explicit scope: %+v", request)
	}
}

type inspectionLoader struct {
	info    func(context.Context, string, string) (MergeInfoData, error)
	blame   func(context.Context, string, string, string) (string, error)
	compare func(context.Context, string, string, string, string) (string, error)
}

func (f inspectionLoader) LoadModule(context.Context, ModuleRequest) (ModuleData, error) {
	return ModuleData{}, nil
}
func (f inspectionLoader) LoadMergeInfo(ctx context.Context, wc, source string) (MergeInfoData, error) {
	return f.info(ctx, wc, source)
}
func (f inspectionLoader) LoadBlame(ctx context.Context, wc, target, revision string) (string, error) {
	return f.blame(ctx, wc, target, revision)
}
func (f inspectionLoader) CompareRevisions(ctx context.Context, wc, target, from, to string) (string, error) {
	return f.compare(ctx, wc, target, from, to)
}

func TestMergeRevisionSelectionUsesEligibleOrMergedList(t *testing.T) {
	a := testApp(t, baseActions())
	a.options.ModuleLoader = inspectionLoader{info: func(_ context.Context, wc, source string) (MergeInfoData, error) {
		if wc != "/wc" || source != "svn://host/branch" {
			t.Errorf("bad mergeinfo request %s %s", wc, source)
		}
		return MergeInfoData{Eligible: []int64{4, 2, 3}, Merged: []int64{1}}, nil
	}}
	a.openForm("action", ActionMerge)
	a.model.form.fields[0].value = []rune("svn://host/branch")
	a.handleFormKey(key{kind: keyCtrlL})
	a.handleTaskResult(nextTask(t, a))
	if a.model.overlay != overlayRevisions || !slices.Equal(a.model.revisions.rows, []int64{4, 3, 2}) {
		t.Fatalf("eligible revisions missing: %+v", a.model.revisions)
	}
	a.handleRevisionKey(key{kind: keyRune, rune: ' '})
	a.handleRevisionKey(key{kind: keyDown})
	a.handleRevisionKey(key{kind: keyRune, rune: ' '})
	a.handleRevisionKey(key{kind: keyEnter})
	if string(a.model.form.fields[1].value) != "3,4" || a.model.overlay != overlayForm {
		t.Fatal("selected revisions did not reach merge form")
	}
	a.model.form.fields[2].value = []rune("是")
	a.loadMergeRevisions()
	a.handleTaskResult(nextTask(t, a))
	if !slices.Equal(a.model.revisions.rows, []int64{1}) {
		t.Fatal("reverse merge offered unmerged revisions")
	}
	a.wait.Wait()
}

func TestMergeInfoDoesNotOverwriteChangedSource(t *testing.T) {
	a := testApp(t, baseActions())
	a.openForm("action", ActionMerge)
	a.model.form.fields[0].value = []rune("svn://new")
	a.acceptMergeInfo("svn://old", MergeInfoData{Eligible: []int64{9}})
	if a.model.overlay != overlayForm || len(a.model.revisions.rows) != 0 {
		t.Fatal("stale mergeinfo applied to another source")
	}
}

func TestHistoryInspectionUsesStructuredArgumentsAndNoConfirmation(t *testing.T) {
	calls := make(chan []string, 2)
	a := testApp(t, baseActions())
	a.options.ModuleLoader = inspectionLoader{blame: func(_ context.Context, wc, target, revision string) (string, error) {
		calls <- []string{wc, target, revision}
		return "r12 alice source line", nil
	}, compare: func(_ context.Context, wc, target, from, to string) (string, error) {
		calls <- []string{wc, target, from, to}
		return "@@ -1 +1 @@\n+changed", nil
	}}
	a.model.replaceEntries([]svn.StatusEntry{{Path: "file.go", Item: "modified"}})
	a.handleKey(key{kind: keyRune, rune: 'b'})
	a.submitForm()
	a.handleTaskResult(nextTask(t, a))
	if call := <-calls; !slices.Equal(call, []string{"/wc", "file.go", "HEAD"}) {
		t.Fatalf("bad blame request: %v", call)
	}
	if !strings.Contains(a.model.detailContent, "source line") || a.model.detailReturn != overlayForm {
		t.Fatal("blame source unavailable")
	}
	a.model.overlay = overlayNone
	a.handleKey(key{kind: keyRune, rune: 'v'})
	a.model.form.fields[1].value = []rune("12")
	a.model.form.fields[2].value = []rune("19")
	a.submitForm()
	a.handleTaskResult(nextTask(t, a))
	if call := <-calls; !slices.Equal(call, []string{"/wc", "file.go", "12", "19"}) {
		t.Fatalf("bad comparison: %v", call)
	}
	a.wait.Wait()
}

type canceledRead struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (r canceledRead) Read([]byte) (int, error) {
	r.cancel()
	return 0, errors.New("operating system interrupted read")
}
func TestCanceledTerminalReaderDoesNotEmitStaleError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := make(chan any, 2)
	readInput(ctx, canceledRead{ctx: ctx, cancel: cancel}, events)
	select {
	case event := <-events:
		t.Fatalf("canceled reader emitted stale event: %#v", event)
	default:
	}
}
