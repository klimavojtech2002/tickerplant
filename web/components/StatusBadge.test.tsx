import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { connection, initialConnection, type ConnectionEvent, type ConnectionState } from "@/lib/connection";

import { StatusBadge } from "./StatusBadge";

const fold = (...events: ConnectionEvent[]): ConnectionState => events.reduce(connection, initialConnection);

describe("StatusBadge", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(100_000);
  });
  afterEach(() => vi.useRealTimers());

  it("reads Connecting before the first open", () => {
    render(<StatusBadge conn={initialConnection} />);
    expect(screen.getByText("Connecting…")).toBeDefined();
  });

  it("reads Live as soon as the open event is processed, with no one-second wait", () => {
    const conn = fold({ type: "open", now: 100_000 });
    render(<StatusBadge conn={conn} />);
    expect(screen.getByText("Live")).toBeDefined(); // the clock has not ticked: still 0
    expect(screen.queryByText("Connecting…")).toBeNull();
  });

  it("shows the age of the last view and turns quiet then no-data as the clock runs", () => {
    const conn = fold({ type: "open", now: 100_000 }, { type: "view", now: 100_000 });
    const { container } = render(<StatusBadge conn={conn} />);
    act(() => vi.advanceTimersByTime(1000)); // clock reads 101_000
    expect(screen.getByText("Live · 1.0 s")).toBeDefined();
    act(() => vi.advanceTimersByTime(3000)); // 104_000: telemetry silent for 4 s
    expect(screen.getByText("No data from edge · 4 s")).toBeDefined();
    expect(container.querySelector(".badge-no-data")).not.toBeNull();
  });

  it("counts frames dropped, in the singular and the plural", () => {
    const one = fold({ type: "open", now: 1 }, { type: "invalid" });
    const { rerender } = render(<StatusBadge conn={one} />);
    expect(screen.getByText("1 frame dropped")).toBeDefined();
    rerender(<StatusBadge conn={connection(one, { type: "invalid" })} />);
    expect(screen.getByText("2 frames dropped")).toBeDefined();
  });

  it("shows no dropped text when nothing was dropped", () => {
    const { container } = render(<StatusBadge conn={fold({ type: "open", now: 1 })} />);
    expect(container.querySelector(".badge-dropped")).toBeNull();
  });

  it("owns its clock: it ticks once a second and clears its timers on unmount", () => {
    const { unmount } = render(<StatusBadge conn={fold({ type: "open", now: 1 })} />);
    expect(vi.getTimerCount()).toBe(2);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
