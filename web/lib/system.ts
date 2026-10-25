// What the system card shows, worked out from the status poll: which state the card is in, how long each processing-time bar is, and
// the words for the start time and the engine's last event. Every figure is a whole number; the bars use the same digit-string
// percent as the order book, so nothing here turns a measurement into a float.

import { barPct } from "./ticks";
import type { PollState } from "./status";
import type { Status } from "./types";

export type SystemState =
  | { kind: "loading" }
  /** The numbers are current, or one or two polls behind (a single miss is noise and shows nothing). */
  | { kind: "ok"; status: Status }
  /** The server stopped answering after having answered: the last numbers are shown, with when they were received. */
  | { kind: "stale"; status: Status; lastOkAt: number }
  /** The server has never answered. */
  | { kind: "gone" };

/** systemState reduces the poll to what the card has to tell apart. */
export function systemState(poll: PollState): SystemState {
  switch (poll.kind) {
    case "loading":
      return { kind: "loading" };
    case "ok":
    case "degraded":
      return { kind: "ok", status: poll.status };
    case "unavailable":
      return poll.status !== null && poll.lastOkAt !== null
        ? { kind: "stale", status: poll.status, lastOkAt: poll.lastOkAt }
        : { kind: "gone" };
  }
}

export type Latency = NonNullable<Status["latency"]>;

/** The shortest time the card tells apart from nothing, in nanoseconds. */
export const resolution = 1_000;

const resolved = (ns: number): number => (ns < resolution ? 0 : ns);

export type LatencyBars = { p50: number; p99: number; p999: number; max: number };

/**
 * latencyBars gives each value's bar as a whole percent of the largest of the four. The quantiles are estimates from histogram buckets
 * and can sit a little above the exact maximum, so the scale is the largest value, not the maximum: the longest bar is 100 and none
 * is above it. A value under a microsecond has no bar, and all four are 0 when none reaches one.
 */
export function latencyBars(l: Latency): LatencyBars {
  // Under a microsecond the measurement cannot tell one value from another (they read <1 µs), so it gets no bar at all.
  const values = [l.p50Ns, l.p99Ns, l.p999Ns, l.maxNs].map(resolved);
  const largest = values.reduce((a, b) => (b > a ? b : a), 0);
  const bar = (v: number): number => (largest === 0 ? 0 : barPct(String(v), String(largest)));
  return { p50: bar(values[0]!), p99: bar(values[1]!), p999: bar(values[2]!), max: bar(values[3]!) };
}

/** The largest of the four values, for the sentence that names the scale. */
export function slowest(l: Latency): number {
  return Math.max(l.p50Ns, l.p99Ns, l.p999Ns, l.maxNs);
}

const two = (n: number) => String(n).padStart(2, "0");
const months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

/**
 * describeStart words the time the server started in the visitor's zone: the time of day for today, with the date before it for any
 * other day. A start time of 0 (the edge was built without one) or one in the future (the clocks disagree) has nothing honest to
 * say, so it gives null.
 */
export function describeStart(startedAtUnixMs: number, now: Date = new Date()): string | null {
  if (startedAtUnixMs <= 0 || startedAtUnixMs > now.getTime()) {
    return null;
  }
  const d = new Date(startedAtUnixMs);
  const clock = `${two(d.getHours())}:${two(d.getMinutes())}:${two(d.getSeconds())}`;
  const today =
    d.getFullYear() === now.getFullYear() && d.getMonth() === now.getMonth() && d.getDate() === now.getDate();
  return today ? clock : `${d.getDate()} ${months[d.getMonth()]} ${clock}`;
}
