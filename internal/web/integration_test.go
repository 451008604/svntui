package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"svntui/internal/appdirs"
	"svntui/internal/config"
	"svntui/internal/domain"
	"svntui/internal/operation"
	"svntui/internal/safepoint"
	"svntui/internal/svn"
	"svntui/internal/wclock"
)

type fixture struct {
	t              *testing.T
	root, repo, wc string
	opts           Options
	s              *Server
	cookie         *http.Cookie
	csrf, id       string
}

func command(t *testing.T, dir, binary string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", binary, args, err, output)
	}
	return string(output)
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	for _, binary := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skip(binary + " unavailable")
		}
	}
	root := t.TempDir()
	f := &fixture{t: t, root: root, repo: filepath.Join(root, "repo"), wc: filepath.Join(root, "wc")}
	command(t, root, "svnadmin", "create", f.repo)
	repositoryURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(f.repo)}).String()
	command(t, root, "svn", "checkout", repositoryURL, f.wc)
	f.write("a.txt", "base a\n")
	f.write("b.txt", "base b\n")
	command(t, f.wc, "svn", "add", "a.txt", "b.txt")
	command(t, f.wc, "svn", "commit", "-m", "initial", "--username", "test")
	command(t, f.wc, "svn", "update")
	dirs := appdirs.Dirs{Config: filepath.Join(root, "config"), State: filepath.Join(root, "state"), Cache: filepath.Join(root, "cache")}
	if err := dirs.Ensure(); err != nil {
		t.Fatal(err)
	}
	recorder := operation.Recorder{Path: filepath.Join(dirs.State, "operations.jsonl")}
	client := svn.NewClient()
	client.Observer = recorder.Observe
	store := config.NewStore(dirs)
	if err := store.Update(func(cfg *config.Config) error {
		return cfg.AddProject(config.Project{Name: "测试项目", Path: f.wc})
	}); err != nil {
		t.Fatal(err)
	}
	f.opts = Options{Client: client, ConfigStore: store, SafePoints: safepoint.New(dirs, client), Operations: recorder, LockDir: filepath.Join(dirs.State, "working-copy-locks"), StateDir: dirs.State, Version: "0.3.0", Origin: "http://127.0.0.1:39811", Assets: fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html><h1>SVN</h1>")}}}
	f.restart()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := f.s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	canonicalWC, err := canonical(f.wc)
	if err != nil {
		t.Fatal(err)
	}
	f.id = projectID(canonicalWC)
	return f
}
func (f *fixture) write(path, text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.wc, path), []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}
func (f *fixture) restart() {
	f.t.Helper()
	if f.s != nil {
		if err := f.s.Close(context.Background()); err != nil {
			f.t.Fatal(err)
		}
	}
	server, err := New(f.opts)
	if err != nil {
		f.t.Fatal(err)
	}
	f.s = server
	f.cookie = nil
	f.csrf = ""
	result := f.call("POST", "/api/v1/session", map[string]any{"token": f.s.startup}, nil)
	if result.Code != 200 {
		f.t.Fatalf("bootstrap: %s", result.Body.String())
	}
	f.cookie = result.Result().Cookies()[0]
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	readData(f.t, result, &session)
	f.csrf = session.CSRF
}
func (f *fixture) path(suffix string) string { return "/api/v1/projects/" + f.id + suffix }
func (f *fixture) call(method, path string, body any, alter func(*http.Request)) *httptest.ResponseRecorder {
	f.t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			f.t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, f.opts.Origin+path, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", f.opts.Origin)
	if f.cookie != nil {
		request.AddCookie(f.cookie)
	}
	if f.csrf != "" {
		request.Header.Set("X-CSRF-Token", f.csrf)
	}
	if alter != nil {
		alter(request)
	}
	response := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(response, request)
	return response
}
func readData(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()
	var result struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *apiError       `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	if !result.OK {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(result.Data, target); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) preview(input PreviewRequest) Preview {
	f.t.Helper()
	response := f.call("POST", f.path("/previews"), input, nil)
	var result Preview
	readData(f.t, response, &result)
	return result
}
func (f *fixture) execute(preview Preview) *httptest.ResponseRecorder {
	return f.call("POST", f.path("/operations"), map[string]any{"preview_id": preview.ID, "confirm": true, "confirmation": "YES"}, nil)
}
func (f *fixture) waitJob(id string) Job {
	f.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response := f.call("GET", "/api/v1/jobs/"+id, nil, nil)
		var job Job
		readData(f.t, response, &job)
		if job.State != "running" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatal("job timeout")
	return Job{}
}
func requireStatus(t *testing.T, response *httptest.ResponseRecorder, want int) {
	t.Helper()
	if response.Code != want {
		t.Fatalf("HTTP %d, want %d: %s", response.Code, want, response.Body.String())
	}
}

func TestSessionOriginCSRFAndUnknownAPI(t *testing.T) {
	f := newFixture(t)
	requireStatus(t, f.call("POST", "/api/v1/session", map[string]any{"token": f.s.startup}, nil), 401)
	if !f.cookie.HttpOnly || f.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %+v", f.cookie)
	}
	if strings.Contains(f.s.BootstrapURL(), "?token=") {
		t.Fatal("secret in query")
	}
	cases := []struct {
		name   string
		alter  func(*http.Request)
		status int
	}{
		{"cookie", func(r *http.Request) { r.Header.Del("Cookie") }, 401},
		{"csrf", func(r *http.Request) { r.Header.Del("X-CSRF-Token") }, 403},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "http://evil.invalid") }, 403},
		{"missing origin", func(r *http.Request) { r.Header.Del("Origin") }, 403},
		{"host", func(r *http.Request) { r.Host = "evil.invalid" }, 403},
		{"fetch site", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireStatus(t, f.call("POST", f.path("/previews"), PreviewRequest{Action: "cleanup"}, tc.alter), tc.status)
		})
	}
	requireStatus(t, f.call("GET", "/api/v1/not-a-route", nil, nil), 404)
	requireStatus(t, f.call("GET", "/missing.js", nil, nil), 404)
	requireStatus(t, f.call("GET", "/projects/current", nil, nil), 200)
	requireStatus(t, f.call("POST", f.path("/previews"), map[string]any{"action": "cleanup", "command": "rm -rf /"}, nil), 400)
	requireStatus(t, f.call("POST", f.path("/previews"), PreviewRequest{Action: "shell"}, nil), 400)
}

func TestSelectedCommitDuplicateExecutionAndDraftRevision(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "selected\n")
	f.write("b.txt", "unselected\n")
	var draft Draft
	readData(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{"a.txt"}, Message: "selected only", Revision: 0}, nil), &draft)
	preview := f.preview(PreviewRequest{Action: "commit", Targets: []string{"a.txt"}, Message: "selected only"})
	var wg sync.WaitGroup
	responses := make([]*httptest.ResponseRecorder, 8)
	for i := range responses {
		wg.Add(1)
		go func() { defer wg.Done(); responses[i] = f.execute(preview) }()
	}
	wg.Wait()
	var id string
	for _, response := range responses {
		requireStatus(t, response, 202)
		var job Job
		readData(t, response, &job)
		if id != "" && job.ID != id {
			t.Fatal("duplicate jobs")
		}
		id = job.ID
	}
	job := f.waitJob(id)
	if job.State != "succeeded" {
		t.Fatalf("commit: %+v", job)
	}
	entries, err := f.opts.Client.Status(t.Context(), f.wc, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Path != "b.txt" {
		t.Fatalf("unselected change committed: %+v", entries)
	}
	log, err := f.opts.Client.Log(t.Context(), f.wc, "HEAD:0", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0].Message != "selected only" {
		t.Fatalf("repeated commit: %+v", log)
	}
	requireStatus(t, f.call("PUT", f.path("/draft"), draft, nil), 409)
	var empty Draft
	readData(t, f.call("GET", f.path("/draft"), nil, nil), &empty)
	if empty.Message != "" || len(empty.Targets) != 0 || empty.Revision <= draft.Revision {
		t.Fatalf("uncleared draft: %+v", empty)
	}
	f.restart()
	readData(t, f.call("GET", f.path("/draft"), nil, nil), &empty)
	if empty.Revision <= draft.Revision {
		t.Fatalf("draft revision not persistent: %+v", empty)
	}
	var jobs []Job
	readData(t, f.call("GET", "/api/v1/jobs", nil, nil), &jobs)
	if len(jobs) != 0 {
		t.Fatal("replayed jobs after restart")
	}
}

func TestDraftRestartConflictAndStatusSelection(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "draft\n")
	var saved Draft
	readData(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{"a.txt"}, Message: "persist", Revision: 0}, nil), &saved)
	requireStatus(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{"b.txt"}, Message: "old tab", Revision: 0}, nil), 409)
	f.opts.Origin = "http://127.0.0.1:39812"
	f.restart()
	var draft Draft
	readData(t, f.call("GET", f.path("/draft"), nil, nil), &draft)
	if draft.Message != "persist" || !slices.Equal(draft.Targets, []string{"a.txt"}) || draft.Revision != saved.Revision {
		t.Fatalf("lost draft: %+v", draft)
	}
	f.write("b.txt", "new unrelated change\n")
	command(t, f.wc, "svn", "revert", "a.txt")
	readData(t, f.call("GET", f.path("/draft"), nil, nil), &draft)
	if len(draft.Targets) != 0 || draft.Message != "persist" {
		t.Fatalf("draft selected new changes: %+v", draft)
	}
}

func TestStalePreviewIncludesBinaryPropertiesAndSchedules(t *testing.T) {
	for _, kind := range []string{"binary", "property", "schedule"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			f.write("a.txt", "first\n")
			preview := f.preview(PreviewRequest{Action: "commit", Targets: []string{"a.txt"}, Message: "must not commit"})
			switch kind {
			case "binary":
				f.write("a.txt", "\x00next\x00")
			case "property":
				command(t, f.wc, "svn", "propset", "custom:key", "changed", "a.txt")
			case "schedule":
				command(t, f.wc, "svn", "delete", "b.txt")
			}
			response := f.execute(preview)
			requireStatus(t, response, 409)
			if !strings.Contains(response.Body.String(), "stale_preview") {
				t.Fatal(response.Body.String())
			}
			var jobs []Job
			readData(t, f.call("GET", "/api/v1/jobs", nil, nil), &jobs)
			if len(jobs) != 0 {
				t.Fatal("stale preview created job")
			}
		})
	}
}

func TestSharedLockAndPathContainment(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "change\n")
	preview := f.preview(PreviewRequest{Action: "commit", Targets: []string{"a.txt"}, Message: "locked"})
	lock, err := wclock.Acquire(f.opts.LockDir, f.wc, "other process")
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.execute(preview), 409)
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(f.root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("never reveal this"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(f.wc, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, target := range []string{"../outside/secret", outside, ".svn/wc.db", "link/secret", "a/../../secret"} {
		response := f.call("GET", f.path("/diff")+"?target="+url.QueryEscape(target), nil, nil)
		if response.Code < 400 {
			t.Fatalf("unsafe path %s: %s", target, response.Body.String())
		}
	}
	response := f.call("GET", f.path("/diff")+"?target=link", nil, nil)
	requireStatus(t, response, 200)
	if strings.Contains(response.Body.String(), "never reveal") {
		t.Fatal("symlink target read")
	}
	requireStatus(t, f.call("POST", f.path("/previews"), PreviewRequest{Action: "move", Targets: []string{"a.txt"}, NewPath: "link/secret"}, nil), 403)
}

func TestHistoryProjectsAndScopedAudit(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "second\n")
	p := f.preview(PreviewRequest{Action: "commit", Targets: []string{"a.txt"}, Message: "history needle"})
	var job Job
	readData(t, f.execute(p), &job)
	if got := f.waitJob(job.ID); got.State != "succeeded" {
		t.Fatalf("%+v", got)
	}
	var history History
	readData(t, f.call("GET", f.path("/history")+"?limit=1&search=needle", nil, nil), &history)
	if len(history.Entries) != 1 || history.Entries[0].Message != "history needle" || !history.HasMore {
		t.Fatalf("history %+v", history)
	}
	var next History
	readData(t, f.call("GET", f.path("/history")+fmt.Sprintf("?before_revision=%d", history.NextRevision), nil, nil), &next)
	if len(next.Entries) != 1 || next.Entries[0].Message != "initial" {
		t.Fatalf("cursor %+v", next)
	}
	response := f.call("GET", f.path(fmt.Sprintf("/history/%d/diff", history.Entries[0].Revision)), nil, nil)
	requireStatus(t, response, 200)
	if !strings.Contains(response.Body.String(), "second") {
		t.Fatal(response.Body.String())
	}
	requireStatus(t, f.call("GET", f.path("/history")+"?target="+url.QueryEscape("../outside"), nil, nil), 400)
	var projects []Project
	readData(t, f.call("GET", "/api/v1/projects", nil, nil), &projects)
	if len(projects) != 1 || projects[0].ID != f.id {
		t.Fatalf("projects %+v", projects)
	}
	if err := f.opts.Operations.Append(operation.Record{ID: "foreign", Directory: f.root, Arguments: []string{"commit"}}); err != nil {
		t.Fatal(err)
	}
	var records []operation.Record
	readData(t, f.call("GET", f.path("/operations"), nil, nil), &records)
	if slices.ContainsFunc(records, func(r operation.Record) bool { return r.ID == "foreign" }) {
		t.Fatal("foreign audit exposed")
	}
	requireStatus(t, f.call("DELETE", f.path(""), map[string]any{}, nil), 200)
	if _, err := os.Stat(f.wc); err != nil {
		t.Fatal("removing project removed WC")
	}
}

func TestConflictArtifactsResolveAndSafePointRestore(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "saved local change\n")
	manifest, err := f.opts.SafePoints.Create(t.Context(), f.wc, safepoint.CreateOptions{Reason: "web restore"})
	if err != nil {
		t.Fatal(err)
	}
	command(t, f.wc, "svn", "revert", "a.txt")
	restore := f.preview(PreviewRequest{Action: "restore", SafePointID: manifest.ID})
	var job Job
	readData(t, f.execute(restore), &job)
	if got := f.waitJob(job.ID); got.State != "succeeded" {
		t.Fatalf("restore %+v", got)
	}
	content, err := os.ReadFile(filepath.Join(f.wc, "a.txt"))
	if err != nil || string(content) != "saved local change\n" {
		t.Fatalf("restore content %s / %v", content, err)
	}
	// 另一个 WC 的快照不能通过当前项目列出或恢复。
	peer := filepath.Join(f.root, "peer")
	command(t, f.root, "svn", "checkout", (&url.URL{Scheme: "file", Path: filepath.ToSlash(f.repo)}).String(), peer)
	if err := os.WriteFile(filepath.Join(peer, "b.txt"), []byte("peer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign, err := f.opts.SafePoints.Create(t.Context(), peer, safepoint.CreateOptions{Reason: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, f.call("GET", f.path("/safepoints/")+foreign.ID, nil, nil), 404)
	var manifests []safepoint.Manifest
	readData(t, f.call("GET", f.path("/safepoints"), nil, nil), &manifests)
	if len(manifests) != 1 || manifests[0].ID != manifest.ID {
		t.Fatalf("scope %+v", manifests)
	}
	requireStatus(t, f.call("POST", f.path("/previews"), PreviewRequest{Action: "restore", SafePointID: foreign.ID}, nil), 404)
	// 真实 update 生成文本冲突，验证两侧制品和服务的冲突标记保护。
	command(t, peer, "svn", "commit", "-m", "remote edit")
	f.write("b.txt", "local edit\n")
	command(t, f.wc, "svn", "update", "--accept", "postpone")
	var conflicts []Conflict
	readData(t, f.call("GET", f.path("/conflicts"), nil, nil), &conflicts)
	if len(conflicts) != 1 || conflicts[0].Path != "b.txt" || !strings.Contains(conflicts[0].Mine, "local edit") || !strings.Contains(conflicts[0].Theirs, "peer") {
		t.Fatalf("conflict %+v", conflicts)
	}
	resolve := f.preview(PreviewRequest{Action: "resolve", Targets: []string{"b.txt"}, Accept: "working"})
	readData(t, f.execute(resolve), &job)
	if got := f.waitJob(job.ID); got.State != "failed" {
		t.Fatalf("marked conflict resolved: %+v", got)
	}
	resolve = f.preview(PreviewRequest{Action: "resolve", Targets: []string{"b.txt"}, Accept: "theirs-full"})
	readData(t, f.execute(resolve), &job)
	if got := f.waitJob(job.ID); got.State != "succeeded" {
		t.Fatalf("resolve %+v", got)
	}
	readData(t, f.call("GET", f.path("/conflicts"), nil, nil), &conflicts)
	if len(conflicts) != 0 {
		t.Fatal("conflict remains")
	}
}

func TestDailyMutationsAndHighRiskConfirmation(t *testing.T) {
	f := newFixture(t)
	apply := func(input PreviewRequest) Job {
		t.Helper()
		preview := f.preview(input)
		if preview.Risk == "high" {
			requireStatus(t, f.call("POST", f.path("/operations"), map[string]any{"preview_id": preview.ID, "confirm": true}, nil), 400)
		}
		var job Job
		readData(t, f.execute(preview), &job)
		job = f.waitJob(job.ID)
		if job.State != "succeeded" {
			t.Fatalf("%s: %+v", input.Action, job)
		}
		return job
	}
	f.write("new.txt", "new\n")
	apply(PreviewRequest{Action: "add", Targets: []string{"new.txt"}})
	apply(PreviewRequest{Action: "move", Targets: []string{"new.txt"}, NewPath: "moved.txt"})
	apply(PreviewRequest{Action: "changelist", Targets: []string{"moved.txt"}, Changelist: "ready"})
	apply(PreviewRequest{Action: "commit", Changelist: "ready", Message: "group selection"})
	f.write("moved.txt", "discard me\n")
	job := apply(PreviewRequest{Action: "revert", Targets: []string{"moved.txt"}})
	if job.SafePointID == "" {
		t.Fatal("revert did not expose automatic SafePoint")
	}
	content, err := os.ReadFile(filepath.Join(f.wc, "moved.txt"))
	if err != nil || string(content) != "new\n" {
		t.Fatalf("revert %s %v", content, err)
	}
	apply(PreviewRequest{Action: "delete", Targets: []string{"moved.txt"}})
	apply(PreviewRequest{Action: "revert", Targets: []string{"moved.txt"}})
	apply(PreviewRequest{Action: "cleanup"})
	update := f.preview(PreviewRequest{Action: "update"})
	if !strings.Contains(update.Output, "预览") && !strings.Contains(update.Output, "HEAD") {
		t.Fatalf("missing update dry-run: %+v", update)
	}
	var updateJob Job
	readData(t, f.execute(update), &updateJob)
	if job := f.waitJob(updateJob.ID); job.State != "succeeded" {
		t.Fatalf("update %+v", job)
	}
}

func TestPropertyAndTreeConflictRestrictions(t *testing.T) {
	for _, kind := range []string{"property", "tree"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			peer := filepath.Join(f.root, "peer")
			command(t, f.root, "svn", "checkout", (&url.URL{Scheme: "file", Path: filepath.ToSlash(f.repo)}).String(), peer)
			if kind == "property" {
				command(t, f.wc, "svn", "propset", "custom:conflict", "local", "a.txt")
				command(t, peer, "svn", "propset", "custom:conflict", "remote", "a.txt")
			} else {
				f.write("a.txt", "local modified\n")
				command(t, peer, "svn", "delete", "a.txt")
			}
			command(t, peer, "svn", "commit", "-m", kind+" conflict")
			command(t, f.wc, "svn", "update", "--accept", "postpone")
			var conflicts []Conflict
			readData(t, f.call("GET", f.path("/conflicts"), nil, nil), &conflicts)
			if len(conflicts) != 1 || !slices.Contains(conflicts[0].Kinds, kind) || !slices.Equal(conflicts[0].Accepts, []string{"working"}) {
				t.Fatalf("conflict restrictions %+v", conflicts)
			}
			requireStatus(t, f.call("POST", f.path("/previews"), PreviewRequest{Action: "resolve", Targets: []string{"a.txt"}, Accept: "theirs-full"}, nil), 400)
		})
	}
}

func TestFailedCommitPreservesDraft(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "pending\n")
	var draft Draft
	readData(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{"a.txt"}, Message: "retain on failure", Revision: 0}, nil), &draft)
	preview := f.preview(PreviewRequest{Action: "commit", Targets: []string{"a.txt"}, Message: "retain on failure"})
	// 远端提交不改变本地指纹，但会让实际提交因 out-of-date 失败。
	peer := filepath.Join(f.root, "peer")
	command(t, f.root, "svn", "checkout", (&url.URL{Scheme: "file", Path: filepath.ToSlash(f.repo)}).String(), peer)
	if err := os.WriteFile(filepath.Join(peer, "a.txt"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command(t, peer, "svn", "commit", "-m", "remote")
	var job Job
	readData(t, f.execute(preview), &job)
	job = f.waitJob(job.ID)
	if job.State != "failed" || job.Error == nil || job.Error.Code != "out_of_date" {
		t.Fatalf("failure classification %+v", job)
	}
	var after Draft
	readData(t, f.call("GET", f.path("/draft"), nil, nil), &after)
	if after.Message != draft.Message || after.Revision != draft.Revision {
		t.Fatalf("failed commit cleared draft: %+v", after)
	}
}

func TestWriteSurvivesDisconnectAndCloseWaits(t *testing.T) {
	f := newFixture(t)
	committed := make(chan struct{})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(unblock)
	baseObserver := f.opts.Client.Observer
	f.opts.Client.Observer = func(command svn.Command, result svn.Result, err error) {
		baseObserver(command, result, err)
		if len(command.Args) > 0 && command.Args[0] == "commit" {
			close(committed)
			<-release
		}
	}
	f.restart()
	f.write("a.txt", "background commit\n")
	first := f.preview(PreviewRequest{Action: "commit", Targets: []string{"a.txt"}, Message: "disconnect"})
	second := f.preview(PreviewRequest{Action: "cleanup"})
	ctx, cancel := context.WithCancel(t.Context())
	response := f.call("POST", f.path("/operations"), map[string]any{"preview_id": first.ID, "confirm": true}, func(r *http.Request) { *r = *r.WithContext(ctx) })
	var job Job
	readData(t, response, &job)
	cancel()
	select {
	case <-committed:
	case <-time.After(10 * time.Second):
		t.Fatal("commit did not run")
	}
	requireStatus(t, f.execute(second), 409)
	done := make(chan error, 1)
	go func() { done <- f.s.Close(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("Close returned during active write: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	unblock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not wait correctly")
	}
	f.s.mu.Lock()
	finished := f.s.jobs[job.ID]
	f.s.mu.Unlock()
	if finished.State != "succeeded" {
		t.Fatalf("HTTP cancellation canceled write: %+v", finished)
	}
	requireStatus(t, f.execute(second), 503)
}

type streamRecorder struct {
	headers http.Header
	chunks  chan string
}

func (r *streamRecorder) Header() http.Header         { return r.headers }
func (r *streamRecorder) WriteHeader(int)             {}
func (r *streamRecorder) Write(p []byte) (int, error) { r.chunks <- string(p); return len(p), nil }
func (r *streamRecorder) Flush()                      {}
func TestSSEJobDTOAndClose(t *testing.T) {
	f := newFixture(t)
	request := httptest.NewRequest("GET", f.opts.Origin+"/api/v1/events", nil).WithContext(t.Context())
	request.AddCookie(f.cookie)
	recorder := &streamRecorder{headers: make(http.Header), chunks: make(chan string, 16)}
	done := make(chan struct{})
	go func() { defer close(done); f.s.Handler().ServeHTTP(recorder, request) }()
	select {
	case <-recorder.chunks:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE did not connect")
	}
	if recorder.headers.Get("Content-Type") != "text/event-stream" {
		t.Fatal("incorrect SSE headers")
	}
	preview := f.preview(PreviewRequest{Action: "cleanup"})
	var job Job
	readData(t, f.execute(preview), &job)
	select {
	case chunk := <-recorder.chunks:
		if !strings.HasPrefix(chunk, "event: job\ndata: ") {
			t.Fatalf("invalid SSE %q", chunk)
		}
		line := strings.TrimSuffix(strings.TrimPrefix(chunk, "event: job\ndata: "), "\n\n")
		var event Job
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.ID != job.ID || event.ProjectID != f.id || event.Action != "cleanup" {
			t.Fatalf("event %s", line)
		}
		if !strings.Contains(line, `"error":null`) {
			t.Fatalf("job error shape %s", line)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no job SSE event")
	}
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not terminate SSE")
	}
}

func TestOutputLimitIsExplicitAndSpillRemoved(t *testing.T) {
	f := newFixture(t)
	var spill string
	baseObserver := f.opts.Client.Observer
	f.opts.Client.Observer = func(command svn.Command, result svn.Result, err error) {
		baseObserver(command, result, err)
		if result.OutputTruncated {
			spill = result.OutputPath
		}
	}
	f.restart()
	f.write("a.txt", strings.Repeat("long diff output\n", 100000))
	response := f.call("GET", f.path("/diff")+"?target=a.txt", nil, nil)
	requireStatus(t, response, 413)
	if !strings.Contains(response.Body.String(), "output_limit") {
		t.Fatal(response.Body.String())
	}
	if spill == "" {
		t.Fatal("test did not exercise spilling")
	}
	if _, err := os.Stat(spill); !os.IsNotExist(err) {
		t.Fatalf("spill leaked: %s %v", spill, err)
	}
}

func TestFingerprintScopesAndUpdateStaleContents(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "selected change\n")
	f.write("untracked.txt", "before\n")
	preview := f.preview(PreviewRequest{Action: "commit", Targets: []string{"a.txt"}, Message: "selected scope"})
	f.write("untracked.txt", "after!\n")
	var job Job
	readData(t, f.execute(preview), &job)
	if got := f.waitJob(job.ID); got.State != "succeeded" {
		t.Fatalf("unrelated content invalidated selected commit: %+v", got)
	}
	f.write("b.txt", "first local change\n")
	update := f.preview(PreviewRequest{Action: "update"})
	f.write("b.txt", "other local change\n")
	requireStatus(t, f.execute(update), 409)
	f.write("untracked.txt", strings.Repeat("x", 600*1024))
	requireStatus(t, f.call("GET", f.path("/diff")+"?target=untracked.txt", nil, nil), 413)
}

func TestPreconditionsHoldSharedLockAndVetoWrites(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "must remain local\n")
	veto := errors.New("veto mutation")
	called := 0
	check := func() error {
		called++
		lock, err := wclock.Acquire(f.opts.LockDir, f.wc, "probe precondition boundary")
		if lock != nil {
			_ = lock.Release()
		}
		if !errors.Is(err, wclock.ErrBusy) {
			t.Errorf("precondition ran outside shared lock: %v", err)
		}
		return veto
	}
	_, err := f.s.actions.ExecuteWithPrecondition(t.Context(), domain.ActionRequest{Action: domain.ActionCommit, WorkingCopy: f.wc, Targets: []string{"a.txt"}, Arguments: []string{"vetoed commit"}}, check)
	if !errors.Is(err, veto) {
		t.Fatalf("commit ignored veto: %v", err)
	}
	manifest, err := f.opts.SafePoints.Create(t.Context(), f.wc, safepoint.CreateOptions{Reason: "boundary test"})
	if err != nil {
		t.Fatal(err)
	}
	f.write("a.txt", "newer local state\n")
	_, err = f.opts.SafePoints.RestoreWithPrecondition(t.Context(), manifest.ID, f.wc, safepoint.RestoreOptions{CreateBackup: true}, check)
	if !errors.Is(err, veto) {
		t.Fatalf("restore ignored veto: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(f.wc, "a.txt"))
	if err != nil || string(content) != "newer local state\n" || called != 2 {
		t.Fatalf("veto mutated working copy: %q / %v; callbacks=%d", content, err, called)
	}
	log, err := f.opts.Client.Log(t.Context(), f.wc, "HEAD:0", 10)
	if err != nil || len(log) != 1 {
		t.Fatalf("veto committed: %+v %v", log, err)
	}
}
