// The page wraps the book and the health section in separate error boundaries, keyed on the
// current view and the poll state's kind. These tests replace the two panels with ones that
// throw on demand, so they check the wiring of those keys and not the panels themselves.

import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { FakeEventSource } from "@/lib/testing/fakeEventSource";

import Page from "./page";

vi.mock("@/components/BookPanel", () => ({
  BookPanel: ({ view }: { view: { venue: string; seq: number } }) => {
    if (view.venue === "bad") {
      throw new Error("book failed");
    }
    return <p>book seq {view.seq}</p>;
  },
}));

vi.mock("@/components/HealthPanel", () => ({
  HealthSection: ({ state }: { state: { kind: string } }) => {
    if (state.kind === "loading") {
      throw new Error("health failed");
    }
    return <p>health {state.kind}</p>;
  },
}));

const statusBody = JSON.stringify({
  schema: 1,
  mode: "synthetic",
  version: "dev",
  venue: "synthetic",
  symbol: "DEMO",
  startedAtUnixMs: 1,
  depth: 10,
  lastEventAgeMs: 5,
  delivery: { consumers: 1, delivered: 10, dropped: 2, published: 12 },
  engine: {
    gaps: 0,
    resyncs: 0,
    disconnects: 0,
    wouldCrosses: 0,
    checksumMismatches: 0,
    snapshotRejects: 0,
    snapshotErrors: 0,
    rejected: 0,
  },
});

const view = (venue: string, seq: number) =>
  JSON.stringify({ venue, symbol: "BTC/USD", seq, bids: [["62721.7", "1.0"]], asks: [["62721.8", "1.0"]] });

describe("Page error boundaries", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.reset();
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(statusBody, { status: 200 })),
    );
    vi.spyOn(console, "error").mockImplementation(() => {}); // React reports the caught errors
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("a good view with the same seq as the bad one clears the book's fallback", async () => {
    render(<Page />);
    await act(async () => {
      FakeEventSource.last().open();
      FakeEventSource.last().emit("view", view("bad", 7));
      await vi.advanceTimersByTimeAsync(10);
    });
    // The poll has answered by now, so only the book's boundary is showing its fallback.
    expect(screen.getAllByText("This panel failed to render.")).toHaveLength(1);
    await act(async () => {
      FakeEventSource.last().emit("view", view("kraken", 7)); // same seq, a different view object
    });
    expect(screen.queryByText("This panel failed to render.")).toBeNull();
    expect(screen.getByText("book seq 7")).toBeDefined();
  });

  it("the health section retries when the poll state changes kind", async () => {
    render(<Page />);
    // Before the first answer the state is loading, which the mock health section cannot render.
    expect(screen.getAllByText("This panel failed to render.")).toHaveLength(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10); // the status poll answers: loading -> ok
    });
    expect(screen.getByText("health ok")).toBeDefined();
  });
});
