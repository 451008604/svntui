package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"svntui/internal/svn"
)

func TestDiagnosticBudgetPreservesStagesAndUTF8(t *testing.T) {
	huge := strings.Repeat("错误<&\x00\n\xff", 100000)
	failure := &svn.CommandError{Kind: svn.ErrorConflict, Result: svn.Result{Stderr: huge, ExitCode: 1}, Err: errors.New("failed command")}
	info := errorInfo(errors.Join(stageError("SVN 写入失败", failure), stageError("草稿持久化失败", errors.New("disk full")), stageError("刷新状态失败", errors.New("status unavailable"))))
	if info.Code != "conflict" || info.status != 409 || info.exit != 1 {
		t.Fatalf("classification changed: %+v", info)
	}
	if !utf8.ValidString(info.Message) {
		t.Fatal("invalid diagnostic UTF-8")
	}
	encoded, err := json.Marshal(info.Message)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > diagnosticLimit {
		t.Fatalf("escaped diagnostic exceeded budget: %d", len(encoded))
	}
	for _, label := range []string{"SVN 写入失败", "草稿持久化失败", "刷新状态失败", "诊断已截断"} {
		if !strings.Contains(info.Message, label) {
			t.Fatalf("lost independent failure label %q", label)
		}
	}
	// 单独的 HTTP 错误也需要同样预算，不能绕过任务汇总路径重新返回完整 stderr。
	direct := errorInfo(failure)
	encoded, err = json.Marshal(direct.Message)
	if err != nil || len(encoded) > diagnosticLimit || !strings.Contains(direct.Message, "诊断已截断") {
		t.Fatalf("unbounded direct error: %d / %v", len(encoded), err)
	}
}

func TestRejectedCommitDiagnosticsBoundedInJobsAndSSE(t *testing.T) {
	f := newFixture(t)
	var rejected error
	baseObserver := f.opts.Client.Observer
	f.opts.Client.Observer = func(command svn.Command, result svn.Result, err error) {
		baseObserver(command, result, err)
		if len(command.Args) > 0 && command.Args[0] == "commit" {
			rejected = err
		}
	}
	f.restart()
	hookDirectory := filepath.Join(f.repo, "hooks")
	hookName := "pre-commit"
	hook := "#!/bin/sh\ncat \"$(dirname \"$0\")/rejection.txt\" >&2\nexit 1\n"
	if os.PathSeparator == '\\' {
		hookName += ".bat"
		hook = "@echo off\r\ntype \"%~dp0rejection.txt\" 1>&2\r\nexit /b 1\r\n"
	}
	stderr := "HOOK_REJECTED_REAL_COMMIT\n" + strings.Repeat("X", 2<<20)
	if err := os.WriteFile(filepath.Join(hookDirectory, "rejection.txt"), []byte(stderr), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hookDirectory, hookName), []byte(hook), 0o700); err != nil {
		t.Fatal(err)
	}
	f.write("a.txt", "hook must reject this change\n")
	preview := f.preview(PreviewRequest{Action: "commit", Targets: []string{"a.txt"}, Message: "reject with large stderr"})
	request := httptest.NewRequest("GET", f.opts.Origin+"/api/v1/events", nil).WithContext(t.Context())
	request.AddCookie(f.cookie)
	stream := &streamRecorder{headers: make(http.Header), chunks: make(chan string, 16)}
	streamDone := make(chan struct{})
	go func() { defer close(streamDone); f.s.Handler().ServeHTTP(stream, request) }()
	select {
	case <-stream.chunks:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE did not connect")
	}
	var accepted Job
	readData(t, f.execute(preview), &accepted)
	job := f.waitJob(accepted.ID)
	if job.State != "failed" || job.Error == nil {
		t.Fatalf("hook did not reject mutation: %+v", job)
	}
	if job.Error.Code != "output_limit" {
		t.Fatalf("output-limit category changed: %s", job.Error.Code)
	}
	if rejected == nil {
		t.Fatal("real commit failure not observed")
	}
	var commandErr *svn.CommandError
	if !errors.As(rejected, &commandErr) || len(commandErr.Result.Stderr) <= outputLimit {
		t.Fatal("hook did not exercise more than 1 MiB stderr")
	}
	if len(job.Output) > outputLimit || !strings.Contains(job.Output, "任务输出已截断") || !utf8.ValidString(job.Output) {
		t.Fatal("unbounded or invalid displayed output")
	}
	assertDiagnostic := func(value Job) {
		t.Helper()
		encoded, err := json.Marshal(value.Error.Message)
		if err != nil || len(encoded) > diagnosticLimit {
			t.Fatalf("diagnostic JSON exceeds budget: %d / %v", len(encoded), err)
		}
		for _, label := range []string{"SVN 写入失败", "HOOK_REJECTED_REAL_COMMIT", "诊断已截断", "任务输出超过"} {
			if !strings.Contains(value.Error.Message, label) {
				t.Fatalf("missing explicit failure/truncation label %q", label)
			}
		}
	}
	assertDiagnostic(job)
	const jobEnvelopeLimit = outputLimit + diagnosticLimit + 8192
	for _, path := range []string{"/api/v1/jobs", "/api/v1/jobs/" + job.ID} {
		response := f.call("GET", path, nil, nil)
		requireStatus(t, response, 200)
		if response.Body.Len() > jobEnvelopeLimit {
			t.Fatalf("%s serialized %d bytes", path, response.Body.Len())
		}
	}
	deadline := time.After(5 * time.Second)
	seenFailure := false
	for !seenFailure {
		select {
		case chunk := <-stream.chunks:
			if len(chunk) > jobEnvelopeLimit {
				t.Fatalf("SSE serialized %d bytes", len(chunk))
			}
			if !strings.HasPrefix(chunk, "event: job\ndata: ") {
				continue
			}
			var event Job
			if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(chunk, "event: job\ndata: "), "\n\n")), &event); err != nil {
				t.Fatal(err)
			}
			if event.State == "failed" {
				assertDiagnostic(event)
				seenFailure = true
			}
		case <-deadline:
			t.Fatal("missing failed job event")
		}
	}
	// 同一真实命令失败经公共 HTTP 错误输出时，也只能序列化受限诊断。
	directError := errorInfo(rejected)
	if directError.Code != string(commandErr.Kind) || directError.exit != commandErr.Result.ExitCode {
		t.Fatalf("SVN category/exit changed: %+v", directError)
	}
	response := httptest.NewRecorder()
	f.s.fail(response, rejected)
	if response.Body.Len() > diagnosticLimit+1024 || !strings.Contains(response.Body.String(), "诊断已截断") {
		t.Fatalf("unbounded API diagnostics: %d", response.Body.Len())
	}
	entries, err := f.opts.Client.Log(t.Context(), f.wc, "HEAD:0", 10)
	if err != nil || len(entries) != 1 {
		t.Fatalf("rejecting hook advanced repository: %d / %v", len(entries), err)
	}
	if err := f.s.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-streamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE did not stop")
	}
}
