package svn

import "testing"

func TestReverseRevisionSpec(t *testing.T) {
	got, err := reverseRevisionSpec("120,123-126")
	if err != nil {
		t.Fatal(err)
	}
	if got != "-120,-126,-125,-124,-123" {
		t.Fatalf("reverse spec = %q", got)
	}
}

func TestReverseRevisionSpecRejectsHugeRange(t *testing.T) {
	if _, err := reverseRevisionSpec("1-20000"); err == nil {
		t.Fatal("huge reverse range was accepted")
	}
}
