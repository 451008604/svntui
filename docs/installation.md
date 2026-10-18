# svntui v0.2.0 安装指南

## 选择安装包

svntui 是命令行和终端交互工具。发布包采用解压安装形式，包含程序、许可证和离线说明，不需要 Go，也不包含 SVN。

下载入口：[GitHub v0.2.0 Release](https://github.com/451008604/svntui/releases/tag/v0.2.0)。

| 系统与架构 | 文件名 | 解压后的程序 |
| --- | --- | --- |
| macOS Apple 芯片 / arm64 | `svntui-v0.2.0-macos-arm64.tar.gz` | `svntui-v0.2.0-macos-arm64/svntui` |
| Linux Intel/AMD 64 位 / amd64 | `svntui-v0.2.0-linux-amd64.tar.gz` | `svntui-v0.2.0-linux-amd64/svntui` |
| Windows Intel/AMD 64 位 / amd64 | `svntui-v0.2.0-windows-amd64.zip` | `svntui-v0.2.0-windows-amd64/svntui.exe` |

macOS/Linux 可用 `uname -m` 确认架构：`arm64` 对应 Apple 芯片，`x86_64` 对应 amd64。Windows 可在“设置 → 系统 → 关于 → 系统类型”查看。Intel Mac、Linux ARM64 和 Windows ARM64 本次没有原生预编译包；源码构建需要 Go 1.24 或更高版本，步骤见 [README](../README.md#从源码构建)。

## 1. 安装系统 SVN

svntui 调用 PATH 中的 `svn` / `svn.exe`，使用该 SVN 客户端的认证配置。日常使用不需要 `svnadmin`。本次跨平台回归使用了 SVN 1.14.x。

### macOS

已安装 Homebrew 时，按 [Homebrew Subversion 页面](https://formulae.brew.sh/formula/subversion) 安装：

```bash
brew install subversion
svn --version --quiet
```

### Linux

Debian / Ubuntu：

```bash
sudo apt-get update
sudo apt-get install subversion
svn --version --quiet
```

其他发行版从系统包管理器安装 Subversion，参见 [Apache 提供的各平台包索引](https://subversion.apache.org/packages.html)。交互界面需要 UTF-8 终端与 `stty`。

### Windows

安装 [Apache 包索引](https://subversion.apache.org/packages.html#windows) 中的 SVN 命令行客户端，例如 TortoiseSVN 或 SlikSVN。使用 TortoiseSVN 时，需要选择 **command line client tools** 组件；仅安装资源管理器右键菜单不足以提供 `svn.exe`。

将实际包含 `svn.exe` 的目录加入用户 PATH，重新打开 PowerShell，然后执行：

```powershell
Get-Command svn
svn --version --quiet
```

Windows TUI 使用原生控制台，建议在 Windows 10 或更新系统的 Windows Terminal / PowerShell 中运行。不要通过双击程序检查启动结果，终端会保留错误提示。

## 2. 下载、校验和安装 svntui

从 Release 下载适合本机的安装包及 `SHA256SUMS.txt`，保存在同一目录。下面的校验命令只检查当前平台的包，无需下载另外两个平台。校验不一致时先重新下载，不要继续安装。

### macOS Apple 芯片

在下载目录打开终端：

```bash
awk '$2 == "svntui-v0.2.0-macos-arm64.tar.gz"' SHA256SUMS.txt | shasum -a 256 -c -
```

确认输出为 `OK` 后安装：

```bash
tar -xzf svntui-v0.2.0-macos-arm64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 0755 svntui-v0.2.0-macos-arm64/svntui "$HOME/.local/bin/svntui"
export PATH="$HOME/.local/bin:$PATH"
svntui --help
```

要让 PATH 在新终端中生效，将 `export PATH="$HOME/.local/bin:$PATH"` 加入 `~/.zshrc`；使用 Bash 时加入 `~/.bashrc`。已有同样配置无需重复添加。

### Linux amd64

在下载目录打开终端：

```bash
awk '$2 == "svntui-v0.2.0-linux-amd64.tar.gz"' SHA256SUMS.txt | sha256sum -c -
```

确认输出为 `OK` 后安装：

```bash
tar -xzf svntui-v0.2.0-linux-amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 0755 svntui-v0.2.0-linux-amd64/svntui "$HOME/.local/bin/svntui"
export PATH="$HOME/.local/bin:$PATH"
svntui --help
```

将 `export PATH="$HOME/.local/bin:$PATH"` 加入所用 Shell 的启动配置，例如 Bash 的 `~/.bashrc`，或 Zsh 的 `~/.zshrc`。若系统已包含此路径，无需重复添加。

### Windows amd64

在下载目录打开 PowerShell，校验并解压到当前目录的版本文件夹：

```powershell
$package = 'svntui-v0.2.0-windows-amd64.zip'
$line = @(Get-Content .\SHA256SUMS.txt | Where-Object { $_ -match ('^[0-9a-fA-F]{64}  ' + [regex]::Escape($package) + '$') })
if ($line.Count -ne 1) { throw '校验清单中未找到唯一的 Windows 安装包' }
$expected = ($line[0] -split '\s+')[0]
$actual = (Get-FileHash -LiteralPath $package -Algorithm SHA256).Hash
if ($actual -ine $expected) { throw '安装包 SHA-256 校验失败，请重新下载' }
Expand-Archive -LiteralPath $package -DestinationPath .
New-Item -ItemType Directory -Force "$env:LOCALAPPDATA\Programs\svntui" | Out-Null
Copy-Item .\svntui-v0.2.0-windows-amd64\svntui.exe "$env:LOCALAPPDATA\Programs\svntui\svntui.exe" -Force
& "$env:LOCALAPPDATA\Programs\svntui\svntui.exe" --help
```

在“编辑账户的环境变量 → 用户变量 → Path → 编辑 → 新建”中添加 `%LOCALAPPDATA%\Programs\svntui`。重新打开 PowerShell 后确认：

```powershell
Get-Command svntui
svntui --help
svntui "C:\work\project"
```

也可以不配置 PATH，直接用 `& "$env:LOCALAPPDATA\Programs\svntui\svntui.exe" "C:\work\project"` 启动。

发行程序未做发行者代码签名或 macOS 公证。若设备拦截，请遵循设备的应用运行策略；Windows 智能应用控制相关行为见 [Microsoft 官方说明](https://learn.microsoft.com/en-us/windows/apps/develop/smart-app-control/overview)。

## 3. 第一次使用

先确认原生 SVN 能访问你的仓库。还没有工作副本时，在终端中检出并按 SVN 提示完成认证（替换示例 URL）：

```bash
svn checkout https://svn.example.com/repository/trunk project
cd project
svntui
```

已有工作副本时直接进入目录运行 `svntui`，或指定目录：

```bash
svntui "/path/to/working-copy"
```

| 操作 | 按键 |
| --- | --- |
| 切换模块 / 查看帮助 | `1`–`9` 或 `Tab` / `?` |
| 移动光标 / 看差异 | 方向键或 `j/k` / `d` |
| 选择文件 / 筛选 | `Space` / `/` |
| 打开提交页 | `C` |
| 提交页切换区域 / 确认 | `Tab` / `Ctrl-S` |
| 调用外部编辑器写说明 | 提交页 `Ctrl-E` |
| 上下文操作 / 命令面板 | `a` / `:` |
| 文件历史 / 历史翻页 | 工作区 `h` / 历史页 `[`、`]` |
| 退出 | `q` 或 `Ctrl-C` |

提交确认会列出实际目标；目录选择不会递归提交未选中的子文件。提交说明草稿只在当前 TUI 会话中保留。

在工作副本根目录，可使用以下脚本命令；把文件名替换为项目里的实际路径：

```bash
svntui status --path . --remote --json
svntui diff --path . src/main.go
svntui update --path .
svntui update --path . --apply --yes
svntui commit --path . --message "fix: update message" --yes src/main.go
svntui history log --path . --page --limit 50
svntui safepoint create --path . --reason "before refactor"
```

选项放在位置参数之前；含空格的路径用引号包住；以 `-` 开头的路径放在 `--` 后。`update`、`merge`、`patch apply` 默认预演，写入需要 `--apply --yes`。提交等工作副本写操作需显式使用 `--yes`。

更多内容见 [工作流指南](https://github.com/451008604/svntui/blob/v0.2.0/docs/workflows.md)，安装包内也包含 `docs/workflows.md`：changelist、历史筛选、项目切换、合并/冲突、属性、externals、补丁和 SafePoint。

## 4. 升级与卸载

升级时先退出 svntui；若自行安装了自动更新后台服务，也先停止该服务。下载新版本、校验，再替换原位置的程序，配置和 SafePoint 会保留。后台服务恢复方式取决于平台及你的托管方式。

卸载时，若使用过 `svntui automation service install`，先运行 `svntui automation service uninstall`；然后删除自己安装的 `svntui` / `svntui.exe`，按需移除 PATH 项。配置、操作日志和 SafePoint 不会随程序删除，位置见 [README](../README.md#文件位置)。

## 常见问题

- **找不到 svn：** 确认 `svn --version --quiet` 能运行，安装命令行组件并重新打开终端。
- **找不到 svntui：** 检查 PATH，或先使用程序的完整路径。
- **不是工作副本：** 指定含 `.svn` 的工作副本或其子目录；普通 Git 项目无法当作 SVN 工作副本。
- **认证或网络失败：** 先在同一用户的终端用原生 `svn info` / `svn update` 处理连接或认证问题。
- **终端显示不完整：** 放大窗口或试用 `svntui --no-color`；Linux/macOS 检查 UTF-8 环境与 `stty` 是否可用。
- **SafePoint 版本不兼容：** v0.2.0 使用 v4 清单；更早清单可查看和手工提取，不能自动恢复。
