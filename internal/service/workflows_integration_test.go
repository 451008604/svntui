package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"svntui/internal/appdirs"
	"svntui/internal/domain"
	"svntui/internal/safepoint"
	"svntui/internal/svn"
)

func workflowFixture(t *testing.T) (Actions, string, string) {
	t.Helper()
	for _, name := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " unavailable")
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wc := filepath.Join(root, "wc")
	runServiceCommand(t, root, "svnadmin", "create", repo)
	runServiceCommand(t, root, "svn", "checkout", serviceFileURL(repo), wc)
	for _, name := range []string{"a.txt", "b.txt", "foo", "foo@"} {
		if err := os.WriteFile(filepath.Join(wc, name), []byte("base\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runServiceCommand(t, wc, "svn", "add", "--force", ".")
	runServiceCommand(t, wc, "svn", "commit", "-m", "base")
	runServiceCommand(t, wc, "svn", "update")
	client := svn.NewClient()
	dirs := appdirs.Dirs{Config: filepath.Join(root, "config"), State: filepath.Join(root, "state"), Cache: filepath.Join(root, "cache")}
	return Actions{Client: client, LockDir: filepath.Join(root, "locks"), SafePoints: safepoint.New(dirs, client)}, wc, repo
}

func TestPropertySelectionMatchesPreviewAndExecution(t *testing.T) {
	actions, wc, _ := workflowFixture(t)
	request := domain.ActionRequest{Action: domain.ActionProperty, WorkingCopy: wc, Targets: []string{"a.txt", "b.txt"}, Arguments: []string{"test:flag", "yes"}}
	preview := actions.PreviewAction(request)
	if !strings.Contains(preview, "a.txt") || !strings.Contains(preview, "b.txt") {
		t.Fatal(preview)
	}
	if _, err := actions.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"a.txt", "b.txt"} {
		value, err := actions.Client.PropGet(t.Context(), wc, "test:flag", target)
		if err != nil || strings.TrimSpace(value) != "yes" {
			t.Fatalf("%s: %q %v", target, value, err)
		}
	}
	props, err := actions.Client.Properties(t.Context(), wc, ".")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := props["test:flag"]; ok {
		t.Fatal("root was modified")
	}
}

func TestServiceCommitLiteralAtAndChangelist(t *testing.T) {
	actions, wc, repo := workflowFixture(t)
	for _, name := range []string{"foo", "foo@"} {
		if err := os.WriteFile(filepath.Join(wc, name), []byte(name+" changed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runServiceCommand(t, wc, "svn", "changelist", "task", "foo@@")
	_, err := actions.Execute(t.Context(), domain.ActionRequest{Action: domain.ActionCommit, WorkingCopy: wc, Targets: []string{"foo@"}, Arguments: []string{"selected only"}})
	if err != nil {
		t.Fatal(err)
	}
	untouched, err := actions.Client.Cat(t.Context(), serviceFileURL(repo)+"/foo", "HEAD")
	if err != nil || untouched != "base\n" {
		t.Fatalf("unselected target committed: %q %v", untouched, err)
	}
	selected, err := actions.Client.Cat(t.Context(), serviceFileURL(repo)+"/foo@@", "HEAD")
	if err != nil || selected != "foo@ changed\n" {
		t.Fatalf("selected target missing: %q %v", selected, err)
	}
}

func TestRemotePropertyPreviewAndSelectedUpdate(t *testing.T) {
	actions, wc, repo := workflowFixture(t)
	peer := filepath.Join(t.TempDir(), "peer")
	runServiceCommand(t, filepath.Dir(peer), "svn", "checkout", serviceFileURL(repo), peer)
	for _, target := range []string{"a.txt", "b.txt"} {
		runServiceCommand(t, peer, "svn", "propset", "test:remote", "yes", target)
	}
	runServiceCommand(t, peer, "svn", "commit", "-m", "properties")
	request := domain.ActionRequest{Action: domain.ActionUpdate, WorkingCopy: wc, Targets: []string{"a.txt"}}
	preview, err := actions.Execute(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview.Output, "属性=modified") || strings.Contains(preview.Output, "b.txt") {
		t.Fatal(preview.Output)
	}
	request.Arguments = []string{"--apply"}
	if _, err := actions.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	props, err := actions.Client.Properties(t.Context(), wc, "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if props["test:remote"] != "" {
		t.Fatal("unselected path updated")
	}
}

func TestMoveUsesSelectedSource(t *testing.T) {
	actions, wc, _ := workflowFixture(t)
	request := domain.ActionRequest{Action: domain.ActionMove, WorkingCopy: wc, Targets: []string{"a.txt"}, Arguments: []string{"moved.txt"}}
	normalized, err := actions.NormalizeAction(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized.Arguments) != 2 || normalized.Arguments[0] != "a.txt" {
		t.Fatalf("%+v", normalized)
	}
	if _, err := actions.Execute(t.Context(), normalized); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wc, "moved.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestPatchPreviewThenApply(t *testing.T) {
	actions, wc, _ := workflowFixture(t)
	path := filepath.Join(t.TempDir(), "change.patch")
	if err := os.WriteFile(filepath.Join(wc, "a.txt"), []byte("patched\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := actions.Client.DiffToFile(t.Context(), wc, []string{"a.txt"}, path); err != nil {
		t.Fatal(err)
	}
	runServiceCommand(t, wc, "svn", "revert", "a.txt")
	request := domain.ActionRequest{Action: domain.ActionPatch, WorkingCopy: wc, Arguments: []string{path}}
	if _, err := actions.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(wc, "a.txt"))
	if string(data) != "base\n" {
		t.Fatal("preview mutated file")
	}
	request.Arguments = append(request.Arguments, "--apply")
	if _, err := actions.Execute(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(filepath.Join(wc, "a.txt"))
	if string(data) != "patched\n" {
		t.Fatalf("%q", data)
	}
}

func TestResolveDirectoryPropertyConflict(t *testing.T) {
	actions, wc, repo := workflowFixture(t)
	peer := filepath.Join(t.TempDir(), "peer")
	runServiceCommand(t, wc, "svn", "propset", "test:conflict", "base", ".")
	runServiceCommand(t, wc, "svn", "commit", "-m", "base property")
	runServiceCommand(t, filepath.Dir(peer), "svn", "checkout", serviceFileURL(repo), peer)
	runServiceCommand(t, peer, "svn", "propset", "test:conflict", "remote", ".")
	runServiceCommand(t, peer, "svn", "commit", "-m", "remote property")
	runServiceCommand(t, wc, "svn", "propset", "test:conflict", "local", ".")
	runServiceCommand(t, wc, "svn", "update", "--accept", "postpone")
	_, err := actions.Execute(t.Context(), domain.ActionRequest{Action: domain.ActionResolve, WorkingCopy: wc, Targets: []string{"."}, Arguments: []string{"working"}})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := actions.Client.Status(t.Context(), wc, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsConflict() {
			t.Fatalf("conflict remains: %+v", entry)
		}
	}
}

func TestRepositoryNavigationPreservesAtSignNames(t *testing.T) {
	actions, wc, _ := workflowFixture(t)
	if err := os.MkdirAll(filepath.Join(wc, "dir@", "nested@"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wc, "dir@", "file@"), []byte("content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runServiceCommand(t, wc, "svn", "add", "dir@@")
	runServiceCommand(t, wc, "svn", "commit", "-m", "at-sign tree")
	modules := Modules{Client: actions.Client}
	root, err := modules.LoadModule(t.Context(), domain.ModuleRequest{Module: domain.ModuleRepository, WorkingCopy: wc})
	if err != nil {
		t.Fatal(err)
	}
	var directory string
	for _, row := range root.Rows {
		if row.Cells[1] == "dir@" {
			directory = row.Target
		}
	}
	if directory == "" {
		t.Fatal("missing literal at-sign directory")
	}
	child, err := modules.LoadModule(t.Context(), domain.ModuleRequest{Module: domain.ModuleRepository, WorkingCopy: wc, Target: directory})
	if err != nil || len(child.Rows) != 2 || child.Parent != root.Location {
		t.Fatalf("directory navigation: %+v %v", child, err)
	}
	for _, row := range child.Rows {
		if _, err := actions.Execute(t.Context(), domain.ActionRequest{Action: domain.ActionProperties, Module: domain.ModuleRepository, WorkingCopy: wc, Targets: []string{row.Target}}); err != nil {
			t.Fatalf("literal remote property %s: %v", row.Target, err)
		}
	}
	parent, err := modules.LoadModule(t.Context(), domain.ModuleRequest{Module: domain.ModuleRepository, WorkingCopy: wc, Target: child.Parent})
	if err != nil || parent.Location != root.Location {
		t.Fatalf("parent navigation: %+v %v", parent, err)
	}
}
