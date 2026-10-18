package operation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestRecorderRotationKeepsLatestAcrossFiles(t *testing.T) {
	recorder := Recorder{Path: filepath.Join(t.TempDir(), "operations.jsonl"), MaxBytes: 1, MaxBackups: 2}
	for i := range 6 {
		if err := recorder.Append(Record{ID: fmt.Sprint(i), Arguments: []string{"status"}}); err != nil {
			t.Fatal(err)
		}
	}
	records, err := recorder.Last(10)
	if err != nil {
		t.Fatal(err)
	}
	assertRecordIDs(t, records, []string{"5", "4", "3"})
	if _, err := os.Stat(recorder.Path + ".3"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest backup was retained: %v", err)
	}
	records, err = recorder.Last(2)
	if err != nil {
		t.Fatal(err)
	}
	assertRecordIDs(t, records, []string{"5", "4"})
}

func TestRecorderLastSkipsDamagedAndOversizedLines(t *testing.T) {
	recorder := Recorder{Path: filepath.Join(t.TempDir(), "operations.jsonl")}
	var content bytes.Buffer
	encoder := json.NewEncoder(&content)
	// 超长行跨过多次块读取，尾部半条记录模拟进程中断。
	for _, record := range []Record{
		{ID: "oldest"},
		{ID: "oversized", Arguments: []string{strings.Repeat("x", maxRecordBytes+(256<<10))}},
		{ID: "latest", Arguments: []string{strings.Repeat("中", 24000)}},
	} {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	content.WriteString("{\"id\":\"interrupted")
	if err := os.WriteFile(recorder.Path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := recorder.Last(10)
	if err != nil {
		t.Fatal(err)
	}
	assertRecordIDs(t, records, []string{"latest", "oldest"})
	if records[0].Arguments[0] != strings.Repeat("中", 24000) {
		t.Fatal("record crossing a read block was corrupted")
	}
	if err := os.WriteFile(recorder.Path, []byte(`{"id":"no-newline"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err = recorder.Last(1)
	if err != nil {
		t.Fatal(err)
	}
	assertRecordIDs(t, records, []string{"no-newline"})
}

func TestRecorderLastBoundsAndOversizedAppend(t *testing.T) {
	recorder := Recorder{Path: filepath.Join(t.TempDir(), "operations.jsonl")}
	if _, err := recorder.Last(10001); err == nil {
		t.Fatal("unbounded read limit was accepted")
	}
	if err := recorder.Append(Record{ID: "too large", Arguments: []string{strings.Repeat("x", maxRecordBytes)}}); err == nil {
		t.Fatal("oversized record was accepted")
	}
	var content bytes.Buffer
	encoder := json.NewEncoder(&content)
	for i := range 1005 {
		if err := encoder.Encode(Record{ID: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(recorder.Path, content.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{0, -1} {
		records, err := recorder.Last(limit)
		if err != nil || len(records) != 1000 || records[0].ID != "1004" || records[999].ID != "5" {
			t.Fatalf("Last(%d) returned %d records, error %v", limit, len(records), err)
		}
	}
}

func TestRecorderLastReadsLargeFileWithBoundedMemory(t *testing.T) {
	recorder := Recorder{Path: filepath.Join(t.TempDir(), "operations.jsonl")}
	writeLargeLog(t, recorder.Path, 128<<20)
	// 读取足够记录后必须停止，较旧备份故意设置成不可读取的目录。
	if err := os.Mkdir(recorder.Path+".1", 0o700); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	records, err := recorder.Last(3)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	assertRecordIDs(t, records, []string{"newest", "middle", "oldest"})
	allocated := after.TotalAlloc - before.TotalAlloc
	t.Logf("128 MiB log, Last(3): %d bytes allocated", allocated)
	if allocated > 8<<20 {
		t.Fatalf("reading 3 records from 128 MiB allocated %d bytes", allocated)
	}
}

func BenchmarkLastLargeFile(b *testing.B) {
	recorder := Recorder{Path: filepath.Join(b.TempDir(), "operations.jsonl")}
	writeLargeLog(b, recorder.Path, 64<<20)
	b.ReportAllocs()
	for b.Loop() {
		records, err := recorder.Last(3)
		if err != nil || len(records) != 3 {
			b.Fatalf("Last(3) returned %d records: %v", len(records), err)
		}
	}
}

func writeLargeLog(t testing.TB, path string, size int64) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	tail := []byte("\n{\"id\":\"oldest\"}\n{\"id\":\"middle\"}\n{\"id\":\"newest\"}\n")
	block := bytes.Repeat([]byte("{\"id\":\"historical\"}\n"), 4096)
	for remaining := size - int64(len(tail)); remaining > 0; {
		count := min(remaining, int64(len(block)))
		if _, err := file.Write(block[:count]); err != nil {
			t.Fatal(err)
		}
		remaining -= count
	}
	if _, err := file.Write(tail); err != nil {
		t.Fatal(err)
	}
}

func assertRecordIDs(t *testing.T, records []Record, want []string) {
	t.Helper()
	got := make([]string, 0, len(records))
	for _, record := range records {
		got = append(got, record.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("record IDs = %v, want %v", got, want)
	}
}
