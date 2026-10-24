"use client";

// The market area: the order book card, with its heading, and a placeholder of the same height before the first book. Later
// the depth chart joins it here.

import { memo } from "react";

import { copy } from "@/lib/copy";
import type { ViewEvent } from "@/lib/types";

import { Ladder } from "./Ladder";

export const Market = memo(function Market({ view }: { view: ViewEvent | null }) {
  const levels = view ? Math.max(view.bids.length, view.asks.length) : 0;
  return (
    <section className="mk-card" aria-labelledby="market-title">
      <h2 id="market-title" className="mk-title">
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
  );
});
