import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { StrictMode } from "react";
import { renderToString } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { FakeEventSource } from "@/lib/testing/fakeEventSource";

import Page from "./page";
import { at } from "@/lib/testing/at";

const statusBody = JSON.stringify({
  schema: 1,
  mode: "synthetic",
  chaos: false,
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

const view = JSON.stringify({
  venue: "kraken",
  symbol: "BTC/USD",
  seq: 7,
  bids: [["62721.7", "1.35823999"]],
  asks: [["62721.8", "0.00114202"]],
});

const telemetry = JSON.stringify({
  t: 1,
  seq: 7,
  gaps: 0,
  resyncs: 0,
  disconnects: 0,
  wouldCrosses: 0,
  checksumMismatches: 0,
  snapshotRejects: 0,
  snapshotErrors: 0,
  rejected: 0,
  consumers: 1,
  events: [],
});

describe("Page under React Strict Mode", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    FakeEventSource.reset();
    vi.stubGlobal("EventSource", FakeEventSource);
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(statusBody, { status: 200 })),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("leaves exactly one live connection after the double mount, and ignores the first one", async () => {
    render(
      <StrictMode>
        <Page />
      </StrictMode>,
    );
    const open = FakeEventSource.instances.filter((es) => es.close.mock.calls.length === 0);
    expect(FakeEventSource.instances.length).toBeGreaterThanOrEqual(2); // mounted, cleaned up, mounted
    expect(open).toHaveLength(1);
    const live = at(open, 0);
    const discarded = at(FakeEventSource.instances, 0);
    expect(discarded).not.toBe(live);

    await act(async () => {
      live.open();
      live.emit("telemetry", telemetry);
      live.emit("view", view);
      discarded.emit("view", view.replace('"seq":7', '"seq":1').replace("62721.7", "1.0"));
      await vi.advanceTimersByTimeAsync(10);
    });
    expect(screen.getByRole("heading", { level: 1, name: "BTC/USD" })).toBeInTheDocument();
    expect(screen.getByText("kraken")).toBeInTheDocument();
    expect(screen.getByText("Live")).toBeInTheDocument(); // the pill reads Live from the events
    expect(document.querySelector(".pill-age")?.textContent).toMatch(/^· \d+ ms$/);
    expect(screen.getAllByText("62721.7").length).toBeGreaterThan(0); // the live view, not the stale one
    expect(screen.queryByText("1.0")).toBeNull();
  });

  it("tells a silent pipe from a quiet market using the telemetry events it receives", async () => {
    render(<Page />);
    const es = FakeEventSource.last();
    await act(async () => {
      es.open();
      es.emit("telemetry", telemetry);
      es.emit("view", view);
      await vi.advanceTimersByTimeAsync(2500);
    });
    await act(async () => {
      es.emit("telemetry", telemetry); // a beat at 2.5 s keeps the pipe alive...
      await vi.advanceTimersByTimeAsync(2500); // ...though no view arrives for 5 s
    });
    expect(screen.getByText("Live")).toBeInTheDocument();
    expect(document.querySelector(".pill-age")?.textContent).toMatch(/^· 5\.\d s$/);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1500); // 4 s since the last beat; the pill's clock last ticked at 3.5 s
    });
    expect(screen.getByText("No data")).toBeInTheDocument();
    expect(document.querySelector(".pill-age")?.textContent).toBe("· 3 s");
  });

  it("renders the same first frame on the server as the first client render", () => {
    const html = renderToString(<Page />);
    expect(html).toContain("Connecting…");
    expect(html).toContain("Waiting for the first book…");
    expect(html).toContain("loading status…");
    expect(html).not.toContain("Live");
  });

  it("shows the health tiles once the status poll answers", async () => {
    render(
      <StrictMode>
        <Page />
      </StrictMode>,
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10);
    });
    expect(screen.getByTestId("tile-delivered").textContent).toContain("10");
  });

  it("closes the connection and leaves no timer behind when it unmounts", async () => {
    const { unmount } = render(
      <StrictMode>
        <Page />
      </StrictMode>,
    );
    const live = FakeEventSource.instances.filter((es) => es.close.mock.calls.length === 0);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10); // the status poll settles
    });
    unmount();
    expect(at(live, 0).close).toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("recovers from a refusal without a reload", async () => {
    render(
      <StrictMode>
        <Page />
      </StrictMode>,
    );
    const before = FakeEventSource.instances.length;
    await act(async () => {
      FakeEventSource.last().fail();
      await vi.advanceTimersByTimeAsync(31_000);
    });
    expect(FakeEventSource.instances.length).toBe(before + 1);
  });

  describe("Pause", () => {
    const viewAt = (bid: string, ask: string, seq: number) =>
      JSON.stringify({ venue: "kraken", symbol: "BTC/USD", seq, bids: [[bid, "1"]], asks: [[ask, "1"]] });
    const mid = () => document.querySelector('[data-field="mid"]')?.textContent;

    it("freezes the hero and the order book at the book on screen, keeps taking views, and Resume shows the newest", async () => {
      render(<Page />);
      const es = FakeEventSource.last();
      await act(async () => {
        es.open();
        es.emit("telemetry", telemetry);
        es.emit("view", viewAt("100.0", "100.2", 1));
        await vi.advanceTimersByTimeAsync(10);
      });
      expect(mid()).toBe("100.10");

      fireEvent.click(screen.getByRole("button", { name: "Pause" }));
      expect(screen.getByRole("button", { name: "Resume" })).toBeInTheDocument();

      await act(async () => {
        es.emit("view", viewAt("200.0", "200.4", 2)); // a new book arrives while paused
        es.emit("telemetry", telemetry);
        await vi.advanceTimersByTimeAsync(1000);
      });
      expect(mid()).toBe("100.10"); // the hero did not move
      expect(within(screen.getByRole("table", { name: "bids" })).getByText("100.0")).toBeInTheDocument(); // nor did the order book
      expect(within(screen.getByRole("table", { name: "bids" })).queryByText("200.0")).not.toBeInTheDocument();
      expect(screen.getByText(/^Paused\./)).toBeInTheDocument();
      expect(screen.getByText("Live")).toBeInTheDocument(); // the connection state is still live

      fireEvent.click(screen.getByRole("button", { name: "Resume" }));
      expect(mid()).toBe("200.20"); // the page shows the present at once
      expect(screen.queryByText(/^Paused\./)).not.toBeInTheDocument();
    });

    it("builds the mid line from the views, and starts it over when the connection is rebuilt", async () => {
      render(<Page />);
      const es = FakeEventSource.last();
      await act(async () => {
        es.open();
        for (let i = 0; i < 12; i++) {
          es.emit("view", viewAt("100.0", "100.2", i));
          await vi.advanceTimersByTimeAsync(600);
        }
      });
      expect(screen.getByRole("img", { name: /^Mid price, .* of history/ })).toBeInTheDocument();
      await act(async () => {
        es.fail(); // the connection is lost
        await vi.advanceTimersByTimeAsync(10);
      });
      expect(screen.queryByRole("img", { name: /^Mid price, .* of history/ })).not.toBeInTheDocument();
    });
  });
});
