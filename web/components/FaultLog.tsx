"use client";

// The recent faults the engine recorded, newest first, as a timeline. The card shows the five newest in a body of a fixed height and
// lets the visitor open the rest in place. Times are absolute and in the visitor's own zone, never "a minute ago": the server's clock
// and the browser's can differ, and an age would hide that.

import { memo, useState } from "react";

import { copy } from "@/lib/copy";
import { describeFault, faultSummary, isResyncKind, newEntries } from "@/lib/faults";
import { formatClock } from "@/lib/hero";
import type { TelemetryEvent } from "@/lib/types";

export type FaultLogProps = {
  /** The merged events, newest first. */
  events: readonly TelemetryEvent[];
  eventBaseline: number | null;
  missedEvents: number;
  /** False before the first telemetry frame: the card says nothing yet. */
  known: boolean;
};

const shownFirst = 5;

function Icon({ kind }: { kind: string }) {
  if (kind === "disconnect") {
    return (
      <svg width="17" height="17" viewBox="0 0 18 18" aria-hidden="true">
        <path
          d="M2.5 7.2a9.5 9.5 0 0 1 13 0M5 10a6 6 0 0 1 8 0M7.4 12.7a2.4 2.4 0 0 1 3.2 0M3 3l12 12"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.6"
          strokeLinecap="round"
        />
      </svg>
    );
  }
  return (
    <svg width="17" height="17" viewBox="0 0 18 18" aria-hidden="true">
      <path d="M9 2.5l7 12.2H2z" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinejoin="round" />
      <path d="M9 7.2v3.4M9 12.6v.1" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" />
    </svg>
  );
}

export const FaultLog = memo(function FaultLog({ events, eventBaseline, missedEvents, known }: FaultLogProps) {
  const [expanded, setExpanded] = useState(false);
  const c = copy.integrity;
  const newestResyncId = faultSummary(0, events).latest?.id ?? null;
  const fresh = newEntries(eventBaseline, events);
  const shown = expanded ? events : events.slice(0, shownFirst);

  return (
    <section className="mk-card mk-faults" aria-labelledby="faults-title">
      <h2 id="faults-title" className="mk-title">
        {c.logTitle}
      </h2>
      <p className="mk-lede">{c.logLede}</p>
      <p className="log-meta">
        {fresh > 0 ? c.logNew(fresh) : null}
        {fresh > 0 && missedEvents > 0 ? " " : null}
        {missedEvents > 0 ? c.logMissed(missedEvents) : null}
      </p>
      <div className="log-body">
        {!known || events.length === 0 ? (
          <p className="log-empty">{known ? c.logEmpty : ""}</p>
        ) : (
          <ul className="tl" id="fault-list">
            {shown.map((e) => {
              const said = describeFault(e, newestResyncId);
              return (
                <li key={e.id} data-kind={e.kind}>
                  <span className={`ic ${isResyncKind(e.kind) ? "ic-warn" : "ic-info"}`}>
                    <Icon kind={e.kind} />
                  </span>
                  <div>
                    <div className="what">{said.title}</div>
                    <div className="why">{said.detail}</div>
                  </div>
                  <time dateTime={new Date(e.atUnixMs).toISOString()}>{formatClock(e.atUnixMs)}</time>
                </li>
              );
            })}
          </ul>
        )}
      </div>
      {events.length > shownFirst ? (
        <button
          type="button"
          className="log-toggle"
          aria-expanded={expanded}
          aria-controls="fault-list"
          onClick={() => setExpanded((x) => !x)}
        >
          {expanded ? c.showFewer : c.showAll(events.length)}
        </button>
      ) : null}
    </section>
  );
});
