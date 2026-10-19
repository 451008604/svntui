// @vitest-environment jsdom
import { act, StrictMode, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { DiffViewer } from "./DiffViewer";
let container: HTMLDivElement, root: Root;
const bigText = [
  "Index: big.txt",
  "===================================================================",
  "--- big.txt",
  "+++ big.txt",
  "@@ -1,4500 +1,4500 @@",
  ...Array.from(
    { length: 4500 },
    (_, index) =>
      ` ${[12, 2205, 4205].includes(index) ? "jump-hit " : ""}context-${index}`,
  ),
].join("\n");
const button = (label: string) => {
  const result = container.querySelector<HTMLButtonElement>(
    `[aria-label="${label}"]`,
  );
  if (!result) throw new Error("Missing " + label);
  return result;
};
async function click(element: HTMLElement) {
  await act(async () => element.click());
}
async function search(value: string) {
  const input = container.querySelector<HTMLInputElement>(
    '[aria-label="搜索差异"]',
  )!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(
      HTMLInputElement.prototype,
      "value",
    )!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
async function mount(element: React.ReactNode) {
  await act(async () => root.render(<StrictMode>{element}</StrictMode>));
}
const displayed = () => container.querySelectorAll("[data-display-row]");
const pagination = () =>
  container.querySelector('[aria-label="差异分页"]')?.textContent;
beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  Element.prototype.scrollIntoView = vi.fn();
  container = document.createElement("div");
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.unstubAllGlobals();
});
describe("mounted bounded complete-diff rendering", () => {
  it.each(["unified", "split"])(
    "renders at most1000 display rows without hidden full-file nodes: %s",
    async (mode) => {
      localStorage.setItem("diff-mode", mode);
      await mount(<DiffViewer title="big.txt" text={bigText} />);
      expect(displayed()).toHaveLength(1000);
      expect(container.querySelectorAll("*").length).toBeLessThan(10000);
      expect(pagination()).toContain("1–1000 / 4505 行");
      expect(pagination()).toContain("第 1/5 页");
      expect(pagination()).toContain("完整差异已载入");
      expect(button("上一页差异").disabled).toBe(true);
      await click(button("下一页差异"));
      expect(displayed()).toHaveLength(1000);
      expect(pagination()).toContain("1001–2000 / 4505 行");
    },
  );
  it.each(["unified", "split"])(
    "searches all received source lines and jumps next/previous matches across pages: %s",
    async (mode) => {
      localStorage.setItem("diff-mode", mode);
      await mount(<DiffViewer title="big.txt" text={bigText} />);
      await search("jump-hit");
      expect(
        container.querySelector('[aria-label="差异搜索匹配数"]')?.textContent,
      ).toBe("1/3");
      await click(button("下一个匹配"));
      expect(pagination()).toContain("2001–3000 / 4505 行");
      expect(container.querySelector(".match code")?.textContent).toContain(
        "context-2205",
      );
      const line = container.querySelector('[data-line="2210"]')!;
      expect(line.classList.contains("match")).toBe(true);
      expect(line.querySelector(".line-no")?.textContent).toBe("2206");
      await click(button("下一个匹配"));
      expect(pagination()).toContain("4001–4505 / 4505 行");
      expect(container.querySelector(".match code")?.textContent).toContain(
        "context-4205",
      );
      await click(button("上一个匹配"));
      expect(pagination()).toContain("2001–3000 / 4505 行");
      await click(button("上一个匹配"));
      expect(pagination()).toContain("1–1000 / 4505 行");
      await click(button("上一个匹配"));
      expect(pagination()).toContain("4001–4505 / 4505 行");
      expect(
        container.querySelector('[aria-label="差异搜索匹配数"]')?.textContent,
      ).toBe("3/3");
      expect(displayed().length).toBeLessThanOrEqual(1000);
      expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
    },
  );
  it.each(["unified", "split"])(
    "reveals the same unique match again after manual paging or scrolling: %s",
    async (mode) => {
      localStorage.setItem("diff-mode", mode);
      await mount(<DiffViewer title="big.txt" text={bigText} />);
      await search("context-4205");
      expect(pagination()).toContain("第 5/5 页");
      expect(container.querySelector('[aria-label="差异搜索匹配数"]')?.textContent).toBe("1/1");
      for (const direction of ["下一个匹配", "上一个匹配"]) {
        await click(button("上一页差异"));
        expect(pagination()).toContain("第 4/5 页");
        expect(container.querySelector(".match")).toBeNull();
        await click(button(direction));
        expect(pagination()).toContain("第 5/5 页");
        expect(container.querySelector(".match code")?.textContent).toContain("context-4205");
        vi.mocked(Element.prototype.scrollIntoView).mockClear();
        await click(button(direction));
        expect(Element.prototype.scrollIntoView).toHaveBeenCalled();
      }
    },
  );
  it("uses display-row mapping when deletion and addition source indices share split rows", async () => {
    localStorage.setItem("diff-mode", "split");
    const text = [
      "@@ -1,1500 +1,1500 @@",
      ...Array.from({ length: 1500 }, (_, index) => `-old-${index}`),
      ...Array.from(
        { length: 1500 },
        (_, index) => `+new-${index}${index === 1200 ? " unique-late" : ""}`,
      ),
    ].join("\n");
    await mount(<DiffViewer title="replace.txt" text={text} />);
    await search("unique-late");
    expect(pagination()).toContain("1001–1501 / 1501 行");
    expect(container.querySelector(".match code")?.textContent).toContain(
      "new-1200",
    );
    expect(container.querySelector(".match .line-no")?.textContent).toBe(
      "1201",
    );
    expect(displayed().length).toBeLessThanOrEqual(1000);
  });
  it("preserves raw property/binary metadata, hostile literal text and hunk line numbers", async () => {
    const text =
      "Property changes on: file\nCannot display: binary\n<img src=x onerror=alert(1)>\n@@ -8,2 +8,2 @@\n-before\n+after\n context";
    await mount(<DiffViewer title="small.txt" text={text} />);
    expect(container.querySelector('[data-line="0"] code')?.textContent).toBe(
      "Property changes on: file",
    );
    expect(
      container.querySelector('[data-line="2"] code')?.textContent,
    ).toContain("<img");
    expect(container.querySelector("img")).toBeNull();
    expect(
      container.querySelector('[data-line="4"] .line-no')?.textContent,
    ).toBe("8");
    const splitButton = [...container.querySelectorAll("button")].find(
      (element) => element.textContent === "并排",
    )!;
    await click(splitButton);
    expect(
      container.querySelector('[data-line="5"] .line-no')?.textContent,
    ).toBe("8");
    expect(
      container.querySelector('[data-line="6"] .line-no')?.textContent,
    ).toBe("9");
    expect(container.textContent).toContain("Cannot display: binary");
  });
  it("resets/clamps the page on file/text and mode changes using the real file selector", async () => {
    function Files() {
      const [file, setFile] = useState("big.txt");
      return (
        <DiffViewer
          title={file}
          text={file === "big.txt" ? bigText : "small raw metadata"}
          files={["big.txt", "small.txt"]}
          onFile={setFile}
        />
      );
    }
    await mount(<Files />);
    await click(button("下一页差异"));
    await click(button("下一页差异"));
    expect(pagination()).toContain("第 3/5 页");
    const selector = container.querySelector<HTMLSelectElement>(
      '[aria-label="切换差异文件"]',
    )!;
    await act(async () => {
      selector.value = "small.txt";
      selector.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(pagination()).toBeUndefined();
    expect(displayed()).toHaveLength(1);
    expect(container.textContent).toContain("small raw metadata");
    await act(async () => {
      selector.value = "big.txt";
      selector.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(pagination()).toContain("第 1/5 页");
    await click(button("下一页差异"));
    await click(
      [...container.querySelectorAll("button")].find(
        (element) => element.textContent === "并排",
      )!,
    );
    expect(pagination()).toContain("第 1/5 页");
    expect(displayed()).toHaveLength(1000);
  });
});
