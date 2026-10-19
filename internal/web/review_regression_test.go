package web

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"svntui/internal/storage"
	"svntui/internal/svn"
)

func TestNestedPathNormalizationRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"dir/nested/a.txt", "./dir/nested/a.txt", "dir/other/../nested/a.txt"} {
		normalized, err := safeTarget(root, input)
		if err != nil {
			t.Fatal(err)
		}
		if normalized != "dir/nested/a.txt" {
			t.Fatalf("API separator changed: %q", normalized)
		}
		for range 3 {
			next, err := safeTarget(root, normalized)
			if err != nil || next != normalized {
				t.Fatalf("normalization is not stable: %q => %q (%v)", normalized, next, err)
			}
			normalized = next
		}
		targets, err := normalizeTargets(root, []string{normalized})
		if err != nil || !slices.Equal(targets, []string{normalized}) {
			t.Fatalf("target round-trip: %+v %v", targets, err)
		}
	}
	for _, input := range []string{"../outside", `dir\nested\a.txt`, ".svn/wc.db", "dir/.svn/entries"} {
		if _, err := safeTarget(root, input); err == nil {
			t.Fatalf("unsafe path accepted: %q", input)
		}
	}
	// 原生 Windows 执行时额外验证驱动器、设备名和 ADS 不可经由斜杠形式绕过。
	if os.PathSeparator == '\\' {
		for _, input := range []string{"C:/outside", "C:relative", "//server/share/a", "dir/NUL", "dir/COM1.txt", "dir/file:stream"} {
			if _, err := safeTarget(root, input); err == nil {
				t.Fatalf("unsafe Windows path accepted: %q", input)
			}
		}
	}
}

func TestNestedCommitMoveAndDraftReload(t *testing.T) {
	f := newFixture(t)
	if err := os.MkdirAll(filepath.Join(f.wc, "nested", "deeper"), 0o700); err != nil {
		t.Fatal(err)
	}
	const path = "nested/deeper/a.txt"
	const moved = "nested/deeper/moved.txt"
	f.write(path, "nested base\n")
	command(t, f.wc, "svn", "add", "nested")
	command(t, f.wc, "svn", "commit", "-m", "nested baseline")
	command(t, f.wc, "svn", "update")
	f.write(path, "nested selected change\n")
	var draft Draft
	readData(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{path}, Message: "nested commit", Revision: 0}, nil), &draft)
	f.restart()
	var loaded Draft
	readData(t, f.call("GET", f.path("/draft"), nil, nil), &loaded)
	if !slices.Equal(loaded.Targets, []string{path}) || loaded.Revision != draft.Revision {
		t.Fatalf("nested draft lost on reload: %+v", loaded)
	}
	commit := f.preview(PreviewRequest{Action: "commit", Targets: loaded.Targets, Message: loaded.Message})
	if !slices.Equal(commit.Targets, []string{path}) {
		t.Fatalf("nested preview paths: %+v", commit.Targets)
	}
	var job Job
	readData(t, f.execute(commit), &job)
	if got := f.waitJob(job.ID); got.State != "succeeded" {
		t.Fatalf("nested commit: %+v", got)
	}
	move := f.preview(PreviewRequest{Action: "move", Targets: []string{path}, NewPath: moved})
	if !slices.Equal(move.Targets, []string{path, moved}) {
		t.Fatalf("nested move paths: %+v", move.Targets)
	}
	readData(t, f.execute(move), &job)
	if got := f.waitJob(job.ID); got.State != "succeeded" {
		t.Fatalf("nested second mutation: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(f.wc, filepath.FromSlash(moved))); err != nil {
		t.Fatal(err)
	}
	readData(t, f.call("GET", f.path("/draft"), nil, nil), &loaded)
	readData(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{moved}, Message: "nested move draft", Revision: loaded.Revision}, nil), &draft)
	f.restart()
	readData(t, f.call("GET", f.path("/draft"), nil, nil), &loaded)
	if !slices.Equal(loaded.Targets, []string{moved}) || loaded.Message != "nested move draft" {
		t.Fatalf("second nested draft lost: %+v", loaded)
	}
	persisted := storedDraft{}
	project, err := f.s.project(f.id)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.LoadJSON(f.s.draftPath(project), &persisted); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(persisted.Targets, []string{moved}) {
		t.Fatalf("native separators persisted: %+v", persisted.Targets)
	}
}

