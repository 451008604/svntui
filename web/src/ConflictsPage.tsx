import { useEffect, useState } from "react";
import { projectURL, type Project, type Operation, type Conflict } from "./api";
import { Button, Empty, ErrorBox, useRead } from "./ui";
export function ConflictsPage({
  project,
  refresh,
  onOperation,
}: {
  project: Project;
  refresh: number;
  onOperation: (o: Operation) => void;
}) {
  const { data, error, loading } = useRead<Conflict[]>(
    projectURL(project.id, "conflicts"),
    refresh,
  );
  const [path, setPath] = useState("");
  useEffect(
    () =>
      setPath((prev) =>
        data?.some((c) => c.path === prev) ? prev : data?.[0]?.path || "",
      ),
    [data],
  );
  const conflict = data?.find((c) => c.path === path);
  const names: Record<string, string> = {
    working: "使用已整理的工作文件",
    base: "使用基础版本",
    "mine-full": "使用完整本地版本",
    "theirs-full": "使用完整远端版本",
  };
  return (
    <div className="conflicts-page">
      <div className="panel conflict-list">
        <div className="panel-head">
          <strong>冲突文件</strong>
          <span className="count">{data?.length || 0}</span>
        </div>
        <ErrorBox error={error} />
        {loading ? (
          <Empty title="正在读取冲突…" />
        ) : !data?.length ? (
          <Empty title="没有未解决冲突">
            更新后如出现冲突，可在此比较版本并明确选择解决方式
          </Empty>
        ) : (
          data.map((c) => (
            <button
              key={c.path}
              onClick={() => setPath(c.path)}
              className={path === c.path ? "active" : ""}
            >
              <span className="badge conflicted">
                {c.kinds
                  .map(
                    (k) =>
                      ({ text: "文本", property: "属性", tree: "目录树" })[k] ||
                      k,
                  )
                  .join(" / ")}
              </span>
              <code>{c.path}</code>
            </button>
          ))
        )}
      </div>
      {conflict && (
        <div className="conflict-detail">
          <div className="panel conflict-heading">
            <h3>{conflict.path}</h3>
            <pre>{conflict.detail}</pre>
            <div className="button-row">
              {conflict.accepts.map((accept) => (
                <Button
                  key={accept}
                  onClick={() =>
                    onOperation({
                      action: "resolve",
                      targets: [conflict.path],
                      accept,
                    })
                  }
                >
                  {names[accept] || accept}
                </Button>
              ))}
            </div>
            <p className="caption muted">
              只读比较。请使用外部工具编辑；选择后仍需预览和确认。
            </p>
          </div>
          <div className="conflict-columns">
            {[
              ["BASE · 基础版本", conflict.base],
              ["MINE · 本地版本", conflict.mine],
              ["THEIRS · 远端版本", conflict.theirs],
            ].map(([title, body]) => (
              <section className="panel" key={title}>
                <div className="panel-head">
                  <strong>{title}</strong>
                </div>
                <pre>{body || "（空内容）"}</pre>
              </section>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}
