// StatusBadge names the stream state in words, with a dot for glance value —
// state is never color-alone. "live" degrades to "stalled" when no view has
// arrived for a while, so a silently dead pipe cannot masquerade as healthy.

import type { StreamStatus } from "@/lib/store";

export const stalledAfterMs = 10_000;

export function statusLabel(status: StreamStatus, updatedAt: number, now: number): string {
  if (status === "live" && updatedAt > 0 && now - updatedAt > stalledAfterMs) {
    return "stalled";
  }
  return status;
}

export function StatusBadge({
  status,
  updatedAt,
  now,
}: {
  status: StreamStatus;
  updatedAt: number;
  now: number;
}) {
  const label = statusLabel(status, updatedAt, now);
  return (
    <span className={`badge badge-${label}`}>
      <span className="badge-dot" aria-hidden />
      {label}
    </span>
  );
}
