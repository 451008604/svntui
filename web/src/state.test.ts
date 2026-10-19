import { describe, it, expect, vi } from "vitest";
import {
  DraftQueue,
  singlePathTarget,
  mergeJobs,
  jobNeedsRefresh,
  parseDiff,
  reconcileSelection,
  splitDiff,
  statusLabel,
} from "./state";
import { ApiError, errorText, type Draft, type Entry } from "./api";
const entry = (path: string, extra: Partial<Entry> = {}): Entry => ({
  path,
  item: "modified",
  props: "none",
  repos_item: "none",
  repos_props: "none",
  changelist: "",
  tree_conflicted: false,
  revision: 125,
  ...extra,
});
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}
describe("SVN unified diff model", () => {
  it("tracks old/new line numbers across deletion, addition and context", () => {
    const lines = parseDiff(
      "Index: hello.txt\n--- hello.txt\n+++ hello.txt\n@@ -8,2 +8,3 @@\n-before\n+after\n+new\n context",
    );
    expect(lines.slice(4)).toEqual([
      { kind: "delete", text: "before", old: 8 },
      { kind: "add", text: "after", next: 8 },
      { kind: "add", text: "new", next: 9 },
      { kind: "context", text: "context", old: 9, next: 10 },
    ]);
  });
  it("pairs actual hunk content without inventing deleted or added source lines", () => {
    const rows = splitDiff(
      parseDiff("@@ -1,2 +1,3 @@\n-old\n+first\n+second\n same"),
    );
    expect(rows[1].left?.text).toBe("old");
    expect(rows[1].right?.text).toBe("first");
    expect(rows[2].left).toBeUndefined();
    expect(rows[2].right?.text).toBe("second");
    expect(rows[3].left?.text).toBe("same");
  });
  it("keeps property/binary guidance and unknown sections as literal metadata", () => {
    const raw =
      "Property changes on: x\nAdded: svn:ignore\n## -0,0 +1 ##\n+*.tmp\nCannot display: binary";
    expect(
      parseDiff(raw)
        .map((l) => l.text)
        .join("\n"),
    ).toBe(raw);
    expect(parseDiff(raw).every((l) => l.kind === "meta")).toBe(true);
  });
  it("resets counters at each file and hunk", () => {
    const lines = parseDiff(
      "@@ -9 +10 @@\n-old\n+new\nIndex: next\n--- next\n+++ next\n@@ -0,0 +1 @@\n+first",
    );
    expect(lines.at(-1)).toEqual({ kind: "add", text: "first", next: 1 });
  });
});
it("keeps header-like source text inside a hunk as real additions and deletions", () => {
  const lines = parseDiff("@@ -1 +1 @@\n--- old value\n+++ new value");
  expect(lines[1]).toEqual({ kind: "delete", text: "-- old value", old: 1 });
  expect(lines[2]).toEqual({ kind: "add", text: "++ new value", next: 1 });
});
describe("explicit selection", () => {
  it("removes disappeared paths while preserving order and never selecting new changes", () => {
    expect(
      reconcileSelection(
        ["b", "a", "gone"],
        [entry("a"), entry("new"), entry("b")],
      ),
    ).toEqual(["b", "a"]);
  });
  it("distinguishes property and tree conflicts from ordinary modifications", () => {
    expect(statusLabel(entry("p", { item: "normal", props: "modified" }))).toBe(
      "属性修改",
    );
    expect(statusLabel(entry("p", { tree_conflicted: true }))).toBe("冲突");
  });
});
describe("revision-aware serialized drafts", () => {
  it("allows only one write in flight and carries the acknowledged revision to latest content", async () => {
    const first = deferred<Draft>();
    const save = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementation(async (d: Draft) => ({
        ...d,
        revision: d.revision + 1,
      }));
    const queue = new DraftQueue(
      { targets: [], message: "", revision: 7 },
      save,
      () => {},
    );
    queue.update({ targets: ["a"], message: "one" });
    const flushing = queue.flush();
    queue.update({ targets: ["b"], message: "two" });
    queue.update({ targets: ["b", "c"], message: "latest" });
    expect(save).toHaveBeenCalledTimes(1);
    first.resolve({ targets: ["a"], message: "one", revision: 8 });
    await flushing;
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1][0]).toEqual({
      targets: ["b", "c"],
      message: "latest",
      revision: 8,
    });
    expect(queue.current.revision).toBe(9);
  });
  it("retains the newest content on a 409 and requires deliberate retry", async () => {
    const first = deferred<Draft>();
    const error = new ApiError(409, "stale_draft", "草稿已变化");
    const save = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementation(async (d: Draft) => ({
        ...d,
        revision: d.revision + 1,
      }));
    const state = vi.fn();
    const queue = new DraftQueue(
      { targets: [], message: "", revision: 2 },
      save,
      state,
    );
    queue.update({ targets: ["a"], message: "original" });
    const flushing = queue.flush();
    queue.update({ targets: ["b"], message: "latest" });
    first.reject(error);
    await expect(flushing).rejects.toBe(error);
    expect(queue.pending?.message).toBe("latest");
    await expect(queue.flush()).rejects.toBe(error);
    expect(save).toHaveBeenCalledTimes(1);
    await queue.retry();
    expect(save.mock.calls[1][0].message).toBe("latest");
    expect(state).toHaveBeenLastCalledWith("已保存");
  });
  it("settles concurrent flush callers before a caller can execute a commit", async () => {
    const saved = deferred<Draft>();
    const queue = new DraftQueue(
      { targets: [], message: "", revision: 0 },
      () => saved.promise,
      () => {},
    );
    queue.update({ targets: ["a"], message: "commit" });
    const first = queue.flush();
    let committed = false;
    const beforeCommit = queue.flush().then(() => {
      committed = true;
    });
    await Promise.resolve();
    expect(committed).toBe(false);
    saved.resolve({ targets: ["a"], message: "commit", revision: 1 });
    await Promise.all([first, beforeCommit]);
    expect(committed).toBe(true);
    expect(queue.pending).toBeNull();
  });
});
describe("auth failure guidance", () => {
  it("distinguishes SVN authentication from browser session auth by code", () => {
    expect(
      errorText(new ApiError(401, "authentication", "repository denied")),
    ).toContain("svn info");
    expect(errorText(new ApiError(401, "unauthorized", "expired"))).toContain(
      "浏览器会话",
    );
    expect(errorText(new ApiError(403, "permission", "denied"))).toContain(
      "访问权限",
    );
  });
});

