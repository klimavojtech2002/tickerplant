import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { gap } from "@/lib/ladder";
import type { ViewEvent } from "@/lib/types";

import { Ladder } from "./Ladder";

// Ladder calls gap once per render, so counting its calls counts renders.
vi.mock("@/lib/ladder", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/ladder")>();
  return { ...actual, gap: vi.fn(actual.gap) };
});

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

const prices = (body: HTMLElement) =>
  within(body)
    .getAllByRole("row")
    .map((r) => r.querySelector(".price")!.textContent);

describe("Ladder", () => {
  it("lists the asks farthest first, then the spread row, then the bids best first", () => {
    const { container } = render(<Ladder view={view} />);
    expect(prices(container.querySelector("tbody.asks")!)).toEqual(["62721.9", "62721.8"]);
    expect(prices(container.querySelector("tbody.bids")!)).toEqual(["62721.7", "62721.6"]);
    const order = [...container.querySelectorAll("tbody")].map((b) => b.className);
    expect(order).toEqual(["asks", "between", "bids"]);
  });

  it("prints every price exactly, with no separator, and a price cell holds nothing else", () => {
    render(<Ladder view={{ ...view, bids: [["1234567.5", "1.0"]], asks: [["1234567.8", "1.0"]] }} />);
    const cells = [...document.querySelectorAll("td.price")].map((c) => c.textContent);
    expect(cells).toEqual(["1234567.8", "1234567.5"]);
  });

  it("prints each size exactly, with the trailing zeros of the fraction in their own lighter element", () => {
    const { container } = render(<Ladder view={view} />);
    const sizes = [...container.querySelectorAll("td.size")].map((c) => c.textContent);
    expect(sizes).toEqual(["2.00000000", "0.00114202", "1.35823999", "0.50000000"]);
    const half = container.querySelectorAll("td.size")[3]!;
    expect(half.querySelector(".z")).toHaveTextContent("0000000");
    expect(half.firstChild).toHaveTextContent("0.5");
  });

  it("marks exactly the best bid and the best ask, and says so to a screen reader outside the price cell", () => {
    const { container } = render(<Ladder view={view} />);
    const best = [...container.querySelectorAll("[data-best]")];
    expect(best.map((r) => r.getAttribute("data-best"))).toEqual(["ask", "bid"]);
    expect(best[0]!.querySelector(".price")).toHaveTextContent(/^62721\.8$/);
    expect(best[1]!.querySelector(".price")).toHaveTextContent(/^62721\.7$/);
    expect(within(best[0] as HTMLElement).getByText("best ask")).toBeInTheDocument();
    expect(within(best[1] as HTMLElement).getByText("best bid")).toBeInTheDocument();
  });

  it("sizes the bars as whole percents of the largest size in the whole book", () => {
    const { container } = render(<Ladder view={view} />);
    const widths = [...container.querySelectorAll<HTMLElement>(".meter-bar")].map((b) => b.style.width);
    // 2.00000000 is the largest: asks 100% and 2% (the floor), bids 67% and 25%
    expect(widths).toEqual(["100%", "2%", "67%", "25%"]);
  });

  it("shows the exact spread and the mid between the sides", () => {
    render(<Ladder view={view} />);
    const row = document.querySelector(".ladder-gap")!;
    expect(row).toHaveTextContent("Spread 0.1 · Mid 62721.75");
  });

  it.each([
    ["one side empty", { ...view, asks: [] }],
    ["crossed", { ...view, bids: [["62722.0", "1"]] }],
    ["prices of different scales", { ...view, asks: [["62721.80", "1"]] }],
  ])("shows dashes in the spread row for a book that is %s", (_name, v) => {
    render(<Ladder view={v as ViewEvent} />);
    expect(document.querySelector(".ladder-gap")).toHaveTextContent("Spread — · Mid —");
  });

  it("names the table and its columns", () => {
    render(<Ladder view={view} />);
    expect(screen.getByRole("table", { name: "Order book" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Price" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "Size" })).toBeInTheDocument();
  });

  it("does not render again for the same view, only for a new one", () => {
    const renders = () => vi.mocked(gap).mock.calls.length;
    const start = renders();
    const { rerender } = render(<Ladder view={view} />);
    expect(renders()).toBe(start + 1);
    rerender(<Ladder view={view} />); // a telemetry-only change: the page re-renders, the view is the same object
    expect(renders()).toBe(start + 1);
    rerender(<Ladder view={{ ...view, seq: 43 }} />);
    expect(renders()).toBe(start + 2);
  });

  it("draws a side with no levels as an empty body, not a crash", () => {
    const { container } = render(<Ladder view={{ ...view, bids: [] }} />);
    expect(container.querySelectorAll("tbody.bids tr")).toHaveLength(0);
    expect(container.querySelectorAll("[data-best]")).toHaveLength(1);
  });
});
