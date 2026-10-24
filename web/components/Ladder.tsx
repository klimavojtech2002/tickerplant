"use client";

// The order book as a table: asks above with the best nearest the middle, the spread and mid between, bids below. Prices and
// sizes are printed exactly as the venue sent them; the only things derived are the bar widths (whole percents) and the spread
// row. A size has its trailing zeros drawn lighter, which only changes how the same text looks.

import { memo } from "react";

import { copy } from "@/lib/copy";
import { barShares, gap, trimZeros } from "@/lib/ladder";
import type { ViewEvent } from "@/lib/types";

type Side = "bid" | "ask";

function Row({
  side,
  price,
  size,
  share,
  best,
}: {
  side: Side;
  price: string;
  size: string;
  share: number;
  best: boolean;
}) {
  const [kept, muted] = trimZeros(size);
  return (
    <tr className={`ladder-row ladder-${side}`} data-best={best ? side : undefined}>
      <td className="price">{price}</td>
      <td className="size">
        {kept}
        {muted ? <span className="z">{muted}</span> : null}
      </td>
      <td className="meter">
        <span className="meter-bar" style={{ width: `${share}%` }} aria-hidden="true" />
        {best ? (
          <span className="visually-hidden">{side === "bid" ? copy.market.bestBid : copy.market.bestAsk}</span>
        ) : null}
      </td>
    </tr>
  );
}

// Memoised: the page re-renders every second for telemetry, and the book only changes with a view.
export const Ladder = memo(function Ladder({ view }: { view: ViewEvent }) {
  const shares = barShares(view);
  const between = gap(view);
  // Asks are listed farthest first so the best ask sits next to the best bid.
  const asks = view.asks.map((level, i) => ({ level, i })).reverse();
  return (
    <table className="ladder" aria-label={copy.market.title}>
      <thead>
        <tr>
          <th>{copy.market.price}</th>
          <th>{copy.market.size}</th>
          <th className="meter">
            <span className="visually-hidden">{copy.market.meter}</span>
          </th>
        </tr>
      </thead>
      <tbody className="asks">
        {asks.map(({ level: [price, size], i }) => (
          <Row key={price} side="ask" price={price} size={size} share={shares.asks[i]!} best={i === 0} />
        ))}
      </tbody>
      <tbody className="between">
        <tr className="ladder-gap">
          <td colSpan={3}>
            {copy.market.spread} <b>{between ? between.spread : "—"}</b> · {copy.market.mid}{" "}
            <b>{between ? between.mid : "—"}</b>
          </td>
        </tr>
      </tbody>
      <tbody className="bids">
        {view.bids.map(([price, size], i) => (
          <Row key={price} side="bid" price={price} size={size} share={shares.bids[i]!} best={i === 0} />
        ))}
      </tbody>
    </table>
  );
});
