export type Project = {
  id: string;
  name: string;
  path: string;
  modified: number;
  conflicts: number;
  unversioned: number;
  error?: string;
};
export type Entry = {
  path: string;
  item: string;
  props: string;
  repos_item: string;
  repos_props: string;
  changelist: string;
  tree_conflicted: boolean;
  revision: number;
};
export type Workspace = {
  entries: Entry[];
  url: string;
  root: string;
  revision: number;
};
export type Draft = { targets: string[]; message: string; revision: number };
export type Job = {
  id: string;
  project_id: string;
  action: string;
  state: "running" | "succeeded" | "failed";
  summary: string;
  output: string;
  error: null | { code: string; message: string };
  started_at: string;
  finished_at?: string;
  safepoint_id?: string;
};
export type Action =
  | "commit"
  | "add"
  | "delete"
  | "move"
  | "revert"
  | "update"
  | "changelist"
  | "resolve"
  | "cleanup"
  | "restore";
export type Operation = {
  action: Action;
  targets: string[];
  message?: string;
  changelist?: string;
  new_path?: string;
  revision?: string;
  accept?: string;
  remove?: boolean;
  safepoint_id?: string;
};
export type Preview = {
  id: string;
  action: Action;
  targets: string[];
  summary: string;
  output: string;
  risk: "normal" | "high";
  expires_at: string;
};
export type HistoryEntry = {
  revision: number;
  author: string;
  date: string;
  message: string;
  paths: {
    action: string;
    path: string;
    copy_from_path?: string;
    copy_from_revision?: number;
  }[];
};
export type History = {
  entries: HistoryEntry[];
  next_revision: number;
  has_more: boolean;
};
export type Conflict = {
  path: string;
  kinds: string[];
  base: string;
  mine: string;
  theirs: string;
  detail: string;
  accepts: string[];
};
export type SafePoint = { id: string; created_at: string; reason: string };
export type Manifest = SafePoint & {
  working_copy: string;
  base_revision: number;
  size_bytes?: number;
  entries?: {
    path: string;
    status: string;
    kind: string;
    changelist?: string;
  }[];
};
export type Audit = {
  id: string;
  started_at: string;
  duration: number;
  arguments: string[];
  exit_code: number;
  error_brief?: string;
};
let csrf = "";
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}
export async function api<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  headers.set("Accept", "application/json");
  if (init.body) headers.set("Content-Type", "application/json");
  if (init.method && init.method !== "GET") headers.set("X-CSRF-Token", csrf);
  const response = await fetch("/api/v1" + path, {
    ...init,
    headers,
    credentials: "same-origin",
  });
  const envelope = await response.json().catch(() => {
    throw new ApiError(
      response.status,
      "invalid_response",
      "服务器未返回 JSON，请检查本地服务状态",
    );
  });
  if (!response.ok || !envelope.ok)
    throw new ApiError(
      response.status,
      envelope.error?.code || "request_failed",
      envelope.error?.message || "请求失败",
    );
  return envelope.data as T;
}
export const put = <T>(path: string, body: unknown) =>
  api<T>(path, { method: "PUT", body: JSON.stringify(body) });
export const post = <T>(path: string, body: unknown) =>
  api<T>(path, { method: "POST", body: JSON.stringify(body) });
export const projectURL = (id: string, path: string) =>
  `/projects/${encodeURIComponent(id)}/${path}`;
export function query(values: Record<string, string | number | undefined>) {
  const q = new URLSearchParams();
  Object.entries(values).forEach(([k, v]) => {
    if (v !== undefined && v !== "") q.set(k, String(v));
  });
  return q.toString();
}
// Bootstrap 在 React 生命周期之外只执行一次，并立即清除地址中的密钥。
let bootstrap: Promise<{ version: string }> | undefined;
export function session() {
  if (bootstrap) return bootstrap;
  const hash = new URLSearchParams(location.hash.slice(1));
  const token = hash.get("token") || "";
  if (location.hash)
    history.replaceState(null, "", location.pathname + location.search);
  bootstrap = (
    token
      ? post<{ csrf_token: string; version: string }>("/session", { token })
      : api<{ csrf_token: string; version: string }>("/session")
  ).then((data) => {
    csrf = data.csrf_token;
    return data;
  });
  return bootstrap;
}
export const errorText = (e: unknown) => {
  const message = e instanceof Error ? e.message : String(e);
  if (e instanceof ApiError && e.code === "unauthorized")
    return message + "。请使用当前本地服务启动链接重新建立浏览器会话。";
  if (
    e instanceof ApiError &&
    (e.code === "authentication" || e.code === "permission")
  )
    return (
      message +
      "。请在本机终端对该工作副本运行 svn info，完成仓库认证或检查访问权限，再返回刷新。浏览器不保存 SVN 密码。"
    );
  return message;
};
