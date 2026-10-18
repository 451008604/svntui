//go:build !windows

package automation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSecretFilePermissions(t *testing.T) {
	for _, test := range []struct {
		name string
		mode os.FileMode
		deny bool
	}{
		{name: "owner_only", mode: 0o600},
		{name: "owner_read_only", mode: 0o400},
		{name: "group_read", mode: 0o640, deny: true},
		{name: "everyone_read", mode: 0o604, deny: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "webhook.secret")
			if err := os.WriteFile(path, []byte("  secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}
			secret, err := resolveSecret("file:" + path)
			if test.deny {
				if err == nil || len(secret) != 0 {
					t.Fatalf("insecure permissions accepted: secret=%q err=%v", secret, err)
				}
				return
			}
			if err != nil || string(secret) != "secret" {
				t.Fatalf("secure file rejected: secret=%q err=%v", secret, err)
			}
		})
	}
}
