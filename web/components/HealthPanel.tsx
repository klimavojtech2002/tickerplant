// HealthPanel shows /api/status as stat tiles — counters and latency quantiles are
// headline numbers, not a chart. When the endpoint stops answering the tiles are dimmed
// and say so; the panel never presents old numbers as live and never hides itself.

import { formatAgeMs, formatNs } from "@/lib/format";
import type { PollState } from "@/lib/status";
import type { Status } from "@/lib/types";

export function HealthPanel({ state, now }: { state: PollState; now: number }) {
  if (state.kind === "loading") {
    return <section aria-label="health">loading status…</section>;
  }
  const unavailable = state.kind === "unavailable";
  if (state.status === null) {
    return (
      <section aria-label="health" data-state="unavailable">
        Unavailable
      </section>
    );
  }
  const s = state.status;
  return (
    <section
      aria-label="health"
      data-state={state.kind}
      className={unavailable ? "health-dimmed" : undefined}
    >
      {unavailable ? <p>{unavailableText(state.lastOkAt, now)}</p> : null}
      <p className="identity">
        {s.mode} · {s.venue} · {s.symbol} · {s.version}
      </p>
      <div className="tiles">
        {counterTiles(s).map(([label, value]) => (
          <Stat key={label} label={label} value={value} />
        ))}
      </div>
      {s.latency ? (
        <div className="tiles">
          <Stat label="count" value={s.latency.count} />
          <Stat label="p50" value={formatNs(s.latency.p50Ns)} />
          <Stat label="p99" value={formatNs(s.latency.p99Ns)} />
          <Stat label="p99.9" value={formatNs(s.latency.p999Ns)} />
          <Stat label="max" value={formatNs(s.latency.maxNs)} />
        </div>
      ) : null}
    </section>
  );
}

/** "Unavailable", plus how long ago the numbers were fresh when that is known. */
function unavailableText(lastOkAt: number | null, now: number): string {
  if (lastOkAt === null || now === 0) {
    return "Unavailable";
  }
  const secs = Math.max(0, Math.floor((now - lastOkAt) / 1000));
  return `Unavailable · last updated ${secs} s ago`;
}

function counterTiles(s: Status): [string, number | string][] {
  return [
    ["delivered", s.delivery.delivered],
    ["dropped", s.delivery.dropped],
    ["consumers", s.delivery.consumers],
    ["published", s.delivery.published],
    ["gaps", s.engine.gaps],
    ["resyncs", s.engine.resyncs],
    ["disconnects", s.engine.disconnects],
    ["would-cross", s.engine.wouldCrosses],
    ["checksum drift", s.engine.checksumMismatches],
    ["snapshot rejects", s.engine.snapshotRejects],
    ["snapshot errors", s.engine.snapshotErrors],
    ["rejected", s.engine.rejected],
    ["last event", s.lastEventAgeMs === -1 ? "no event yet" : formatAgeMs(s.lastEventAgeMs)],
  ];
}

/** The test id for a label: lowercase, every run of other characters becomes "-". */
export function tileID(label: string): string {
  return `tile-${label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
}

function Stat({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="tile" data-testid={tileID(label)}>
      <div className="tile-label">{label}</div>
      <div className="tile-value">{String(value)}</div>
    </div>
  );
}
