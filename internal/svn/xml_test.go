package svn

import (
	"strings"
	"testing"
	"time"
)

func TestParseInfo(t *testing.T) {
	input := `<info><entry kind="dir" path="." revision="42"><url>https://example.test/svn/app/branches/a</url><relative-url>^/branches/a</relative-url><repository><root>https://example.test/svn/app</root><uuid>repo-1</uuid></repository><wc-info><wcroot-abspath>/tmp/app</wcroot-abspath><depth>infinity</depth></wc-info></entry></info>`
	info, err := parseInfo([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if info.Revision != 42 || info.UUID != "repo-1" || info.WCAbsolute != "/tmp/app" {
		t.Fatalf("unexpected info: %+v", info)
	}
}

func TestParseStatusAndDirtyState(t *testing.T) {
	input := `<status><target path="."><entry path="clean.txt"><wc-status item="normal" props="none" revision="7"/></entry><entry path="changed.txt"><wc-status item="modified" props="none" revision="7" switched="true"/></entry><entry path="new.txt"><wc-status item="unversioned" props="none"/></entry><entry path="tree"><wc-status item="normal" props="none" tree-conflicted="true"/></entry></target></status>`
	entries, err := parseStatus([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(entries))
	}
	if entries[0].IsVersionedDirty() || !entries[1].IsVersionedDirty() || entries[2].IsVersionedDirty() {
		t.Fatalf("unexpected dirty classification: %+v", entries)
	}
	if !entries[3].IsConflict() {
		t.Fatal("tree conflict was not detected")
	}
}

func TestParseLog(t *testing.T) {
	input := `<log><logentry revision="9"><author>dev</author><date>2026-08-30T01:02:03.000000Z</date><paths><path action="A" copyfrom-path="/trunk" copyfrom-rev="8">/branches/a</path></paths><msg>branch</msg></logentry></log>`
	entries, err := parseLog([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Paths[0].CopyFromRev != 8 || entries[0].Date.IsZero() {
		t.Fatalf("unexpected log: %+v", entries)
	}
	if entries[0].Date.Location() != time.UTC {
		t.Fatalf("expected UTC date: %s", entries[0].Date)
	}
}

func TestParseUpdateOutput(t *testing.T) {
	changed, revision := ParseUpdateOutput("U    a.txt\n U   properties\nUU   both.txt\n   C tree\nA    dir/b.txt\nUpdated to revision 123.\n")
	if revision != 123 || len(changed) != 5 || changed[1].Action != "U" || changed[2].Action != "UU" || changed[3].Action != "C" {
		t.Fatalf("unexpected update parse: revision=%d changed=%+v", revision, changed)
	}
}

func TestRedactURLCredentials(t *testing.T) {
	input := "failed https://alice:secret@example.test/repo and svn+ssh://bob@example.test/x"
	got := redactURLCredentials(input)
	if strings.Contains(got, "secret") || strings.Contains(got, "bob@") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("redactURLCredentials() = %q", got)
	}
}

func TestParseList(t *testing.T) {
	data := []byte(`<?xml version="1.0"?><lists><list path="file:///repo/trunk"><entry kind="file"><name>hello.txt</name><size>6</size><commit revision="7"><author>alice</author><date>2026-08-30T01:02:03.000000Z</date></commit></entry><entry kind="dir"><name>docs</name><commit revision="6"><author>bob</author><date>2026-08-29T01:02:03.000000Z</date></commit></entry></list></lists>`)
	entries, err := parseList(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name != "hello.txt" || entries[0].Kind != "file" || entries[0].Size != 6 || entries[0].Revision != 7 {
		t.Fatalf("unexpected list: %+v", entries)
	}
	if entries[0].Date.IsZero() || entries[1].Name != "docs" || entries[1].Kind != "dir" {
		t.Fatalf("unexpected list metadata: %+v", entries)
	}
}

func TestParseBlame(t *testing.T) {
	data := []byte(`<?xml version="1.0"?><blame><target path="hello.txt"><entry line-number="1"><commit revision="7"><author>alice</author><date>2026-08-30T01:02:03.000000Z</date></commit></entry><entry line-number="2"><commit revision="8"><author>bob</author><date>2026-08-30T02:03:04.000000Z</date></commit><merged path="/branches/topic/hello.txt" revision="9"/></entry></target></blame>`)
	lines, err := parseBlame(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Line != 1 || lines[0].Revision != 7 || lines[0].Author != "alice" {
		t.Fatalf("unexpected blame: %+v", lines)
	}
	if lines[1].MergedPath != "/branches/topic/hello.txt" || lines[1].MergedRevision != 9 || lines[1].Date.IsZero() {
		t.Fatalf("unexpected merged blame: %+v", lines[1])
	}
}
