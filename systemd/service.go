// Package systemd 生成可选的 svntui 用户服务配置。
package systemd

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// 返回用于运行自动更新调度器的 systemd 用户服务。
func UserService(executable string) (string, error) {
	// 按目标 Linux 的路径规则校验，避免依赖生成配置的宿主系统。
	if !path.IsAbs(executable) {
		return "", errors.New("svntui 可执行文件路径必须是绝对路径")
	}
	for _, character := range executable {
		if character < ' ' {
			return "", errors.New("svntui 可执行文件路径包含无效字符")
		}
	}
	// 转义 systemd 占位符及命令参数中的特殊字符。
	executable = strings.ReplaceAll(executable, "%", "%%")
	executable = strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(executable)
	return fmt.Sprintf(`[Unit]
Description=svntui SVN 自动更新
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-%%h/.config/svntui/automation.env
ExecStart=%s automation run
Restart=on-failure
RestartSec=15s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=false

[Install]
WantedBy=default.target
`, `"`+executable+`"`), nil
}