func TestSuccessfulCommitClearsDraftWhenRefreshFails(t *testing.T) {
	for _, persistFailure := range []bool{false, true} {
		name := "refresh_only"
		if persistFailure {
			name = "refresh_and_persistence"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			var server *Server
			baseObserver := f.opts.Client.Observer
			f.opts.Client.Observer = func(command svn.Command, result svn.Result, err error) {
				baseObserver(command, result, err)
				if len(command.Args) > 0 && command.Args[0] == "commit" && err == nil {
					// 真正提交成功后，后续 info/status 的 1 字节限制稳定触发刷新失败。
					server.opts.Client.MaxOutputBytes = 1
					if persistFailure {
						directory := filepath.Join(f.opts.StateDir, "web", "drafts")
						if err := os.Rename(directory, directory+".saved"); err != nil {
							t.Error(err)
						}
						if err := os.WriteFile(directory, []byte("unavailable draft directory"), 0o600); err != nil {
							t.Error(err)
						}
					}
				}
			}
			f.restart()
			server = f.s
			f.write("a.txt", "committed despite refresh failure\n")
			var before Draft
			readData(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{"a.txt"}, Message: "clear after real commit", Revision: 0}, nil), &before)
			preview := f.preview(PreviewRequest{Action: "commit", Targets: before.Targets, Message: before.Message})
			var job Job
			readData(t, f.execute(preview), &job)
			job = f.waitJob(job.ID)
			if job.State != "failed" || job.Error == nil || !strings.Contains(job.Error.Message, "刷新工作副本状态失败") {
				t.Fatalf("refresh failure not explicit: %+v", job)
			}
			entries, err := f.opts.Client.Log(t.Context(), f.wc, "HEAD:0", 10)
			if err != nil || len(entries) != 2 || entries[0].Revision != 2 || entries[0].Message != before.Message {
				t.Fatalf("repository did not advance: %+v / %v", entries, err)
			}
			server.opts.Client.MaxOutputBytes = svn.DefaultMaxOutputBytes
			if persistFailure {
				if !strings.Contains(job.Error.Message, "保存草稿清除状态失败") {
					t.Fatalf("persistence failure not explicit alongside refresh: %+v", job)
				}
				return
			}
			project, err := server.project(f.id)
			if err != nil {
				t.Fatal(err)
			}
			var persisted storedDraft
			if err := storage.LoadJSON(server.draftPath(project), &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted.Message != "" || len(persisted.Targets) != 0 || persisted.Revision <= before.Revision {
				t.Fatalf("successful commit retained old durable draft: %+v", persisted)
			}
			requireStatus(t, f.call("PUT", f.path("/draft"), before, nil), 409)
			f.restart()
			var after Draft
			readData(t, f.call("GET", f.path("/draft"), nil, nil), &after)
			if after.Revision != persisted.Revision || after.Message != "" {
				t.Fatalf("cleared draft did not survive restart: %+v", after)
			}
		})
	}
}

func TestDefaultSVNOutputCapAllowsLargeDiff(t *testing.T) {
	f := newFixture(t)
	if f.s.opts.Client.MaxOutputBytes != svn.DefaultMaxOutputBytes {
		t.Fatalf("default SVN output cap: %d", f.s.opts.Client.MaxOutputBytes)
	}
	f.write("a.txt", strings.Repeat("ordinary diff output\n", 80000))
	response := f.call("GET", f.path("/diff")+"?target=a.txt", nil, nil)
	requireStatus(t, response, 200)
	var diff struct {
		Text string `json:"text"`
	}
	readData(t, response, &diff)
	if len(diff.Text) <= outputLimit {
		t.Fatalf("did not exercise more than 1 MiB: %d", len(diff.Text))
	}
}
