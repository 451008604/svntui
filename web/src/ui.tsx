import { useEffect, useRef, useState, type ReactNode } from "react";
import { api, errorText } from "./api";
export function Icon({ name, size = 18 }: { name: string; size?: number }) {
  const paths: Record<string, ReactNode> = {
    folder: <path d="M3 7h6l2 2h10v11H3zM3 7V4h6l2 3" />,
    refresh: (
      <>
        <path d="M20 7v5h-5M4 17v-5h5" />
        <path d="M6 7a8 8 0 0113 3M18 17a8 8 0 01-13-3" />
      </>
    ),
    check: <path d="m5 12 4 4L20 5" />,
    activity: <path d="M3 12h4l3-8 4 16 3-8h4" />,
    plus: <path d="M12 4v16M4 12h16" />,
    branch: (
      <>
        <circle cx="6" cy="5" r="2" />
        <circle cx="18" cy="5" r="2" />
        <circle cx="6" cy="19" r="2" />
        <path d="M6 7v10M18 7c0 6-12 2-12 8" />
      </>
    ),
    history: (
      <>
        <path d="M3 6v5h5M4 10a9 9 0 119 11" />
        <path d="M12 7v6l4 2" />
      </>
    ),
    conflict: (
      <>
        <path d="m12 3 10 18H2z" />
        <path d="M12 9v5M12 17v1" />
      </>
    ),
    close: <path d="m5 5 14 14M19 5 5 19" />,
    search: (
      <>
        <circle cx="10" cy="10" r="6" />
        <path d="m15 15 6 6" />
      </>
    ),
  };
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.7"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {paths[name] || paths.folder}
    </svg>
  );
}
export function Button({
  children,
  onClick,
  primary = false,
  disabled = false,
  title,
  className = "",
}: {
  children: ReactNode;
  onClick?: () => void;
  primary?: boolean;
  disabled?: boolean;
  title?: string;
  className?: string;
}) {
  return (
    <button
      type="button"
      className={`${primary ? "primary" : ""} ${className}`}
      onClick={onClick}
      disabled={disabled}
      title={title}
    >
      {children}
    </button>
  );
}
export function ErrorBox({ error }: { error: unknown }) {
  return error ? (
    <div className="error" role="alert">
      {errorText(error)}
    </div>
  ) : null;
}
export function Empty({
  title,
  children,
}: {
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="empty">
      <Icon name="branch" size={34} />
      <h3>{title}</h3>
      {children && <p>{children}</p>}
    </div>
  );
}
export function Modal({
  title,
  children,
  onClose,
  wide = false,
}: {
  title: string;
  children: ReactNode;
  onClose: () => void;
  wide?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const restore = useRef<Element | null>(null);
  useEffect(() => {
    restore.current = document.activeElement;
    const el = ref.current!;
    el.querySelector<HTMLElement>("input,textarea,select,button")?.focus();
    const key = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
      }
      if (e.key === "Tab") {
        const targets = [
          ...el.querySelectorAll<HTMLElement>(
            'button:not(:disabled),input:not(:disabled),textarea:not(:disabled),select:not(:disabled),[tabindex="0"]',
          ),
        ];
        const first = targets[0],
          last = targets[targets.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last?.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first?.focus();
        }
      }
    };
    el.addEventListener("keydown", key);
    return () => {
      el.removeEventListener("keydown", key);
      (restore.current as HTMLElement)?.focus?.();
    };
  }, []);
  return (
    <div className="overlay">
      <div
        ref={ref}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={`modal ${wide ? "wide" : ""}`}
      >
        <header>
          <h2>{title}</h2>
          <button aria-label="关闭对话框" onClick={onClose}>
            <Icon name="close" />
          </button>
        </header>
        {children}
      </div>
    </div>
  );
}
export function useRead<T>(url: string | null, refresh = 0) {
  const [data, setData] = useState<T | null>(null),
    [error, setError] = useState<unknown>(null),
    [loading, setLoading] = useState(false);
  useEffect(() => {
    setData(null);
    setError(null);
    if (!url) return;
    const controller = new AbortController();
    setLoading(true);
    api<T>(url, { signal: controller.signal })
      .then(setData)
      .catch((e) => {
        if (!controller.signal.aborted) setError(e);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [url, refresh]);
  return { data, error, loading };
}
