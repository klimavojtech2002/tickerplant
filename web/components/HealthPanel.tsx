// HealthPanel shows /metrics as stat tiles — counters and latency quantiles are
// headline numbers, not a chart. An unreachable endpoint says so; it never shows
// stale numbers as if they were live.

import type { Metrics } from "@/lib/types";

export function HealthPanel({ metrics }: { metrics: Metrics | null | undefined }) {
  if (metrics === undefined) {
    return <section aria-label="health">loading metrics…</section>;
  }
  if (metrics === null) {
    return <section aria-label="health">/metrics unreachable</section>;
  }
  const e = metrics.engine;
  const d = metrics.delivery;
  const l = metrics.latency;
  return (
    <section aria-label="health">
      <div className="tiles">
        <Stat label="delivered" value={d.delivered} />
        <Stat label="dropped" value={d.dropped} />
        <Stat label="consumers" value={d.consumers} />
        <Stat label="gaps" value={e.gaps} />
        <Stat label="resyncs" value={e.resyncs} />
        <Stat label="disconnects" value={e.disconnects} />
        <Stat label="would-cross" value={e.wouldCrosses} />
        <Stat label="cksum drift" value={e.checksumMismatches} />
      </div>
      {l ? (
        <div className="tiles">
          <Stat label="latency n" value={l.count} />
          <Stat label="p50" value={l.p50} />
          <Stat label="p99" value={l.p99} />
          <Stat label="p99.9" value={l.p99_9} />
          <Stat label="max" value={l.max} />
        </div>
      ) : null}
    </section>
  );
}

function Stat({ label, value }: { label: string; value: number | string }) {
  return (
    <div className="tile">
      <div className="tile-label">{label}</div>
      <div className="tile-value">{String(value)}</div>
    </div>
  );
}
