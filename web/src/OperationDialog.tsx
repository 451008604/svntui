import { useState } from "react";
import {
  post,
  projectURL,
  ApiError,
  type Project,
  type Operation,
  type Preview,
  type Job,
} from "./api";
import { Button, Modal, Icon, ErrorBox } from "./ui";
import { actions, time } from "./shared";
import { singlePathTarget } from "./state";
export function OperationDialog({
  project,
  request,
  onClose,
  onJob,
  beforeCommit,
}: {
  project: Project;
  request: Operation;
  onClose: () => void;
  onJob: (j: Job) => void;
  beforeCommit: () => Promise<void>;
}) {
  const [input, setInput] = useState(request),
    [preview, setPreview] = useState<Preview | null>(null),
    [busy, setBusy] = useState(false),
    [error, setError] = useState<unknown>(null),
    [yes, setYes] = useState(""),
    [uncertain, setUncertain] = useState(false);
  const manualDelete =
    request.action === "delete" && request.targets.length === 0;
  const [deletePath, setDeletePath] = useState("");
  const edit = (data: Partial<Operation>) => {
    setInput({ ...input, ...data });
    setPreview(null);
    setYes("");
    setError(null);
  };
  const create = async () => {
    if (manualDelete && !deletePath.trim()) return;
    setBusy(true);
    setError(null);
    try {
      if (input.action === "commit") await beforeCommit();
      setPreview(
        await post<Preview>(projectURL(project.id, "previews"), input),
      );
    } catch (e) {
      setError(e);
    } finally {
      setBusy(false);
    }
  };
  const execute = async () => {
    if (!preview) return;
    setBusy(true);
    setError(null);
    try {
      if (input.action === "commit") await beforeCommit();
      const job = await post<Job>(projectURL(project.id, "operations"), {
        preview_id: preview.id,
        confirm: true,
        confirmation: yes,
      });
      onJob(job);
    } catch (e) {
      setError(e);
      if (e instanceof ApiError && e.status === 409) {
        setPreview(null);
        setYes("");
      } else if (!(e instanceof ApiError)) {
        setUncertain(true);
      }
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title={`${actions[input.action]} · ${project.name}`}
      onClose={() => {
        if (!busy) onClose();
      }}
      wide
    >
      <div className="modal-content">
        <div className="notice">
          先预览实际范围，再明确确认执行。工作副本变化后需要重新预览。
        </div>
        {!preview && (
          <>
            {!manualDelete && (
              <label>
                操作范围
                <select
                  aria-label="操作范围"
                  value={
                    input.action === "update" && input.targets.length === 0
                      ? "all"
                      : "selected"
                  }
                  disabled={input.action !== "update"}
                  onChange={(e) =>
                    edit({
                      targets: e.target.value === "all" ? [] : request.targets,
                    })
                  }
                >
                  <option value="selected">
                    所选路径（{input.targets.length}）
                  </option>
                  {input.action === "update" && (
                    <option value="all">整个工作副本</option>
                  )}
                </select>
              </label>
            )}
            {input.action === "delete" && (
              <div className="notice">
                SVN
                会拒绝删除含本地修改的路径。请先保存或提交需要保留的内容，再通过“还原”明确处理本地修改；干净的版本化文件可从项目菜单“删除版本化路径…”输入相对路径。
              </div>
            )}
            {manualDelete && (
              <label>
                工作副本相对路径
                <input
                  aria-label="待删除的版本化相对路径"
                  value={deletePath}
                  onChange={(event) => {
                    setDeletePath(event.target.value);
                    edit({ targets: singlePathTarget(event.target.value) });
                  }}
                  placeholder="例如 docs/旧 文档@2026.txt"
                  autoComplete="off"
                />
                <span className="muted">
                  一次输入一个字面路径，空格、中文和 @ 均视为文件名的一部分。
                </span>
              </label>
            )}
            {input.action === "update" && (
              <label>
                目标修订
                <input
                  placeholder="HEAD（最新修订）"
                  value={input.revision || ""}
                  onChange={(e) => edit({ revision: e.target.value })}
                />
                <span className="muted">
                  更新预览可能规范化为整个工作副本，以下方实际范围为准。
                </span>
              </label>
            )}
            {input.action === "move" && (
              <label>
                新的相对路径
                <input
                  value={input.new_path || ""}
                  onChange={(e) => edit({ new_path: e.target.value })}
                  placeholder="例如 src/new-name.go"
                />
              </label>
            )}
            {input.action === "commit" && (
              <label>
                提交说明
                <textarea rows={5} value={input.message || ""} readOnly />
              </label>
            )}
            {input.action === "changelist" && (
              <>
                <label>
                  分组名称
                  <input
                    value={input.changelist || ""}
                    onChange={(e) => edit({ changelist: e.target.value })}
                  />
                </label>
                <label className="inline">
                  <input
                    type="checkbox"
                    checked={!!input.remove}
                    onChange={(e) => edit({ remove: e.target.checked })}
                  />
                  从当前分组移除
                </label>
              </>
            )}
            {input.action === "resolve" && (
              <div className="notice">
                解决方式：{input.accept}。working
                表示已在外部工具整理好工作文件；含冲突标记的内容仍会被服务器拒绝。
              </div>
            )}
            <div className="target-list">
              {input.targets.length ? (
                input.targets.map((p) => <code key={p}>{p}</code>)
              ) : (
                <span>
                  {manualDelete
                    ? "请先输入要删除的单个版本化相对路径"
                    : "工作副本范围（预览将显示最终规范化范围）"}
                </span>
              )}
            </div>
          </>
        )}
        {preview && (
          <>
            <div
              className={`preview-title ${preview.risk === "high" ? "danger" : ""}`}
            >
              <Icon name={preview.risk === "high" ? "conflict" : "check"} />
              <strong>{preview.summary}</strong>
              <span>{preview.risk === "high" ? "高风险" : "普通操作"}</span>
            </div>
            <p>
              实际目标 <strong>{preview.targets.length}</strong> 项 · 预览有效至{" "}
              {time(preview.expires_at)}
            </p>
            <div className="target-list">
              {preview.targets.map((p) => (
                <code key={p}>{p === "." ? "整个工作副本（.）" : p}</code>
              ))}
            </div>
            <pre className="output">{preview.output || "无附加输出"}</pre>
            {preview.risk === "high" && (
              <label className="danger">
                此操作会覆盖或删除本地内容。输入 YES 确认
                <input
                  aria-label="输入 YES 确认"
                  autoComplete="off"
                  value={yes}
                  onChange={(e) => setYes(e.target.value)}
                  placeholder="YES"
                />
              </label>
            )}
          </>
        )}
        <ErrorBox error={error} />
        {uncertain && (
          <div className="error">
            请求连接中断，执行结果尚未确认。请关闭并在“活动”中刷新任务、核对工作副本；不会自动重发写入。
          </div>
        )}
      </div>
      <footer>
        <Button onClick={onClose} disabled={busy}>
          取消
        </Button>
        {preview ? (
          <>
            <Button onClick={() => setPreview(null)} disabled={busy}>
              重新预览
            </Button>
            <Button
              primary
              onClick={execute}
              disabled={
                busy || uncertain || (preview.risk === "high" && yes !== "YES")
              }
            >
              {busy ? "执行中…" : "确认执行"}
            </Button>
          </>
        ) : (
          <Button
            primary
            onClick={create}
            disabled={busy || (manualDelete && !deletePath.trim())}
          >
            {busy ? "正在预览…" : "生成预览"}
          </Button>
        )}
      </footer>
    </Modal>
  );
}
