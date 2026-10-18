# svntui

`svntui` 是一个面向 Linux、macOS 和 Windows 终端的 SVN 客户端。它直接调用系统 `svn`，不会重新实现 SVN 协议，也不会保存 SVN 密码。界面、帮助和错误提示默认使用中文。

当前版本：**0.2.0**。Go + 系统 SVN，TUI 与脚本命令共用操作服务。

- 九模块 TUI：Workspace、Repository、History、Branches、Conflicts、Automations、SafePoints、Operations、Projects。
- 独立提交页、多行说明和外部编辑器、原生 changelist、筛选选择、命名项目与最近工作副本。
- 连续仓库导航、历史分页与检索、源码 blame、两修订比较、合并修订选择及预演。
- CLI 工作副本写操作、属性和 externals、定点/稀疏更新、补丁导入导出、统一 JSON 和退出码。
- 状态、diff、临时选择集、add/delete/move/commit/revert/update/cleanup/lock/resolve/属性等工作副本操作。
- 任意仓库路径收藏、远端 copy、switch、merge dry-run、修订拣选和反向合并。
- 定时更新指定工作副本，支持星期、活跃时段、随机抖动、失败退避、Linux systemd 用户服务和 Windows 登录计划任务。
- 通用 JSON Webhook、静态请求头、HMAC-SHA256 签名，最多尝试三次（含首次）。
- SafePoint：保存文本/属性 diff、二进制或新增文件副本、移动状态及选中的未版本化文件，并支持校验和恢复。
- 系统原生配置目录、跨进程工作副本写锁和脱敏操作日志。

详细操作见 [0.2 工作流指南](docs/workflows.md)。提交草稿在当前 TUI 会话内按项目保留；退出程序后不持久化。冲突按文本、属性、树类型展示，可调用外部工具后复查并显式标记解决；内置逐块合并及属性三方编辑器尚未提供。普通 SVN 补丁不能携带二进制内容，二进制备份使用 SafePoint。

## 环境要求

- Go 1.24 或更高版本（仅构建时需要）。
- 可执行的系统 `svn`（Windows 为 `svn.exe`）并已加入 `PATH`；`svnadmin` 只用于开发测试。
- Linux/macOS 需要 UTF-8 终端和 `stty`；Windows 10 或更高版本使用原生控制台，应用会启用 UTF-8 和虚拟终端模式。
- 颜色不是识别状态的唯一方式，可使用 `--no-color`。

## 构建与安装

先取得源码：

```bash
git clone https://github.com/451008604/svntui.git
cd svntui
```

Linux/macOS：

```bash
go build -o svntui ./cmd/svntui
mkdir -p ~/.local/bin
install -m 0755 svntui ~/.local/bin/svntui
```

Windows PowerShell：

```powershell
go build -o svntui.exe ./cmd/svntui
./svntui.exe
```

