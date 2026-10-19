import { useCallback, useEffect, useRef, useState } from "react";
import { api, session, type Project, type Job, type Operation } from "./api";
import { Button, Empty, ErrorBox, Icon, Modal } from "./ui";
import { WorkspacePage } from "./WorkspacePage";
import { HistoryPage } from "./HistoryPage";
import { ConflictsPage } from "./ConflictsPage";
import { Activities } from "./Activities";
import { OperationDialog } from "./OperationDialog";
import { mergeJobs, jobNeedsRefresh } from "./state";
import { RegisterProject } from "./RegisterProject";
export default function App() {
  const [auth, setAuth] = useState<{ version: string } | null>(null),
    [authError, setAuthError] = useState<unknown>(null);
  useEffect(() => {
    session().then(setAuth).catch(setAuthError);
  }, []);
  if (!auth)
    return (
      <div className="auth-screen">
        <div className="auth-card">
          <div className="brand-mark">
            <Icon name="branch" size={28} />
          </div>
          <h1>
            svntui <span>工作台</span>
          </h1>
          {authError ? (
            <>
              <ErrorBox error={authError} />
              <p>
                请从当前本地服务启动时输出的完整链接重新打开。旧进程的会话和一次性链接不能用于新进程。
              </p>
              <Button onClick={() => location.reload()}>检查现有会话</Button>
            </>
          ) : (
            <p>正在建立安全的本地会话…</p>
          )}
        </div>
      </div>
    );
  return <AuthenticatedApp version={auth.version} />;
}
function AuthenticatedApp({ version }: { version: string }) {
  const [projects, setProjects] = useState<Project[]>([]),
    [projectID, setProjectID] = useState(
      localStorage.getItem("project-id") || "",
    ),
    [page, setPage] = useState("workspace"),
    [error, setError] = useState<unknown>(null),
    [loading, setLoading] = useState(true),
    [refresh, setRefresh] = useState(0),
    [jobs, setJobs] = useState<Job[]>([]),
    [connected, setConnected] = useState(false),
    [register, setRegister] = useState(false),
    [drawer, setDrawer] = useState(false),
    [operation, setOperation] = useState<Operation | null>(null),
    [remove, setRemove] = useState(false),
    [commitEpoch, setCommitEpoch] = useState(0),
    [railWidth, setRailWidth] = useState(
      Number(localStorage.getItem("rail-width")) || 224,
    );
  const flushRef = useRef<() => Promise<void>>(async () => {});
  const currentID = useRef(projectID);
  currentID.current = projectID;
  const jobSnapshot = useRef<Job[]>([]);
  const project = projects.find((p) => p.id === projectID);
  const active = jobs.some(
    (j) => j.project_id === projectID && j.state === "running",
  );
  const refreshAll = useCallback(() => setRefresh((v) => v + 1), []);
  const registerFlush = useCallback((f: () => Promise<void>) => {
    flushRef.current = f;
  }, []);
  const loadProjects = useCallback(async () => {
    try {
      const next = await api<Project[]>("/projects");
      setProjects(next || []);
      setProjectID((prev) =>
        next.some((p) => p.id === prev) ? prev : next[0]?.id || "",
      );
      setError(null);
    } catch (e) {
      setError(e);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    loadProjects();
  }, [loadProjects, refresh]);
  useEffect(() => {
    localStorage.setItem("project-id", projectID);
  }, [projectID]);
  // 终态和首次收到的完成任务都会刷新；旧快照不得覆盖较新的 SSE 结果。
  const applyJobs = useCallback(
    (incoming: Job[]) => {
      const previous = new Map(jobSnapshot.current.map((job) => [job.id, job]));
      const completed = incoming.filter((job) =>
        jobNeedsRefresh(previous.get(job.id), job),
      );
      jobSnapshot.current = mergeJobs(jobSnapshot.current, incoming);
      setJobs(jobSnapshot.current);
      if (completed.length) {
        refreshAll();
        if (completed.some((job) => job.project_id === currentID.current))
          setCommitEpoch((value) => value + 1);
      }
    },
    [refreshAll],
  );
  const receive = useCallback((job: Job) => applyJobs([job]), [applyJobs]);
  const reloadJobs = useCallback(async () => {
    try {
      const snapshot = await api<Job[]>("/jobs");
      applyJobs(snapshot);
      setError(null);
    } catch (error) {
      setError(error);
    }
  }, [applyJobs]);
  useEffect(() => {
    reloadJobs();
    const events = new EventSource("/api/v1/events");
    events.addEventListener("job", (e) => {
      try {
        receive(JSON.parse((e as MessageEvent).data));
      } catch {
        setError(new Error("任务事件无法读取，请刷新活动"));
      }
    });
    events.onopen = () => {
      setConnected(true);
      reloadJobs();
      refreshAll();
    };
    events.onerror = () => setConnected(false);
    return () => events.close();
  }, [receive, reloadJobs, refreshAll]);
  useEffect(() => {
    const key = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "r") {
        e.preventDefault();
        refreshAll();
        reloadJobs();
      }
      if (e.key === "Escape") setDrawer(false);
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [refreshAll, reloadJobs]);
  const chooseProject = async (id: string) => {
    try {
      if (project && id !== projectID) await flushRef.current();
      setProjectID(id);
      setPage("workspace");
      setDrawer(false);
    } catch (e) {
      setError(e);
    }
  };
  const beginOperation = async (o: Operation) => {
    setError(null);
    try {
      await flushRef.current();
      setOperation(o);
    } catch (e) {
      setError(e);
    }
  };
  const resizeRail = (e: React.PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    const move = (event: PointerEvent) => {
      const width = Math.max(180, Math.min(380, event.clientX));
      setRailWidth(width);
      localStorage.setItem("rail-width", String(width));
    };
    const end = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", end);
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", end);
  };
  return (
    <div
      className="app-shell"
      style={{ "--rail-width": `${railWidth}px` } as React.CSSProperties}
    >
      <aside className="project-rail">
        <div className="brand">
          <div className="brand-mark">
            <Icon name="branch" size={23} />
          </div>
          <div>
            <strong>svntui</strong>
            <span>SVN 工作台</span>
          </div>
          <span className="version">{version}</span>
        </div>
        <div className="rail-section">
          <span>本地项目</span>
          <button
            aria-label="添加项目"
            title="添加工作副本"
            onClick={() => setRegister(true)}
          >
            <Icon name="plus" size={17} />
          </button>
        </div>
        <nav className="projects" aria-label="本地项目">
          {projects.map((p) => (
            <button
              className={`project ${projectID === p.id ? "active" : ""}`}
              key={p.id}
              onClick={() => chooseProject(p.id)}
              title={p.path}
            >
              <Icon name="folder" />
              <span>
                <strong>{p.name}</strong>
                <small>
                  {p.error
                    ? "工作副本不可用"
                    : `${p.modified} 项修改 · ${p.conflicts} 项冲突`}
                </small>
              </span>
              {p.modified > 0 && <b>{p.modified}</b>}
            </button>
          ))}
        </nav>
        <div className="rail-bottom">
          <div>
            <span className={`connection-dot ${connected ? "online" : ""}`} />
            {connected ? "本地服务已连接" : "事件连接恢复中"}
          </div>
          <button onClick={() => setDrawer(true)}>
            <Icon name="activity" />
            活动与恢复{" "}
            <span>
              {jobs.filter((j) => j.state === "running").length || ""}
            </span>
          </button>
          <span className="caption">离线资源 · 仅本机访问</span>
        </div>
      </aside>
      <div
        className="rail-resizer"
        role="separator"
        aria-label="调整项目栏宽度"
        aria-orientation="vertical"
        tabIndex={0}
        onPointerDown={resizeRail}
        onKeyDown={(e) => {
          if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
            e.preventDefault();
            setRailWidth((v) =>
              Math.max(
                180,
                Math.min(380, v + (e.key === "ArrowRight" ? 16 : -16)),
              ),
            );
          }
        }}
      />
      <main className="main-shell">
        <header className="topbar">
          <div className="project-identity">
            <Icon name="folder" />
            <strong>{project?.name || "欢迎使用 svntui"}</strong>
            <span className="path" title={project?.path}>
              {project?.path || "连接本地工作副本，开始管理更改"}
            </span>
          </div>
          <div className="top-actions">
            <Button
              onClick={() => {
                refreshAll();
                reloadJobs();
              }}
              title="刷新状态 (⌘/Ctrl+R)"
            >
              <Icon name="refresh" />
              刷新
            </Button>
            {project && (
              <>
                <Button
                  onClick={() =>
                    beginOperation({ action: "update", targets: [] })
                  }
                  disabled={active}
                >
                  更新工作副本
                </Button>
                <details className="project-menu">
                  <summary aria-label="项目操作">•••</summary>
                  <div>
                    <button
                      onClick={() =>
                        beginOperation({ action: "cleanup", targets: [] })
                      }
                      disabled={active}
                    >
                      清理工作副本
                    </button>
                    <button onClick={() => setRemove(true)} disabled={active}>
                      移除项目登记
                    </button>
                  </div>
                </details>
              </>
            )}
          </div>
        </header>
        {project && (
          <nav className="workspace-tabs" aria-label="项目视图">
            {[
              ["workspace", "folder", "工作区"],
              ["history", "history", "提交历史"],
              ["conflicts", "conflict", "冲突处理"],
            ].map(([key, icon, label]) => (
              <button
                className={page === key ? "active" : ""}
                key={key}
                onClick={() => setPage(key)}
              >
                <Icon name={icon} size={17} />
                {label}
                {key === "conflicts" && project.conflicts > 0 && (
                  <span className="count danger">{project.conflicts}</span>
                )}
              </button>
            ))}
            <span>工作副本 / 本地</span>
          </nav>
        )}
        <ErrorBox error={error} />
        {project?.error && <ErrorBox error={new Error(project.error)} />}
        <div className="page-content">
          {loading ? (
            <Empty title="正在读取本地项目…" />
          ) : !project ? (
            <div className="welcome">
              <div className="welcome-visual">
                <Icon name="branch" size={64} />
              </div>
              <span className="eyebrow">本地工作副本</span>
              <h1>添加第一个 SVN 工作副本</h1>
              <p>
                连接本地 SVN 工作副本。查看差异、整理提交、处理冲突，
                <br />
                在执行前明确每个操作的范围。
              </p>
              <Button primary onClick={() => setRegister(true)}>
                <Icon name="plus" />
                添加本地工作副本
              </Button>
              <p className="caption">使用已检出的目录；不会创建新仓库</p>
            </div>
          ) : (
            <>
              <div
                className={
                  page === "workspace" ? "page-visible" : "page-hidden"
                }
              >
                <WorkspacePage
                  key={project.id}
                  project={project}
                  refresh={refresh}
                  commitEpoch={commitEpoch}
                  registerFlush={registerFlush}
                  onOperation={beginOperation}
                  locked={active || operation?.action === "commit"}
                />
              </div>
              {page === "history" && (
                <HistoryPage project={project} refresh={refresh} />
              )}{" "}
              {page === "conflicts" && (
                <ConflictsPage
                  project={project}
                  refresh={refresh}
                  onOperation={beginOperation}
                />
              )}
            </>
          )}
        </div>
        <footer className="statusbar">
          <span>
            <span className={`connection-dot ${connected ? "online" : ""}`} />
            {connected ? "实时同步" : "等待重新连接"}
          </span>
          <span>
            {project
              ? `${project.modified} 项修改 · ${project.unversioned} 项未版本化`
              : "尚未连接工作副本"}
          </span>
          <button onClick={() => setDrawer(true)}>
            <Icon name="activity" size={14} />
            {active ? "有操作正在执行" : "查看活动与 SafePoints"}
          </button>
        </footer>
      </main>
      {register && (
        <RegisterProject
          onClose={() => setRegister(false)}
          onRegistered={(p) => {
            setProjects((prev) => [...prev.filter((x) => x.id !== p.id), p]);
            setProjectID(p.id);
            setPage("workspace");
            setRegister(false);
            refreshAll();
          }}
        />
      )}
      {remove && project && (
        <Modal title="移除项目登记" onClose={() => setRemove(false)}>
          <div className="modal-content">
            <p>
              从项目列表移除 <strong>{project.name}</strong>？
            </p>
            <p className="muted">仅移除登记；本地工作副本文件会保留。</p>
          </div>
          <footer>
            <Button onClick={() => setRemove(false)}>取消</Button>
            <Button
              primary
              onClick={async () => {
                try {
                  await flushRef.current();
                  await api(`/projects/${project.id}`, { method: "DELETE" });
                  setRemove(false);
                  refreshAll();
                } catch (e) {
                  setError(e);
                  setRemove(false);
                }
              }}
            >
              移除登记
            </Button>
          </footer>
        </Modal>
      )}
      {drawer && (
        <Activities
          project={project}
          jobs={jobs}
          connected={connected}
          onClose={() => setDrawer(false)}
          onRefresh={() => {
            reloadJobs();
            refreshAll();
          }}
          onOperation={beginOperation}
          refresh={refresh}
        />
      )}{" "}
      {operation && project && (
        <OperationDialog
          project={project}
          request={operation}
          beforeCommit={() => flushRef.current()}
          onClose={() => setOperation(null)}
          onJob={(job) => {
            receive(job);
            setOperation(null);
            setDrawer(true);
          }}
        />
      )}
    </div>
  );
}
