"use client";

// StatusBadge names the connection state in words, with a dot for glance value: state
// is never colour alone. The words come from connectionLabel, which tells a quiet market
// (telemetry fresh, no view for a while) from a dead pipe (no telemetry). The badge owns
// its one-second clock so the page does not re-render for it.

import { connectionLabel, type ConnectionState } from "@/lib/connection";
import { useNow } from "@/lib/useNow";

export function StatusBadge({ conn }: { conn: ConnectionState }) {
  const now = useNow(1000);
  const label = connectionLabel(conn, now);
  return (
    <span className="badge-group">
      <span className={`badge badge-${label.kind}`}>
        <span className="badge-dot" aria-hidden />
        {label.text}
      </span>
      {conn.dropped > 0 ? (
        <span className="badge-dropped">
          {conn.dropped} {conn.dropped === 1 ? "frame" : "frames"} dropped
        </span>
      ) : null}
    </span>
  );
}