Windows 可执行文件目前没有代码签名；启用智能应用控制的设备可能拦截程序启动。请遵循设备的应用运行策略，详见 [Microsoft 智能应用控制说明](https://learn.microsoft.com/en-us/windows/apps/develop/smart-app-control/overview)。

在工作副本中直接运行：

```bash
svntui
```

也可以显式指定工作副本：

```bash
svntui /path/to/working-copy
```

## TUI 操作

| 按键 | 功能 |
| --- | --- |
| `1`–`9` / `Tab` | 切换模块 |
| `j/k` / 方向键 | 移动光标 |
| `Space` / `A` / `I` / `X` | 选择 / 全选可见 / 反选可见 / 清空选择 |
| `/` | 筛选工作区或历史；详情页搜索 |
| `C` | 打开提交页，`Ctrl-S` 确认、`Ctrl-E` 外部编辑说明 |
| `a` | 上下文操作与表单；预演结果中进入应用确认 |
| `b` / `v` | 逐行追溯源码 / 比较两个修订 |
| `d` / `Enter` | 工作区查看当前差异；历史页分别查看内容差异/修订摘要 |
| `h` | 在工作区打开当前文件的修订历史 |
| `r` | 刷新当前模块 |
| `:` | 打开命令面板 |
| `?` | 帮助 |
| `c` | 取消正在执行的只读任务 |
| `q` / `Ctrl-C` | 退出 |

命令面板默认展示可搜索的指令列表和中文说明：输入中文或英文筛选，使用方向键选择，按 `Enter` 确认；需要参数的命令会先填入模板，再次按 `Enter` 执行。仍可选择“手动输入”使用完整命令。

命令示例：

```text
:update
:update --apply
:add
:commit "fix: correct order state"
:revert
:cleanup
:lock "editing binary asset"
:unlock
:resolve working
:ignore "*.log" .
:property svn:needs-lock "*" assets/image.psd
:copy https://svn.example/repo/source https://svn.example/repo/branches/new HEAD "create branch"
:switch https://svn.example/repo/branches/new
:merge https://svn.example/repo/trunk
:merge https://svn.example/repo/trunk 120,123-126
:merge https://svn.example/repo/trunk 126 --reverse
:merge https://svn.example/repo/trunk 120,123-126 --apply
```

`update` 默认执行远端变化预览，追加 `--apply` 才会更新。`merge` 默认只执行 dry-run，追加 `--apply` 才会修改工作副本；merge 永远不会自动提交。

普通写操作使用一次确认；copy、delete、revert、switch 和 merge 等高风险操作必须输入大写 `YES`。revert 和脏工作副本的专家 switch/merge 会先创建 SafePoint。

## 路径收藏与基础命令

```bash
svntui status /path/to/wc
svntui status --remote /path/to/wc
svntui diff --working-copy /path/to/wc src/file.go

svntui favorite add --group backend --name trunk https://svn.example/repo/trunk
svntui favorite list
svntui favorite remove trunk

svntui config set expert-mode true
svntui config set editor nvim
svntui config set merge-tool meld
svntui config show
```

收藏只提供名称和分组，不会把目录推断为 trunk、branch 或 tag。

Repository 和 History 命令支持远端浏览、稀疏 checkout/export、日志与 blame：

```bash
svntui repository list --revision HEAD --depth immediates https://svn.example/repo/trunk
svntui repository compare --summary BRANCH_URL TRUNK_URL
svntui repository checkout --depth files https://svn.example/repo/trunk /work/trunk
svntui repository export https://svn.example/repo/tags/v1 ./release-v1
svntui history log --limit 50 /work/trunk
svntui history blame --revision 120 https://svn.example/repo/trunk/main.go
```

在 TUI 中查看历史：

- 工作区选中文件后按 `h`：打开该文件历史，默认每页 100 条。
- 直接按 `3`：打开整个工作副本历史，`[` / `]` 翻页，`/` 筛选作者、说明与日期。
- 历史页按 `Enter`：查看提交说明和 changed paths。
- 历史页按 `d`：查看选中 revision 的实际内容 diff；从文件历史进入时只显示该文件的变化。
- 工作区中的未版本化文本文件也可按 `d`/`Enter` 预览，界面会按“相对空文件新增”展示；目录、二进制文件和超过 512 KiB 的文件会显示明确提示而不会直接展开。

命令行也可直接查看某个本地文件的历史：`svntui history log --limit 100 /work/trunk/main.go`；查看每一行最后由谁在哪个 revision 修改，可使用 `svntui history blame /work/trunk/main.go`。

远端 mkdir/delete/move/copy 必须同时给出完整 URL、提交说明和 `--yes`，例如：

```bash
svntui repository copy --revision HEAD --message "create release branch" --yes SOURCE_URL TARGET_URL
```

文本冲突可以在 TUI 的 Conflicts 页面查看 BASE/MINE/THEIRS 内容，直接按 `e`/`m` 启动编辑器/合并工具，返回后自动复查；也可使用 CLI：

```bash
svntui conflict edit --path /path/to/wc conflicted.txt
svntui conflict merge-tool --path /path/to/wc conflicted.txt
```

合并工具固定接收 `BASE MINE THEIRS WORKING` 四个参数。工具退出后应用会重新读取 SVN 状态，不会仅凭退出码自动标记 resolved。

## 自动更新

创建 Webhook：

```bash
export SVNTUI_WEBHOOK_SECRET='replace-me'
svntui automation webhook add \
  --name team-alerts \
  --url https://hooks.example/svntui \
  --secret-ref env:SVNTUI_WEBHOOK_SECRET \
  --header X-Team=backend
```

也可以使用仅当前用户可读的文件：`--secret-ref file:/secure/path/webhook-secret`。后台服务优先推荐文件引用。Linux 若使用 `env:`，可把变量写入权限为 `0600` 的 `~/.config/svntui/automation.env`，systemd 用户服务会可选读取该文件；Windows 登录任务不会代为保存环境变量或密钥，建议使用受当前用户 ACL 保护的 `file:` 引用。

创建自动更新任务：

```bash
svntui automation add \
  --name trunk-update \
  --path /srv/work/trunk \
  --interval 15m \
  --days mon,tue,wed,thu,fri \
  --windows 09:00-12:00,13:00-19:00 \
  --timezone Asia/Shanghai \
  --webhook webhook-xxxxxxxx
```

默认策略：

- 校验 Repository UUID、Root URL 和分支 URL，发现漂移立即暂停。
- 工作副本存在版本化修改、missing、冲突或未允许的 switched subtree 时跳过。
- 默认 `--ignore-externals`，且后台使用非交互认证。
- 网络失败退避，连续三次失败后暂停；认证、权限、冲突和仓库身份错误立即暂停。
- skipped、failed、paused 必推；成功变化和 no-change 可分别用 `--notify-success`、`--notify-no-change` 开启。
- 系统休眠后只补执行一次，不追赶所有错过的周期。

管理命令：

```bash
svntui automation list
svntui automation run-now TASK_ID
svntui automation pause TASK_ID "maintenance"
svntui automation resume TASK_ID
svntui automation disable TASK_ID
svntui automation runs TASK_ID
svntui automation run --once
svntui automation run
```

安装可选后台任务：

```bash
svntui automation service print
svntui automation service install
svntui automation service uninstall
```

Linux 会安装并启动 systemd 用户服务；Windows 会通过 `schtasks.exe` 安装当前用户登录时运行的计划任务。两者都以当前用户身份运行，具体 SVN 访问权限仍由该用户决定。其他系统可直接托管 `svntui automation run`。

## SafePoint

```bash
svntui safepoint create --path /path/to/wc --reason "before refactor"
svntui safepoint create --path /path/to/wc --include local-note.txt
svntui safepoint list
svntui safepoint inspect SAFEPOINT_ID
svntui safepoint validate --path /path/to/wc SAFEPOINT_ID
svntui safepoint restore --path /path/to/wc SAFEPOINT_ID
svntui safepoint cleanup
```

恢复前会验证 Repository UUID、工作副本 URL、根 BASE revision，以及每个受影响版本化路径的 UUID、URL 和 BASE revision，避免混合修订工作副本误恢复。新快照采用 v4（完整子树及属性数据）；v3 及更早清单仍可查看、保留原始文件，但会拒绝自动恢复，避免将缺少内容的快照当作完整保护。目标工作副本已有修改时，默认先创建一个新的 SafePoint；可用 `--backup=false` 禁止自动保护并让恢复直接失败。

尚有文本、属性、树冲突或阻塞/不完整节点时，SafePoint 会在创建或恢复写入前拒绝，不会产生“成功但无法还原”的保护。复制/移动恢复为等价路径与内容变更，并提示历史关系变化。

保留策略默认是30天和全局2 GiB，自动清理始终保护每个工作副本最新的一份。

## 文件位置

Linux 遵循 XDG 目录约定：

- 配置：`${XDG_CONFIG_HOME:-~/.config}/svntui/config.json`
- 自动任务：`${XDG_STATE_HOME:-~/.local/state}/svntui/automations.json`
- SafePoint：`${XDG_STATE_HOME:-~/.local/state}/svntui/safepoints/`
- 操作日志：`${XDG_STATE_HOME:-~/.local/state}/svntui/operations.jsonl`
- 缓存：`${XDG_CACHE_HOME:-~/.cache}/svntui/`

Windows 使用当前用户目录：

- 配置：`%APPDATA%\svntui\config.json`
- 自动任务、SafePoint 和操作日志：`%LOCALAPPDATA%\svntui\`
- 缓存：`%LOCALAPPDATA%\svntui\`

三个平台目录都可分别用 `XDG_CONFIG_HOME`、`XDG_STATE_HOME` 和 `XDG_CACHE_HOME` 覆盖。

配置使用原子替换、跨进程更新锁并限制为当前用户访问。操作日志只记录脱敏后的 SVN 参数、状态和错误摘要；默认每份 10 MiB，保留 5 份轮转备份，读取最近记录按文件尾部扫描。SVN stdout/stderr 各默认最多在内存保留 8 MiB，超限 stdout 保存临时文件并在错误中返回路径；该文件由使用者确认后清理。需要完整大 diff 时使用 `patch export --output`。

## License

MIT，见 [LICENSE](LICENSE)。

## 开发验证

```bash
GOCACHE=/tmp/svntui-gocache go test ./...
GOCACHE=/tmp/svntui-gocache go vet ./...
GOOS=linux GOARCH=amd64 go build ./cmd/svntui
GOOS=windows GOARCH=amd64 go build ./cmd/svntui
```

测试会通过本地 `svnadmin` 仓库验证状态、历史、自动更新和 SafePoint 往返恢复，不需要网络仓库。
