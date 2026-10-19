import { useEffect, useRef, useState } from "react";
import {
  api,
  put,
  projectURL,
  query,
  type Project,
  type Workspace,
  type Draft,
  type Operation,
} from "./api";
import { DraftQueue, reconcileSelection, statusLabel } from "./state";
import { Button, Empty, ErrorBox, Icon, useRead } from "./ui";
import { DiffViewer } from "./DiffViewer";
export function WorkspacePage({
  project,
  refresh,
  commitEpoch,
  onOperation,
  registerFlush,
  locked,
}: {
  project: Project;
  refresh: number;
  commitEpoch: number;
  onOperation: (o: Operation) => void;
  registerFlush: (projectID: string, f: () => Promise<void>) => void;
  locked: boolean;
}) {
  const {
    data: workspace,
    error,
    loading,
  } = useRead<Workspace>(projectURL(project.id, "status?remote=1"), refresh);
  const [selected, setSelected] = useState<string[]>([]),
    [message, setMessage] = useState(""),
    [focus, setFocus] = useState(""),
    [filter, setFilter] = useState(""),
    [search, setSearch] = useState(""),
    [saveState, setSaveState] = useState("正在载入草稿…"),
    [draftError, setDraftError] = useState<unknown>(null),
    [draftReady, setDraftReady] = useState(false),
    [draftReload, setDraftReload] = useState(0),
    [lost, setLost] = useState(0),
    [tableSize, setTableSize] = useState(43);
  const lastSelection = useRef<number | null>(null);
  const queue = useRef<DraftQueue | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const rows = workspace?.entries || [];
  const visible = rows.filter(
    (e) =>
      e.path.toLowerCase().includes(search.toLowerCase()) &&
      (!filter ||
        (filter === "conflict"
          ? statusLabel(e) === "冲突"
          : filter === "props"
            ? e.props === "modified"
            : filter === "group"
              ? !!e.changelist
              : e.item === filter)),
  );
  const diff = useRead<{ text: string }>(
    focus ? projectURL(project.id, `diff?${query({ target: focus })}`) : null,
    refresh,
  );
  const draftContent = useRef({ targets: selected, message });
  draftContent.current = { targets: selected, message };
  useEffect(() => {
    const controller = new AbortController();
    let mounted = true;
    setDraftReady(false);
    setDraftError(null);
    setSaveState("正在载入草稿…");
    api<Draft>(projectURL(project.id, "draft"), { signal: controller.signal })
      .then((draft) => {
        if (!mounted) return;
        setSelected(draft.targets || []);
        setMessage(draft.message);
        queue.current = new DraftQueue(
          draft,
          (d) => put<Draft>(projectURL(project.id, "draft"), d),
          (state) => {
            if (mounted) setSaveState(state);
          },
        );
        setSaveState("已保存");
        setDraftReady(true);
      })
      .catch((e) => {
        if (mounted) setDraftError(e);
      });
    return () => {
      mounted = false;
      controller.abort();
      clearTimeout(timer.current);
      queue.current?.flush().catch(() => {});
    };
  }, [project.id, commitEpoch, draftReload]);
  const selectionReady = draftReady && !!workspace && !loading && !error;
  // 提交和切换项目前排空保存队列，确认前冻结提交面板。
  useEffect(() => {
    registerFlush(project.id, async () => {
      clearTimeout(timer.current);
      if (!draftReady || !queue.current) throw new Error("草稿尚未载入");
      queue.current.update({
        ...draftContent.current,
        targets: workspace
          ? reconcileSelection(draftContent.current.targets, workspace.entries)
          : draftContent.current.targets,
      });
      try {
        await queue.current.flush();
        setDraftError(null);
      } catch (e) {
        setDraftError(e);
        throw e;
      }
    });
  }, [draftReady, workspace, registerFlush, project.id]);
  // 防抖只合并内容；DraftQueue 串行携带服务器 revision，错误保持可见。
  useEffect(() => {
    if (!draftReady || !queue.current) return;
    queue.current.update({
      targets: workspace
        ? reconcileSelection(selected, workspace.entries)
        : selected,
      message,
    });
    clearTimeout(timer.current);
    timer.current = setTimeout(() => {
      queue.current
        ?.flush()
        .then(() => setDraftError(null))
        .catch(setDraftError);
    }, 550);
    return () => clearTimeout(timer.current);
  }, [selected, message, draftReady, workspace]);
  useEffect(() => {
    if (!workspace || !draftReady) return;
    const previous = draftContent.current.targets;
    const next = reconcileSelection(previous, workspace.entries);
    setLost(previous.length - next.length);
    if (next.length !== previous.length) setSelected(next);
    setFocus((prev) =>
      workspace.entries.some((e) => e.path === prev)
        ? prev
        : workspace.entries[0]?.path || "",
    );
  }, [workspace, draftReady]);
  const operation = (action: Operation["action"], targets = selected) =>
    onOperation({
      action,
      targets: workspace
        ? reconcileSelection(targets, workspace.entries)
        : targets,
      message: action === "commit" ? message : undefined,
    });
  useEffect(() => {
    const keyboard = (e: KeyboardEvent) => {
      const field =
        e.target instanceof HTMLElement &&
        ["INPUT", "TEXTAREA", "SELECT"].includes(e.target.tagName);
      if (
        (e.metaKey || e.ctrlKey) &&
        e.key === "Enter" &&
        selected.length &&
        message.trim() &&
        !locked
      ) {
        e.preventDefault();
        operation("commit");
      }
      if (e.key === "/" && !field) {
        e.preventDefault();
        document.getElementById("file-search")?.focus();
      }
    };
    window.addEventListener("keydown", keyboard);
    return () => window.removeEventListener("keydown", keyboard);
  }, [selected, message, locked]);
  useEffect(() => {
    lastSelection.current = null;
  }, [search, filter]);
  const choose = (path: string, checked: boolean) =>
    setSelected((prev) =>
      checked ? [...new Set([...prev, path])] : prev.filter((p) => p !== path),
    );
  return (
    <div className="workspace">
      <div
        className="work-main"
        style={{ "--table-size": `${tableSize}%` } as React.CSSProperties}
      >
        <section className="changes panel">
          <div className="panel-head">
            <div>
              <strong>本地更改</strong>
              <span className="count">{rows.length}</span>
            </div>
            <span className="muted" title={workspace?.url}>
              r{workspace?.revision ?? "—"} · 已选 {selected.length} 项
            </span>
          </div>
          <div className="changes-tools">
            <label className="search">
              <Icon name="search" size={15} />
              <input
                id="file-search"
                aria-label="搜索文件"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="搜索文件…  /"
              />
            </label>
            <select
              aria-label="状态筛选"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            >
              <option value="">所有状态</option>
              <option value="modified">修改</option>
              <option value="added">新增</option>
              <option value="deleted">删除</option>
              <option value="unversioned">未版本化</option>
              <option value="conflict">冲突</option>
              <option value="missing">丢失</option>
              <option value="props">属性修改</option>
              <option value="group">有变更分组</option>
            </select>
          </div>
          <div className="selection-actions">
            <button
              onClick={() =>
                setSelected((prev) => [
                  ...new Set([...prev, ...visible.map((e) => e.path)]),
                ])
              }
              disabled={!selectionReady || locked}
            >
              选择可见项
            </button>
            <button
              onClick={() => setSelected([])}
              disabled={!selectionReady || locked}
            >
              取消选择
            </button>
            <span>{visible.length} 项可见</span>
          </div>
          <ErrorBox error={error} />
          {lost > 0 && (
            <div className="notice">
              {lost} 个已选路径已不在当前状态中，已移出选择。
            </div>
          )}
          <div className="table-scroll">
            {loading ? (
              <Empty title="正在读取工作副本…" />
            ) : !visible.length ? (
              <Empty title={rows.length ? "没有匹配的更改" : "工作副本干净"}>
                使用“更新”同步仓库，或刷新本地状态
              </Empty>
            ) : (
              <table className="changes-table">
                <thead>
                  <tr>
                    <th className="check-col">
                      <input
                        aria-label="选择全部可见路径"
                        type="checkbox"
                        disabled={!selectionReady || locked}
                        checked={
                          visible.length > 0 &&
                          visible.every((e) => selected.includes(e.path))
                        }
                        onChange={(e) =>
                          e.target.checked
                            ? setSelected((prev) => [
                                ...new Set([
                                  ...prev,
                                  ...visible.map((x) => x.path),
                                ]),
                              ])
                            : setSelected((prev) =>
                                prev.filter(
                                  (p) => !visible.some((x) => x.path === p),
                                ),
                              )
                        }
                      />
                    </th>
                    <th>文件 / 路径</th>
                    <th>状态</th>
                    <th>分组</th>
                    <th>仓库</th>
                  </tr>
                </thead>
                <tbody>
                  {visible.map((entry, i) => (
                    <tr
                      key={entry.path}
                      className={focus === entry.path ? "focused" : ""}
                      tabIndex={0}
                      aria-selected={selected.includes(entry.path)}
                      onClick={() => setFocus(entry.path)}
                      onKeyDown={(e) => {
                        if (e.key === " ") {
                          e.preventDefault();
                          if (selectionReady && !locked)
                            choose(entry.path, !selected.includes(entry.path));
                        }
                        if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                          e.preventDefault();
                          const next = e.key === "ArrowDown" ? i + 1 : i - 1;
                          (
                            e.currentTarget.parentElement?.children[
                              next
                            ] as HTMLElement
                          )?.focus();
                          if (visible[next]) setFocus(visible[next].path);
                        }
                      }}
                    >
                      <td>
                        <input
                          aria-label={`选择 ${entry.path}`}
                          type="checkbox"
                          checked={selected.includes(entry.path)}
                          disabled={!selectionReady || locked}
                          onClick={(e) => {
                            e.stopPropagation();
                            if (e.shiftKey && lastSelection.current !== null) {
                              const start = Math.min(lastSelection.current, i),
                                end = Math.max(lastSelection.current, i);
                              setSelected((prev) => [
                                ...new Set([
                                  ...prev,
                                  ...visible
                                    .slice(start, end + 1)
                                    .map((x) => x.path),
                                ]),
                              ]);
                              e.preventDefault();
                            }
                            lastSelection.current = i;
                          }}
                          onChange={(e) => choose(entry.path, e.target.checked)}
                        />
                      </td>
                      <td className="path-cell" title={entry.path}>
                        <Icon name="folder" size={14} />
                        <span>{entry.path}</span>
                      </td>
                      <td>
                        <span
                          className={`badge ${statusLabel(entry) === "冲突" ? "conflicted" : entry.item}`}
                        >
                          {statusLabel(entry)}
                        </span>
                        {entry.props === "modified" &&
                          entry.item !== "normal" && (
                            <span className="prop-dot" title="属性修改">
                              {" "}
                              P
                            </span>
                          )}
                      </td>
                      <td>{entry.changelist || "—"}</td>
                      <td>
                        {entry.repos_item &&
                        entry.repos_item !== "none" &&
                        entry.repos_item !== "normal" ? (
                          <span className="badge remote">
                            远端
                            {statusLabel({ ...entry, item: entry.repos_item })}
                          </span>
                        ) : (
                          "—"
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
          <div className="file-actions">
            <Button
              onClick={() => operation("add")}
              disabled={!selected.length || locked}
            >
              添加
            </Button>
            <Button
              onClick={() => operation("delete")}
              disabled={!selected.length || locked}
            >
              删除
            </Button>
            <Button
              onClick={() => operation("move")}
              disabled={selected.length !== 1 || locked}
            >
              移动
            </Button>
            <Button
              onClick={() => operation("revert")}
              disabled={!selected.length || locked}
            >
              还原
            </Button>
            <Button
              onClick={() => operation("changelist")}
              disabled={!selected.length || locked}
            >
              变更分组
            </Button>
            <Button
              onClick={() => operation("update")}
              disabled={!selected.length || locked}
            >
              更新所选
            </Button>
          </div>
        </section>
        <div
          className="pane-resizer"
          role="separator"
          tabIndex={0}
          aria-label="调整文件表与差异窗格高度"
          aria-orientation="horizontal"
          onKeyDown={(e) => {
            if (e.key === "ArrowUp" || e.key === "ArrowDown") {
              e.preventDefault();
              setTableSize((v) =>
                Math.max(
                  25,
                  Math.min(75, v + (e.key === "ArrowDown" ? 3 : -3)),
                ),
              );
            }
          }}
          onPointerDown={(e) => {
            e.preventDefault();
            const rect = e.currentTarget.parentElement!.getBoundingClientRect();
            const move = (event: PointerEvent) =>
              setTableSize(
                Math.max(
                  25,
                  Math.min(
                    75,
                    ((event.clientY - rect.top) / rect.height) * 100,
                  ),
                ),
              );
            const end = () => {
              window.removeEventListener("pointermove", move);
              window.removeEventListener("pointerup", end);
            };
            window.addEventListener("pointermove", move);
            window.addEventListener("pointerup", end);
          }}
        />
        <DiffViewer
          text={diff.data?.text || ""}
          loading={diff.loading}
          error={diff.error}
          title={focus}
          files={rows.map((e) => e.path)}
          onFile={setFocus}
        />
      </div>
      <aside className="commit-panel panel">
        <div className="panel-head">
          <strong>提交更改</strong>
          <Icon name="branch" />
        </div>
        <div className="commit-body">
          <div className="commit-count">
            <strong>{selected.length}</strong>
            <span>个明确选择的路径</span>
          </div>
          <div className="selected-targets">
            {selected.length ? (
              selected.map((p) => (
                <div key={p}>
                  <code title={p}>{p}</code>
                  <button
                    aria-label={`移除选择 ${p}`}
                    disabled={locked}
                    onClick={() => choose(p, false)}
                  >
                    ×
                  </button>
                </div>
              ))
            ) : (
              <p className="muted">
                在文件表中勾选需要提交的更改。新增文件需先添加到版本控制。
              </p>
            )}
          </div>
          <label>
            提交说明
            <textarea
              aria-label="提交说明"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              disabled={!draftReady || locked}
              placeholder="描述本次更改的目的…"
              rows={7}
            />
          </label>
          <div
            className={`draft-state ${draftError ? "danger" : ""}`}
            role="status"
          >
            <span className="status-dot" />
            {saveState}
          </div>
          <ErrorBox error={draftError} />
          {!!draftError && (
            <div className="button-row">
              <Button
                onClick={() =>
                  queue.current
                    ?.retry()
                    .then(() => setDraftError(null))
                    .catch(setDraftError)
                }
              >
                重试保存
              </Button>
              <Button
                onClick={() => setDraftReload((v) => v + 1)}
                title="以服务器草稿替换当前内容"
              >
                载入服务器草稿
              </Button>
            </div>
          )}
          <p className="muted caption">
            草稿保存在本地服务，重新打开仍可继续。提交前将校验实际目标和内容。
          </p>
        </div>
        <footer>
          <Button
            primary
            onClick={() => operation("commit")}
            disabled={
              !selectionReady || !selected.length || !message.trim() || locked
            }
          >
            <Icon name="check" />
            预览提交 <kbd>⌘/Ctrl ↵</kbd>
          </Button>
        </footer>
      </aside>
    </div>
  );
}
