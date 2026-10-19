import { useEffect, useState } from "react";
import {
  projectURL,
  query,
  type Project,
  type History,
  type HistoryEntry,
} from "./api";
import { Button, Empty, ErrorBox, useRead } from "./ui";
import { DiffViewer } from "./DiffViewer";
import { time } from "./shared";
export function HistoryPage({
  project,
  refresh,
}: {
  project: Project;
  refresh: number;
}) {
  const [form, setForm] = useState({
      target: "",
      author: "",
      search: "",
      since: "",
      until: "",
    }),
    [filters, setFilters] = useState(form),
    [cursors, setCursors] = useState<number[]>([]),
    [selected, setSelected] = useState<HistoryEntry | null>(null);
  const history = useRead<History>(
    projectURL(
      project.id,
      `history?${query({ ...filters, limit: 50, before_revision: cursors.at(-1) })}`,
    ),
    refresh,
  );
  const diff = useRead<{ text: string }>(
    selected
      ? projectURL(
          project.id,
          `history/${selected.revision}/diff?${query({ target: filters.target })}`,
        )
      : null,
    refresh,
  );
  useEffect(() => {
    setSelected(history.data?.entries[0] || null);
  }, [history.data]);
  return (
    <div className="history-page">
      <section className="panel history-list">
        <div className="panel-head">
          <strong>提交历史</strong>
          <span>每页 50 条</span>
        </div>
        <form
          className="history-filters"
          onSubmit={(e) => {
            e.preventDefault();
            setFilters(form);
            setCursors([]);
          }}
        >
          <label>
            工作副本相对路径
            <input
              value={form.target}
              onChange={(e) => setForm({ ...form, target: e.target.value })}
              placeholder="整个工作副本"
            />
          </label>
          <div className="form-grid">
            <label>
              作者
              <input
                value={form.author}
                onChange={(e) => setForm({ ...form, author: e.target.value })}
              />
            </label>
            <label>
              提交说明包含
              <input
                value={form.search}
                onChange={(e) => setForm({ ...form, search: e.target.value })}
              />
            </label>
            <label>
              开始日期
              <input
                type="date"
                value={form.since}
                onChange={(e) => setForm({ ...form, since: e.target.value })}
              />
            </label>
            <label>
              结束日期
              <input
                type="date"
                value={form.until}
                onChange={(e) => setForm({ ...form, until: e.target.value })}
              />
            </label>
          </div>
          <button className="primary">应用筛选</button>
        </form>
        <ErrorBox error={history.error} />
        <div className="history-entries">
          {history.loading ? (
            <Empty title="正在读取历史…" />
          ) : !history.data?.entries.length ? (
            <Empty title="本页没有匹配记录">筛选后的空页仍可能有下一页</Empty>
          ) : (
            history.data.entries.map((entry) => (
              <button
                className={`history-entry ${selected?.revision === entry.revision ? "active" : ""}`}
                key={entry.revision}
                onClick={() => setSelected(entry)}
              >
                <div>
                  <strong className="revision">r{entry.revision}</strong>
                  <span>{entry.author || "未知作者"}</span>
                  <time>{time(entry.date)}</time>
                </div>
                <p>{entry.message || "（无提交说明）"}</p>
                <span className="muted">
                  {entry.paths?.length || 0} 个变更路径
                </span>
              </button>
            ))
          )}
        </div>
        <footer>
          <Button
            disabled={!cursors.length || history.loading}
            onClick={() => setCursors(cursors.slice(0, -1))}
          >
            上一页
          </Button>
          <span>第 {cursors.length + 1} 页</span>
          <Button
            disabled={!history.data?.has_more || history.loading}
            onClick={() =>
              setCursors([...cursors, history.data!.next_revision])
            }
          >
            下一页
          </Button>
        </footer>
      </section>
      <div className="history-detail">
        {selected && (
          <section className="panel revision-detail">
            <div className="panel-head">
              <strong>r{selected.revision}</strong>
              <span>
                {selected.author} · {time(selected.date)}
              </span>
            </div>
            <p className="commit-message">
              {selected.message || "（无提交说明）"}
            </p>
            <div className="history-paths">
              {(selected.paths || []).map((p, i) => (
                <div key={i}>
                  <span className="badge">{p.action}</span>
                  <code>{p.path}</code>
                  {p.copy_from_path && (
                    <small>
                      ← {p.copy_from_path}@r{p.copy_from_revision}
                    </small>
                  )}
                </div>
              ))}
            </div>
            <p className="caption muted">
              下方显示本次修订在{filters.target || "整个工作副本"}
              的差异；以上路径属于仓库命名空间。
            </p>
          </section>
        )}
        <DiffViewer
          text={diff.data?.text || ""}
          loading={diff.loading}
          error={diff.error}
          title={
            selected
              ? `r${selected.revision} · ${filters.target || "完整修订"}`
              : ""
          }
        />
      </div>
    </div>
  );
}
