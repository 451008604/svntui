// @vitest-environment jsdom
import { act, StrictMode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  api,
  put,
  post,
  ApiError,
  type Draft,
  type Project,
  type Workspace,
  type Entry,
} from "./api";
import { AuthenticatedApp } from "./App";
import { WorkspacePage } from "./WorkspacePage";
import { OperationDialog } from "./OperationDialog";
vi.mock("./api", async (original) => ({
  ...(await original<typeof import("./api")>()),
  api: vi.fn(),
  put: vi.fn(),
  post: vi.fn(),
}));
const a: Project = {
  id: "a",
  name: "Project A",
  path: "/wc/a",
  modified: 1,
  conflicts: 0,
  unversioned: 0,
};
const b: Project = { ...a, id: "b", name: "Project B", path: "/wc/b" };
const entry = (path: string): Entry => ({
  path,
  item: "modified",
  props: "none",
  repos_item: "none",
  repos_props: "none",
  changelist: "",
  tree_conflicted: false,
  revision: 125,
});
const status = (paths = ["same.txt"]): Workspace => ({
  entries: paths.map(entry),
  root: a.path,
  url: "file:///repo/trunk",
  revision: 125,
});
const draft = (targets = ["same.txt"]): Draft => ({
  targets,
  message: "local message",
  revision: 0,
});
function deferred<T>() {
  let resolve!: (v: T) => void, reject!: (e: unknown) => void;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
}
let container: HTMLDivElement, root: Root;
const mockApi = vi.mocked(api),
  mockPut = vi.mocked(put),
  mockPost = vi.mocked(post);
