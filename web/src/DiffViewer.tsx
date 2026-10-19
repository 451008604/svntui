import { useEffect, useMemo, useRef, useState } from "react";
import { parseDiff, splitDiff, type DiffLine } from "./state";
import { Icon, Empty, ErrorBox } from "./ui";
const ROWS_PER_PAGE = 1000;
export function DiffViewer({
  text,
  loading,
  error,
  title,
  files,
  onFile,
}: {
  text: string;
  loading?: boolean;
  error?: unknown;
  title: string;
  files?: string[];
  onFile?: (path: string) => void;
}) {
  const [mode, setMode] = useState(
    localStorage.getItem("diff-mode") === "split" ? "split" : "unified",
  );
  const [search, setSearch] = useState(""),
    [match, setMatch] = useState(0),
    [revealRequest, setRevealRequest] = useState(0),
    [page, setPage] = useState(0);
  const lines = useMemo(() => parseDiff(text), [text]);
  // 对整份输入建立一次索引；渲染只读取当前页，不创建隐藏的全文件 DOM。
  const split = useMemo(() => {
    const sourceIndices = new Map<DiffLine, number>();
    lines.forEach((line, index) => sourceIndices.set(line, index));
    const rows = splitDiff(lines),
      displayRowBySource = new Array<number>(lines.length);
    rows.forEach((row, index) => {
      for (const line of [row.meta, row.left, row.right]) {
        if (line) displayRowBySource[sourceIndices.get(line)!] = index;
      }
    });
    return { rows, sourceIndices, displayRowBySource };
  }, [lines]);
  const matches = useMemo(() => {
    const needle = search.toLowerCase();
    return needle
      ? lines.flatMap((line, index) =>
          line.text.toLowerCase().includes(needle) ? [index] : [],
        )
      : [];
  }, [lines, search]);
  const activeMatch = Math.min(match, Math.max(0, matches.length - 1));
  const source = matches[activeMatch];
  const rowCount = mode === "unified" ? lines.length : split.rows.length;
  const pageCount = Math.max(1, Math.ceil(rowCount / ROWS_PER_PAGE));
  const currentPage = Math.min(page, pageCount - 1),
    start = currentPage * ROWS_PER_PAGE,
    end = Math.min(rowCount, start + ROWS_PER_PAGE);
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    setPage(0);
    setMatch(0);
  }, [text, title, mode]);
  // 全文件搜索命中先跳到对应显示页，再在已挂载的实际源行上定位。
  useEffect(() => {
    if (source === undefined) return;
    const row = mode === "unified" ? source : split.displayRowBySource[source];
    setPage(Math.floor(row / ROWS_PER_PAGE));
  }, [source, mode, split, search, title, revealRequest]);
  useEffect(() => {
    root.current
      ?.querySelector(`[data-line="${source}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [source, currentPage, mode, text, search, title, revealRequest]);
  const navigateMatch = (index: number) => {
    setMatch(index);
    // 单个命中绕回自身时，也重新分页定位并滚动到实际源行。
    setRevealRequest((request) => request + 1);
  };
  const content = (value: string) => {
    if (!search) return value;
    const at = value.toLowerCase().indexOf(search.toLowerCase());
    return at < 0 ? (
      value
    ) : (
      <>
        {value.slice(0, at)}
        <mark>{value.slice(at, at + search.length)}</mark>
        {value.slice(at + search.length)}
      </>
    );
  };
  const className = (line: DiffLine, index: number) =>
    `diff-line ${line.kind} ${source === index ? "match" : ""}`;
  return (
    <section className="diff panel">
      <div className="panel-head">
        <div className="truncate">
          <strong>差异</strong>
          <span className="mono">{title || "选择文件"}</span>
        </div>
        <div className="segmented">
          {["unified", "split"].map((value) => (
            <button
              key={value}
              className={mode === value ? "active" : ""}
              onClick={() => {
                setMode(value);
                localStorage.setItem("diff-mode", value);
              }}
            >
              {value === "unified" ? "统一" : "并排"}
            </button>
          ))}
        </div>
      </div>
      <div className="diff-tools">
        {files && files.length > 0 && (
          <select
            aria-label="切换差异文件"
            value={title}
            onChange={(event) => onFile?.(event.target.value)}
          >
            {files.map((file) => (
              <option key={file}>{file}</option>
            ))}
          </select>
        )}
        <label className="search small">
          <Icon name="search" size={14} />
          <input
            aria-label="搜索差异"
            placeholder="在完整差异中查找"
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setMatch(0);
            }}
          />
        </label>
        {search && (
          <>
            <span aria-label="差异搜索匹配数">
              {matches.length ? activeMatch + 1 : 0}/{matches.length}
            </span>
            <button
              aria-label="上一个匹配"
              disabled={!matches.length}
              onClick={() =>
                navigateMatch((activeMatch - 1 + matches.length) % matches.length)
              }
            >
              ↑
            </button>
            <button
              aria-label="下一个匹配"
              disabled={!matches.length}
              onClick={() => navigateMatch((activeMatch + 1) % matches.length)}
            >
              ↓
            </button>
          </>
        )}
      </div>
      <ErrorBox error={error} />
      <div className="diff-body" ref={root}>
        {loading ? (
          <Empty title="正在读取差异…" />
        ) : !text ? (
          <Empty title={title ? "没有文本差异" : "选择文件查看差异"}>
            二进制、目录和纯属性变更可能没有文本内容
          </Empty>
        ) : mode === "unified" ? (
          <div className="diff-lines">
            {lines.slice(start, end).map((line, offset) => {
              const index = start + offset;
              return (
                <div
                  key={index}
                  data-display-row={index}
                  data-line={index}
                  className={className(line, index)}
                >
                  <span className="line-no">{line.old}</span>
                  <span className="line-no">{line.next}</span>
                  <span className="sign">
                    {line.kind === "add"
                      ? "+"
                      : line.kind === "delete"
                        ? "-"
                        : ""}
                  </span>
                  <code>{content(line.text)}</code>
                </div>
              );
            })}
          </div>
        ) : (
          <div className="split-lines">
            {split.rows.slice(start, end).map((row, offset) => {
              const index = start + offset;
              return row.meta ? (
                <div
                  key={index}
                  data-display-row={index}
                  data-line={split.sourceIndices.get(row.meta)}
                  className={className(
                    row.meta,
                    split.sourceIndices.get(row.meta)!,
                  )}
                >
                  <code>{content(row.meta.text)}</code>
                </div>
              ) : (
                <div className="split-row" key={index} data-display-row={index}>
                  {[row.left, row.right].map((line, side) => {
                    const sourceIndex = line
                      ? split.sourceIndices.get(line)
                      : undefined;
                    return (
                      <div
                        key={side}
                        data-line={sourceIndex}
                        className={
                          line
                            ? className(line, sourceIndex!)
                            : "diff-line blank"
                        }
                      >
                        <span className="line-no">
                          {side === 0 ? line?.old : line?.next}
                        </span>
                        <code>{content(line?.text || "")}</code>
                      </div>
                    );
                  })}
                </div>
              );
            })}
          </div>
        )}
      </div>
      {!loading && !!text && pageCount > 1 && (
        <nav className="diff-pagination" aria-label="差异分页">
          <button
            aria-label="上一页差异"
            disabled={currentPage === 0}
            onClick={() => setPage(currentPage - 1)}
          >
            上一页
          </button>
          <div aria-live="polite">
            <span>
              显示 {start + 1}–{end} / {rowCount} 行 · 第 {currentPage + 1}/
              {pageCount} 页
            </span>
            <small>完整差异已载入，按显示行分页</small>
          </div>
          <button
            aria-label="下一页差异"
            disabled={currentPage === pageCount - 1}
            onClick={() => setPage(currentPage + 1)}
          >
            下一页
          </button>
        </nav>
      )}
    </section>
  );
}
