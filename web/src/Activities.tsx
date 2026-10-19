import { useEffect, useRef, useState } from "react";
import {
  projectURL,
  ApiError,
  type Project,
  type Operation,
  type Job,
  type SafePoint,
  type Manifest,
  type Audit,
} from "./api";
import { Button, Empty, ErrorBox, Icon, useRead } from "./ui";
import { actions, time } from "./shared";
export function Activities({
  project,
  jobs,
  connected,
  onClose,
  onRefresh,
  onOperation,
  refresh,
}: {
  project: Project | undefined;
  jobs: Job[];
  connected: boolean;
  onClose: () => void;
  onRefresh: () => void;
  onOperation: (o: Operation) => void;
  refresh: number;
}) {
  const drawerRef = useRef<HTMLElement>(null);
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const element = drawerRef.current!;
    element.querySelector<HTMLElement>("button")?.focus();
    const key = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        onClose();
      }
      if (event.key === "Tab") {
        const items = [
          ...element.querySelectorAll<HTMLElement>(
            "button:not(:disabled),input,select,summary",
          ),
        ];
        const first = items[0],
          last = items.at(-1);
        if (event.shiftKey && document.activeElement === first) {
          event.preventDefault();
          last?.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first?.focus();
        }
      }
    };
    element.addEventListener("keydown", key);
    return () => {
      element.removeEventListener("keydown", key);
      previous?.focus();
    };
  }, []);
  const [tab, setTab] = useState("jobs"),
    [sid, setSid] = useState("");
  const safes = useRead<SafePoint[]>(
    tab === "safes" && project ? projectURL(project.id, "safepoints") : null,
    refresh,
  );
  const audit = useRead<Audit[]>(
    tab === "audit" && project ? projectURL(project.id, "operations") : null,
    refresh,
  );
  const manifest = useRead<Manifest>(
    sid && project
      ? projectURL(project.id, `safepoints/${encodeURIComponent(sid)}`)
      : null,
    refresh,
  );
  const relevant = project
    ? jobs.filter((j) => j.project_id === project.id)
    : jobs;
  return (
    <div className="drawer-backdrop">
      <aside
        ref={drawerRef}
        className="activity-drawer"
        role="dialog"
        aria-modal="true"
        aria-label="活动与恢复"
      >
        <header>
          <div>
            <h2>活动与恢复</h2>
            <p>{project?.name || "所有项目"}</p>
          </div>
          <button aria-label="关闭活动" onClick={onClose}>
            <Icon name="close" />
          </button>
        </header>
        <div className="drawer-tabs">
          {[
            ["jobs", "当前任务"],
            ["audit", "操作审计"],
            ["safes", "SafePoints"],
          ].map(([key, label]) => (
            <button
              className={tab === key ? "active" : ""}
              key={key}
              onClick={() => setTab(key)}
            >
              {label}
            </button>
          ))}
          <button aria-label="刷新活动" onClick={onRefresh}>
            <Icon name="refresh" size={16} />
          </button>
        </div>
        <div className="drawer-content">
          <div className="notice">
            {connected ? "实时事件已连接。" : "连接恢复中，请核对任务快照。"}
            任务由当前服务进程维护；服务重启后请查看真实工作副本和审计记录，写入不会重放。
          </div>
          {tab === "jobs" &&
            (!relevant.length ? (
              <Empty title="当前进程没有任务">操作确认后会显示在这里</Empty>
            ) : (
              relevant.map((job) => (
                <article className="job-card" key={job.id}>
                  <div className="job-heading">
                    <strong>{actions[job.action] || job.action}</strong>
                    <span className={`badge ${job.state}`}>
                      {
                        {
                          running: "执行中",
                          succeeded: "已完成",
                          failed: "失败",
                        }[job.state]
                      }
                    </span>
                  </div>
                  <p>{job.summary || "正在执行 SVN 操作…"}</p>
                  <time>{time(job.started_at)}</time>
                  {job.error && (
                    <ErrorBox
                      error={new ApiError(0, job.error.code, job.error.message)}
                    />
                  )}
                  <details>
                    <summary>查看输出</summary>
                    <pre>{job.output || "暂无输出"}</pre>
                  </details>
                  {job.safepoint_id && (
                    <button
                      onClick={() => {
                        setTab("safes");
                        setSid(job.safepoint_id!);
                      }}
                    >
                      检查保护点 {job.safepoint_id.slice(0, 14)}…
                    </button>
                  )}
                </article>
              ))
            ))}
          {tab === "audit" && (
            <>
              <ErrorBox error={audit.error} />
              {audit.loading ? (
                <Empty title="正在读取审计…" />
              ) : !audit.data?.length ? (
                <Empty title="没有此项目的审计记录" />
              ) : (
                audit.data.map((record) => (
                  <article className="job-card" key={record.id}>
                    <div className="job-heading">
                      <time>{time(record.started_at)}</time>
                      <span
                        className={`badge ${record.exit_code === 0 ? "succeeded" : "failed"}`}
                      >
                        退出码 {record.exit_code}
                      </span>
                    </div>
                    <code className="audit-command">
                      svn {(record.arguments || []).join(" ")}
                    </code>
                    <p className="muted">
                      耗时 {(record.duration / 1e9).toFixed(2)} 秒
                    </p>
                    {record.error_brief && (
                      <div className="error">{record.error_brief}</div>
                    )}
                  </article>
                ))
              )}
            </>
          )}
          {tab === "safes" && (
            <>
              <ErrorBox error={safes.error} />
              {safes.loading ? (
                <Empty title="正在读取 SafePoints…" />
              ) : !safes.data?.length ? (
                <Empty title="还没有保护点">
                  还原等保护操作创建的 SafePoint 会显示在此
                </Empty>
              ) : (
                safes.data.map((s) => (
                  <button
                    className={`safe-item ${sid === s.id ? "active" : ""}`}
                    key={s.id}
                    onClick={() => setSid(s.id)}
                  >
                    <strong>{s.reason || "工作副本保护点"}</strong>
                    <time>{time(s.created_at)}</time>
                    <code>{s.id}</code>
                  </button>
                ))
              )}
              <ErrorBox error={manifest.error} />
              {manifest.loading && <p>正在检查保护点…</p>}
              {manifest.data && (
                <section className="manifest">
                  <h3>保护点清单</h3>
                  <p>
                    基础修订 r{manifest.data.base_revision} ·{" "}
                    {((manifest.data.size_bytes || 0) / 1024).toFixed(1)} KiB
                  </p>
                  <p className="mono">{manifest.data.working_copy}</p>
                  <div className="target-list">
                    {(manifest.data.entries || []).map((e) => (
                      <code key={e.path}>
                        {e.status} · {e.path} <span>{e.kind}</span>
                      </code>
                    ))}
                  </div>
                  <Button
                    primary
                    onClick={() =>
                      onOperation({
                        action: "restore",
                        targets: [],
                        safepoint_id: manifest.data!.id,
                      })
                    }
                  >
                    预览恢复此 SafePoint
                  </Button>
                  <p className="caption muted">
                    恢复前将保护当前更改；修订或仓库身份不匹配时会拒绝恢复。
                  </p>
                </section>
              )}
            </>
          )}
        </div>
      </aside>
    </div>
  );
}
