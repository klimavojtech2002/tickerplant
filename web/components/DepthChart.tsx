"use client";

// The cumulative depth chart: how much size rests at each price, bids stepping left from the middle and asks stepping right.
// The picture is drawn from exact sums by lib/geometry.ts; the labels around it are ordinary text on top of the SVG (SVG text
// would be stretched by the box). It is an image to a screen reader, and the order book next to it is its text alternative.

import { memo, useMemo, useState, type CSSProperties, type PointerEvent } from "react";

import { copy } from "@/lib/copy";
import { priceEnds, tickLabels } from "@/lib/depth";
import { depthBox, depthShape, nearestPoint } from "@/lib/geometry";
import { groupThousands } from "@/lib/group";
import { fromScaled } from "@/lib/ticks";
import { baseCurrency } from "@/lib/hero";
import { mid } from "@/lib/mid";
import type { ViewEvent } from "@/lib/types";

/** The heading, the subtitle and the legend of the depth card; the placeholder uses the same head, so the legend takes the same room. */
export function DepthHead({ subtitle, idle }: { subtitle: string; idle: boolean }) {
  return (
    <div className="mk-head">
      <div>
        <h2 id="depth-title" className="mk-title">
          {copy.market.depthTitle}
        </h2>
        <p className="mk-lede">{subtitle}</p>
      </div>
      <p className={`legend${idle ? " legend-idle" : ""}`} aria-hidden="true">
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
  );
}

export const DepthChart = memo(function DepthChart({ view }: { view: ViewEvent }) {
  const shape = useMemo(() => depthShape(view), [view]); // moving the pointer re-renders, a book does not need to be redrawn for it
  // Where the pointer is across the plot, as a fraction of its width, or null when it is away. The level under it is found again
  // from the current book on every render, so a new book can never leave the tooltip on a level that is gone.
  const [pointer, setPointer] = useState<number | null>(null);
  const active = shape && pointer !== null ? nearestPoint(shape.points, pointer) : null;
  const onMove = (e: PointerEvent<HTMLDivElement>) => {
    if (e.pointerType === "touch") {
      return; // a finger would leave the tooltip stuck; the order book is the way to read exact numbers
    }
    const box = e.currentTarget.getBoundingClientRect();
    if (box.width > 0) {
      setPointer((e.clientX - box.left) / box.width); // outside the plot is fine: the nearest level is still the edge one
    }
  };
  const ends = priceEnds(view);
  const levels = Math.max(view.bids.length, view.asks.length);
  const ticks = shape ? tickLabels(shape.top, shape.scale) : [];
  const longest = Math.max(4, ...ticks.map((t) => t.length)); // the label column is as wide as its longest label, in characters
  const currency = baseCurrency(view.symbol);
  const bestBid = view.bids[0]?.[0] ?? null;
  const bestAsk = view.asks[0]?.[0] ?? null;
  const midPrice = mid(bestBid ?? undefined, bestAsk ?? undefined);
  const label = copy.market.depthLabel(bestBid, bestAsk, levels);

  return (
    <section className="mk-card mk-depth" aria-labelledby="depth-title">
      <DepthHead subtitle={copy.market.depthLede(levels, currency)} idle={false} />
      {shape === null || ends === null ? (
        <p className="chart-note">{copy.market.cannotDraw}</p>
      ) : (
        <div className="chart" style={{ "--ylab": `${longest}ch` } as CSSProperties}>
          <div
            className="chart-plot"
            onPointerMove={onMove}
            onPointerLeave={() => setPointer(null)}
            onPointerCancel={() => setPointer(null)}
          >
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
              {active ? (
                <line
                  className="chart-hover"
                  x1={(active.left / 100) * depthBox.width}
                  x2={(active.left / 100) * depthBox.width}
                  y1={depthBox.top}
                  y2={depthBox.base}
                  vectorEffect="non-scaling-stroke"
                />
              ) : null}
              {shape.midLeft !== null ? (
                <line
                  className="chart-mid"
                  x1={(shape.midLeft / 100) * depthBox.width}
                  x2={(shape.midLeft / 100) * depthBox.width}
                  y1={depthBox.top}
                  y2={depthBox.base}
                  vectorEffect="non-scaling-stroke"
                />
              ) : null}
            </svg>
            {shape.midLeft !== null && midPrice !== null ? (
              <span
                className="chart-midchip"
                aria-hidden="true"
                style={{ left: `clamp(3.25rem, ${shape.midLeft}%, calc(100% - 3.25rem))` }}
              >
                {groupThousands(midPrice)}
              </span>
            ) : null}
            {active ? (
              <>
                <span
                  className={`chart-dot chart-dot-${active.side}`}
                  style={{ left: `${active.left}%`, top: `${active.top}%` }}
                />
                <div
                  className="chart-tip"
                  style={{
                    left: `clamp(5rem, ${active.left}%, calc(100% - 5rem))`,
                    top: `max(3.5rem, ${active.top}%)`,
                  }}
                >
                  <b>{active.price}</b>
                  <span>
                    {(active.side === "bid" ? copy.market.tipBid : copy.market.tipAsk)(
                      fromScaled(active.cum, shape!.scale),
                      currency,
                    )}
                  </span>
                </div>
              </>
            ) : null}
            {ticks.map((text, i) => (
              <span key={text} className="chart-ylab" aria-hidden="true" style={{ top: `${shape.tickTops[i]}%` }}>
                {text}
              </span>
            ))}
          </div>
          <div className="chart-axis" aria-hidden="true">
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
