import { useState } from "react";
import { post, type Project } from "./api";
import { Button, Modal, ErrorBox } from "./ui";
export function RegisterProject({
  onClose,
  onRegistered,
}: {
  onClose: () => void;
  onRegistered: (p: Project) => void;
}) {
  const [path, setPath] = useState(""),
    [name, setName] = useState(""),
    [error, setError] = useState<unknown>(null),
    [busy, setBusy] = useState(false);
  return (
    <Modal title="添加本地工作副本" onClose={onClose}>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          setError(null);
          try {
            onRegistered(await post<Project>("/projects", { path, name }));
          } catch (e) {
            setError(e);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="modal-content">
          <p className="muted">
            连接已经检出的本地 SVN 工作副本，服务器将验证并登记其根目录。
          </p>
          <label>
            工作副本路径
            <input
              required
              value={path}
              onChange={(e) => setPath(e.target.value)}
              placeholder="/Users/you/projects/my-project"
              autoComplete="off"
            />
          </label>
          <label>
            项目名称（可选）
            <input
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="默认使用目录名称"
            />
          </label>
          <ErrorBox error={error} />
        </div>
        <footer>
          <Button onClick={onClose} disabled={busy}>
            取消
          </Button>
          <button className="primary" disabled={busy || !path.trim()}>
            {busy ? "正在验证…" : "验证并添加"}
          </button>
        </footer>
      </form>
    </Modal>
  );
}
