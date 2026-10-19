package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDraftStateDirectoryAliases(t *testing.T) {
	for _, kind := range []string{"case", "symlink", "relative"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			var alias string
			switch kind {
			case "case":
				alias = filepath.Join(filepath.Dir(f.opts.StateDir), "STATE")
				original, err := os.Stat(f.opts.StateDir)
				if err != nil {
					t.Fatal(err)
				}
				other, err := os.Stat(alias)
				if os.IsNotExist(err) || (err == nil && !os.SameFile(original, other)) {
					t.Skip("state filesystem is case-sensitive")
				}
				if err != nil {
					t.Fatal(err)
				}
			case "symlink":
				alias = filepath.Join(f.root, "state-alias")
				if err := os.Symlink(f.opts.StateDir, alias); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			case "relative":
				// 两个独立服务使用不同路径表示，锁键不依赖启动时的绝对路径。
				t.Chdir(f.root)
				alias = "state"
			}
			peer := sharedDraftServerAt(t, f, alias)
			project, err := f.s.project(f.id)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := f.s.lockDraft(t.Context(), project)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Release()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			defer cancel()
			other, err := peer.s.lockDraft(ctx, project)
			if other != nil {
				_ = other.Release()
				t.Fatal("state-directory alias bypassed the held draft transaction")
			}
			if err == nil || errorInfo(err).Code != "draft_busy" {
				t.Fatalf("alias contention was not retryable: %v", err)
			}
			_ = lock.Release()

			// 同一持久版本通过别名保存、重启和提交，清空后旧请求必须被拒绝。
			f.write("a.txt", "state alias commit\n")
			var saved Draft
			readData(t, peer.call("PUT", peer.path("/draft"), Draft{Targets: []string{"a.txt"}, Message: "alias commit", Revision: 0}, nil), &saved)
			peer.restart()
			requireStatus(t, f.call("PUT", f.path("/draft"), Draft{Message: "stale alias save", Revision: 0}, nil), 409)
			var loaded Draft
			readData(t, f.call("GET", f.path("/draft"), nil, nil), &loaded)
			if loaded.Message != saved.Message || loaded.Revision != saved.Revision {
				t.Fatalf("alias save lost across restart: %+v", loaded)
			}
			preview := f.preview(PreviewRequest{Action: "commit", Targets: saved.Targets, Message: saved.Message})
			var job Job
			readData(t, f.execute(preview), &job)
			job = f.waitJob(job.ID)
			if job.State != "succeeded" {
				t.Fatalf("alias commit failed: %+v", job)
			}
			requireStatus(t, peer.call("PUT", peer.path("/draft"), saved, nil), 409)
			peer.restart()
			readData(t, peer.call("GET", peer.path("/draft"), nil, nil), &loaded)
			if loaded.Message != "" || len(loaded.Targets) != 0 || loaded.Revision <= saved.Revision {
				t.Fatalf("alias tombstone lost across restart: %+v", loaded)
			}
		})
	}
}
