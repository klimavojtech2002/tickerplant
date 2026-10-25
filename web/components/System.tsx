"use client";

// The system card: what the server says about itself on its status page. The numbers come from a poll, not the stream, so a card
// that cannot reach the server says so plainly and keeps the last numbers it had, with the time it received them. Nothing here is
// compared with or summed into anything else: delivery counters are per viewer and processing times are estimates.

import { memo } from "react";

import { copy } from "@/lib/copy";
import { formatAgeMs, formatLatency } from "@/lib/format";
import { groupThousands } from "@/lib/group";
import { formatClock } from "@/lib/hero";
import type { PollState } from "@/lib/status";
import { describeStart, latencyBars, resolution, slowest, systemState } from "@/lib/system";
import type { Status } from "@/lib/types";

const count = (n: number): string => groupThousands(String(n));

function Facts({ status }: { status: Status }) {
  const c = copy.system;
  const started = describeStart(status.startedAtUnixMs);
  return (
    <div className="rows">
      {started ? (
        <div className="row" data-row="started">
          <span className="row-k">{c.started}</span>
          <span className="row-v row-plain">{started}</span>
        </div>
      ) : null}
      <div className="row" data-row="lastEvent">
        <span className="row-k">{c.lastEvent}</span>
        <span className="row-v row-plain">
          {status.lastEventAgeMs < 0 ? c.lastEventNone : c.lastEventAge(formatAgeMs(status.lastEventAgeMs))}
        </span>
      </div>
    </div>
  );
}

function Delivery({ status }: { status: Status }) {
  const c = copy.system;
  const values: Record<string, number> = { ...status.delivery };
  return (
    <div>
      <h3 className="sys-h">{c.delivery}</h3>
      <div className="rows">
        {(["consumers", "published", "delivered", "dropped"] as const).map((id) => {
          const [label, definition] = c.rows[id]!;
          return (
            <div className="row" key={id} data-row={id}>
              <span className="row-k">
                {label}
                <small>{definition}</small>
              </span>
              <span className="row-v">{count(values[id]!)}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

function Processing({ latency }: { latency: NonNullable<Status["latency"]> }) {
  const c = copy.system;
  const bars = latencyBars(latency);
  const values = { p50: latency.p50Ns, p99: latency.p99Ns, p999: latency.p999Ns, max: latency.maxNs };
  return (
    <div>
      <h3 className="sys-h">{c.processing}</h3>
      <div className="rows">
        {c.latencyRows.map(([id, label, definition]) => (
          <div className="p-row" key={id} data-row={id}>
            <span className="row-k">
              {label}
              <small>{definition}</small>
            </span>
            <span className="p-track" aria-hidden="true">
              <i style={{ width: `${bars[id]}%` }} />
            </span>
            <span className="row-v" data-latency={id}>
              {formatLatency(values[id])}
            </span>
          </div>
        ))}
        <div className="row" data-row="measured">
          <span className="row-k">{c.measured}</span>
          <span className="row-v" data-field="measured">
            {count(latency.count)}
          </span>
        </div>
      </div>
      {slowest(latency) >= resolution ? <p className="sys-note">{c.scale(formatLatency(slowest(latency)))}</p> : null}
      <p className="sys-note">{c.caption}</p>
    </div>
  );
}

export const System = memo(function System({ poll, edgeURL }: { poll: PollState; edgeURL: string }) {
  const c = copy.system;
  const state = systemState(poll);
  const status = state.kind === "ok" || state.kind === "stale" ? state.status : null;

  return (
    <section className="mk-card mk-system" aria-labelledby="system-title">
      <h2 id="system-title" className="mk-title">
        {state.kind === "gone" ? c.goneTitle : c.title}
      </h2>
      {status ? (
        <p className="mk-lede">{`${c.modes[status.mode] ?? c.modes.unknown} · ${status.venue} · ${status.symbol}`}</p>
      ) : state.kind === "gone" ? null : (
        <p className="mk-lede">{c.checking}</p>
      )}
      {/* The region is always in the page, empty when there is nothing to say, so that what appears in it is announced. */}
      <div role="status" className="sys-status">
        {state.kind === "stale" ? <p className="stale-line">{c.staleLine(formatClock(state.lastOkAt))}</p> : null}
        {state.kind === "gone" ? <p className="mk-lede">{c.goneBody(edgeURL)}</p> : null}
      </div>
      {status ? (
        <>
          <Facts status={status} />
          <div className="sys2">
            <Delivery status={status} />
            {status.latency ? (
              <Processing latency={status.latency} />
            ) : (
              <div>
                <h3 className="sys-h">{c.processing}</h3>
                <p className="sys-note">{c.noLatency}</p>
              </div>
            )}
          </div>
        </>
      ) : null}
    </section>
  );
});
