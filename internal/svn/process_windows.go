package svn

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func configureSVNProcess(cmd *exec.Cmd) {
	// 隔离父控制台，避免线程代码页与 SVN 参数代码页不一致；输出仍由管道收集。
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
