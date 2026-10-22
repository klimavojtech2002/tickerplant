import { describe, expect, it } from "vitest";

import { connection, connectionLabel, initialConnection, type ConnectionState } from "./connection";

const st = (over: Partial<ConnectionState>): ConnectionState => ({ ...initialConnection, ...over });

describe("connection reducer", () => {
  it("opens: resets the attempt, the retry time and the view time, and remembers it opened", () => {
    const before = st({ link: "closed", attempt: 4, retryAt: 9000, viewAt: 555, beatAt: 100, everOpened: true });
    expect(connection(before, { type: "open", now: 7000 })).toEqual({
      link: "open",
      everOpened: true,
      attempt: 0,
      retryAt: null,
      openAt: 7000,
      beatAt: 7000,
      viewAt: 0,
      dropped: 0,
    });
  });

  it("remembers that a connection opened, so a later connecting reads Reconnecting", () => {
    const opened = connection(initialConnection, { type: "open", now: 5 });
    expect(opened.everOpened).toBe(true);
    const again = connection(opened, { type: "connecting" });
    expect(connectionLabel(again, 6)).toEqual({ kind: "reconnecting", text: "Reconnecting…" });
    expect(connectionLabel(connection(initialConnection, { type: "connecting" }), 6).kind).toBe("connecting");
  });

  it("closes with the attempt and the time of the rebuild", () => {
    const next = connection(st({ link: "open", openAt: 1, beatAt: 1 }), { type: "closed", attempt: 3, retryAt: 12_000 });
    expect(next).toMatchObject({ link: "closed", attempt: 3, retryAt: 12_000 });
  });

  it("keeps the attempt when a new source is created, but clears the retry time", () => {
    const next = connection(st({ link: "closed", attempt: 3, retryAt: 12_000 }), { type: "connecting" });
    expect(next).toMatchObject({ link: "connecting", attempt: 3, retryAt: null });
  });

  it("closed, then connecting, then closed again carries the new numbers", () => {
    let s = connection(initialConnection, { type: "closed", attempt: 1, retryAt: 1000 });
    s = connection(s, { type: "connecting" });
    expect(s.retryAt).toBeNull();
    s = connection(s, { type: "closed", attempt: 2, retryAt: 3000 });
    expect(s).toMatchObject({ link: "closed", attempt: 2, retryAt: 3000 });
  });

  it("a transient error clears the retry time, whatever the link was", () => {
    const next = connection(st({ link: "closed", attempt: 2, retryAt: 5000 }), { type: "transient" });
    expect(next).toMatchObject({ link: "transient", retryAt: null, attempt: 2 });
  });

  it("records the receipt times of telemetry and views", () => {
    const open = st({ link: "open", openAt: 10, beatAt: 10 });
    expect(connection(open, { type: "telemetry", now: 1234 }).beatAt).toBe(1234);
    expect(connection(open, { type: "view", now: 2345 }).viewAt).toBe(2345);
  });

  it("counts every refused frame", () => {
    let s = initialConnection;
    s = connection(s, { type: "invalid" });
    s = connection(s, { type: "invalid" });
    expect(s.dropped).toBe(2);
  });

  it("returns the same object when nothing changes", () => {
    expect(connection(initialConnection, { type: "connecting" })).toBe(initialConnection);
    const transient = connection(initialConnection, { type: "transient" });
    expect(connection(transient, { type: "transient" })).toBe(transient);
  });

  it("returns a new object for events that always change something", () => {
    const open = connection(initialConnection, { type: "open", now: 5 });
    expect(connection(open, { type: "telemetry", now: 5 })).not.toBe(open);
    expect(connection(open, { type: "view", now: 5 })).not.toBe(open);
    expect(connection(open, { type: "invalid" })).not.toBe(open);
  });

  it("does not change the input", () => {
    const s = st({ link: "open", openAt: 1, beatAt: 1 });
    const copy = { ...s };
    connection(s, { type: "closed", attempt: 1, retryAt: 9 });
    expect(s).toEqual(copy);
  });
});

