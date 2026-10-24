import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { mid } from "@/lib/mid";
import type { ViewEvent } from "@/lib/types";

import { DepthChart } from "./DepthChart";

// DepthChart works out the mid once per render, so counting its calls counts renders.
vi.mock("@/lib/mid", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/mid")>();
  return { ...actual, mid: vi.fn(actual.mid) };
});

const view: ViewEvent = {
  venue: "kraken",
  symbol: "BTC/USD",
  seq: 1,
  bids: [
    ["62721.7", "1.0"],
    ["62718.5", "1.0"],
  ],
  asks: [
    ["62721.9", "2.0"],
    ["62725.5", "1.0"],
  ],
};

describe("DepthChart", () => {
  it("is a labelled image with the best prices and the number of levels, pointing to the order book for the numbers", () => {
    render(<DepthChart view={view} />);
    expect(
      screen.getByRole("img", {
        name: "Cumulative depth: best bid 62721.7, best ask 62721.9, up to 2 levels a side. The order book has the exact numbers.",
      }),
    ).toBeInTheDocument();
  });

  it("says what it shows, in the base currency of the symbol", () => {
    render(<DepthChart view={view} />);
    expect(screen.getByText("Cumulative size resting at each price, top 2 levels, in BTC.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Market depth" })).toBeInTheDocument();
  });

  it("leaves the currency out when the symbol has none", () => {
    render(<DepthChart view={{ ...view, symbol: "DEMO" }} />);
    expect(screen.getByText("Cumulative size resting at each price, top 2 levels.")).toBeInTheDocument();
  });

  it("draws a line and an area for each side", () => {
    const { container } = render(<DepthChart view={view} />);
    expect(container.querySelectorAll("path.chart-line-bid")).toHaveLength(1);
    expect(container.querySelectorAll("path.chart-line-ask")).toHaveLength(1);
    expect(container.querySelectorAll("svg path")).toHaveLength(4);
    for (const line of container.querySelectorAll("path.chart-line")) {
      expect(line.getAttribute("d")).toMatch(/^M [\d.]+ [\d.]+( L [\d.]+ [\d.]+)+$/);
    }
  });

  it("labels the size axis with exact decimals of a round top, and the price axis with its two ends", () => {
    const { container } = render(<DepthChart view={view} />);
    // the largest cumulative size is 3.0, so the axis tops at 5: a quarter step is 1.25
    expect([...container.querySelectorAll(".chart-ylab")].map((e) => e.textContent)).toEqual([
      "1.25",
      "2.5",
      "3.75",
      "5",
    ]);
    const ends = [...container.querySelectorAll(".chart-axis span")].map((e) => e.textContent);
    expect(ends).toEqual(["62,718.5", "62,725.5"]);
  });

  it("marks the mid price with its exact value", () => {
    const { container } = render(<DepthChart view={view} />);
    expect(container.querySelector(".chart-midchip")).toHaveTextContent("62,721.8");
    expect(container.querySelector("line.chart-mid")).toBeInTheDocument();
  });

  it("draws no mid price when the two best prices cannot be averaged exactly, but still draws the sides", () => {
    const { container } = render(<DepthChart view={{ ...view, asks: [["62721.90", "1.0"]] }} />);
    expect(container.querySelector(".chart-midchip")).toBeNull();
    expect(container.querySelector("path.chart-line-ask")).toBeInTheDocument();
  });

  it("names a book with no bids by its asks alone", () => {
    render(<DepthChart view={{ ...view, bids: [] }} />);
    expect(screen.getByRole("img", { name: /best bid none, best ask 62721.9/ })).toBeInTheDocument();
  });

  it("draws the one side that is there, with a note and no mid", () => {
    const { container } = render(<DepthChart view={{ ...view, asks: [] }} />);
    expect(screen.getByText("Only one side of the book has levels.")).toBeInTheDocument();
    expect(container.querySelector("path.chart-line-ask")).toBeNull();
    expect(container.querySelector(".chart-midchip")).toBeNull();
    expect(container.querySelector("line.chart-mid")).toBeNull();
  });

  it("says it cannot draw a book whose sizes are not positive, and draws nothing", () => {
    const { container } = render(<DepthChart view={{ ...view, bids: [["62721.7", "0.0"]] }} />);
    expect(
      screen.getByText("Depth cannot be drawn for this book. The order book has the numbers."),
    ).toBeInTheDocument();
    expect(container.querySelector("svg")).toBeNull();
  });

  it("keeps the legend out of the accessibility tree, since the colours are named by the order book's headers", () => {
    const { container } = render(<DepthChart view={view} />);
    expect(container.querySelector(".legend")).toHaveAttribute("aria-hidden", "true");
  });

  it("does not render again for the same view, only for a new one", () => {
    const renders = () => vi.mocked(mid).mock.calls.length;
    const start = renders();
    const { rerender } = render(<DepthChart view={view} />);
    expect(renders()).toBe(start + 1);
    rerender(<DepthChart view={view} />); // a telemetry-only change: the page re-renders, the view is the same object
    expect(renders()).toBe(start + 1);
    rerender(<DepthChart view={{ ...view, seq: 2 }} />);
    expect(renders()).toBe(start + 2);
  });

  it("puts no style attribute inside the svg", () => {
    const { container } = render(<DepthChart view={view} />);
    expect(container.querySelectorAll("svg [style]")).toHaveLength(0);
  });
});
