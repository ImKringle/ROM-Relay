import { useEffect, useState } from "react";

// ConfirmButton: a destructive action that asks once, in place. The first
// press arms it (and names what will happen); a second press within a few
// seconds does it. Errors land beside the button.
export function ConfirmButton({ label, confirm, onConfirm, small = true }: { label: string; confirm: string; onConfirm: () => Promise<unknown>; small?: boolean }) {
  const [armed, setArmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!armed) return;
    const t = setTimeout(() => setArmed(false), 4000);
    return () => clearTimeout(t);
  }, [armed]);
  return (
    <span className="inline-grid gap-1">
      <button
        className={`btn btn-danger ${small ? "btn-sm" : ""} ${armed ? "border-danger bg-danger/15" : ""}`}
        disabled={busy}
        title={armed ? "Press again to confirm. This cannot be undone." : undefined}
        onClick={async () => {
          if (!armed) return setArmed(true);
          setBusy(true);
          setError("");
          try {
            await onConfirm();
          } catch (e) {
            setError((e as Error).message);
          } finally {
            setBusy(false);
            setArmed(false);
          }
        }}
      >
        {armed ? confirm : label}
      </button>
      {error && <span className="max-w-xs text-xs text-danger [overflow-wrap:anywhere]">{error}</span>}
    </span>
  );
}
