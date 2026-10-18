package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"svntui/internal/service"
	"svntui/internal/svn"
)

func cliFixture(t *testing.T) (string, string) {
	t.Helper()
	return cliFixturePath(t, "")
}

func cliFixturePath(t *testing.T, repositoryPath string) (string, string) {
	t.Helper()
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is unavailable", binary)
		}
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for key, dir := range map[string]string{"XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache"} {
		t.Setenv(key, filepath.Join(root, dir))
	}
	repo, wc := filepath.Join(root, "repository"), filepath.Join(root, "working copy")
	runCommand(t, root, "svnadmin", "create", repo)
	checkoutURL := repositoryURL(repo)
	if repositoryPath != "" {
		checkoutURL += "/" + repositoryPath
		runCommand(t, root, "svn", "mkdir", checkoutURL, "-m", "create project")
	}
	runCommand(t, root, "svn", "checkout", checkoutURL, wc)
	for _, name := range []string{"selected @ 中文.txt", "second file.txt", "untouched.txt"} {
		writeCLIFile(t, wc, name, "base\n")
	}
	runCommand(t, wc, "svn", "add", "--force", ".")
	runCommand(t, wc, "svn", "commit", "--username", "fixture", "-m", "needle origin")
	runCommand(t, wc, "svn", "update")
	return wc, repo
}

func writeCLIFile(t *testing.T, directory, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readCLIFile(t *testing.T, directory, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func cliOutput(t *testing.T, args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run(%q): %v\nstdout: %s\nstderr: %s", args, err, &stdout, &stderr)
	}
	return stdout.String()
}

func svnOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "svn", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("svn %q: %v\n%s", args, err, output)
	}
	return string(output)
}

func TestCLICommitSelectedSpecialFilename(t *testing.T) {
	wc, repo := cliFixture(t)
	selected := "selected @ 中文.txt"
	writeCLIFile(t, wc, selected, "selected change\n")
	writeCLIFile(t, wc, "untouched.txt", "keep local\n")
	// 通过命令入口提交明确目标，仓库内容证明没有扩大提交范围。
	cliOutput(t, "commit", "--path", wc, "--yes", "--message", "--json", selected)
	for name, want := range map[string]string{selected: "selected change\n", "untouched.txt": "base\n"} {
		got := svnOutput(t, wc, "cat", repositoryURL(repo)+"/"+url.PathEscape(name)+"@")
		if got != want {
			t.Errorf("repository %q = %q, want %q", name, got, want)
		}
	}
	if got := strings.TrimSpace(svnOutput(t, wc, "log", "--xml", "-l", "1", repositoryURL(repo))); !strings.Contains(got, "<msg>--json</msg>") {
		t.Fatalf("message argument was consumed: %s", got)
	}
}

func TestCLIChangelistCommitPreservesOtherChanges(t *testing.T) {
	wc, repo := cliFixture(t)
	selected := "selected @ 中文.txt"
	for _, name := range []string{selected, "second file.txt", "untouched.txt"} {
		writeCLIFile(t, wc, name, name+" changed\n")
	}
	cliOutput(t, "changelist", "add", "--path", wc, "--yes", "任务一", selected, "second file.txt")
	var groups map[string][]string
	if err := json.Unmarshal([]byte(cliOutput(t, "changelist", "list", "--path", wc)), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups["任务一"]) != 2 || !slices.Contains(groups["任务一"], selected) || !slices.Contains(groups["任务一"], "second file.txt") {
		t.Fatalf("groups = %+v", groups)
	}
	cliOutput(t, "commit", "--path", wc, "--yes", "--message", "group commit", "--changelist", "任务一")
	for name, want := range map[string]string{selected: selected + " changed\n", "second file.txt": "second file.txt changed\n", "untouched.txt": "base\n"} {
		if got := svnOutput(t, wc, "cat", repositoryURL(repo)+"/"+url.PathEscape(name)+"@"); got != want {
			t.Errorf("repository %q = %q, want %q", name, got, want)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"commit", "--path", wc, "--yes", "--message", "empty group", "--changelist", "missing"}, &stdout, &stderr); err == nil {
		t.Fatal("empty changelist must not fall back to committing the root")
	}
}

