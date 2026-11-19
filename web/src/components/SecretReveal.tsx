import { useState } from "react";

// SecretReveal shows a freshly issued key exactly once, with a copy button.
// The relay keeps only a hash, so this is the only chance to save it.
export function SecretReveal({ secret, extra, onDone }: { secret: string; extra?: string; onDone: () => void }) {
  const [copied, setCopied] = useState(false);
  return (
    <div className="grid gap-2 rounded border-2 border-gold/60 bg-gold/5 p-3" role="status">
      <strong className="text-sm text-gold">Copy this key now. It will not be shown again.</strong>
      <code className="block select-all rounded border-2 border-line bg-void p-2 text-xs [overflow-wrap:anywhere]">{secret}</code>
      {extra && <span className="text-xs text-muted">{extra}</span>}
      <div className="flex gap-2">
        <button
          className="btn btn-sm btn-primary"
          onClick={async () => {
            await navigator.clipboard.writeText(secret);
            setCopied(true);
          }}
        >
          {copied ? "Copied ✓" : "Copy"}
        </button>
        <button className="btn btn-sm" onClick={onDone}>
          I saved it
        </button>
      </div>
    </div>
  );
}
