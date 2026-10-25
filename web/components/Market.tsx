"use client";

// The market area: the depth chart and the order book side by side (stacked on a tablet), each in a card of a fixed height that is
// there before the first book, so nothing moves when it arrives. On a phone one card is shown at a time, chosen with a switch that
// exists only at that width (the others see both cards and no switch).

import { memo, useState } from "react";

import { copy } from "@/lib/copy";
import type { ViewEvent } from "@/lib/types";

import { DepthChart, DepthHead } from "./DepthChart";
import { Ladder } from "./Ladder";

/** The depth card as it is before the first book, and for a book with no levels. */
function DepthPlaceholder({ text }: { text: string }) {
  return (
    <section className="mk-card mk-depth" aria-labelledby="depth-title">
      <DepthHead subtitle={text} idle />
      <div className="chart" />
    </section>
  );
}

export const Market = memo(function Market({ view }: { view: ViewEvent | null }) {
  const [shown, setShown] = useState<"depth" | "ladder">("depth");
  const levels = view ? Math.max(view.bids.length, view.asks.length) : 0;
  return (
    <>
      <div className="mk-switch" role="group" aria-label={copy.market.switchLabel}>
        <button type="button" aria-pressed={shown === "depth"} onClick={() => setShown("depth")}>
          {copy.market.switchDepth}
        </button>
        <button type="button" aria-pressed={shown === "ladder"} onClick={() => setShown("ladder")}>
          {copy.market.switchLadder}
        </button>
      </div>
      <div className="mk-grid" data-shown={shown}>
        {view === null ? (
          <DepthPlaceholder text={copy.market.waiting} />
        ) : levels === 0 ? (
          <DepthPlaceholder text={copy.market.empty} />
        ) : (
          <DepthChart view={view} />
        )}
        <section className="mk-card mk-ladder" aria-labelledby="ladder-title">
          <h2 id="ladder-title" className="mk-title">
            {copy.market.title}
          </h2>
          {view === null ? (
            <>
              <p className="mk-lede">{copy.market.waiting}</p>
              <div className="mk-body" />
            </>
          ) : levels === 0 ? (
            <>
              <p className="mk-lede">{copy.market.empty}</p>
              <div className="mk-body" />
            </>
          ) : (
            <>
              <p className="mk-lede">{copy.market.ladderLede(levels)}</p>
              <div className="mk-body">
                <Ladder view={view} />
              </div>
            </>
          )}
        </section>
      </div>
    </>
  );
});
