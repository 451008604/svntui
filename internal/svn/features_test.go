package svn

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func newFeatureRepository(t *testing.T) (Client, string, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("local SVN integration")
	}
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s unavailable", binary)
		}
	}
	root := t.TempDir()
	repository, wc := filepath.Join(root, "repo"), filepath.Join(root, "wc@")
	runTestCommand(t, root, "svnadmin", "create", repository)
	client := NewClient()
	url := fileURL(repository)
	if _, err := client.Checkout(t.Context(), url, wc, "", ""); err != nil {
		t.Fatal(err)
	}
	return client, wc, url
}

func writeFeatureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLiteralTargetsAndSelectedCommit(t *testing.T) {
	client, wc, url := newFeatureRepository(t)
	for _, name := range []string{"foo", "foo@", "foo@123"} {
		writeFeatureFile(t, filepath.Join(wc, name), "base "+name+"\n")
	}
	if _, err := client.Add(t.Context(), wc, []string{"foo", "foo@", "foo@123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Commit(t.Context(), wc, []string{"foo", "foo@", "foo@123"}, "initial"); err != nil {
		t.Fatal(err)
	}
	writeFeatureFile(t, filepath.Join(wc, "foo"), "unselected\n")
	writeFeatureFile(t, filepath.Join(wc, "foo@"), "selected\n")
	if _, err := client.Commit(t.Context(), wc, []string{"foo@"}, "selected at-sign"); err != nil {
		t.Fatal(err)
	}
	entries, err := client.Status(t.Context(), wc, false, false)
	if err != nil || len(entries) != 1 || entries[0].Path != "foo" {
		t.Fatalf("selected commit affected wrong file: %+v %v", entries, err)
	}
	content, err := client.Cat(t.Context(), filepath.Join(wc, "foo@"), "BASE")
	if err != nil || content != "selected\n" {
		t.Fatalf("cat literal: %q %v", content, err)
	}
	info, err := client.Info(t.Context(), filepath.Join(wc, "foo@"))
	if err != nil || info.Revision != 2 {
		t.Fatalf("info literal: %+v %v", info, err)
	}
	diff, err := client.DiffRevisions(t.Context(), wc, "foo@", "1", "2")
	if err != nil || !strings.Contains(diff, "+selected") {
		t.Fatalf("revision diff: %s %v", diff, err)
	}
	// URL 的显式 peg 仍指向第一版。
	content, err = client.Cat(t.Context(), url+"/foo@@1", "1")
	if err != nil || content != "base foo@\n" {
		t.Fatalf("URL peg: %q %v", content, err)
	}
	if _, err := client.Move(t.Context(), wc, "foo@123", "moved@"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wc, "moved@")); err != nil {
		t.Fatal(err)
	}
	entries, err = client.Status(t.Context(), wc, false, false)
	if err != nil {
		t.Fatal(err)
	}
	moved := slices.IndexFunc(entries, func(entry StatusEntry) bool { return entry.Path == "moved@" })
	if moved < 0 || entries[moved].MovedFrom != "foo@123" {
		t.Fatalf("move tracking: %+v", entries)
	}
	if _, err := client.Revert(t.Context(), wc, []string{"foo", "foo@123", "moved@"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Copy(t.Context(), filepath.Join(wc, "foo@"), filepath.Join(wc, "copied@"), "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wc, "copied@")); err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(t.TempDir(), "export@")
	if _, err := client.Export(t.Context(), filepath.Join(wc, "foo@"), exportPath, "BASE", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(exportPath); err != nil {
		t.Fatal(err)
	}
}

func TestMissingDeletedHistoryAndReplacementBase(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	target := filepath.Join(wc, "file@")
	writeFeatureFile(t, target, "original\n")
	if _, err := client.Add(t.Context(), wc, []string{"file@"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Commit(t.Context(), wc, []string{"file@"}, "base"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"missing", "deleted"} {
		if state == "deleted" {
			if _, err := client.Delete(t.Context(), wc, []string{"file@"}); err != nil {
				t.Fatal(err)
			}
		}
		logs, err := client.Log(t.Context(), target, "", 10)
		if err != nil || len(logs) != 1 || logs[0].Message != "base" {
			t.Fatalf("%s history: %+v %v", state, logs, err)
		}
	}
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Add(t.Context(), wc, []string{"file@"}); err != nil {
		t.Fatal(err)
	}
	current, err := client.Info(t.Context(), target)
	if err != nil || current.Revision != 0 || current.Kind != "dir" {
		t.Fatalf("replacement current: %+v %v", current, err)
	}
	base, err := client.InfoAt(t.Context(), target, "BASE")
	if err != nil || base.Revision != 1 || base.Kind != "file" || base.WCAbsolute == "" {
		t.Fatalf("replacement BASE: %+v %v", base, err)
	}
}

func TestPropertiesChangelistsAndBlameContent(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	target := filepath.Join(wc, "notes@")
	writeFeatureFile(t, target, "first\nsecond\n")
	if _, err := client.Add(t.Context(), wc, []string{"notes@"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Commit(t.Context(), wc, []string{"notes@"}, "notes"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PropSet(t.Context(), wc, "custom:test", "a\nb\n", "notes@"); err != nil {
		t.Fatal(err)
	}
	properties, err := client.Properties(t.Context(), wc, "notes@")
	if err != nil || properties["custom:test"] != "a\nb\n" {
		t.Fatalf("properties: %#v %v", properties, err)
	}
	if _, err := client.Changelist(t.Context(), wc, "review", []string{"notes@"}, false); err != nil {
		t.Fatal(err)
	}
	entries, err := client.Status(t.Context(), wc, false, false)
	if err != nil || len(entries) != 1 || entries[0].Changelist != "review" || !entries[0].IsVersionedDirty() {
		t.Fatalf("changelist status: %+v %v", entries, err)
	}
	if _, err := client.DeleteProperty(t.Context(), wc, "custom:test", []string{"notes@"}); err != nil {
		t.Fatal(err)
	}
	properties, err = client.Properties(t.Context(), wc, "notes@")
	if err != nil || len(properties) != 0 {
		t.Fatalf("propdel: %#v %v", properties, err)
	}
	if _, err := client.Changelist(t.Context(), wc, "", []string{"notes@"}, true); err != nil {
		t.Fatal(err)
	}
	lines, err := client.BlameWithContent(t.Context(), target, "BASE")
	if err != nil || len(lines) != 2 || lines[0].Content != "first" || lines[1].Content != "second" {
		t.Fatalf("blame content: %+v %v", lines, err)
	}
}

func TestUpdateDepthRevisionAndPatch(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	directory := filepath.Join(wc, "dir@")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "file@")
	writeFeatureFile(t, target, "first\n")
	if _, err := client.Add(t.Context(), wc, []string{"dir@"}); err != nil {
		t.Fatal(err)
	}
	runTestCommand(t, wc, "svn", "commit", "-m", "nested")
	writeFeatureFile(t, target, "second\n")
	if _, err := client.Commit(t.Context(), wc, []string{"dir@/file@"}, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateWithOptions(t.Context(), wc, UpdateOptions{Targets: []string{"dir@/file@"}, Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "first\n" {
		t.Fatalf("update revision: %q %v", data, err)
	}
	if _, err := client.UpdateWithOptions(t.Context(), wc, UpdateOptions{Targets: []string{"dir@"}, Depth: "empty"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("set-depth retained child: %v", err)
	}
	info, err := client.Info(t.Context(), directory)
	if err != nil || info.Depth != "empty" {
		t.Fatalf("persistent depth: %+v %v", info, err)
	}
	if _, err := client.UpdateWithOptions(t.Context(), wc, UpdateOptions{Targets: []string{"dir@"}, Depth: "infinity", IgnoreExternals: true}); err != nil {
		t.Fatal(err)
	}
	writeFeatureFile(t, target, "patched\n")
	patchPath := filepath.Join(t.TempDir(), "change@.patch")
	if err := client.DiffToFile(t.Context(), wc, []string{"dir@/file@"}, patchPath); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Revert(t.Context(), wc, []string{"dir@/file@"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Patch(t.Context(), wc, patchPath, true); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "second\n" {
		t.Fatalf("dry-run changed file: %q %v", data, err)
	}
	if _, err := client.Patch(t.Context(), wc, patchPath, false); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(target)
	if err != nil || string(data) != "patched\n" {
		t.Fatalf("patch: %q %v", data, err)
	}
}

func TestLogPageTraversesEmptyFilteredPages(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	target := filepath.Join(wc, "history@")
	writeFeatureFile(t, target, "initial\n")
	if _, err := client.Add(t.Context(), wc, []string{"history@"}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"needle initial", "second", "third", "fourth"} {
		writeFeatureFile(t, target, message+"\n")
		if _, err := client.Commit(t.Context(), wc, []string{"history@"}, message); err != nil {
			t.Fatal(err)
		}
	}
	options := LogOptions{Limit: 2, Search: "needle"}
	first, err := client.LogWithOptions(t.Context(), target, options)
	if err != nil || len(first.Entries) != 0 || !first.HasMore || first.NextRevision != 2 {
		t.Fatalf("empty first page: %+v %v", first, err)
	}
	options.StartRevision = first.NextRevision
	second, err := client.LogWithOptions(t.Context(), target, options)
	if err != nil || len(second.Entries) != 1 || second.Entries[0].Revision != 1 || second.HasMore {
		t.Fatalf("second page: %+v %v", second, err)
	}
	page, err := client.LogWithOptions(t.Context(), target, LogOptions{RevisionRange: "2:3", Limit: 2})
	if err != nil || len(page.Entries) != 2 || page.Entries[0].Revision != 3 || page.HasMore {
		t.Fatalf("bounded range: %+v %v", page, err)
	}
	author := page.Entries[0].Author
	single, singleErr := client.LogWithOptions(t.Context(), target, LogOptions{RevisionRange: "HEAD", Limit: 1})
	if singleErr != nil || len(single.Entries) != 1 || single.HasMore {
		t.Fatalf("single HEAD page: %+v %v", single, singleErr)
	}
	ascending, ascendingErr := client.LogWithOptions(t.Context(), target, LogOptions{RevisionRange: "1:HEAD", Limit: 2})
	if ascendingErr != nil || len(ascending.Entries) != 2 || ascending.Entries[0].Revision != 4 || ascending.NextRevision != 2 {
		t.Fatalf("ascending symbolic range: %+v %v", ascending, ascendingErr)
	}

	page, err = client.LogWithOptions(t.Context(), target, LogOptions{Limit: 2, Author: author, Since: time.Now().Add(-time.Hour), Until: time.Now().Add(time.Hour)})
	if err != nil || len(page.Entries) != 2 {
		t.Fatalf("date/author filters: %+v %v", page, err)
	}
	page, err = client.LogWithOptions(t.Context(), target, LogOptions{Limit: 2, Author: "not-an-author"})
	if err != nil || len(page.Entries) != 0 || page.NextRevision != 2 {
		t.Fatalf("author filtering cursor: %+v %v", page, err)
	}
}

func TestOutputLimitAndFullPatchExport(t *testing.T) {
	client, wc, _ := newFeatureRepository(t)
	target := filepath.Join(wc, "large@")
	writeFeatureFile(t, target, "old\n")
	if _, err := client.Add(t.Context(), wc, []string{"large@"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Commit(t.Context(), wc, []string{"large@"}, "base"); err != nil {
		t.Fatal(err)
	}
	writeFeatureFile(t, target, strings.Repeat("new line\n", 1000)+"last marker\n")
	client.MaxOutputBytes = 128
	result, err := client.Run(t.Context(), wc, "diff", "--git", "--", "large@")
	if !errors.Is(err, ErrOutputLimit) || !result.OutputTruncated || result.OutputPath == "" || len(result.Stdout) > 128 {
		t.Fatalf("unbounded output: %+v %v", result, err)
	}
	t.Cleanup(func() { os.Remove(result.OutputPath) })
	full, err := os.ReadFile(result.OutputPath)
	if err != nil || !strings.HasSuffix(string(full), "+last marker\n") {
		t.Fatalf("spill lost output: %v", err)
	}
	patchPath := filepath.Join(t.TempDir(), "large.patch")
	if err := client.DiffToFile(t.Context(), wc, []string{"large@"}, patchPath); err != nil {
		t.Fatal(err)
	}
	patch, err := os.ReadFile(patchPath)
	if err != nil || string(patch) != string(full) {
		t.Fatalf("streamed patch differs: %v", err)
	}
	_, err = client.Info(t.Context(), target)
	var commandErr *CommandError
	if !errors.Is(err, ErrOutputLimit) || !errors.As(err, &commandErr) || commandErr.Kind != ErrorOutputLimit {
		t.Fatalf("XML overflow became parse error: %v", err)
	}
	if commandErr != nil && commandErr.Result.OutputPath != "" {
		t.Cleanup(func() { os.Remove(commandErr.Result.OutputPath) })
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := client.DiffToFile(ctx, wc, nil, patchPath); err == nil {
		t.Fatal("canceled export succeeded")
	}
	retained, err := os.ReadFile(patchPath)
	if err != nil || string(retained) != string(full) {
		t.Fatal("failed export replaced existing patch")
	}
}

func TestParseStatusChangelistAndOptionalInfoRevision(t *testing.T) {
	entries, err := ParseStatus([]byte(`<status><target path="."><entry path="old"><wc-status item="deleted" revision="3" moved-to="new"/></entry></target><changelist name="review"><entry path="new"><wc-status item="added" revision="-1" copied="true" moved-from="old"/></entry></changelist></status>`))
	if err != nil || len(entries) != 2 || entries[0].MovedTo != "new" || entries[1].MovedFrom != "old" || entries[1].Changelist != "review" {
		t.Fatalf("status fields: %+v %v", entries, err)
	}
	for _, revision := range []string{"Resource is not under version control.", "-1", ""} {
		info, err := parseInfo([]byte(`<info><entry kind="file" revision="` + revision + `"/></info>`))
		if err != nil || info.Revision != 0 {
			t.Fatalf("unversioned revision %q: %+v %v", revision, info, err)
		}
	}
	if _, err := parseInfo([]byte(`<info><entry revision="broken"/></info>`)); err == nil {
		t.Fatal("malformed revision accepted")
	}
	properties, err := parseProperties([]byte(`<properties><target><property name="binary" encoding="base64">//4=</property></target></properties>`))
	if err != nil || properties["binary"] != string([]byte{255, 254}) {
		t.Fatalf("binary property: %#v %v", properties, err)
	}
	for input, want := range map[string]string{"foo": "foo", "foo@": "foo@@", "foo@123": "foo@123@", "https://host/file@12": "https://host/file@12"} {
		if got := LiteralTarget(input); got != want {
			t.Errorf("LiteralTarget(%q)=%q want %q", input, got, want)
		}
	}
}