describe("monotonic job snapshots", () => {
  const job = (
    state: import("./api").Job["state"],
    extra: Partial<import("./api").Job> = {},
  ): import("./api").Job => ({
    id: "job-one",
    project_id: "project-one",
    action: "commit",
    state,
    summary: "提交",
    output: "",
    error: null,
    started_at: "2026-10-19T01:00:00Z",
    ...(state === "running" ? {} : { finished_at: "2026-10-19T01:00:01Z" }),
    ...extra,
  });
  it("keeps a terminal SSE result when an older running GET snapshot arrives later", () => {
    const terminal = job("succeeded");
    const merged = mergeJobs([terminal], [job("running")]);
    expect(merged).toEqual([terminal]);
    expect(jobNeedsRefresh(terminal, job("running"))).toBe(false);
  });
  it("refreshes real status and draft for a first-observed terminal result, once", () => {
    const terminal = job("failed");
    expect(jobNeedsRefresh(undefined, terminal)).toBe(true);
    expect(jobNeedsRefresh(job("running"), terminal)).toBe(true);
    expect(jobNeedsRefresh(terminal, terminal)).toBe(false);
  });
  it("does not drop a newly observed SSE task missing from an older snapshot", () => {
    const terminal = job("succeeded");
    const other = job("running", { id: "job-two" });
    expect(mergeJobs([terminal, other], [])).toHaveLength(2);
  });
  it("bounds retained finished history to100 while retaining every active task", () => {
    const jobs = Array.from({ length: 130 }, (_, index) =>
      job("succeeded", {
        id: `finished-${index}`,
        started_at: new Date(2026, 0, 1, 0, index).toISOString(),
      }),
    );
    const merged = mergeJobs(
      [],
      [
        ...jobs,
        job("running", { id: "active-one" }),
        job("running", { id: "active-two" }),
      ],
    );
    expect(merged.filter((j) => j.state !== "running")).toHaveLength(100);
    expect(merged.filter((j) => j.state === "running")).toHaveLength(2);
    expect(merged.some((j) => j.id === "finished-0")).toBe(false);
  });
});

describe("explicit literal deletion paths", () => {
  it("keeps spaces, Chinese and peg-looking @ characters as one exact target", () => {
    expect(singlePathTarget("docs/旧 文档@2026.txt")).toEqual([
      "docs/旧 文档@2026.txt",
    ]);
    expect(singlePathTarget(" file name ")).toEqual([" file name "]);
  });
  it("never turns an empty or whitespace-only field into whole-working-copy deletion", () => {
    expect(singlePathTarget("")).toEqual([]);
    expect(singlePathTarget("  ")).toEqual([]);
  });
});

describe("draft content deduplication", () => {
  it("does not write an untouched draft, but saves a return to original content behind an in-flight write", async () => {
    const first = deferred<Draft>();
    const initial: Draft = { targets: ["a"], message: "original", revision: 1 };
    const save = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementation(async (value: Draft) => ({
        ...value,
        revision: value.revision + 1,
      }));
    const queue = new DraftQueue(initial, save, () => {});
    queue.update(initial);
    await queue.flush();
    expect(save).not.toHaveBeenCalled();
    queue.update({ targets: ["a"], message: "edited" });
    const running = queue.flush();
    queue.update({ targets: ["a"], message: "original" });
    first.resolve({ targets: ["a"], message: "edited", revision: 2 });
    await running;
    expect(save).toHaveBeenCalledTimes(2);
    expect(save.mock.calls[1][0]).toEqual({ ...initial, revision: 2 });
    expect(queue.current.message).toBe("original");
  });
});
