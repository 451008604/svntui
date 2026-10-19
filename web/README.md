# svntui 本地浏览器界面

React + TypeScript 的本机 SVN 客户端。构建产物由 Go 二进制嵌入，浏览器无需 CDN、Node 或外网。

## 可复现构建

使用 Node 24（当前构建基线 24.19.0）和 npm：

```sh
npm ci --registry=https://registry.npmjs.org
npm run typecheck
npm test
npm run build
```

版本直接固定在 package.json，传递依赖完整记录在 package-lock.json。`npm run build` 将资源输出到 `../internal/webui/dist`，并通过 Vite `build.license` 生成非隐藏的 `THIRD_PARTY_LICENSES.md`。发行打包应携带该许可证文件。Go 的 `webui.Assets()` 返回去掉 dist 前缀的文件系统。

请通过 `svntui web` 提供的同源启动链接使用界面。Vite 只用于构建，独立开发服务器并未绕过本地服务的 Origin、Host 和 CSRF 校验。

## 实现边界

- `api.ts`：会话交换、CSRF、typed HTTP DTO、JSON envelope 与错误分类。
- `state.ts`：精确路径选择、统一差异解析、串行 revision 草稿保存、有界且单向合并的任务状态。
- `WorkspacePage` / `HistoryPage` / `ConflictsPage`：工作区、历史分页和只读三版本冲突比较。
- `OperationDialog`：真实服务器预览、实际范围展示、YES 高风险确认和单次明确执行。
- `Activities`：SSE/快照任务恢复、操作审计、项目作用域的 SafePoint 清单与恢复。

所有写入均经过 API allowlist 和服务器验证；网络异常不会自动重发写入。仓库认证请在本机终端完成，界面不收集或保存 SVN 密码。
