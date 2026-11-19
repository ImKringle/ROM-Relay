import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

// Icon draws one of the relay's 24x24 stroke paths (tool icons come from the
// server's registry as path data).
export function Icon({ d, className = "size-4" }: { d: string; className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {d.split(/(?=M)/).map((p, i) => (
        <path key={i} d={p.trim()} />
      ))}
    </svg>
  );
}

export const icons = {
  download: "M12 3v12M8 11l4 4 4-4M5 20h14",
  close: "M6 6l12 12M18 6 6 18",
  search: "M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16ZM21 21l-4.3-4.3",
  menu: "M4 6h16M4 12h16M4 18h16",
  activity: "M12 7v5l3 2M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18Z",
  user: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM4 21a8 8 0 0 1 16 0",
  filter: "M4 6h16M7 12h10M10 18h4",
  back: "m15 18-6-6 6-6",
  check: "m5 12 5 5L20 7",
  upload: "M12 21V9M8 13l4-4 4 4M5 4h14",
  trash: "M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3",
  refresh: "M20 11a8 8 0 0 0-14.3-4.9L4 8M4 4v4h4M4 13a8 8 0 0 0 14.3 4.9L20 16M20 20v-4h-4",
  external: "M14 4h6v6M20 4l-9 9M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5",
};

export function Spinner({ label = "Loading" }: { label?: string }) {
  return (
    <div className="flex items-center gap-3 p-6 text-sm text-muted" role="status">
      <span className="size-3 animate-pulse bg-cyan" aria-hidden="true" />
      <span className="pixel text-[0.65rem]">{label}…</span>
    </div>
  );
}

export function ErrorNote({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const message = error instanceof Error ? error.message : String(error);
  return (
    <div className="panel flex flex-wrap items-center justify-between gap-3 border-danger/50 p-4 text-sm text-danger" role="alert">
      <span className="[overflow-wrap:anywhere]">{message}</span>
      {onRetry && (
        <button className="btn btn-sm" onClick={onRetry}>
          Retry
        </button>
      )}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="panel grid place-items-center gap-3 p-10 text-center text-muted">{children}</div>;
}

// Hero is the CRT-styled page header.
export function Hero({ eyebrow, title, children, actions }: { eyebrow?: string; title: string; children?: ReactNode; actions?: ReactNode }) {
  return (
    <section className="crt mb-6 flex flex-wrap items-end justify-between gap-4 p-[clamp(18px,3.5vw,32px)]">
      <div className="relative z-10 min-w-0">
        {eyebrow && <span className="eyebrow">{eyebrow}</span>}
        <h1 className="pixel m-0 mt-2 text-[clamp(1.05rem,3vw,1.6rem)] text-text [text-shadow:0_0_14px_#5ee7ff66]">{title}</h1>
        {children && <div className="mt-3 max-w-2xl text-sm text-muted">{children}</div>}
      </div>
      {actions && <div className="relative z-10 flex flex-wrap gap-2">{actions}</div>}
    </section>
  );
}

export function Fact({ label, value }: { label: string; value?: string }) {
  if (!value) return null;
  return (
    <div className="rounded border-2 border-line bg-void/40 p-3">
      <span className="eyebrow mb-1 block text-[0.5rem] text-faint">{label}</span>
      <strong className="block truncate text-sm" title={value}>
        {value}
      </strong>
    </div>
  );
}

// Modal: a dialog over a scrim. Escape and the scrim close it; focus moves in.
export function Modal({ open, onClose, label, children, wide }: { open: boolean; onClose: () => void; label: string; children: ReactNode; wide?: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const key = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", key);
    document.body.style.overflow = "hidden";
    ref.current?.focus();
    return () => {
      document.removeEventListener("keydown", key);
      document.body.style.overflow = "";
    };
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="scrim" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div
        ref={ref}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={label}
        className={`panel panel-hi relative max-h-[90dvh] w-full overflow-y-auto p-[clamp(16px,3vw,28px)] outline-none ${wide ? "max-w-5xl" : "max-w-xl"}`}
      >
        <button className="btn btn-sm absolute right-3 top-3 z-10" onClick={onClose} aria-label="Close">
          <Icon d={icons.close} />
        </button>
        {children}
      </div>
    </div>
  );
}

// useData loads one API resource and re-runs when its key changes. Stale
// responses (an older key answering late) are dropped.
export function useData<T>(load: () => Promise<T>, deps: unknown[]) {
  const [state, setState] = useState<{ data?: T; error?: unknown; loading: boolean }>({ loading: true });
  const seq = useRef(0);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const run = useCallback(load, deps);
  const reload = useCallback(() => {
    const mine = ++seq.current;
    setState((s) => ({ ...s, loading: true, error: undefined }));
    run().then(
      (data) => mine === seq.current && setState({ data, loading: false }),
      (error) => mine === seq.current && setState({ error, loading: false }),
    );
  }, [run]);
  useEffect(reload, [reload]);
  return { ...state, reload, setData: (data: T) => setState({ data, loading: false }) };
}