function button(text: string) {
  const found = [...container.querySelectorAll("button")].find(
    (button) => button.textContent?.trim() === text,
  );
  if (!found) throw new Error("Missing button:" + text);
  return found;
}
async function click(element: HTMLElement) {
  await act(async () => {
    element.click();
  });
}
async function edit(selector: string, value: string) {
  const element = container.querySelector<
    HTMLInputElement | HTMLTextAreaElement
  >(selector)!;
  const prototype =
    element instanceof HTMLTextAreaElement
      ? HTMLTextAreaElement.prototype
      : HTMLInputElement.prototype;
  await act(async () => {
    Object.getOwnPropertyDescriptor(prototype, "value")!.set!.call(
      element,
      value,
    );
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function mount(element: React.ReactNode) {
  await act(async () => {
    root.render(<StrictMode>{element}</StrictMode>);
  });
}
beforeEach(() => {
  vi.clearAllMocks();
  vi.useFakeTimers();
  localStorage.clear();
  localStorage.setItem("project-id", "a");
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.stubGlobal(
    "EventSource",
    class {
      addEventListener() {}
      close() {}
    },
  );
  Element.prototype.scrollIntoView = vi.fn();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
  mockApi.mockImplementation(async (path) => {
    if (path === "/projects") return [a, b];
    if (path === "/jobs") return [];
    if (path.includes("/status")) return status();
    if (path.endsWith("/draft")) return draft();
    if (path.includes("/diff")) return { text: "@@ -1 +1 @@\n-old\n+new" };
    return [];
  });
  mockPut.mockImplementation(async (_path, body) => ({
    ...(body as Draft),
    revision: (body as Draft).revision + 1,
  }));
  mockPost.mockImplementation(async (path) =>
    path === "/projects"
      ? b
      : {
          id: "preview",
          action: "revert",
          targets: ["same.txt"],
          summary: "还原操作预览",
          output: "preview",
          risk: "high",
          expires_at: "2026-10-20T00:00:00Z",
        },
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});
describe("actual App project transition handlers", () => {
  it("keeps an operation on A when B is clicked while its draft flush is delayed", async () => {
    await mount(<AuthenticatedApp version="0.3.0" />);
    await edit('textarea[aria-label="提交说明"]', "pending operation message");
    const save = deferred<Draft>();
    mockPut.mockImplementationOnce(() => save.promise);
    await click(button("还原"));
    const projectB = container.querySelector<HTMLButtonElement>(
      'button.project[title="/wc/b"]',
    )!;
    expect(projectB.disabled).toBe(true);
    expect(container.querySelector('[role="dialog"]')).toBeNull();
    expect(mockPut).toHaveBeenCalledOnce();
    await click(projectB);
    await act(async () => save.resolve({ ...draft(), revision: 1 }));
    const dialog = container.querySelector('[role="dialog"]')!;
    expect(dialog.getAttribute("aria-label")).toContain("Project A");
    await click(button("生成预览"));
    expect(mockPost).toHaveBeenCalledWith(
      "/projects/a/previews",
      expect.objectContaining({ action: "revert", targets: ["same.txt"] }),
    );
    expect(
      mockPost.mock.calls.some(([path]) => path === "/projects/b/previews"),
    ).toBe(false);
  });
  it("does not unmount unsaved A after successful B registration when draft save fails409", async () => {
    let registered = false;
    const originalRead = mockApi.getMockImplementation()!;
    mockApi.mockImplementation((path, init) =>
      path === "/projects"
        ? Promise.resolve(registered ? [a, b] : [a])
        : originalRead(path, init),
    );
    mockPost.mockImplementation(async () => {
      registered = true;
      return b;
    });
    await mount(<AuthenticatedApp version="0.3.0" />);
    await edit('textarea[aria-label="提交说明"]', "unsaved text from A");
    mockPut.mockRejectedValue(new ApiError(409, "stale_draft", "草稿已变化"));
    await click(
      container.querySelector<HTMLButtonElement>('[aria-label="添加项目"]')!,
    );
    await edit('[role="dialog"] input', "/wc/b");
    const form = container.querySelector("form")!;
    await act(async () => {
      form.dispatchEvent(
        new Event("submit", { bubbles: true, cancelable: true }),
      );
    });
    expect(mockPost).toHaveBeenCalledWith(
      "/projects",
      expect.objectContaining({ path: "/wc/b" }),
    );
    expect(
      container.querySelector<HTMLTextAreaElement>(
        'textarea[aria-label="提交说明"]',
      )?.value,
    ).toBe("unsaved text from A");
    expect(
      container.querySelector("button.project.active")?.getAttribute("title"),
    ).toBe("/wc/a");
    expect(container.textContent).toContain("草稿已变化");
  });
});
describe("actual WorkspacePage read lifecycle", () => {
  const props = {
    project: a,
    refresh: 0,
    commitEpoch: 0,
    onOperation: vi.fn(),
    registerFlush: vi.fn(),
    locked: false,
  };
  it.each(["status-first", "draft-first"])(
    "reconciles stale draft targets with both successful reads: %s",
    async (order) => {
      const readStatus = deferred<Workspace>(),
        readDraft = deferred<Draft>();
      mockApi.mockImplementation(async (path) =>
        path.includes("/status")
          ? readStatus.promise
          : path.endsWith("/draft")
            ? readDraft.promise
            : { text: "" },
      );
      await mount(<WorkspacePage {...props} />);
      if (order === "status-first") {
        await act(async () => readStatus.resolve(status(["new.txt"])));
        await act(async () => readDraft.resolve(draft(["gone.txt"])));
      } else {
        await act(async () => readDraft.resolve(draft(["gone.txt"])));
        expect(mockPut).not.toHaveBeenCalled();
        await act(async () => readStatus.resolve(status(["new.txt"])));
      }
      expect(container.querySelector(".commit-count strong")?.textContent).toBe(
        "0",
      );
      expect(
        container.querySelector(".selected-targets")?.textContent,
      ).not.toContain("gone.txt");
      await act(async () => vi.advanceTimersByTimeAsync(600));
      expect(
        mockPut.mock.calls.every(
          ([, body]) => (body as Draft).targets.length === 0,
        ),
      ).toBe(true);
    },
  );
  it.each(["status-first", "draft-first"])(
    "reconciles post-operation draft reload in either response order: %s",
    async (order) => {
      await mount(<WorkspacePage {...props} />);
      const readStatus = deferred<Workspace>(),
        readDraft = deferred<Draft>();
      mockApi.mockImplementation(async (path) =>
        path.includes("/status")
          ? readStatus.promise
          : path.endsWith("/draft")
            ? readDraft.promise
            : { text: "" },
      );
      await mount(<WorkspacePage {...props} refresh={1} commitEpoch={1} />);
      if (order === "status-first") {
        await act(async () => readStatus.resolve(status(["new.txt"])));
        await act(async () => readDraft.resolve(draft(["same.txt"])));
      } else {
        await act(async () => readDraft.resolve(draft(["same.txt"])));
        await act(async () => readStatus.resolve(status(["new.txt"])));
      }
      expect(container.querySelector(".commit-count strong")?.textContent).toBe(
        "0",
      );
      await act(async () => vi.advanceTimersByTimeAsync(600));
      expect((mockPut.mock.calls.at(-1)?.[1] as Draft).targets).toEqual([]);
    },
  );
  it("keeps loaded draft targets during an in-flight or failed status read", async () => {
    const readStatus = deferred<Workspace>();
    mockApi.mockImplementation(async (path) =>
      path.includes("/status")
        ? readStatus.promise
        : path.endsWith("/draft")
          ? draft(["same.txt"])
          : { text: "" },
    );
    await mount(<WorkspacePage {...props} />);
    expect(container.querySelector(".commit-count strong")?.textContent).toBe(
      "1",
    );
    await act(async () =>
      readStatus.reject(new Error("SVN status unavailable")),
    );
    await act(async () => vi.advanceTimersByTimeAsync(600));
    expect(mockPut).not.toHaveBeenCalled();
    expect(container.querySelector(".commit-count strong")?.textContent).toBe(
      "1",
    );
  });
});
describe("actual OperationDialog busy lifecycle", () => {
  it("uses the current Escape callback while preview or execution is busy", async () => {
    const preview = deferred<unknown>();
    mockPost.mockImplementationOnce(() => preview.promise);
    const close = vi.fn();
    await mount(
      <OperationDialog
        project={a}
        request={{ action: "revert", targets: ["same.txt"] }}
        onClose={close}
        onJob={vi.fn()}
        beforeCommit={async () => {}}
      />,
    );
    await click(button("生成预览"));
    const dialog = container.querySelector('[role="dialog"]')!;
    await act(async () => {
      dialog.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    });
    expect(close).not.toHaveBeenCalled();
    await act(async () =>
      preview.resolve({
        id: "preview",
        action: "revert",
        targets: ["same.txt"],
        summary: "还原操作预览",
        output: "preview",
        risk: "high",
        expires_at: "2026-10-20T00:00:00Z",
      }),
    );
    await act(async () => {
      dialog.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    });
    expect(close).toHaveBeenCalledOnce();
    await edit('[aria-label="输入 YES 确认"]', "YES");
    const executed = deferred<unknown>();
    mockPost.mockImplementationOnce(() => executed.promise);
    await click(button("确认执行"));
    await act(async () => {
      dialog.dispatchEvent(
        new KeyboardEvent("keydown", { key: "Escape", bubbles: true }),
      );
    });
    expect(close).toHaveBeenCalledOnce();
    await act(async () =>
      executed.resolve({
        id: "job-one",
        project_id: "a",
        action: "revert",
        state: "running",
        summary: "还原",
        output: "",
        error: null,
        started_at: "2026-10-19T00:00:00Z",
      }),
    );
  });
});

describe("status failure does not trap project recovery", () => {
  function failAStatus() {
    const original = mockApi.getMockImplementation()!;
    mockApi.mockImplementation((path, init) =>
      path.startsWith("/projects/a/status")
        ? Promise.reject(new Error("remote SVN unavailable"))
        : original(path, init),
    );
  }
  it.each(["untouched", "edited-and-saved"])(
    "leaves unhealthy A for healthy B with %s draft",
    async (kind) => {
      failAStatus();
      await mount(<AuthenticatedApp version="0.3.0" />);
      if (kind === "edited-and-saved") {
        const message = container.querySelector<HTMLTextAreaElement>(
          'textarea[aria-label="提交说明"]',
        )!;
        expect(message.disabled).toBe(false);
        await edit(
          'textarea[aria-label="提交说明"]',
          "preserve draft across outage",
        );
      }
      await click(
        container.querySelector<HTMLButtonElement>(
          'button.project[title="/wc/b"]',
        )!,
      );
      expect(
        container.querySelector("button.project.active")?.getAttribute("title"),
      ).toBe("/wc/b");
      if (kind === "untouched") expect(mockPut).not.toHaveBeenCalled();
      else
        expect(mockPut).toHaveBeenCalledWith(
          "/projects/a/draft",
          expect.objectContaining({
            targets: ["same.txt"],
            message: "preserve draft across outage",
          }),
        );
    },
  );
  it("can register and navigate to B when A status is unavailable", async () => {
    failAStatus();
    let registered = false;
    const original = mockApi.getMockImplementation()!;
    mockApi.mockImplementation((path, init) =>
      path === "/projects"
        ? Promise.resolve(registered ? [a, b] : [a])
        : original(path, init),
    );
    mockPost.mockImplementation(async () => {
      registered = true;
      return b;
    });
    await mount(<AuthenticatedApp version="0.3.0" />);
    await click(
      container.querySelector<HTMLButtonElement>('[aria-label="添加项目"]')!,
    );
    await edit('[role="dialog"] input', "/wc/b");
    await act(async () => {
      container
        .querySelector("form")!
        .dispatchEvent(
          new Event("submit", { bubbles: true, cancelable: true }),
        );
    });
    expect(
      container.querySelector("button.project.active")?.getAttribute("title"),
    ).toBe("/wc/b");
  });
  it("allows cleanup preview entry without an unrelated remote status read", async () => {
    failAStatus();
    await mount(<AuthenticatedApp version="0.3.0" />);
    await click(button("清理工作副本"));
    expect(
      container.querySelector('[role="dialog"]')?.getAttribute("aria-label"),
    ).toContain("清理 · Project A");
    await click(button("生成预览"));
    expect(mockPost).toHaveBeenCalledWith(
      "/projects/a/previews",
      expect.objectContaining({ action: "cleanup", targets: [] }),
    );
  });
  it("still blocks navigation on a genuine pending409 save when status also failed", async () => {
    failAStatus();
    await mount(<AuthenticatedApp version="0.3.0" />);
    await edit('textarea[aria-label="提交说明"]', "keep this local message");
    mockPut.mockRejectedValue(
      new ApiError(409, "stale_draft", "save conflict"),
    );
    await click(
      container.querySelector<HTMLButtonElement>(
        'button.project[title="/wc/b"]',
      )!,
    );
    expect(
      container.querySelector("button.project.active")?.getAttribute("title"),
    ).toBe("/wc/a");
    expect(
      container.querySelector<HTMLTextAreaElement>(
        'textarea[aria-label="提交说明"]',
      )?.value,
    ).toBe("keep this local message");
    expect(container.textContent).toContain("save conflict");
  });
});
