package operation

import (
	"path/filepath"
	"testing"
	"time"

	"svntui/internal/svn"
)

func TestRecorderRedactsAndReadsNewestFirst(t *testing.T) {
	recorder := Recorder{Path: filepath.Join(t.TempDir(), "operations.jsonl")}
	recorder.Observe(svn.Command{Args: []string{"commit", "--password", "secret"}}, svn.Result{Started: time.Now(), ExitCode: 0}, nil)
	recorder.Observe(svn.Command{Args: []string{"status"}}, svn.Result{Started: time.Now(), ExitCode: 0}, nil)
	records, err := recorder.Last(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].Arguments[0] != "status" || records[1].Arguments[2] != "[REDACTED]" {
		t.Fatalf("unexpected records: %+v", records)
	}
}
