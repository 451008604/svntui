package systemd

import (
	"strings"
	"testing"
)

func TestUserService(t *testing.T) {
	unit, err := UserService("/opt/svn tools/svntui")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unit, `ExecStart="/opt/svn tools/svntui" automation run`) {
		t.Fatalf("service does not quote executable path:\n%s", unit)
	}
	if !strings.Contains(unit, "ProtectSystem=full") || !strings.Contains(unit, "ProtectHome=false") || !strings.Contains(unit, "WantedBy=default.target") {
		t.Fatalf("service is missing expected directives:\n%s", unit)
	}
}

func TestUserServiceRejectsRelativePath(t *testing.T) {
	if _, err := UserService("svntui"); err == nil {
		t.Fatal("relative executable path was accepted")
	}
}

func TestUserServiceUsesLinuxPaths(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		wantExec   string
	}{
		{
			name:       "linux_absolute",
			executable: "/opt/svntui",
			wantExec:   `ExecStart="/opt/svntui" automation run`,
		},
		{
			name:       "linux_filename_with_backslash",
			executable: `/opt/svn\tools/svntui`,
			wantExec:   `ExecStart="/opt/svn\\tools/svntui" automation run`,
		},
		{name: "windows_drive", executable: `C:\tools\svntui.exe`},
		{name: "windows_drive_with_slashes", executable: "C:/tools/svntui.exe"},
		{name: "windows_unc", executable: `\\server\tools\svntui.exe`},
		{name: "windows_rooted", executable: `\tools\svntui.exe`},
		{name: "empty", executable: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			unit, err := UserService(test.executable)
			if test.wantExec == "" {
				if err == nil {
					t.Fatalf("non-Linux absolute path %q was accepted", test.executable)
				}
				return
			}
			if err != nil {
				t.Fatalf("Linux absolute path %q was rejected: %v", test.executable, err)
			}
			if !strings.Contains(unit, test.wantExec) {
				t.Fatalf("service does not preserve Linux executable path:\n%s", unit)
			}
		})
	}
}
