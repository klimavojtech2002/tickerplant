import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { PollState } from "@/lib/status";
import type { Status, ViewEvent } from "@/lib/types";

import { BookPanel } from "./BookPanel";
import { HealthPanel, tileID } from "./HealthPanel";
import { statusLabel } from "./StatusBadge";

const view: ViewEvent = {
  venue: "kraken",
  symbol: "BTC/USD",
  seq: 42,
  bids: [
    ["62721.7", "1.35823999"],
    ["62721.6", "0.50000000"],
  ],
  asks: [
    ["62721.8", "0.00114202"],
    ["62721.9", "2.00000000"],
  ],
};

describe("BookPanel", () => {
  it("prints the venue's decimal strings verbatim — no float round-trip artifacts", () => {
    render(<BookPanel view={view} />);
    // "0.50000000" is the float detector: a parseFloat->String path renders it
    // "0.5", dropping the venue's trailing zeros. The others pin verbatim output.
    expect(screen.getByText("0.50000000")).toBeDefined();
    expect(screen.getByText("2.00000000")).toBeDefined();
    expect(screen.getByText("1.35823999")).toBeDefined();
    expect(screen.getAllByText("62721.7").length).toBeGreaterThan(0); // best bid tile + row
  });

  it("derives the spread exactly and sizes bars as integer shares", () => {
    const { container } = render(<BookPanel view={view} />);
    expect(screen.getByText("0.1")).toBeDefined(); // 62721.8 - 62721.7
    const bidBars = container.querySelectorAll(".bar-bids");
    expect((bidBars[0] as HTMLElement).style.width).toBe("100%"); // 1.35823999 is the side's max
    expect((bidBars[1] as HTMLElement).style.width).toBe("36%"); // floor(0.5*100/1.35823999)
  });

  it("renders an empty side as dashes, not a crash", () => {
    render(<BookPanel view={{ ...view, bids: [] }} />);
    // best bid and spread both dash out when a side is empty
    expect(screen.getAllByText("—")).toHaveLength(2);
  });
});

