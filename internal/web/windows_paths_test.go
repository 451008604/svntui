package web

import (
	"os"
	"path/filepath"
	"testing"
)

var reservedWindowsComponents = []string{
	"CON", "con.txt", "PRN.tar.gz", "AUX", "nul.txt", "NUL .txt",
	"COM1.txt", "com9.log", "LPT1", "lpt9.tar.gz",
	"COM¹.txt", "COM²", "COM³.log", "LPT¹.txt", "lpt²", "LPT³.log",
	"CONIN$", "conout$.txt",
}
var ordinaryWindowsComponents = []string{
	"COM0.txt", "COM10.txt", "LPT0", "LPT10", "COM1@.txt", "xCON.txt",
	".CON", "file name.txt", "中文 foo@ name.txt", "foo@", "asset@2x.png",
}

func TestWindowsDeviceComponent(t *testing.T) {
	for _, component := range reservedWindowsComponents {
		if !windowsDeviceComponent(component) {
			t.Errorf("device component accepted: %q", component)
		}
	}
	for _, component := range ordinaryWindowsComponents {
		if windowsDeviceComponent(component) {
			t.Errorf("ordinary component rejected: %q", component)
		}
	}
}

func TestSafeTargetPlatformDeviceComponents(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, component := range reservedWindowsComponents {
		for _, suffix := range []string{"", "/child.txt"} {
			target := "dir/" + component + suffix
			actual, err := safeTarget(root, target)
			if os.PathSeparator == '\\' {
				if err == nil || errorInfo(err).Code != "invalid_path" {
					t.Errorf("unsafe native Windows target accepted: %q => %q / %v", target, actual, err)
				}
			} else if err != nil || actual != target {
				t.Errorf("valid POSIX device-like filename rejected: %q => %q / %v", target, actual, err)
			}
		}
	}
	for _, component := range ordinaryWindowsComponents {
		target := "dir/" + component
		if actual, err := safeTarget(root, target); err != nil || actual != target {
			t.Errorf("ordinary target rejected: %q => %q / %v", target, actual, err)
		}
	}
}
