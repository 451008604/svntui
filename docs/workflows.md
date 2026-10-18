# 0.2 工作流指南

## 提交与变更分组

TUI 工作区中，`/` 按路径、状态、changelist 筛选；`A` 全选可见项、`I` 反选可见项、`X` 清空所有选择。筛选隐藏的已选文件仍保留，提交确认页会列出全部实际目标。

按 `C` 打开提交页。`Tab` 切换文件、说明和 diff 区域，`Space` 选择文件，`d` 读取差异。说明支持多行、粘贴和光标移动；`Ctrl-E` 调用配置的编辑器。`Ctrl-S` 进入完整目标确认页，`Esc` 返回保留草稿。取消、失败或切换项目不会丢失当前会话内的草稿，提交成功后清空该项目草稿。

```bash
svntui changelist add --path /work/app --yes feature-login src/login.go src/login_test.go
svntui changelist list --path /work/app
svntui status --path /work/app --changelist feature-login
svntui commit --path /work/app --changelist feature-login --message-file message.txt --yes
svntui changelist remove --path /work/app --yes src/login.go
```

分组直接使用 SVN 原生 changelist，可与 IDE 和原生 SVN 互通。未分组文件仍然显示。提交必须明确目标文件或分组；仅选择 `foo@` 不会提交 `foo`。提交使用 `--depth empty`，只提交明确目标；选中目录不会递归提交未选子文件。新加文件的父目录若尚未入库，需要同时明确选择父目录。

## 统一命令行规则

- 工作副本命令使用 `--path`；旧 `diff --working-copy` 继续兼容。
- 选项放在位置参数之前；以 `--` 分隔以连字符开头的路径。
- 写操作显式使用 `--yes`。`update`、`merge` 和 `patch apply` 默认预演，执行时还需要 `--apply`。
- `--json` 包装结果为固定四键：`ok`、`data`、`error`、`exit_code`；成功时 `error: null`。动作结果包含 `summary`、`output`、`refresh_workspace`。
- 退出码：0 成功；2 参数错误；3 认证/权限；4 冲突、过期或工作副本被占用；5 网络；130 取消；1 其他错误。
- 不带 `--json` 的旧命令保留原有输出。消息中的 `--json` 以及 `--` 后的同名文件保留原值。

```bash
svntui add --path /work/app --yes -- new.txt
svntui move --path /work/app --yes old.txt new.txt
svntui delete --path /work/app --yes obsolete.txt
svntui revert --path /work/app --yes src/main.go
svntui lock --path /work/app --message "editing asset" --yes assets/logo.psd
svntui unlock --path /work/app --yes assets/logo.psd
svntui status --path /work/app --remote --json
```

## 更新与稀疏目录

TUI 上下文菜单 `a` 打开更新表单，填写目标修订、深度和可选目标路径后先预览，再显式应用。远端内容和属性变化分别展示。指定历史修订时，预览会说明远端状态以 HEAD 为准，实际执行使用指定修订。

```bash
svntui update --path /work/app src/main.go
svntui update --path /work/app --revision 120 --apply --yes src/main.go
svntui update --path /work/app --depth files --apply --yes assets
svntui update --path /work/app --depth infinity --apply --yes assets
```

深度支持 `empty/files/immediates/infinity/exclude`，通过 SVN `update --set-depth` 调整。最终冲突状态由 SVN 决定，失败后需要按真实状态复查。

## 历史、仓库和项目

仓库页 `Enter` 进入目录、`Backspace` 返回。历史页 `[` / `]` 前后翻页，`/` 填作者、提交说明关键词和起止日期；筛选后空页仍可继续向后翻。`b` 打开带源码的 blame，`v` 比较两个修订。

```bash
svntui history log --path /work/app --author alice --search login --since 2026-01-01 --limit 50
svntui history log --path /work/app --page --limit 100 --json
svntui history log --path /work/app --page --before 250 --json
svntui history blame --path /work/app --revision 120 src/main.go
svntui history compare --path /work/app --left 120 --right 140 src/main.go
svntui project add --name app /work/app
svntui project list --json
svntui project open app
svntui project remove app
```