describe("HealthPanel", () => {
  // Every counter is a different number, so a tile wired to the wrong field shows up.
  const status: Status = {
    schema: 1,
    mode: "live",
    version: "v1.2.3",
    venue: "kraken",
    symbol: "BTC/USD",
    startedAtUnixMs: 1_000,
    depth: 10,
    lastEventAgeMs: 1500,
    delivery: { consumers: 11, delivered: 12, dropped: 13, published: 14 },
    engine: {
      gaps: 21,
      resyncs: 22,
      disconnects: 23,
      wouldCrosses: 24,
      checksumMismatches: 25,
      snapshotRejects: 26,
      snapshotErrors: 27,
      rejected: 28,
    },
    latency: { count: 100, p50Ns: 1986, p99Ns: 9_900_000, p999Ns: 15_600_000, maxNs: 17_000_000 },
  };
  const ok: PollState = { kind: "ok", status, lastOkAt: 5_000 };

  /** The value shown in the tile with this label. */
  function tile(label: string): string {
    return within(screen.getByTestId(tileID(label))).getByText((_, el) =>
      el?.className === "tile-value",
    ).textContent as string;
  }

  it("shows every counter and quantile in its own labelled tile", () => {
    render(<HealthPanel state={ok} now={6_000} />);
    const want: [string, string][] = [
      ["delivered", "12"],
      ["dropped", "13"],
      ["consumers", "11"],
      ["published", "14"],
      ["gaps", "21"],
      ["resyncs", "22"],
      ["disconnects", "23"],
      ["would-cross", "24"],
      ["checksum drift", "25"],
      ["snapshot rejects", "26"],
      ["snapshot errors", "27"],
      ["rejected", "28"],
      ["last event", "1.5 s"],
      ["count", "100"],
      ["p50", "1.9 µs"],
      ["p99", "9.9 ms"],
      ["p99.9", "15.6 ms"],
      ["max", "17.0 ms"],
    ];
    for (const [label, value] of want) {
      expect(tile(label), label).toBe(value);
    }
    expect(screen.getByText("live · kraken · BTC/USD · v1.2.3")).toBeDefined();
  });

  it("gives every tile its own test id", () => {
    const { container } = render(<HealthPanel state={ok} now={0} />);
    const ids = [...container.querySelectorAll("[data-testid]")].map((e) => e.getAttribute("data-testid"));
    expect(ids).toHaveLength(18);
    expect(new Set(ids).size).toBe(18);
    expect(tileID("p99.9")).toBe("tile-p99-9");
  });

  it("says no event yet for the sentinel, and leaves the latency block out when there is none", () => {
    const quiet: Status = { ...status, lastEventAgeMs: -1, latency: undefined };
    render(<HealthPanel state={{ kind: "ok", status: quiet, lastOkAt: 1 }} now={0} />);
    expect(tile("last event")).toBe("no event yet");
    expect(screen.queryByTestId("tile-p50")).toBeNull();
  });

  it("keeps the numbers, unflagged, while a poll or two fail", () => {
    const { container } = render(
      <HealthPanel state={{ kind: "degraded", status, failures: 2, lastOkAt: 5_000 }} now={9_000} />,
    );
    expect(tile("delivered")).toBe("12");
    expect(container.querySelector("[data-state]")?.getAttribute("data-state")).toBe("degraded");
    expect(container.querySelector(".health-dimmed")).toBeNull();
    expect(screen.queryByText(/Unavailable/)).toBeNull();
  });

  it("dims the numbers and says how stale they are once the endpoint is unavailable", () => {
    const state: PollState = { kind: "unavailable", status, failures: 3, lastOkAt: 5_000 };
    const { container } = render(<HealthPanel state={state} now={17_900} />);
    const section = container.querySelector("section");
    expect(section?.getAttribute("data-state")).toBe("unavailable");
    expect(section?.className).toBe("health-dimmed");
    expect(screen.getByText("Unavailable · last updated 12 s ago")).toBeDefined();
    expect(tile("delivered")).toBe("12"); // still shown, never hidden
  });

  it("never shows a negative age when the clocks disagree, and none before the first tick", () => {
    const state: PollState = { kind: "unavailable", status, failures: 3, lastOkAt: 9_000 };
    const { rerender } = render(<HealthPanel state={state} now={1_000} />);
    expect(screen.getByText("Unavailable · last updated 0 s ago")).toBeDefined();
    rerender(<HealthPanel state={state} now={0} />);
    expect(screen.getByText("Unavailable")).toBeDefined();
  });

  it("says plainly that nothing was ever received", () => {
    const state: PollState = { kind: "unavailable", status: null, failures: 3, lastOkAt: null };
    const { container } = render(<HealthPanel state={state} now={50_000} />);
    expect(screen.getByText("Unavailable")).toBeDefined();
    expect(container.querySelector("section")?.getAttribute("data-state")).toBe("unavailable");
    expect(screen.queryByTestId("tile-delivered")).toBeNull();
  });

  it("says loading while the first polls are still out", () => {
    render(<HealthPanel state={{ kind: "loading", failures: 2 }} now={0} />);
    expect(screen.getByText(/loading status/)).toBeDefined();
  });

  it("renders a hostile venue name as text, not markup", () => {
    const hostile: Status = { ...status, venue: "<img src=x onerror=alert(1)>" };
    const { container } = render(<HealthPanel state={{ kind: "ok", status: hostile, lastOkAt: 1 }} now={0} />);
    expect(container.querySelector("img")).toBeNull();
    expect(screen.getByText(/<img src=x onerror=alert\(1\)>/)).toBeDefined();
  });
});

describe("statusLabel", () => {
  it("degrades live to stalled when views stop arriving", () => {
    expect(statusLabel("live", 1000, 5000)).toBe("live");
    expect(statusLabel("live", 1000, 12_000)).toBe("stalled");
    expect(statusLabel("live", 0, 60_000)).toBe("live"); // no view yet is not a stall
    expect(statusLabel("retrying", 1000, 60_000)).toBe("retrying");
  });
});
