# 架构说明

```text
cmd/svntui
  ├─ internal/tui           终端输入、渲染和异步任务循环
  ├─ internal/domain        TUI/CLI 共享操作请求、结果与查询接口
  ├─ internal/service       风险校验、操作映射和模块数据
  ├─ internal/svn           系统 svn 执行、XML 解析和错误分类
  ├─ internal/automation    调度、任务状态、Webhook 和退避
  ├─ internal/safepoint     快照、恢复和保留策略
  ├─ internal/wclock        Unix flock / Windows LockFileEx 工作副本写锁
  ├─ internal/appdirs       XDG / Windows 用户目录解析
  ├─ internal/operation     脱敏 JSONL 审计记录
  └─ internal/storage       原子 JSON 持久化
```

关键约束：

- 参数以 `exec.CommandContext` 的独立参数传给 `svn`，不拼接或执行 shell 命令。
- TUI 所有阻塞操作都在渲染循环外执行；只读任务可取消。
- TUI、CLI 与后台调度器使用同一工作副本锁；交互操作即使从子目录启动也按工作副本根加锁。
- 预览、确认和执行共用 NormalizeAction 的实际目标；读操作可取消，写操作必须显式确认。
- 原生 changelist 由统一状态 XML 解析器展开；保留 SVN 本地按文件分组语义。
- 本地字面目标与 URL/peg 分开处理，按各 SVN 子命令的解析规则传递，避免 @ 文件名误选。
- SafePoint 在任何覆盖/删除前扫描受影响子树，完整备份未版本化和忽略项，无法保护时中止；补丁前恢复节点类型，补丁后恢复内容并检查剩余拒绝项。
- 日志轮转及尾读共享文件锁；普通 SVN 输出有界并提供溢出文件，完整补丁流式写入文件。
- 自动更新任务绑定 Repository UUID、Root URL 和完整分支 URL。
- 写操作失败或中断后重新读取真实 SVN 状态，不假设事务回滚。
- merge 默认 dry-run，成功执行后仍需用户复查并手动提交。
- 终端、窗口尺寸信号、进程终止信号和后台任务安装均通过构建标签隔离平台实现；Windows 控制台会在运行期间启用 UTF-8 与 VT 模式，并在退出时恢复原设置。
- Windows 的 SVN 子进程独立于父控制台运行，避免继承的线程代码页改变中文参数；标准输出和错误仍由管道收集。外部编辑器继续使用交互终端。
