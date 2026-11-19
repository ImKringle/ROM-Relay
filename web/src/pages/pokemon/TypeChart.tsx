import { useState } from "react";
import { api } from "../../lib/api";
import { TYPE_COLORS } from "../../lib/pokemon";
import { ErrorNote, Hero, Spinner, useData } from "../../components/ui";

type Chart = { types: string[]; rows: { type: string; mults: number[] }[] };

const cap = (s: string) => s[0].toUpperCase() + s.slice(1);
const label = (m: number) => (m === 1 ? "" : m === 0 ? "0" : m === 0.5 ? "½" : m === 0.25 ? "¼" : `${m}`);
function tone(m: number) {
  if (m === 0) return "bg-void text-faint";
  if (m < 1) return "bg-danger/20 text-danger";
  if (m >= 4) return "bg-leaf/40 text-leaf font-black";
  if (m > 1) return "bg-leaf/20 text-leaf";
  return "";
}

// TypeChart: rows are defending types, columns attacking types. Pick one or
// two defending types to see what a dual-type Pokémon takes.
export function TypeChart() {
  const { data, error, loading, reload } = useData(() => api.get<Chart>("/api/typechart"), []);
  const [picked, setPicked] = useState<string[]>([]);
  if (error) return <ErrorNote error={error} onRetry={reload} />;
  if (loading || !data) return <Spinner />;
  const toggle = (t: string) => setPicked((p) => (p.includes(t) ? p.filter((x) => x !== t) : [...p, t].slice(-2)));
  const combined = data.types.map((_, i) => picked.reduce((acc, t) => acc * (data.rows.find((r) => r.type === t)?.mults[i] ?? 1), 1));

  return (
    <>
      <Hero eyebrow="Pokémon tools" title="Type Chart">
        Defending types down the side, attacking types across the top: the multiplier a single-type Pokémon takes. Blank is neutral. Pick up to two defending types to combine them.
      </Hero>
      {picked.length > 0 && (
        <div className="panel mb-4 flex flex-wrap items-center gap-2 p-3 text-sm">
          <strong>{picked.map(cap).join(" / ")} takes:</strong>
          {data.types.map((t, i) =>
            combined[i] === 1 ? null : (
              <span key={t} className={`badge ${tone(combined[i])}`}>
                {cap(t)} ×{label(combined[i]) || 1}
              </span>
            ),
          )}
          <button className="btn btn-sm ml-auto" onClick={() => setPicked([])}>
            Clear
          </button>
        </div>
      )}
      <div className="panel overflow-x-auto p-2">
        <table className="border-collapse text-center text-xs">
          <thead>
            <tr>
              <th className="p-1" />
              {data.types.map((t) => (
                <th key={t} className="p-0.5">
                  <img src={`/assets/typebadges/${t}.png`} alt={t} title={t} width={46} height={20} />
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {data.rows.map((r) => {
              const on = !picked.length || picked.includes(r.type);
              return (
                <tr key={r.type} className={on ? "" : "opacity-25"}>
                  <th className="p-0.5">
                    <button
                      onClick={() => toggle(r.type)}
                      className={`rounded border-2 p-0.5 ${picked.includes(r.type) ? "border-cyan" : "border-transparent"}`}
                      style={{ background: picked.includes(r.type) ? `${TYPE_COLORS[r.type]}44` : undefined }}
                      aria-pressed={picked.includes(r.type)}
                    >
                      <img src={`/assets/typebadges/${r.type}.png`} alt={r.type} title={r.type} width={46} height={20} />
                    </button>
                  </th>
                  {r.mults.map((m, i) => (
                    <td key={i} className={`size-8 border border-line font-bold ${tone(m)}`}>
                      {label(m)}
                    </td>
                  ))}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}
