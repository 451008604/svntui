package svn

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestSVNProcessIsolatesConsole(t *testing.T) {
	for _, existing := range []bool{false, true} {
		cmd := exec.CommandContext(t.Context(), "svn", "--version")
		var flags uint32
		if existing {
			flags = 0x00000200
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
		}
		original := cmd.SysProcAttr
		configureSVNProcess(cmd)
		if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags != flags|createNoWindow {
			t.Fatalf("existing=%v: process attributes = %+v", existing, cmd.SysProcAttr)
		}
		if existing && (cmd.SysProcAttr != original || !cmd.SysProcAttr.HideWindow) {
			t.Fatal("existing process attributes were replaced")
		}
	}
}
