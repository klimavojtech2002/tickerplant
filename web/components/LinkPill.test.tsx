import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { connection, initialConnection, type ConnectionEvent, type ConnectionState } from "@/lib/connection";

import { LinkPill } from "./LinkPill";

const fold = (...events: ConnectionEvent[]): ConnectionState => events.reduce(connection, initialConnection);

describe("LinkPill", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(100_000);
  });
  afterEach(() => vi.useRealTimers());

  it("reads Connecting before the first open", () => {
    render(<LinkPill conn={initialConnection} />);
    expect(screen.getByText("Connecting…")).toBeInTheDocument();
  });

  it("reads Live as soon as the open event is processed, with no one-second wait", () => {
    render(<LinkPill conn={fold({ type: "open", now: 100_000 })} />);
    expect(screen.getByText("Live")).toBeInTheDocument(); // the clock has not ticked: still 0
    expect(screen.queryByText("Connecting…")).not.toBeInTheDocument();
  });

  it("announces only the state word: the age and the countdown stay outside the live region", () => {
    const conn = fold({ type: "open", now: 100_000 }, { type: "view", now: 100_000 });
    const { container } = render(<LinkPill conn={conn} />);
    act(() => vi.advanceTimersByTime(1000));
    const region = container.querySelector('[role="status"]');
    expect(region).toHaveTextContent("Live");
    expect(region?.textContent).toBe("Live"); // no age in it
    expect(region).toHaveAttribute("aria-live", "polite"); // announced when the state changes, politely
    expect(container.querySelector(".pill-age")).toHaveTextContent("· 1.0 s");
    expect(container.querySelector(".pill-age")?.closest('[role="status"]')).toBeNull();
  });

  it("turns to No data as the clock runs, and says how long", () => {
    const conn = fold({ type: "open", now: 100_000 }, { type: "view", now: 100_000 });
    const { container } = render(<LinkPill conn={conn} />);
    act(() => vi.advanceTimersByTime(4000)); // telemetry silent for 4 s
    expect(screen.getByText("No data")).toBeInTheDocument();
    expect(container.querySelector(".pill-age")).toHaveTextContent("4 s");
    expect(container.querySelector(".pill")).toHaveAttribute("data-kind", "no-data");
  });

  it("shows the countdown and the attempt while the stream is rebuilt", () => {
    const conn = fold({ type: "open", now: 100_000 }, { type: "closed", attempt: 1, retryAt: 104_000 });
    const { container } = render(<LinkPill conn={conn} />);
    act(() => vi.advanceTimersByTime(1000));
    expect(screen.getByText("Reconnecting")).toBeInTheDocument();
    expect(container.querySelector(".pill-age")).toHaveTextContent("in 3 s · attempt 1");
  });

  it("counts refused updates, in the singular and the plural", () => {
    const one = fold({ type: "open", now: 1 }, { type: "invalid" });
    const { rerender } = render(<LinkPill conn={one} />);
    expect(screen.getByText("1 update refused")).toBeInTheDocument();
    rerender(<LinkPill conn={connection(one, { type: "invalid" })} />);
    expect(screen.getByText("2 updates refused")).toBeInTheDocument();
  });

  it("shows nothing about refused updates when none were refused", () => {
    const { container } = render(<LinkPill conn={fold({ type: "open", now: 1 })} />);
    expect(container.querySelector(".pill-refused")).not.toBeInTheDocument();
  });

  it("owns its clock: it ticks once a second and clears its timers on unmount", () => {
    const { unmount } = render(<LinkPill conn={fold({ type: "open", now: 1 })} />);
    expect(vi.getTimerCount()).toBe(2);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