describe("connectionLabel", () => {
  const open = (over: Partial<ConnectionState> = {}) =>
    st({ link: "open", everOpened: true, openAt: 1000, beatAt: 1000, ...over });

  const rows: [string, ConnectionState, number, string, string][] = [
    ["first connect", initialConnection, 0, "connecting", "Connecting…"],
    ["first connect, any clock", initialConnection, 99_999, "connecting", "Connecting…"],
    ["reconnecting after it had opened", st({ link: "connecting", everOpened: true }), 5000, "reconnecting", "Reconnecting…"],
    ["browser reconnecting by itself", st({ link: "transient", everOpened: true, beatAt: 1 }), 90_000, "transient", "Reconnecting…"],
    ["transient before the first open", st({ link: "transient" }), 5000, "transient", "Reconnecting…"],
    ["closed, 4 s to go", st({ link: "closed", attempt: 3, retryAt: 14_000 }), 10_000, "reconnecting", "Reconnecting in 4 s (attempt 3)"],
    ["closed, a fraction of a second to go", st({ link: "closed", attempt: 1, retryAt: 10_001 }), 10_000, "reconnecting", "Reconnecting in 1 s (attempt 1)"],
    ["closed, due now", st({ link: "closed", attempt: 2, retryAt: 10_000 }), 10_000, "reconnecting", "Reconnecting in 0 s (attempt 2)"],
    ["closed, overdue", st({ link: "closed", attempt: 2, retryAt: 9000 }), 10_000, "reconnecting", "Reconnecting in 0 s (attempt 2)"],
    ["closed, clock not ticked yet", st({ link: "closed", attempt: 1, retryAt: 1_700_000_000_000 }), 0, "reconnecting", "Reconnecting in 30 s (attempt 1)"],
    ["open, no view yet, just opened", open(), 1000, "live", "Live"],
    ["open, no view yet, 9999 ms of telemetry only", open({ beatAt: 10_999 }), 10_999, "live", "Live"],
    ["open, no view yet, 10000 ms of telemetry only", open({ beatAt: 11_000 }), 11_000, "quiet", "Live · market quiet"],
    ["open, view 200 ms ago", open({ viewAt: 4800, beatAt: 4900 }), 5000, "live", "Live · 200 ms"],
    ["open, view 1.5 s ago", open({ viewAt: 3500, beatAt: 4900 }), 5000, "live", "Live · 1.5 s"],
    ["open, view 9999 ms ago", open({ viewAt: 1001, beatAt: 10_900 }), 11_000, "live", "Live · 9.9 s"],
    ["open, view 10000 ms ago, telemetry fresh", open({ viewAt: 1000, beatAt: 10_900 }), 11_000, "quiet", "Live · market quiet"],
    ["open, telemetry exactly 3000 ms old", open({ viewAt: 4000, beatAt: 2000 }), 5000, "live", "Live · 1.0 s"],
    ["open, telemetry 3001 ms old", open({ viewAt: 4000, beatAt: 1999 }), 5000, "no-data", "No data from edge · 3 s"],
    ["open, telemetry 4 s old while views continue", open({ viewAt: 4990, beatAt: 1000 }), 5000, "no-data", "No data from edge · 4 s"],
    ["open, views stopped while telemetry continues", open({ viewAt: 1100, beatAt: 14_900 }), 15_000, "quiet", "Live · market quiet"],
    ["open, an event stamped after the clock reads 0 ms", open({ viewAt: 6000, beatAt: 6000 }), 5000, "live", "Live · 0 ms"],
  ];
  it.each(rows)("%s", (_name, state, now, kind, text) => {
    expect(connectionLabel(state, now)).toEqual({ kind, text });
  });

  it("an open link whose clock has not ticked reads Live, not Connecting", () => {
    expect(connectionLabel(open({ viewAt: 5000, beatAt: 5000, openAt: 5000 }), 0)).toEqual({ kind: "live", text: "Live · 0 ms" });
    expect(connectionLabel(open({ openAt: 5000, beatAt: 5000 }), 0)).toEqual({ kind: "live", text: "Live" });
  });
});
