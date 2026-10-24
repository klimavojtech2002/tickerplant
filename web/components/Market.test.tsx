import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { ViewEvent } from "@/lib/types";

import { Market } from "./Market";

const view: ViewEvent = {
  venue: "kraken",
  symbol: "BTC/USD",
  seq: 1,
  bids: [["100.0", "1.0"]],
  asks: [["100.1", "1.0"]],
};

describe("Market", () => {
  it("waits for the first book in a card of the same height", () => {
    const { container } = render(<Market view={null} />);
    expect(screen.getByRole("heading", { level: 2, name: "Order book" })).toBeInTheDocument();
    expect(screen.getAllByText("Waiting for the first book…")).toHaveLength(2); // the depth card and the order book card
    expect(container.querySelector(".mk-body")).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("says how many levels there are, using the larger side", () => {
    render(
      <Market
        view={{
          ...view,
          bids: [
            ["100.0", "1.0"],
            ["99.9", "1.0"],
            ["99.8", "1.0"],
          ],
        }}
      />,
    );
    expect(screen.getByText("Top 3 levels. Prices and sizes are the exchange's exact text.")).toBeInTheDocument();
  });

  it("says so when neither side has a level", () => {
    render(<Market view={{ ...view, bids: [], asks: [] }} />);
    expect(screen.getAllByText("No levels on either side yet.")).toHaveLength(2);
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("has the depth chart and the order book as two labelled regions", () => {
    render(<Market view={view} />);
    expect(screen.getByRole("region", { name: "Order book" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: "Market depth" })).toBeInTheDocument();
  });

  it("keeps a depth card of the same kind before the first book", () => {
    const { container } = render(<Market view={null} />);
    expect(screen.getByRole("region", { name: "Market depth" })).toBeInTheDocument();
    expect(container.querySelector(".mk-depth .chart")).toBeInTheDocument();
    expect(container.querySelector("svg")).toBeNull();
  });

  it("never puts a style attribute in markup rendered without a book", () => {
    const { container } = render(<Market view={null} />);
    expect(container.querySelectorAll("[style]")).toHaveLength(0);
  });
});
