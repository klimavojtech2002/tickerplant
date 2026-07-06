// BookPanel renders one complete view: top-of-book stat tiles and the two depth
// tables. Prices and sizes are printed verbatim — the strings the venue's scales
// produced — and the bars' widths are the only derived numbers (integer percent,
// computed in BigInt). Color identifies the side on the bars alone; all text stays
// in ink, and the sides are named by their headers, so identity is never color-only.

import { barPct, maxSize, spread } from "@/lib/ticks";
import type { ViewEvent } from "@/lib/types";

export function BookPanel({ view }: { view: ViewEvent }) {
  const bestBid = view.bids[0];
  const bestAsk = view.asks[0];
  return (
    <section aria-label="order book">
      <div className="tiles">
        <Tile label="best bid" value={bestBid ? bestBid[0] : "—"} />
        <Tile label="best ask" value={bestAsk ? bestAsk[0] : "—"} />
        <Tile label="spread" value={bestBid && bestAsk ? spread(bestBid[0], bestAsk[0]) : "—"} />
        <Tile label="seq" value={String(view.seq)} />
      </div>
      <div className="depth">
        <Side name="bids" levels={view.bids} />
        <Side name="asks" levels={view.asks} />
      </div>
    </section>
  );
}

function Tile({ label, value }: { label: string; value: string }) {
  return (
    <div className="tile">
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
    </div>
  );
}

function Side({ name, levels }: { name: "bids" | "asks"; levels: [string, string][] }) {
  const max = maxSize(levels);
  return (
    <table className="side" aria-label={name}>
      <thead>
        <tr>
          <th>{name === "bids" ? "bid" : "ask"}</th>
          <th>size</th>
        </tr>
      </thead>
      <tbody>
        {levels.map(([price, size]) => (
          <tr key={price}>
            <td className="price">{price}</td>
            <td className="size">
              <span className={`bar bar-${name}`} style={{ width: `${barPct(size, max)}%` }} />
              <span className="size-text">{size}</span>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
