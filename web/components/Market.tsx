"use client";

// The market area: the depth chart and the order book side by side (stacked on a narrow screen), each in a card of a fixed height
// that is there before the first book, so nothing moves when it arrives.

import { memo } from "react";

import { copy } from "@/lib/copy";
import type { ViewEvent } from "@/lib/types";

import { DepthChart } from "./DepthChart";
import { Ladder } from "./Ladder";

/** The depth card as it is before the first book, and for a book with no levels. */
function DepthPlaceholder({ text }: { text: string }) {
  return (
    <section className="mk-card mk-depth" aria-labelledby="depth-title">
      <h2 id="depth-title" className="mk-title">
        {copy.market.depthTitle}
      </h2>
      <p className="mk-lede">{text}</p>
      <div className="chart" />
    </section>
  );
}

export const Market = memo(function Market({ view }: { view: ViewEvent | null }) {
  const levels = view ? Math.max(view.bids.length, view.asks.length) : 0;
  return (
    <div className="mk-grid">
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
  );
});
