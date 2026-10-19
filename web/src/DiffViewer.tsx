import { useEffect, useMemo, useRef, useState } from "react";
import { parseDiff, splitDiff } from "./state";
import { Icon, Empty, ErrorBox } from "./ui";
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
  onFile?: (p: string) => void;
}) {
  const [mode, setMode] = useState(
      localStorage.getItem("diff-mode") || "unified",
    ),
    [search, setSearch] = useState(""),
    [match, setMatch] = useState(0);
  const lines = useMemo(() => parseDiff(text), [text]);
  const matches = useMemo(
    () =>
      search
        ? lines.flatMap((line, i) =>
            line.text.toLowerCase().includes(search.toLowerCase()) ? [i] : [],
          )
        : [],
    [lines, search],
  );
  const root = useRef<HTMLDivElement>(null);
  useEffect(() => {
    setMatch(0);
  }, [search, text]);
  useEffect(() => {
    root.current
      ?.querySelector(`[data-line="${matches[match]}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [matches, match]);
  const content = (s: string) => {
    if (!search) return s;
    const at = s.toLowerCase().indexOf(search.toLowerCase());
    return at < 0 ? (
      s
    ) : (
      <>
        {s.slice(0, at)}
        <mark>{s.slice(at, at + search.length)}</mark>
        {s.slice(at + search.length)}
      </>
    );
  };
  return (
    <section className="diff panel">
      <div className="panel-head">
        <div className="truncate">
          <strong>差异</strong>
          <span className="mono">{title || "选择文件"}</span>
        </div>
        <div className="segmented">
          {["unified", "split"].map((m) => (
            <button
              key={m}
              className={mode === m ? "active" : ""}
              onClick={() => {
                setMode(m);
                localStorage.setItem("diff-mode", m);
              }}
            >
              {m === "unified" ? "统一" : "并排"}
            </button>
          ))}
        </div>
      </div>
      <div className="diff-tools">
        {files && files.length > 0 && (
          <select
            aria-label="切换差异文件"
            value={title}
            onChange={(e) => onFile?.(e.target.value)}
          >
            {files.map((p) => (
              <option key={p}>{p}</option>
            ))}
          </select>
        )}
        <label className="search small">
          <Icon name="search" size={14} />
          <input
            aria-label="搜索差异"
            placeholder="在差异中查找"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </label>
        {search && (
          <>
            <span>
              {matches.length ? match + 1 : 0}/{matches.length}
            </span>
            <button
              aria-label="上一个匹配"
              disabled={!matches.length}
              onClick={() =>
                setMatch((match - 1 + matches.length) % matches.length)
              }
            >
              ↑
            </button>
            <button
              aria-label="下一个匹配"
              disabled={!matches.length}
              onClick={() => setMatch((match + 1) % matches.length)}
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
            {lines.map((line, i) => (
              <div
                key={i}
                data-line={i}
                className={`diff-line ${line.kind} ${matches[match] === i ? "match" : ""}`}
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
            ))}
          </div>
        ) : (
          <div className="split-lines">
            {splitDiff(lines).map((row, i) =>
              row.meta ? (
                <div
                  className="diff-line meta"
                  key={i}
                  data-line={lines.indexOf(row.meta)}
                >
                  <code>{content(row.meta.text)}</code>
                </div>
              ) : (
                <div className="split-row" key={i}>
                  {[row.left, row.right].map((line, j) => (
                    <div
                      key={j}
                      data-line={line ? lines.indexOf(line) : undefined}
                      className={`diff-line ${line?.kind || "blank"}`}
                    >
                      <span className="line-no">
                        {j === 0 ? line?.old : line?.next}
                      </span>
                      <code>{content(line?.text || "")}</code>
                    </div>
                  ))}
                </div>
              ),
            )}
          </div>
        )}
      </div>
    </section>
  );
}
