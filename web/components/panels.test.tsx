import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { Metrics, ViewEvent } from "@/lib/types";

import { BookPanel } from "./BookPanel";
import { HealthPanel } from "./HealthPanel";
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
  const metrics: Metrics = {
    venue: "kraken",
    symbol: "BTC/USD",
    delivery: { consumers: 1, delivered: 10, dropped: 2 },
    engine: { gaps: 3, resyncs: 4, disconnects: 5, wouldCrosses: 6, checksumMismatches: 7 },
    latency: { count: 100, p50: "1ns", p99: "9.9ms", p99_9: "15.6ms", max: "15.63ms" },
  };

  it("shows every counter and quantile it is given", () => {
    render(<HealthPanel metrics={metrics} />);
    for (const v of ["10", "2", "3", "4", "5", "6", "7", "100", "1ns", "9.9ms", "15.6ms"]) {
      expect(screen.getAllByText(v).length).toBeGreaterThan(0);
    }
  });

  it("says so when the endpoint is unreachable or still loading", () => {
    const { rerender } = render(<HealthPanel metrics={undefined} />);
    expect(screen.getByText(/loading/)).toBeDefined();
    rerender(<HealthPanel metrics={null} />);
    expect(screen.getByText(/unreachable/)).toBeDefined();
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
