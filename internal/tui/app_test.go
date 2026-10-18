package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"svntui/internal/svn"
)

type fakeActions struct {
	status func(context.Context, string) ([]svn.StatusEntry, error)
	diff   func(context.Context, string, []string) (string, error)
	exec   func(context.Context, ActionRequest) (ActionResult, error)
}

func TestApplicationBlocksQuitDuringWriteTask(t *testing.T) {
	app := application{model: newModel("/wc")}
	app.model.beginTask("write", false)
	if app.handleKey(key{kind: keyRune, rune: 'q'}) || app.handleKey(key{kind: keyCtrlC}) {
		t.Fatal("application allowed quit during a write task")
	}
	if app.model.message != "写操作运行中，不能退出" {
		t.Fatalf("message = %q", app.model.message)
	}
}

func TestApplicationRefreshesStatusAfterFailedWrite(t *testing.T) {
	statusCalled := make(chan struct{}, 1)
	actions := fakeActions{
		status: func(context.Context, string) ([]svn.StatusEntry, error) {
			statusCalled <- struct{}{}
			return []svn.StatusEntry{{Path: "partial.txt", Item: "modified"}}, nil
		},
		diff: func(context.Context, string, []string) (string, error) { return "", nil },
		exec: func(context.Context, ActionRequest) (ActionResult, error) {
			return ActionResult{}, errors.New("partial write failed")
		},
	}
	app := application{
		ctx: t.Context(), options: Options{WorkingCopy: "/wc", Actions: actions},
		model: newModel("/wc"), events: make(chan any, 4),
	}
	app.startAction(ActionRequest{Action: ActionUpdate, WorkingCopy: "/wc"})
	app.handleTaskResult((<-app.events).(taskResultEvent))
	<-statusCalled
	app.handleTaskResult((<-app.events).(taskResultEvent))
	app.wait.Wait()
	if len(app.model.entries) != 1 || app.model.entries[0].Path != "partial.txt" {
		t.Fatalf("status was not refreshed: %+v", app.model.entries)
	}
}

type moduleLoaderFunc func(context.Context, ModuleRequest) (ModuleData, error)

func (f moduleLoaderFunc) LoadModule(ctx context.Context, request ModuleRequest) (ModuleData, error) {
	return f(ctx, request)
}

type historyLoader struct {
	load func(context.Context, ModuleRequest) (ModuleData, error)
	diff func(context.Context, string, string, string) (string, error)
}

func (l historyLoader) LoadModule(ctx context.Context, request ModuleRequest) (ModuleData, error) {
	return l.load(ctx, request)
}

func (l historyLoader) LoadRevisionDiff(ctx context.Context, workingCopy, target, revision string) (string, error) {
	return l.diff(ctx, workingCopy, target, revision)
}

func (f fakeActions) WorkspaceStatus(ctx context.Context, path string) ([]svn.StatusEntry, error) {
	return f.status(ctx, path)
}

func (f fakeActions) WorkspaceDiff(ctx context.Context, path string, targets []string) (string, error) {
	return f.diff(ctx, path, targets)
}

func (f fakeActions) Execute(ctx context.Context, request ActionRequest) (ActionResult, error) {
	return f.exec(ctx, request)
}

