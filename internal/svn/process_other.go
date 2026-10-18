//go:build !windows

package svn

import "os/exec"

func configureSVNProcess(_ *exec.Cmd) {}
