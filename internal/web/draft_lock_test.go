package web

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"svntui/internal/svn"
	"svntui/internal/wclock"
)

func sharedDraftServer(t *testing.T, f *fixture) *fixture {
	t.Helper()
	return sharedDraftServerAt(t, f, f.opts.StateDir)
}

func sharedDraftServerAt(t *testing.T, f *fixture, stateDir string) *fixture {
	t.Helper()
	peer := &fixture{t: t, root: f.root, wc: f.wc, id: f.id, opts: f.opts}
	peer.opts.StateDir = stateDir
	peer.opts.Origin = "http://127.0.0.1:39812"
	peer.restart()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := peer.s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return peer
}

func TestDraftCompareAndSaveAcrossServers(t *testing.T) {
	f := newFixture(t)
	peer := sharedDraftServer(t, f)
	f.write("a.txt", "draft candidate\n")
	var revision int64
	for round := range 12 {
		start := make(chan struct{})
		results := make(chan *httptest.ResponseRecorder, 2)
		for index, server := range []*fixture{f, peer} {
			go func() {
				<-start
				results <- server.call("PUT", server.path("/draft"), Draft{Targets: []string{"a.txt"}, Message: strings.Repeat(string(rune('A'+index)), 350000), Revision: revision}, nil)
			}()
		}
		close(start)
		first, second := <-results, <-results
		if first.Code != 200 {
			first, second = second, first
		}
		if first.Code != 200 || second.Code != 409 {
			t.Fatalf("round %d expected one accepted save and one conflict; got HTTP %d and %d for revision %d", round, first.Code, second.Code, revision)
		}
		var saved, loaded Draft
		readData(t, first, &saved)
		readData(t, peer.call("GET", peer.path("/draft"), nil, nil), &loaded)
		if saved.Revision != revision+1 || loaded.Revision != saved.Revision || loaded.Message != saved.Message {
			t.Fatalf("round %d acknowledged draft lost: saved revision %d, loaded revision %d", round, saved.Revision, loaded.Revision)
		}
		revision = saved.Revision
	}
}

func TestCommitTombstoneWaitsForSharedDraftTransaction(t *testing.T) {
	f := newFixture(t)
	committed := make(chan struct{})
	baseObserver := f.opts.Client.Observer
	f.opts.Client.Observer = func(command svn.Command, result svn.Result, err error) {
		baseObserver(command, result, err)
		if len(command.Args) > 0 && command.Args[0] == "commit" && err == nil {
			close(committed)
		}
	}
	f.restart()
	peer := sharedDraftServer(t, f)
	f.write("a.txt", "shared draft lock commit\n")
	var before Draft
	readData(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{"a.txt"}, Message: "commit draft", Revision: 0}, nil), &before)
	preview := f.preview(PreviewRequest{Action: "commit", Targets: before.Targets, Message: before.Message})
	project, err := peer.s.project(peer.id)
	if err != nil {
		t.Fatal(err)
	}
	// 独立服务持有草稿事务时，SVN 提交仍可执行，清空必须等待此锁。
	lock, err := peer.s.lockDraft(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	var job Job
	readData(t, f.execute(preview), &job)
	select {
	case <-committed:
	case <-time.After(5 * time.Second):
		t.Fatal("SVN commit blocked by draft lock")
	}
	// 清空等待期间不持有工作副本锁，避免反向锁顺序和阻塞其他 SVN 操作。
	wcLock, err := wclock.Acquire(f.opts.LockDir, f.wc, "test draft wait")
	if err != nil {
		// observer 在 SVN 返回前触发，等待执行函数释放锁。
		deadline := time.Now().Add(time.Second)
		for err != nil && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
			wcLock, err = wclock.Acquire(f.opts.LockDir, f.wc, "test draft wait")
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = wcLock.Release()
	var waiting Job
	readData(t, f.call("GET", "/api/v1/jobs/"+job.ID, nil, nil), &waiting)
	if waiting.State != "running" {
		t.Fatalf("commit cleared draft outside shared transaction: %+v", waiting)
	}
	// 同时到达的旧保存无论先后获取锁，最终都不能覆盖提交清空的版本。
	pending := make(chan *httptest.ResponseRecorder, 1)
	go func() { pending <- peer.call("PUT", peer.path("/draft"), before, nil) }()
	_ = lock.Release()
	response := <-pending
	if response.Code != 200 && response.Code != 409 {
		t.Fatalf("unexpected queued save: %d %s", response.Code, response.Body.String())
	}
	job = f.waitJob(job.ID)
	if job.State != "succeeded" {
		t.Fatalf("commit failed: %+v", job)
	}
	var after Draft
	readData(t, peer.call("GET", peer.path("/draft"), nil, nil), &after)
	if after.Message != "" || len(after.Targets) != 0 || after.Revision <= before.Revision {
		t.Fatalf("precommit save repopulated cleared draft: %+v", after)
	}
	requireStatus(t, peer.call("PUT", peer.path("/draft"), before, nil), 409)
	peer.restart()
	readData(t, peer.call("GET", peer.path("/draft"), nil, nil), &after)
	if after.Message != "" || after.Revision <= before.Revision {
		t.Fatalf("tombstone lost on peer restart: %+v", after)
	}
}

func TestDelayedPrecommitDraftRejectedAcrossServers(t *testing.T) {
	f := newFixture(t)
	f.write("a.txt", "delayed precommit save\n")
	var before Draft
	readData(t, f.call("PUT", f.path("/draft"), Draft{Targets: []string{"a.txt"}, Message: "commit before delayed save", Revision: 0}, nil), &before)
	peer := sharedDraftServer(t, f)
	arrived, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	pause := sync.OnceFunc(func() { close(arrived); <-release })
	baseObserver := peer.opts.Client.Observer
	peer.opts.Client.Observer = func(command svn.Command, result svn.Result, err error) {
		baseObserver(command, result, err)
		if len(command.Args) > 0 && command.Args[0] == "info" {
			pause()
		}
	}
	peer.restart()
	pending := make(chan *httptest.ResponseRecorder, 1)
	go func() { pending <- peer.call("PUT", peer.path("/draft"), before, nil) }()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		unblock()
		t.Fatal("delayed save did not arrive")
	}
	preview := f.preview(PreviewRequest{Action: "commit", Targets: before.Targets, Message: before.Message})
	var job Job
	readData(t, f.execute(preview), &job)
	job = f.waitJob(job.ID)
	unblock()
	requireStatus(t, <-pending, 409)
	if job.State != "succeeded" {
		t.Fatalf("commit failed: %+v", job)
	}
	var after Draft
	readData(t, peer.call("GET", peer.path("/draft"), nil, nil), &after)
	if after.Message != "" || after.Revision <= before.Revision {
		t.Fatalf("delayed save restored committed content: %+v", after)
	}
}

func TestDraftLockWaitHonorsCancellation(t *testing.T) {
	f := newFixture(t)
	peer := sharedDraftServer(t, f)
	project, err := f.s.project(f.id)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := f.s.lockDraft(t.Context(), project)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if other, err := peer.s.lockDraft(ctx, project); !errors.Is(err, context.Canceled) || other != nil {
		t.Fatalf("canceled lock wait: lock=%v error=%v", other, err)
	}
	ctx, cancel = context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	other, err := peer.s.lockDraft(ctx, project)
	if other != nil || err == nil || errorInfo(err).Code != "draft_busy" {
		t.Fatalf("contended lock deadline must be retryable: lock=%v error=%v", other, err)
	}
}