func TestApplicationRunsReadTaskOutsideInputLoop(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	actions := fakeActions{
		status: func(ctx context.Context, _ string) ([]svn.StatusEntry, error) {
			close(started)
			select {
			case <-release:
				return []svn.StatusEntry{{Path: "main.go", Item: "modified"}}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		diff: func(context.Context, string, []string) (string, error) { return "", nil },
		exec: func(context.Context, ActionRequest) (ActionResult, error) { return ActionResult{}, nil },
	}
	app := application{
		ctx:     t.Context(),
		options: Options{WorkingCopy: "/wc", Actions: actions},
		model:   newModel("/wc"),
		events:  make(chan any, 2),
	}
	app.startStatus()
	<-started
	app.handleKey(key{kind: keyRune, rune: '?'})
	if app.model.overlay != overlayHelp {
		t.Fatal("input handling blocked while status was running")
	}
	close(release)
	event := (<-app.events).(taskResultEvent)
	app.handleTaskResult(event)
	app.wait.Wait()
	if len(app.model.entries) != 1 || app.model.entries[0].Path != "main.go" {
		t.Fatalf("status result was not applied: %#v", app.model.entries)
	}
}

func TestApplicationConfirmsAndInjectsWriteAction(t *testing.T) {
	requests := make(chan ActionRequest, 1)
	actions := fakeActions{
		status: func(context.Context, string) ([]svn.StatusEntry, error) { return nil, nil },
		diff:   func(context.Context, string, []string) (string, error) { return "", nil },
		exec: func(_ context.Context, request ActionRequest) (ActionResult, error) {
			requests <- request
			return ActionResult{Summary: "done"}, nil
		},
	}
	app := application{
		ctx:     t.Context(),
		options: Options{WorkingCopy: "/wc", Actions: actions},
		model:   newModel("/wc"),
		events:  make(chan any, 2),
	}
	app.model.replaceEntries([]svn.StatusEntry{{Path: "main.go", Item: "modified"}})
	app.model.toggleSelection()
	app.executePaletteCommand(paletteCommand{
		kind:      commandAction,
		action:    ActionCommit,
		arguments: []string{"issue 42"},
	})
	if app.model.overlay != overlayConfirm {
		t.Fatal("write action did not enter confirmation state")
	}
	app.handleConfirmKey(key{kind: keyRune, rune: 'y'})
	request := <-requests
	want := ActionRequest{
		Action:      ActionCommit,
		Module:      ModuleWorkspace,
		WorkingCopy: "/wc",
		Targets:     []string{"main.go"},
		Arguments:   []string{"issue 42"},
	}
	if !reflect.DeepEqual(request, want) {
		t.Fatalf("request = %#v, want %#v", request, want)
	}
	app.handleTaskResult((<-app.events).(taskResultEvent))
	app.wait.Wait()
}

func TestApplicationLoadsOptionalModuleDataAsynchronously(t *testing.T) {
	requests := make(chan ModuleRequest, 1)
	release := make(chan struct{})
	loader := moduleLoaderFunc(func(ctx context.Context, request ModuleRequest) (ModuleData, error) {
		requests <- request
		select {
		case <-release:
			return ModuleData{Rows: []ModuleRow{{ID: "r42", Cells: []string{"r42"}}}}, nil
		case <-ctx.Done():
			return ModuleData{}, ctx.Err()
		}
	})
	app := application{
		ctx: t.Context(),
		options: Options{
			WorkingCopy:  "/wc",
			Actions:      fakeActions{},
			ModuleLoader: loader,
		},
		model:  newModel("/wc"),
		events: make(chan any, 2),
	}
	app.switchModule(ModuleHistory)
	request := <-requests
	if request.Module != ModuleHistory || request.WorkingCopy != "/wc" {
		t.Fatalf("module request = %#v", request)
	}
	app.handleKey(key{kind: keyRune, rune: '?'})
	if app.model.overlay != overlayHelp {
		t.Fatal("module load blocked input handling")
	}
	close(release)
	app.handleTaskResult((<-app.events).(taskResultEvent))
	app.wait.Wait()
	if got := app.model.moduleData[ModuleHistory].Rows; len(got) != 1 || got[0].ID != "r42" {
		t.Fatalf("module data = %#v", got)
	}
}

func TestCommandPaletteSelectsListEntryAndEditsTemplate(t *testing.T) {
	app := application{model: newModel("/wc")}
	app.model.overlay = overlayCommand
	for _, value := range "提交" {
		app.handleCommandKey(key{kind: keyRune, rune: value})
	}
	entries := filteredPaletteEntries("提交")
	if len(entries) == 0 || entries[0].Command != "commit <提交说明>" {
		t.Fatalf("提交筛选结果 = %#v", entries)
	}
	app.handleCommandKey(key{kind: keyEnter})
	if !app.model.commandEditing || string(app.model.command) != "commit " {
		t.Fatalf("参数编辑状态 = %v，命令 = %q", app.model.commandEditing, string(app.model.command))
	}
	app.handleCommandKey(key{kind: keyEscape})
	if app.model.commandEditing || app.model.overlay != overlayCommand {
		t.Fatal("参数编辑按 Esc 后没有返回命令列表")
	}
}

func TestApplicationLoadsSelectedRevisionDiffAsynchronously(t *testing.T) {
	requested := make(chan string, 1)
	loader := historyLoader{
		load: func(context.Context, ModuleRequest) (ModuleData, error) { return ModuleData{}, nil },
		diff: func(_ context.Context, workingCopy, target, revision string) (string, error) {
			if workingCopy != "/wc" {
				t.Errorf("workingCopy = %q", workingCopy)
			}
			if target != "" {
				t.Errorf("target = %q", target)
			}
			requested <- revision
			return "Index: main.go\n+new line", nil
		},
	}
	app := application{
		ctx:     t.Context(),
		options: Options{WorkingCopy: "/wc", Actions: fakeActions{}, ModuleLoader: loader},
		model:   newModel("/wc"), events: make(chan any, 2),
	}
	app.model.setModule(ModuleHistory)
	app.model.replaceModuleData(ModuleHistory, ModuleData{Rows: []ModuleRow{{ID: "42", Cells: []string{"r42"}}}})
	app.handleKey(key{kind: keyRune, rune: 'd'})
	if revision := <-requested; revision != "42" {
		t.Fatalf("revision = %q", revision)
	}
	app.handleTaskResult((<-app.events).(taskResultEvent))
	app.wait.Wait()
	if app.model.overlay != overlayDetail || app.model.detailTitle != "Revision r42 内容差异" || !strings.Contains(app.model.detailContent, "+new line") {
		t.Fatalf("差异详情 = %q / %q", app.model.detailTitle, app.model.detailContent)
	}
}

func TestApplicationOpensCurrentFileHistory(t *testing.T) {
	requests := make(chan ModuleRequest, 1)
	loader := moduleLoaderFunc(func(_ context.Context, request ModuleRequest) (ModuleData, error) {
		requests <- request
		return ModuleData{Title: "文件历史：main.go"}, nil
	})
	app := application{
		ctx:     t.Context(),
		options: Options{WorkingCopy: "/wc", Actions: fakeActions{}, ModuleLoader: loader},
		model:   newModel("/wc"), events: make(chan any, 2),
	}
	app.model.replaceEntries([]svn.StatusEntry{{Path: "main.go", Item: "modified"}})
	app.handleKey(key{kind: keyRune, rune: 'h'})
	request := <-requests
	if request.Module != ModuleHistory || request.Target != "main.go" {
		t.Fatalf("文件历史请求 = %#v", request)
	}
	app.handleTaskResult((<-app.events).(taskResultEvent))
	app.wait.Wait()
}
