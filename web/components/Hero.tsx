"use client";

// The top of the page: what is being watched, where the numbers come from, and the mid price, large. Every number here is derived
// exactly from the view's decimal strings. The mid price and the best bid and ask may show thousands separators (a transform of the
// string, lossless); the order book below prints the strings untouched.
//
// The hero keeps its own one-second clock, like the connection pill, because whether the connection is stale depends on time passing
// even when nothing arrives.

import { memo, useMemo } from "react";

import { connectionPill, type ConnectionState } from "@/lib/connection";
import { copy } from "@/lib/copy";
import { sparkPath } from "@/lib/geometry";
import { groupThousands, splitFraction } from "@/lib/group";
import { baseCurrency, formatClock, modeChip, spreadTicks } from "@/lib/hero";
import { change, collecting, formatSpan, span, type History } from "@/lib/history";
import { mid } from "@/lib/mid";
import type { PollState } from "@/lib/status";
import { spread } from "@/lib/ticks";
import type { ViewEvent } from "@/lib/types";
import { useNow } from "@/lib/useNow";

export type HeroProps = {
  /** The book to show: the live one, or the frozen one while paused. */
  view: ViewEvent | null;
  history: History;
  conn: ConnectionState;
  poll: PollState;
  /** The time the frozen book was received (ms since the epoch), or null when the page is live. */
  frozenAt: number | null;
  /** The time the last book was received, for the stale notice. */
  lastViewAt: number;
  onTogglePause: () => void;
};

/** A figure with its fraction in a lighter tone, so the whole part reads first. */
function Figure({ value, field }: { value: string; field: string }) {
  const [whole, fraction] = splitFraction(groupThousands(value));
  return (
    <span data-field={field}>
      {whole}
      {fraction ? <span className="frac">{fraction}</span> : null}
    </span>
  );
}

const SPARK_W = 400;
const SPARK_H = 120;

export const Hero = memo(function Hero({ view, history, conn, poll, frozenAt, lastViewAt, onTogglePause }: HeroProps) {
  const now = useNow(1000);
  const kind = connectionPill(conn, now).kind;
  const stale = frozenAt === null && (kind === "no-data" || kind === "reconnecting" || kind === "transient");
  const frozen = frozenAt !== null;

  const bestBid = view?.bids[0];
  const bestAsk = view?.asks[0];
  const midPrice = mid(bestBid?.[0], bestAsk?.[0]);
  const ticks = spreadTicks(bestBid?.[0], bestAsk?.[0]);
  const base = view ? baseCurrency(view.symbol) : null;
  const chip = modeChip(poll);
  const spark = useMemo(() => sparkPath(history, SPARK_W, SPARK_H), [history]);
  const delta = change(history);
  const covered = formatSpan(span(history));

  return (
    <section className={`hero${stale || frozen ? " hero-dim" : ""}`} aria-label="Market">
      <div className="hero-main">
        <div className="instrument">
          {view ? (
            <h1 className="instrument-name">{view.symbol}</h1>
          ) : (
            <h1 className="instrument-name instrument-wait">tickerplant</h1>
          )}
          {view ? <span className="instrument-venue">{view.venue}</span> : null}
          <span className={`chip chip-${chip}`} hidden={chip === "none"}>
            {chip === "demo"
              ? copy.hero.chipDemo
              : chip === "demo-chaos"
                ? copy.hero.chipDemoChaos
                : copy.hero.chipUnknown}
          </span>
          <button
            type="button"
            className="pause-btn"
            aria-pressed={frozen}
            disabled={view === null}
            onClick={onTogglePause}
          >
            <svg width="11" height="11" viewBox="0 0 12 12" aria-hidden="true">
              {frozen ? (
                <path d="M3 1.5v9l7.5-4.5z" fill="currentColor" />
              ) : (
                <path d="M3 1.5v9M9 1.5v9" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" />
              )}
            </svg>
            <span>{frozen ? copy.hero.resume : copy.hero.pause}</span>
          </button>
        </div>

        {frozen ? (
          <div className="banner" role="status">
            <div>
              <b>{copy.hero.pausedTitle(formatClock(frozenAt))}</b>
              <span>{copy.hero.pausedBody}</span>
            </div>
          </div>
        ) : stale && lastViewAt > 0 ? (
          <div className="banner" role="status">
            <div>
              <b>{copy.hero.staleTitle}</b>
              <span>{copy.hero.staleBody(formatClock(lastViewAt))}</span>
            </div>
          </div>
        ) : null}

        {view === null ? (
          <div className="hero-skeleton" aria-busy="true">
            <p className="eyebrow">{copy.hero.midLabel}</p>
            <div className="skel skel-mid" />
            <p className="hero-note">{copy.hero.waiting}</p>
          </div>
        ) : (
          <>
            <p className="eyebrow">{copy.hero.midLabel}</p>
            <div className="midprice">
              {midPrice !== null ? <Figure value={midPrice} field="mid" /> : <span data-field="mid">—</span>}
            </div>
            {midPrice === null ? <p className="hero-note">{copy.hero.oneSided}</p> : null}
            <dl className="quote">
              <div>
                <dt>
                  <i className="dot dot-bid" aria-hidden />
                  {copy.hero.bid}
                </dt>
                <dd>{bestBid ? <Figure value={bestBid[0]} field="bid" /> : <span data-field="bid">—</span>}</dd>
                <dd className="quote-size">{bestBid ? `${bestBid[1]}${base ? ` ${base}` : ""}` : ""}</dd>
              </div>
              <div>
                <dt>
                  <i className="dot dot-ask" aria-hidden />
                  {copy.hero.ask}
                </dt>
                <dd>{bestAsk ? <Figure value={bestAsk[0]} field="ask" /> : <span data-field="ask">—</span>}</dd>
                <dd className="quote-size">{bestAsk ? `${bestAsk[1]}${base ? ` ${base}` : ""}` : ""}</dd>
              </div>
              <div>
                <dt>{copy.hero.spread}</dt>
                <dd data-field="spread">{bestBid && bestAsk ? spread(bestBid[0], bestAsk[0]) : "—"}</dd>
                <dd className="quote-size">{ticks !== null ? copy.hero.ticks(ticks) : ""}</dd>
              </div>
            </dl>
          </>
        )}
      </div>

      <div className="spark">
        {spark && !collecting(history) ? (
          <svg
            className="spark-svg"
            viewBox={`0 0 ${SPARK_W} ${SPARK_H}`}
            preserveAspectRatio="none"
            role="img"
            aria-label={delta !== null ? copy.hero.sparkAria(delta, covered) : copy.hero.sparkAriaFlat(covered)}
          >
            <defs>
              <linearGradient id="spark-fill" x1="0" y1="0" x2="0" y2="1">
                <stop offset="0" className="spark-stop" stopOpacity="0.22" />
                <stop offset="1" className="spark-stop" stopOpacity="0" />
              </linearGradient>
            </defs>
            <path d={spark.area} fill="url(#spark-fill)" />
            <path d={spark.line} className="spark-line" vectorEffect="non-scaling-stroke" />
          </svg>
        ) : (
          <div className="spark-empty">{view ? copy.hero.collecting : ""}</div>
        )}
        <div className="spark-cap">
          <span>{spark && !collecting(history) ? copy.hero.sparkStart(covered) : ""}</span>
          <span className="spark-change">
            {delta !== null && !collecting(history) ? copy.hero.sparkChange(delta, covered) : ""}
          </span>
          <span>{spark && !collecting(history) ? copy.hero.sparkNow : ""}</span>
        </div>
      </div>
    </section>
  );
});