func TestCLIPropertiesRequireExplicitTargets(t *testing.T) {
	wc, _ := cliFixture(t)
	selected := []string{"selected @ 中文.txt", "second file.txt"}
	value := "第一行\nsecond line\n"
	valuePath := filepath.Join(t.TempDir(), "value.txt")
	writeCLIFile(t, filepath.Dir(valuePath), filepath.Base(valuePath), value)
	args := []string{"property", "set", "--path", wc, "--yes", "--value-file", valuePath, "test:note"}
	cliOutput(t, append(args, selected...)...)
	for _, name := range selected {
		if got := cliOutput(t, "property", "get", "--path", wc, "test:note", name); got != value {
			t.Errorf("property %q = %q", name, got)
		}
	}
	client := svn.NewClient()
	for _, target := range []string{".", "untouched.txt"} {
		props, err := client.Properties(t.Context(), wc, target)
		if err != nil || props["test:note"] != "" {
			t.Fatalf("unselected %q properties = %v, %v", target, props, err)
		}
	}
	cliOutput(t, append([]string{"property", "delete", "--path", wc, "--yes", "test:note"}, selected...)...)
	for _, target := range selected {
		props, err := client.Properties(t.Context(), wc, target)
		if err != nil || props["test:note"] != "" {
			t.Fatalf("deleted %q properties = %v, %v", target, props, err)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run(t.Context(), []string{"property", "set", "--path", wc, "--yes", "test:note", "accidental root"}, &stdout, &stderr); err == nil {
		t.Fatal("property set without explicit targets succeeded")
	}
	props, err := client.Properties(t.Context(), wc, ".")
	if err != nil || props["test:note"] != "" {
		t.Fatalf("root properties after rejected command = %v, %v", props, err)
	}
}

func TestCLIUpdatePreviewAndSelectedApply(t *testing.T) {
	wc, repo := cliFixture(t)
	peer := filepath.Join(t.TempDir(), "peer")
	runCommand(t, filepath.Dir(peer), "svn", "checkout", repositoryURL(repo), peer)
	for _, name := range []string{"second file.txt", "untouched.txt"} {
		writeCLIFile(t, peer, name, "remote change\n")
	}
	runCommand(t, peer, "svn", "commit", "-m", "remote")
	var preview struct {
		OK       bool
		Data     map[string]json.RawMessage
		ExitCode int `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(cliOutput(t, "update", "--path", wc, "--json", "second file.txt")), &preview); err != nil {
		t.Fatal(err)
	}
	if !preview.OK || preview.ExitCode != 0 {
		t.Fatalf("preview envelope = %+v", preview)
	}
	for _, key := range []string{"summary", "output", "refresh_workspace"} {
		if _, exists := preview.Data[key]; !exists {
			t.Errorf("missing action result field %q: %s", key, preview.Data)
		}
	}
	if string(preview.Data["refresh_workspace"]) != "false" {
		t.Fatalf("preview was marked as a workspace modification: %s", preview.Data)
	}
	if got := readCLIFile(t, wc, "second file.txt"); got != "base\n" {
		t.Fatalf("preview changed working file: %q", got)
	}
	cliOutput(t, "update", "--path", wc, "--apply", "--yes", "second file.txt")
	if got := readCLIFile(t, wc, "second file.txt"); got != "remote change\n" {
		t.Fatalf("applied file = %q", got)
	}
	if got := readCLIFile(t, wc, "untouched.txt"); got != "base\n" {
		t.Fatalf("unselected file was updated: %q", got)
	}
}

func TestCLIPatchExportPreviewAndApply(t *testing.T) {
	wc, _ := cliFixturePath(t, "trunk")
	writeCLIFile(t, wc, "second file.txt", "patched\n")
	writeCLIFile(t, wc, "untouched.txt", "keep local\n")
	patch := filepath.Join(t.TempDir(), "selected.patch")
	cliOutput(t, "patch", "export", "--path", wc, "--output", patch, "second file.txt")
	patchContent := readCLIFile(t, filepath.Dir(patch), filepath.Base(patch))
	if !strings.Contains(patchContent, "+patched") || strings.Contains(patchContent, "untouched.txt") {
		t.Fatalf("exported patch has wrong scope: %s", patchContent)
	}
	runCommand(t, wc, "svn", "revert", "second file.txt")
	cliOutput(t, "patch", "apply", "--path", wc, patch)
	if got := readCLIFile(t, wc, "second file.txt"); got != "base\n" {
		t.Fatalf("patch preview changed file: %q", got)
	}
	cliOutput(t, "patch", "apply", "--path", wc, "--apply", "--yes", patch)
	if got := readCLIFile(t, wc, "second file.txt"); got != "patched\n" {
		t.Fatalf("patch apply content = %q", got)
	}
	if got := readCLIFile(t, wc, "untouched.txt"); got != "keep local\n" {
		t.Fatalf("patch changed unrelated modification: %q", got)
	}
}

func TestCLIProjectRoundTrip(t *testing.T) {
	wc, _ := cliFixture(t)
	cliOutput(t, "project", "add", "--name", "项目一", wc)
	writeCLIFile(t, wc, "untouched.txt", "dirty\n")
	var projects []service.ProjectSummary
	if err := json.Unmarshal([]byte(cliOutput(t, "project", "list")), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0].Name != "项目一" || projects[0].Path != wc || projects[0].Modified != 1 || projects[0].Error != "" {
		t.Fatalf("project list = %+v", projects)
	}
	cliOutput(t, "project", "remove", "项目一")
	if err := json.Unmarshal([]byte(cliOutput(t, "project", "list")), &projects); err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("removed project remains in recent projects: %+v", projects)
	}
}

func TestCLIHistoryPaginationFilterBlameAndCompare(t *testing.T) {
	wc, repo := cliFixture(t)
	// 构造超过默认 100 条的真实历史，并在最旧一页保留唯一筛选命中。
	var dump bytes.Buffer
	dump.WriteString("SVN-fs-dump-format-version: 2\n\n")
	for revision := 2; revision <= 106; revision++ {
		var properties bytes.Buffer
		for _, property := range [][2]string{
			{"svn:author", "fixture"},
			{"svn:log", fmt.Sprintf("routine %d", revision)},
			{"svn:date", time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")},
		} {
			fmt.Fprintf(&properties, "K %d\n%s\nV %d\n%s\n", len(property[0]), property[0], len(property[1]), property[1])
		}
		properties.WriteString("PROPS-END\n")
		fmt.Fprintf(&dump, "Revision-number: %d\nProp-content-length: %d\nContent-length: %d\n\n%s\n", revision, properties.Len(), properties.Len(), &properties)
		content := fmt.Sprintf("base\nrevision %d\n", revision)
		fmt.Fprintf(&dump, "Node-path: second file.txt\nNode-kind: file\nNode-action: change\nText-content-length: %d\nContent-length: %d\n\n%s\n\n", len(content), len(content), content)
	}
	command := exec.CommandContext(t.Context(), "svnadmin", "load", "--quiet", repo)
	command.Stdin = &dump
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("seed history: %v\n%s", err, output)
	}
	runCommand(t, wc, "svn", "update")
	t.Run("cursor crosses one hundred", func(t *testing.T) {
		var revisions []int64
		before := int64(0)
		for pageNumber := range 5 {
			args := []string{"history", "log", "--page", "--limit", "37"}
			if before > 0 {
				args = append(args, "--before", strconv.FormatInt(before, 10))
			}
			var page svn.LogPage
			if err := json.Unmarshal([]byte(cliOutput(t, append(args, repositoryURL(repo))...)), &page); err != nil {
				t.Fatal(err)
			}
			for _, entry := range page.Entries {
				revisions = append(revisions, entry.Revision)
			}
			if !page.HasMore {
				break
			}
			if page.NextRevision <= 0 || before != 0 && page.NextRevision >= before || pageNumber == 4 {
				t.Fatalf("cursor failed to advance: %+v", page)
			}
			before = page.NextRevision
		}
		if len(revisions) != 106 {
			t.Fatalf("history contains %d revisions, want 106", len(revisions))
		}
		for i, revision := range revisions {
			if want := int64(106 - i); revision != want {
				t.Fatalf("history[%d] = %d, want %d", i, revision, want)
			}
		}
	})
	t.Run("filter scans empty pages", func(t *testing.T) {
		var first svn.LogPage
		if err := json.Unmarshal([]byte(cliOutput(t, "history", "log", "--page", "--limit", "100", "--search", "needle", repositoryURL(repo))), &first); err != nil {
			t.Fatal(err)
		}
		if len(first.Entries) != 0 || !first.HasMore || first.NextRevision != 6 {
			t.Fatalf("empty filtered page lost continuation: %+v", first)
		}
		var matches []svn.LogEntry
		if err := json.Unmarshal([]byte(cliOutput(t, "history", "log", "--limit", "100", "--search", "needle", "--author", "fixture", repositoryURL(repo))), &matches); err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 || matches[0].Revision != 1 {
			t.Fatalf("filtered history omitted oldest match: %+v", matches)
		}
	})
	t.Run("blame includes source lines", func(t *testing.T) {
		var lines []svn.BlameLine
		if err := json.Unmarshal([]byte(cliOutput(t, "history", "blame", "--revision", "HEAD", filepath.Join(wc, "second file.txt"))), &lines); err != nil {
			t.Fatal(err)
		}
		if len(lines) != 2 || lines[0].Content != "base" || lines[0].Revision != 1 || lines[1].Content != "revision 106" || lines[1].Revision != 106 {
			t.Fatalf("blame = %+v", lines)
		}
	})
	t.Run("range filter preserves bounds", func(t *testing.T) {
		var matches []svn.LogEntry
		if err := json.Unmarshal([]byte(cliOutput(t, "history", "log", "--revision", "3:1", "--limit", "1", "--search", "needle", repositoryURL(repo))), &matches); err != nil || len(matches) != 1 || matches[0].Revision != 1 {
			t.Fatalf("range filter lost older match: %+v %v", matches, err)
		}
		if err := json.Unmarshal([]byte(cliOutput(t, "history", "log", "--revision", "3:2", "--before", "3", "--limit", "1", "--search", "needle", repositoryURL(repo))), &matches); err != nil || len(matches) != 0 {
			t.Fatalf("range filter crossed lower bound: %+v %v", matches, err)
		}
	})
	t.Run("compare revisions", func(t *testing.T) {
		diff := cliOutput(t, "history", "compare", "--path", wc, "--left", "1", "--right", "106", "second file.txt")
		if !strings.Contains(diff, "+revision 106") || strings.Contains(diff, "untouched.txt") {
			t.Fatalf("comparison = %s", diff)
		}
	})
}

func TestJSONArgumentsPreserveValuesAndLiteralDelimiter(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want []string
		json bool
	}{
		{name: "message value", args: []string{"commit", "--message", "--json", "file"}, want: []string{"commit", "--message", "--json", "file"}},
		{name: "short message value", args: []string{"commit", "-m", "--json", "--json", "file"}, want: []string{"commit", "-m", "--json", "file"}, json: true},
		{name: "literal operand", args: []string{"commit", "--message=message", "--", "--json"}, want: []string{"commit", "--message=message", "--", "--json"}},
		{name: "enabled before delimiter", args: []string{"--json", "commit", "--", "--json"}, want: []string{"commit", "--", "--json"}, json: true},
		{name: "explicit false", args: []string{"version", "--json", "--json=false"}, want: []string{"version"}},
		{name: "property literal value", args: []string{"property", "set", "p", "--json", "a", "b"}, want: []string{"property", "set", "p", "--json", "a", "b"}},
		{name: "config literal value", args: []string{"config", "set", "editor", "--json"}, want: []string{"config", "set", "editor", "--json"}},
		{name: "nested command", args: []string{"automation", "webhook", "add", "--name", "--json", "--json"}, want: []string{"automation", "webhook", "add", "--name", "--json"}, json: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, enabled := jsonArguments(test.args)
			if !slices.Equal(got, test.want) || enabled != test.json {
				t.Fatalf("jsonArguments(%q) = (%q, %v), want (%q, %v)", test.args, got, enabled, test.want, test.json)
			}
		})
	}
}

func TestCLIJSONHelperProcess(t *testing.T) {
	if os.Getenv("SVNTUI_CLI_HELPER") != "1" {
		return
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 {
		os.Exit(99)
	}
	os.Args = append([]string{"svntui"}, os.Args[separator+1:]...)
	main()
	os.Exit(0)
}

func TestCLIJSONEnvelopeAndProcessExit(t *testing.T) {
	wc, _ := cliFixture(t)
	for _, test := range []struct {
		name string
		args []string
		code int
		kind string
	}{
		{name: "success", args: []string{"version", "--json"}},
		{name: "missing message", args: []string{"commit", "--path", wc, "--yes", "--json", "untouched.txt"}, code: 2, kind: "invalid_arguments"},
		{name: "unknown option", args: []string{"commit", "--not-an-option", "--json"}, code: 2, kind: "invalid_arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), os.Args[0], append([]string{"-test.run=^TestCLIJSONHelperProcess$", "--"}, test.args...)...)
			command.Env = append(os.Environ(), "SVNTUI_CLI_HELPER=1")
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != test.code {
				t.Fatalf("exit = %d, want %d; stdout=%s stderr=%s", code, test.code, &stdout, &stderr)
			}
			decoder := json.NewDecoder(&stdout)
			var envelope map[string]json.RawMessage
			if err := decoder.Decode(&envelope); err != nil {
				t.Fatalf("stdout is not one JSON value: %v; stderr=%s", err, &stderr)
			}
			if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
				t.Fatalf("stdout contains trailing diagnostics: %v", err)
			}
			for _, key := range []string{"ok", "data", "error", "exit_code"} {
				if _, exists := envelope[key]; !exists {
					t.Errorf("missing envelope field %q: %s", key, envelope)
				}
			}
			if string(envelope["ok"]) != strconv.FormatBool(test.code == 0) || string(envelope["exit_code"]) != strconv.Itoa(test.code) {
				t.Fatalf("inconsistent envelope: %s", envelope)
			}
			if test.code == 0 && string(envelope["error"]) != "null" {
				t.Fatalf("successful error must be null: %s", envelope)
			}
			if test.kind != "" {
				var detail struct{ Kind, Message string }
				if err := json.Unmarshal(envelope["error"], &detail); err != nil || detail.Kind != test.kind || detail.Message == "" {
					t.Fatalf("error detail = %+v, %v", detail, err)
				}
				if stderr.Len() == 0 {
					t.Fatal("process error was not written to stderr")
				}
			}
		})
	}
}
