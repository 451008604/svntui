package svn

import "testing"

func TestPatchNotificationDoesNotInterpretFilenames(t *testing.T) {
	for _, test := range []struct {
		output string
		failed bool
	}{
		{"U         conflict.txt\nA         skipped.txt\nU         rejected hunk.txt\n", false},
		{" U        conflict-property.txt\n", false},
		{"C         file.txt\n", true},
		{" C        file.txt\n", true},
		{"Skipped 'missing.txt' -- The target is missing\n", true},
		{"U         file.txt\n>         rejected hunk @@ -1,1 +1,1 @@\n", true},
		{"Summary of conflicts:\n  Text conflicts: 1\n", true},
	} {
		if got := PatchHasProblems(test.output); got != test.failed {
			t.Errorf("PatchHasProblems(%q) = %v, want %v", test.output, got, test.failed)
		}
	}
}
