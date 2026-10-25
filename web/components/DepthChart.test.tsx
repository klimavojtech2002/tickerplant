import { fireEvent, render, screen } from "@testing-library/react";
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

  it("does not draw a book whose prices are of two scales, rather than draw wrong positions", () => {
    const { container } = render(<DepthChart view={{ ...view, asks: [["62721.90", "1.0"]] }} />);
    expect(screen.getByText(/Depth cannot be drawn/)).toBeInTheDocument();
    expect(container.querySelector("svg")).toBeNull();
  });

  it("says level in the singular for a book with one level a side", () => {
    render(<DepthChart view={{ ...view, bids: [["62721.7", "1.0"]], asks: [["62721.9", "1.0"]] }} />);
    expect(screen.getByText("Cumulative size resting at each price, top 1 level, in BTC.")).toBeInTheDocument();
  });

  it("keeps the numbers around the picture out of the accessibility tree, since the order book has them", () => {
    const { container } = render(<DepthChart view={view} />);
    for (const sel of [".chart-midchip", ".chart-axis"]) {
      expect(container.querySelector(sel)).toHaveAttribute("aria-hidden", "true");
    }
    for (const label of container.querySelectorAll(".chart-ylab")) {
      expect(label).toHaveAttribute("aria-hidden", "true");
    }
  });

  it("keeps the mid marker inside the plot with a clamp on its position", () => {
    const { container } = render(<DepthChart view={view} />);
    expect(container.querySelector<HTMLElement>(".chart-midchip")!.style.left).toContain("clamp(3.25rem");
  });

  it("sizes the label column to its longest label", () => {
    const { container } = render(
      <DepthChart view={{ ...view, bids: [["62721.7", "0.0000001"]], asks: [["62721.9", "0.0000001"]] }} />,
    );
    // the axis tops at 1e-7, so a quarter is 0.000000025: 11 characters
    expect(container.querySelector<HTMLElement>(".chart")!.style.getPropertyValue("--ylab")).toBe("11ch");
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

  describe("the tooltip", () => {
    // jsdom has no layout: give the plot a box of 100 by 100 pixels at the origin
    function plot(container: HTMLElement) {
      const el = container.querySelector<HTMLElement>(".chart-plot")!;
      el.getBoundingClientRect = () => ({
        left: 0,
        top: 0,
        width: 100,
        height: 100,
        right: 100,
        bottom: 100,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      });
      return el;
    }
    const move = (el: HTMLElement, clientX: number, pointerType = "mouse") =>
      fireEvent.pointerMove(el, { clientX, clientY: 10, pointerType, pointerId: 1 });

    it("shows nothing until the pointer is over the plot", () => {
      const { container } = render(<DepthChart view={view} />);
      expect(container.querySelector(".chart-tip")).toBeNull();
      expect(container.querySelector("line.chart-hover")).toBeNull();
    });

    it("names the level under the mouse and the exact size resting up to it", () => {
      const { container } = render(<DepthChart view={view} />);
      move(plot(container), 100); // the far right: the highest ask
      const tip = container.querySelector(".chart-tip")!;
      expect(tip.querySelector("b")).toHaveTextContent(/^62725\.5$/);
      expect(tip).toHaveTextContent("3.0 BTC in asks up to here"); // 2.0 + 1.0
      expect(container.querySelector("line.chart-hover")).toBeInTheDocument();
      move(plot(container), 0); // the far left: the lowest bid
      expect(container.querySelector(".chart-tip b")).toHaveTextContent(/^62718\.5$/);
      expect(container.querySelector(".chart-tip")).toHaveTextContent("2.0 BTC in bids down to here");
    });

    it("leaves the currency out when the symbol has none", () => {
      const { container } = render(<DepthChart view={{ ...view, symbol: "DEMO" }} />);
      move(plot(container), 100);
      expect(container.querySelector(".chart-tip")).toHaveTextContent(/^62725\.5\s*3\.0 in asks up to here$/);
      move(plot(container), 0);
      expect(container.querySelector(".chart-tip")).toHaveTextContent(/2\.0 in bids down to here$/);
    });

    it("works with a pen and ignores a finger", () => {
      const { container } = render(<DepthChart view={view} />);
      move(plot(container), 100, "touch");
      expect(container.querySelector(".chart-tip")).toBeNull();
      move(plot(container), 100, "pen");
      expect(container.querySelector(".chart-tip")).toBeInTheDocument();
    });

    it("goes away when the pointer leaves or the gesture is cancelled", () => {
      const { container } = render(<DepthChart view={view} />);
      const el = plot(container);
      move(el, 100);
      fireEvent.pointerLeave(el);
      expect(container.querySelector(".chart-tip")).toBeNull();
      move(el, 100);
      fireEvent.pointerCancel(el);
      expect(container.querySelector(".chart-tip")).toBeNull();
    });

    it("keeps pointing at a level that is still there when a new book arrives, and never at one that is gone", () => {
      const { container, rerender } = render(<DepthChart view={view} />);
      move(plot(container), 100);
      expect(container.querySelector(".chart-tip b")).toHaveTextContent("62725.5");
      rerender(<DepthChart view={{ ...view, asks: [["62721.9", "2.0"]] }} />); // the far ask is gone
      expect(container.querySelector(".chart-tip b")).toHaveTextContent("62721.9");
    });

    it("measures the pointer from the left edge of the plot, wherever the plot is on the page", () => {
      const { container } = render(<DepthChart view={view} />);
      const el = container.querySelector<HTMLElement>(".chart-plot")!;
      el.getBoundingClientRect = () => ({
        left: 200,
        top: 0,
        width: 100,
        height: 100,
        right: 300,
        bottom: 100,
        x: 200,
        y: 0,
        toJSON: () => ({}),
      });
      move(el, 205); // 5 % across: the lowest bid
      expect(container.querySelector(".chart-tip b")).toHaveTextContent("62718.5");
      move(el, 295); // 95 % across: the highest ask
      expect(container.querySelector(".chart-tip b")).toHaveTextContent("62725.5");
    });

    it("does nothing for a plot with no width", () => {
      const { container } = render(<DepthChart view={view} />);
      const el = container.querySelector<HTMLElement>(".chart-plot")!;
      el.getBoundingClientRect = () => ({
        left: 0,
        top: 0,
        width: 0,
        height: 0,
        right: 0,
        bottom: 0,
        x: 0,
        y: 0,
        toJSON: () => ({}),
      });
      move(el, 10);
      expect(container.querySelector(".chart-tip")).toBeNull();
    });

    it("leaves the tooltip's words out of the picture, which keeps its one-line summary", () => {
      const { container } = render(<DepthChart view={view} />);
      move(plot(container), 100);
      expect(container.querySelector("svg")!.getAttribute("aria-label")).toMatch(/^Cumulative depth/);
      expect(container.querySelector("svg .chart-tip")).toBeNull();
    });
  });
});
