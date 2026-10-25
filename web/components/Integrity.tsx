"use client";

// The integrity card: how many faults made the engine rebuild the book and how the latest went, what this page itself found when it
// checked every update it received for a crossed book, and the engine's counters. The page's own check and the server's counter are
// two sentences on purpose: they are measured in different places and never merged into one claim. The counters are separate rows,
// because they do not add up (a checksum venue adds drift to rebuilds).

import { memo } from "react";

import { connectionPill, type ConnectionState } from "@/lib/connection";
import { copy } from "@/lib/copy";
import { describeFault, faultSummary } from "@/lib/faults";
import { groupThousands } from "@/lib/group";
import { formatClock } from "@/lib/hero";
import type { Telemetry, TelemetryEvent } from "@/lib/types";
import { useNow } from "@/lib/useNow";
import { useThrottled } from "@/lib/useThrottled";

export type IntegrityProps = {
  telemetry: Telemetry | null;
  /** The merged fault events, newest first. */
  events: readonly TelemetryEvent[];
  viewsChecked: number;
  viewsRefused: number;
  conn: ConnectionState;
};

const count = (n: number): string => groupThousands(String(n));

const mainRows = ["resyncs", "gaps", "disconnects", "wouldCrosses"] as const;
const moreRows = ["checksumMismatches", "snapshotRejects", "snapshotErrors", "rejected"] as const;

function Row({ id, value }: { id: string; value: number }) {
  const [label, definition] = copy.integrity.rows[id]!;
  return (
    <div className="row" data-row={id}>
      <span className="row-k">
        {label}
        <small>{definition}</small>
      </span>
      <span className="row-v">{count(value)}</span>
    </div>
  );
}

export const Integrity = memo(function Integrity({
  telemetry,
  events,
  viewsChecked,
  viewsRefused,
  conn,
}: IntegrityProps) {
  const now = useNow(1000);
  const kind = connectionPill(conn, now).kind;
  const stale = kind === "no-data" || kind === "reconnecting" || kind === "transient";
  // the page's own counters move with every update; they reach the screen a few times a second
  const checked = useThrottled(viewsChecked, 250);
  const refused = useThrottled(viewsRefused, 250);
  const c = copy.integrity;

  if (telemetry === null) {
    return (
      <section className="mk-card mk-integrity" aria-labelledby="integrity-title">
        <h2 id="integrity-title" className="visually-hidden">
          {c.title}
        </h2>
        <div className="claim claim-idle">
          <p className="claim-head">{c.waiting}</p>
        </div>
      </section>
    );
  }

  const { repaired, inProgress, latest } = faultSummary(telemetry.resyncs, events);
  const headline = repaired > 0 ? c.repaired(count(repaired), repaired) : inProgress ? c.firstInProgress : c.noRebuilds;
  const said = latest ? describeFault(latest, latest.id) : null;
  const latestLine =
    latest && said ? c.latest(said.title, formatClock(latest.atUnixMs), said.rebuild ?? "") : c.untouched;

  let check: { state: "ok" | "refused" | "idle"; text: string };
  if (refused > 0) {
    check = { state: "refused", text: c.checkRefused(count(checked), count(refused)) };
  } else if (checked === 0) {
    check = { state: "idle", text: c.checkNone };
  } else if (conn.link !== "open") {
    check = { state: "idle", text: c.checkClosed(count(checked)) };
  } else {
    check = { state: "ok", text: c.checkOk(count(checked)) };
  }

  return (
    <section className={`mk-card mk-integrity${stale ? " is-stale" : ""}`} aria-labelledby="integrity-title">
      <div className="claim">
        <span className={`seal${repaired > 0 ? "" : " seal-idle"}`} aria-hidden="true">
          <svg width="22" height="22" viewBox="0 0 22 22">
            <path
              d="M5 11.5l4 4 8-9"
              fill="none"
              stroke="currentColor"
              strokeWidth="2.4"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </span>
        <div>
          <h2 id="integrity-title" className="claim-head" data-field="repaired" data-repaired={repaired}>
            {headline}
          </h2>
          <p className="claim-latest">{latestLine}</p>
        </div>
      </div>
      {stale && conn.beatAt > 0 ? <p className="stale-line">{c.stale(formatClock(conn.beatAt))}</p> : null}

      <p className={`check-line check-${check.state}`} data-field="check">
        {check.text}
      </p>
      <p className="server-line">
        {telemetry.wouldCrosses === 0
          ? c.serverNone
          : c.serverSome(count(telemetry.wouldCrosses), telemetry.wouldCrosses)}
      </p>

      <p className="sub-head">{c.since}</p>
      <div className="rows">
        {mainRows.map((id) => (
          <Row key={id} id={id} value={telemetry[id]} />
        ))}
      </div>
      <div className="rows rows-muted">
        {moreRows.map((id) => (
          <Row key={id} id={id} value={telemetry[id]} />
        ))}
      </div>
    </section>
  );
});