分页结果中的 `next_revision` 是下一次 `--before` 的值，使用 `has_more` 判断是否继续；不要通过匹配结果的最后一条推测游标。限定 `--revision` 范围时，翻页继续带上该范围和 `--before`，以保留查询下界。日期接受本地 `YYYY-MM-DD` 或带时区的 RFC3339。

`9` 打开项目页，显示命名/最近工作副本与修改、冲突摘要，`Enter` 切换。移除项目只移除收藏/最近记录，不删除目录。

## 合并与冲突

分支页按 `m` 打开合并表单。填写源 URL 后 `Ctrl-L` 加载修订：正向模式显示可合并列表，反向模式显示已合并列表。`Space` 选取，`A` 全选，`X` 清空，`Enter` 返回表单。`Ctrl-S` 预演，结果中按 `a` 进入应用确认。执行后检查冲突与 diff，再进入提交页手动提交。

```bash
svntui merge --path /work/branch --revision 120,123-126 SOURCE_URL
svntui merge --path /work/branch --revision 120,123-126 --apply --yes SOURCE_URL
svntui merge --path /work/branch --revision 126 --reverse --apply --yes SOURCE_URL
svntui switch --path /work/app --yes TARGET_URL
svntui resolve --path /work/app --accept working --yes conflicted.txt
```

冲突页按文本、属性、树类型展示。`e`/`m` 启动编辑器/合并工具并在退出后重新读取状态；不会仅凭工具成功退出自动标记解决。目录属性或树冲突可整理后用 `resolve working`；文本仍包含冲突标记时会阻止误标记。整版选择支持 `working/mine-full/theirs-full/base`，可用选项取决于 SVN 冲突类型。

配置的 `editor` 和 `merge-tool` 是可执行文件路径；带空格的路径可作为一个参数配置。额外参数请通过包装脚本提供。

## 属性、externals 与补丁

TUI `a` 进入属性表单，多选时共用同一份目标列表；属性值 `Enter` 换行，`Tab` 下一字段，`Ctrl-S` 确认。命令行可从 UTF-8 文件读取多行属性。读取属性保留原始换行。

```bash
svntui property list --path /work/app src/main.go
svntui property get --path /work/app svn:ignore .
svntui property set --path /work/app --yes svn:needs-lock '*' assets/a.psd assets/b.psd
svntui property set --path /work/app --value-file ignore.txt --yes svn:ignore .
svntui property edit --path /work/app --yes svn:ignore .
svntui property delete --path /work/app --yes svn:needs-lock assets/a.psd
svntui externals list --path /work/app .
svntui externals set --path /work/app --value-file externals.txt --yes .
svntui patch export --path /work/app --output review.patch src/main.go
svntui patch apply --path /work/other review.patch
svntui patch apply --path /work/other --apply --yes review.patch
```

externals 展示并保留原始来源 URL、`-r` 和 `@peg` 定义；编辑后仍需明确更新。补丁导出使用相对于工作副本的 SVN 原生格式，支持检出在 `/trunk` 等子目录的工作副本。导入先预演，出现跳过、拒绝或冲突时阻止自动执行；应用前创建 SafePoint，结果需复查后提交。二进制内容不在文本补丁中，应单独传递或使用 SafePoint。

## SafePoint 与性能边界

恢复删除、缺失或替换目录前，会保护后来出现的未版本化及忽略文件。默认先保存恢复前状态；`--backup=false` 遇到需要保护的内容会中止。无法完整备份的特殊节点或嵌套工作副本会在修改前明确报错。纯二进制、替换节点、目录与文件类型变化和属性均由回归覆盖。新清单为 v4；v3 及更早清单仍保留供查看和手工提取，但拒绝自动恢复。未解决冲突及阻塞/不完整节点会在创建或恢复写入前明确拒绝，因为目前不能忠实保存 SVN 冲突元数据。复制/移动恢复为等价路径和内容变更，并提示历史关系变化。

操作日志默认 10 MiB 轮转，最多保留 5 份备份；最近记录读取按文件尾部进行。大 SVN 输出默认每路 8 MiB 内存上限，超限会返回完整 stdout 临时文件路径并报告错误，结构化结果不会被当成完整数据继续解析。完整 diff/补丁可直接导出文件。未提供实验性的 SVN shelving。
