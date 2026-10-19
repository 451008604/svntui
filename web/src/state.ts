import type { Draft, Entry } from "./api";
export function reconcileSelection(selected: string[], entries: Entry[]) {
  const existing = new Set(entries.map((e) => e.path));
  return selected.filter((p) => existing.has(p));
}
export const labels: Record<string, string> = {
  modified: "修改",
  added: "新增",
  deleted: "删除",
  unversioned: "未版本化",
  missing: "丢失",
  conflicted: "冲突",
  replaced: "替换",
  normal: "正常",
  external: "外部引用",
  ignored: "忽略",
  incomplete: "不完整",
  obstructed: "阻塞",
  none: "无",
};
export function statusLabel(e: Entry) {
  if (e.tree_conflicted || e.item === "conflicted" || e.props === "conflicted")
    return "冲突";
  if (e.item === "normal" && e.props === "modified") return "属性修改";
  return labels[e.item] || e.item;
}
export type DraftContent = Pick<Draft, "targets" | "message">;
// 单一队列持有服务器版本，失败时保留本地内容并等待明确重试。
export class DraftQueue {
  current: Draft;
  pending: DraftContent | null = null;
  running: Promise<void> | null = null;
  error: unknown = null;
  constructor(
    initial: Draft,
    private save: (draft: Draft) => Promise<Draft>,
    private changed: (state: string) => void,
  ) {
    this.current = initial;
  }
  update(content: DraftContent) {
    this.pending = { targets: [...content.targets], message: content.message };
    this.changed("待保存");
  }
  async flush(): Promise<void> {
    if (this.running) await this.running;
    if (this.error) throw this.error;
    if (!this.pending) return;
    const run = async () => {
      while (this.pending) {
        const next = this.pending;
        this.pending = null;
        this.changed("保存中…");
        try {
          this.current = await this.save({
            ...next,
            revision: this.current.revision,
          });
        } catch (e) {
          this.pending = this.pending || next;
          this.error = e;
          this.changed("保存失败");
          throw e;
        }
      }
      this.changed("已保存");
    };
    this.running = run();
    try {
      await this.running;
    } finally {
      this.running = null;
    }
  }
  async retry() {
    this.error = null;
    await this.flush();
  }
}
export type DiffLine = {
  kind: "meta" | "context" | "add" | "delete";
  text: string;
  old?: number;
  next?: number;
};
export function parseDiff(text: string): DiffLine[] {
  let old = 0,
    next = 0,
    oldRemaining = 0,
    nextRemaining = 0;
  return text.split("\n").map((line) => {
    const match = line.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
    if (match) {
      old = Number(match[1]);
      next = Number(match[3]);
      oldRemaining = Number(match[2] ?? 1);
      nextRemaining = Number(match[4] ?? 1);
      return { kind: "meta", text: line };
    }
    // 使用 hunk 长度区分文件头和以 --- / +++ 开头的真实文本。
    if (oldRemaining > 0 || nextRemaining > 0) {
      if (line.startsWith("+") && nextRemaining > 0) {
        nextRemaining--;
        return { kind: "add", text: line.slice(1), next: next++ };
      }
      if (line.startsWith("-") && oldRemaining > 0) {
        oldRemaining--;
        return { kind: "delete", text: line.slice(1), old: old++ };
      }
      if (line.startsWith(" ") && oldRemaining > 0 && nextRemaining > 0) {
        oldRemaining--;
        nextRemaining--;
        return {
          kind: "context",
          text: line.slice(1),
          old: old++,
          next: next++,
        };
      }
      if (line.startsWith("\\")) return { kind: "meta", text: line };
    }
    oldRemaining = 0;
    nextRemaining = 0;
    return { kind: "meta", text: line };
  });
}
export function splitDiff(lines: DiffLine[]) {
  const rows: { left?: DiffLine; right?: DiffLine; meta?: DiffLine }[] = [];
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.kind === "meta") {
      rows.push({ meta: line });
      continue;
    }
    if (line.kind === "context") {
      rows.push({ left: line, right: line });
      continue;
    }
    if (line.kind === "delete") {
      const deletes: DiffLine[] = [];
      const adds: DiffLine[] = [];
      while (i < lines.length && lines[i].kind === "delete")
        deletes.push(lines[i++]);
      while (i < lines.length && lines[i].kind === "add") adds.push(lines[i++]);
      i--;
      for (let k = 0; k < Math.max(deletes.length, adds.length); k++)
        rows.push({ left: deletes[k], right: adds[k] });
      continue;
    }
    rows.push({ right: line });
  }
  return rows;
}

// 事件和快照可乱序到达；终态不可回退，客户端只保留 100 条已完成记录。
export function mergeJobs(
  current: import("./api").Job[],
  incoming: import("./api").Job[],
) {
  const jobs = new Map(current.map((job) => [job.id, job]));
  for (const job of incoming) {
    const previous = jobs.get(job.id);
    if (previous && previous.state !== "running") {
      if (job.state === "running") continue;
      if (
        (job.finished_at || job.started_at) <=
        (previous.finished_at || previous.started_at)
      )
        continue;
    }
    jobs.set(job.id, job);
  }
  let finished = 0;
  return [...jobs.values()]
    .sort((a, b) => b.started_at.localeCompare(a.started_at))
    .filter((job) => job.state === "running" || ++finished <= 100);
}
export function jobNeedsRefresh(
  previous: import("./api").Job | undefined,
  incoming: import("./api").Job,
) {
  return (
    incoming.state !== "running" && (!previous || previous.state === "running")
  );
}
