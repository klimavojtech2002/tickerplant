"use client";

// The cumulative depth chart: how much size rests at each price, bids stepping left from the middle and asks stepping right.
// The picture is drawn from exact sums by lib/geometry.ts; the labels around it are ordinary text on top of the SVG (SVG text
// would be stretched by the box). It is an image to a screen reader, and the order book next to it is its text alternative.

import { memo, useMemo } from "react";

import { copy } from "@/lib/copy";
import { priceEnds, tickLabels } from "@/lib/depth";
import { depthBox, depthShape } from "@/lib/geometry";
import { groupThousands } from "@/lib/group";
import { baseCurrency } from "@/lib/hero";
import { mid } from "@/lib/mid";
import type { ViewEvent } from "@/lib/types";

export const DepthChart = memo(function DepthChart({ view }: { view: ViewEvent }) {
  const shape = useMemo(() => depthShape(view), [view]);
  const ends = priceEnds(view);
  const levels = Math.max(view.bids.length, view.asks.length);
  const currency = baseCurrency(view.symbol);
  const bestBid = view.bids[0]?.[0] ?? null;
  const bestAsk = view.asks[0]?.[0] ?? null;
  const midPrice = mid(bestBid ?? undefined, bestAsk ?? undefined);
  const label = copy.market.depthLabel(bestBid, bestAsk, levels);

  return (
    <section className="mk-card mk-depth" aria-labelledby="depth-title">
      <div className="mk-head">
        <div>
          <h2 id="depth-title" className="mk-title">
            {copy.market.depthTitle}
          </h2>
          <p className="mk-lede">{copy.market.depthLede(levels, currency)}</p>
        </div>
        <p className="legend" aria-hidden="true">
          <span>
            <i className="dot dot-bid" />
            {copy.market.legendBids}
          </span>
          <span>
            <i className="dot dot-ask" />
            {copy.market.legendAsks}
          </span>
        </p>
      </div>
      {shape === null || ends === null ? (
        <p className="chart-note">{copy.market.cannotDraw}</p>
      ) : (
        <div className="chart">
          <div className="chart-plot">
            <svg
              className="chart-svg"
              viewBox={`0 0 ${depthBox.width} ${depthBox.height}`}
              preserveAspectRatio="none"
              role="img"
              aria-label={label}
            >
              <defs>
                <linearGradient id="depth-bid" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0" className="stop-bid" stopOpacity="0.36" />
                  <stop offset="1" className="stop-bid" stopOpacity="0" />
                </linearGradient>
                <linearGradient id="depth-ask" x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0" className="stop-ask" stopOpacity="0.32" />
                  <stop offset="1" className="stop-ask" stopOpacity="0" />
                </linearGradient>
              </defs>
              {shape.tickTops.map((top) => (
                <line
                  key={top}
                  className="chart-grid"
                  x1="0"
                  x2={depthBox.width}
                  y1={(top / 100) * depthBox.height}
                  y2={(top / 100) * depthBox.height}
                  vectorEffect="non-scaling-stroke"
                />
              ))}
              {shape.bids ? (
                <>
                  <path d={shape.bids.area} fill="url(#depth-bid)" />
                  <path d={shape.bids.line} className="chart-line chart-line-bid" vectorEffect="non-scaling-stroke" />
                </>
              ) : null}
              {shape.asks ? (
                <>
                  <path d={shape.asks.area} fill="url(#depth-ask)" />
                  <path d={shape.asks.line} className="chart-line chart-line-ask" vectorEffect="non-scaling-stroke" />
                </>
              ) : null}
              {shape.midLeft !== null ? (
                <line
                  className="chart-mid"
                  x1={(shape.midLeft / 100) * depthBox.width}
                  x2={(shape.midLeft / 100) * depthBox.width}
                  y1="6"
                  y2={depthBox.base}
                  vectorEffect="non-scaling-stroke"
                />
              ) : null}
            </svg>
            {shape.midLeft !== null && midPrice !== null ? (
              <span className="chart-midchip" style={{ left: `${shape.midLeft}%` }}>
                {groupThousands(midPrice)}
              </span>
            ) : null}
            {tickLabels(shape.top, shape.scale).map((text, i) => (
              <span key={text} className="chart-ylab" style={{ top: `${shape.tickTops[i]}%` }}>
                {text}
              </span>
            ))}
          </div>
          <div className="chart-axis">
            <span>{groupThousands(ends.low)}</span>
            <span>{groupThousands(ends.high)}</span>
          </div>
          {view.bids.length === 0 || view.asks.length === 0 ? (
            <p className="chart-note">{copy.market.oneSided}</p>
          ) : null}
        </div>
      )}
    </section>
  );
});
