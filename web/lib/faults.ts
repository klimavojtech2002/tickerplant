// The fault log and the numbers the integrity card is allowed to claim, from the engine's telemetry. The engine keeps its last 16
// fault events and resends all of them once a second, so the page merges frames by event id instead of trusting any one frame.
//
// A "fault" here is one that made the engine rebuild the book: a gap, a delta that would cross the book, checksum drift or a
// disconnect. That is exactly what `resyncs` counts, and it counts a rebuild when it starts. A rebuild that fails ends the engine and
// the server with it, so on a server that is still publishing every counted rebuild has finished except one that is running now. The
// event of the newest rebuild carries a duration of -1 until it finishes. Snapshot problems are in the log but are not rebuilds.

import { copy } from "./copy";
import type { Telemetry, TelemetryEvent } from "./types";

/** How many events the engine keeps, and so how many the page merges. */
export const ringSize = 16;

const resyncKinds = new Set(["gap", "cross", "checksum", "disconnect"]);

/** isResyncKind is true for the kinds of fault that start a rebuild of the book. */
export const isResyncKind = (kind: string): boolean => resyncKinds.has(kind);

/**
 * mergeEvents merges the events of a new frame into those the page knows, by id: a later copy of an event replaces the earlier one
 * (its duration may have been filled in), the newest event comes first, and only the newest 16 are kept. `lost` is how many events
 * happened between the frames and were never seen: the frames overlap, so a gap between the oldest event of the new frame and the
 * newest known one is the only way to lose any. A page with no events yet has lost none.
 */
export function mergeEvents(
  known: readonly TelemetryEvent[],
  incoming: readonly TelemetryEvent[],
): { events: TelemetryEvent[]; lost: number } {
  const byId = new Map<number, TelemetryEvent>();
  for (const e of known) {
    byId.set(e.id, e);
  }
  for (const e of incoming) {
    byId.set(e.id, e);
  }
  const events = [...byId.values()].sort((a, b) => b.id - a.id).slice(0, ringSize);
  let lost = 0;
  if (known.length > 0 && incoming.length > 0) {
    const knownNewest = Math.max(...known.map((e) => e.id));
    const incomingOldest = Math.min(...incoming.map((e) => e.id));
    lost = Math.max(0, incomingOldest - (knownNewest + 1));
  }
  return { events, lost };
}

const counters = [
  "resyncs",
  "gaps",
  "disconnects",
  "wouldCrosses",
  "checksumMismatches",
  "snapshotRejects",
  "snapshotErrors",
  "rejected",
] as const;

/**
 * restarted tells whether a frame must come from a server that started again, so that what the page remembers of the old one is
 * dropped: its newest event id went back, or any counter that only ever rises fell. The frame's own clock is not used: it is the
 * server's wall clock, which can step back without a restart.
 */
export function restarted(previous: Telemetry | null, knownNewestId: number, next: Telemetry): boolean {
  if (previous === null) {
    return false;
  }
  const newestId = next.events.reduce((m, e) => Math.max(m, e.id), 0);
  if (newestId < knownNewestId) {
    return true;
  }
  return counters.some((c) => next[c] < previous[c]);
}

/** The newest event of a kind that rebuilds the book, from a list kept newest first. */
function newestResync(events: readonly TelemetryEvent[]): TelemetryEvent | null {
  return events.find((e) => isResyncKind(e.kind)) ?? null;
}

export type FaultSummary = {
  /** Rebuilds that have finished. Within one frame of the truth: see the note on frames in ADR-0028. */
  repaired: number;
  /** True while the newest rebuild has not reported its duration. */
  inProgress: boolean;
  /** The newest event that started a rebuild, or null. */
  latest: TelemetryEvent | null;
};

/**
 * faultSummary is what the headline may say. `events` is newest first. The rebuild in progress is the newest event of a rebuild kind
 * with no duration yet, not the newest event: while it retries, the engine records snapshot problems after it.
 */
export function faultSummary(resyncs: number, events: readonly TelemetryEvent[]): FaultSummary {
  const latest = newestResync(events);
  const inProgress = latest !== null && latest.durationMs === -1;
  return { repaired: Math.max(0, resyncs - (inProgress ? 1 : 0)), inProgress, latest };
}

/** newEntries is how many events the server has recorded since the page's first frame, which is exact because ids rise by one. */
export function newEntries(baselineNewestId: number | null, events: readonly TelemetryEvent[]): number {
  if (baselineNewestId === null || events.length === 0) {
    return 0;
  }
  return Math.max(0, events[0]!.id - baselineNewestId);
}

export type FaultText = {
  title: string;
  /** One or two plain sentences. */
  detail: string;
  /** The clause that says how the rebuild went, for the headline's latest line. */
  rebuild: string | null;
};

const capital = (s: string): string => (s === "" ? s : s[0]!.toUpperCase() + s.slice(1));

/**
 * describeFault words one event. `newestResyncId` is the id of the newest rebuild event: only that one can still be running, so only that
 * one reads "Rebuilding…"; an older one without a duration reads that its time was not recorded.
 */
export function describeFault(e: TelemetryEvent, newestResyncId: number | null): FaultText {
  const c = copy.faults;
  const title = c.titles[e.kind] ?? c.unknownTitle;
  let what: string;
  switch (e.kind) {
    case "gap":
      what = e.expectedSeq > 0 && e.gotSeq > 0 ? c.gapDetail(e.expectedSeq, e.gotSeq) : c.gapPlain;
      break;
    case "cross":
      what = c.crossDetail;
      break;
    case "checksum":
      what = c.checksumDetail;
      break;
    case "disconnect":
      what = c.disconnectDetail;
      break;
    default:
      what = `${capital(e.reason)}.`; // the server's own fixed wording
  }
  if (!isResyncKind(e.kind)) {
    return { title, detail: what, rebuild: null };
  }
  let rebuild: string;
  if (e.durationMs === -1) {
    rebuild = e.id === newestResyncId ? c.rebuilding : c.rebuildNotRecorded;
  } else if (e.durationMs === 0) {
    rebuild = c.rebuiltUnderOne;
  } else {
    rebuild = c.rebuiltIn(e.durationMs);
  }
  const end = rebuild.endsWith("…") ? "" : ".";
  return { title, detail: `${what} ${capital(rebuild)}${end}`, rebuild };
}
