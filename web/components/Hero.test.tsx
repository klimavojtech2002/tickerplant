import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { connection, initialConnection, type ConnectionEvent, type ConnectionState } from "@/lib/connection";
import { addPoint, emptyHistory, type History } from "@/lib/history";
import type { PollState } from "@/lib/status";
import { fromScaled } from "@/lib/ticks";
import type { Status, ViewEvent } from "@/lib/types";

import { Hero, type HeroProps } from "./Hero";

const fold = (...events: ConnectionEvent[]): ConnectionState => events.reduce(connection, initialConnection);

const view = (over: Partial<ViewEvent> = {}): ViewEvent => ({
  venue: "kraken",
  symbol: "BTC/USD",
  seq: 7,
  bids: [["62721.7", "1.35823999"]],
  asks: [["62721.8", "0.00114202"]],
  ...over,
});

const status = (over: Partial<Status> = {}): Status => ({
  schema: 1,
  mode: "live",
  chaos: false,
  version: "v1",
  venue: "kraken",
  symbol: "BTC/USD",
  startedAtUnixMs: 1,
  depth: 10,
  lastEventAgeMs: 0,
  delivery: { consumers: 1, delivered: 1, dropped: 0, published: 1 },
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
  ...over,
});

const ok = (over: Partial<Status> = {}): PollState => ({ kind: "ok", status: status(over), lastOkAt: 1 });

// n mids a second apart, rising 0.25 each: 100.00, 100.25, ...
const history = (n: number): History => {
  let h = emptyHistory;
  for (let i = 0; i < n; i++) {
    h = addPoint(h, i * 1000, fromScaled(10_000n + BigInt(i) * 25n, 2));
  }
  return h;
};

const open = fold({ type: "open", now: 100_000 }, { type: "view", now: 100_000 });

function props(over: Partial<HeroProps> = {}): HeroProps {
  return {
    view: view(),
    history: emptyHistory,
    conn: open,
    poll: ok(),
    frozenAt: null,
    lastViewAt: 100_000,
    onTogglePause: () => {},
    ...over,
  };
}

describe("Hero", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(100_000);
  });
  afterEach(() => vi.useRealTimers());

  it("before the first book shows a placeholder, the app name as the heading, and a disabled Pause", () => {
    render(<Hero {...props({ view: null, conn: initialConnection, poll: { kind: "loading", failures: 0 } })} />);
    expect(screen.getByRole("heading", { level: 1, name: "tickerplant" })).toBeInTheDocument();
    expect(screen.getByText("Waiting for the first book…")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /pause/i })).toBeDisabled();
    expect(document.querySelector(".hero-skeleton")).toHaveAttribute("aria-busy", "true");
  });

  it("shows the instrument, the exact mid, and the best bid and ask with their sizes and the spread in ticks", () => {
    render(<Hero {...props()} />);
    expect(screen.getByRole("heading", { level: 1, name: "BTC/USD" })).toBeInTheDocument();
    expect(screen.getByText("kraken")).toBeInTheDocument();
    expect(document.querySelector('[data-field="mid"]')).toHaveTextContent("62,721.75");
    expect(document.querySelector('[data-field="bid"]')).toHaveTextContent("62,721.7");
    expect(document.querySelector('[data-field="ask"]')).toHaveTextContent("62,721.8");
    expect(document.querySelector('[data-field="spread"]')).toHaveTextContent("0.1");
    expect(screen.getByText("1.35823999 BTC")).toBeInTheDocument();
    expect(screen.getByText("0.00114202 BTC")).toBeInTheDocument();
    expect(screen.getByText("1 tick")).toBeInTheDocument();
    expect(screen.getByText("Mid price")).toBeInTheDocument();
  });

  it("puts the fraction of a figure in its own lighter element and groups only the whole part", () => {
    render(<Hero {...props()} />);
    const mid = document.querySelector('[data-field="mid"]')!;
    expect(mid.querySelector(".frac")).toHaveTextContent(".75");
    expect(mid.textContent!.replaceAll(",", "")).toBe("62721.75"); // nothing lost
  });

  it("names a symbol without a base currency by its sizes alone", () => {
    render(<Hero {...props({ view: view({ symbol: "DEMO" }) })} />);
    expect(screen.getByText("1.35823999")).toBeInTheDocument();
    expect(screen.queryByText(/BTC/)).not.toBeInTheDocument();
  });

  it("shows a dash and says why when one side of the book is empty", () => {
    render(<Hero {...props({ view: view({ asks: [] }) })} />);
    expect(document.querySelector('[data-field="mid"]')).toHaveTextContent("—");
    expect(document.querySelector('[data-field="ask"]')).toHaveTextContent("—");
    expect(document.querySelector('[data-field="spread"]')).toHaveTextContent("—");
    expect(screen.getByText("One side of the book is empty, so there is no mid price.")).toBeInTheDocument();
  });

  describe("the source chip", () => {
    it.each([
      [ok({ mode: "synthetic" }), "Demo feed"],
      [ok({ mode: "synthetic", chaos: true }), "Demo feed · faults injected"],
      [{ kind: "loading", failures: 0 } as PollState, "Source not confirmed"],
      [ok({ mode: "unknown" }), "Source not confirmed"],
    ])("reads %#", (poll, text) => {
      render(<Hero {...props({ poll })} />);
      expect(screen.getByText(text)).toBeVisible();
    });

    it("is absent for a live feed", () => {
      render(<Hero {...props({ poll: ok({ mode: "live" }) })} />);
      expect(document.querySelector(".chip")).not.toBeVisible();
    });
  });

  describe("a connection that has gone quiet", () => {
    const quiet = fold(
      { type: "open", now: 90_000 },
      { type: "view", now: 90_000 },
      { type: "telemetry", now: 90_000 },
    );

    it("dims the numbers and says they are the last book received, and when", () => {
      render(<Hero {...props({ conn: quiet, lastViewAt: 90_000 })} />);
      act(() => vi.advanceTimersByTime(1000)); // the hero's clock ticks: 10 s without telemetry
      expect(screen.getByText("The connection was lost.")).toBeInTheDocument();
      expect(screen.getByText(/last book received, at \d\d:\d\d:\d\d/)).toBeInTheDocument();
      expect(document.querySelector(".hero")).toHaveClass("hero-dim");
    });

    it("is not dimmed while the connection is healthy", () => {
      render(<Hero {...props()} />);
      act(() => vi.advanceTimersByTime(1000));
      expect(document.querySelector(".hero")).not.toHaveClass("hero-dim");
      expect(screen.queryByText("The connection was lost.")).not.toBeInTheDocument();
    });
  });

  describe("Pause", () => {
    it("asks the page to pause or resume when pressed, and says which it will do", async () => {
      vi.useRealTimers();
      const onTogglePause = vi.fn();
      const user = userEvent.setup();
      const { rerender } = render(<Hero {...props({ onTogglePause })} />);
      await user.click(screen.getByRole("button", { name: "Pause" }));
      expect(onTogglePause).toHaveBeenCalledTimes(1);
      rerender(<Hero {...props({ onTogglePause, frozenAt: 100_000 })} />);
      expect(screen.getByRole("button", { name: "Resume" })).toHaveAttribute("aria-pressed", "true");
    });

    it("shows when the frozen book was received, and that the connection keeps updating", () => {
      render(<Hero {...props({ frozenAt: new Date(2026, 9, 24, 12, 4, 11).getTime() })} />);
      expect(screen.getByText("Paused. The book on screen was received at 12:04:11.")).toBeInTheDocument();
      expect(screen.getByText(/connection and the counters keep updating/)).toBeInTheDocument();
      expect(document.querySelector(".hero")).toHaveClass("hero-dim");
    });

    it("shows the pause notice rather than the lost-connection notice when both apply", () => {
      const quiet = fold(
        { type: "open", now: 90_000 },
        { type: "view", now: 90_000 },
        { type: "telemetry", now: 90_000 },
      );
      render(<Hero {...props({ conn: quiet, frozenAt: 90_000, lastViewAt: 90_000 })} />);
      act(() => vi.advanceTimersByTime(1000));
      expect(screen.getByText(/^Paused\./)).toBeInTheDocument();
      expect(screen.queryByText("The connection was lost.")).not.toBeInTheDocument();
    });
  });

  describe("the mid-price line", () => {
    it("says it is collecting until there are enough points", () => {
      render(<Hero {...props({ history: history(5) })} />);
      expect(screen.getByText("Collecting…")).toBeInTheDocument();
      expect(screen.queryByRole("img")).not.toBeInTheDocument();
    });

    it("draws the line, states its real span and the exact change, once there are enough points", () => {
      render(<Hero {...props({ history: history(21) })} />);
      expect(screen.queryByText("Collecting…")).not.toBeInTheDocument();
      expect(screen.getByRole("img", { name: "Mid price over the last 20 s, +5.00" })).toBeInTheDocument();
      expect(screen.getByText("20 s ago")).toBeInTheDocument();
      expect(screen.getByText("+5.00 in 20 s")).toBeInTheDocument();
      expect(screen.getByText("now")).toBeInTheDocument();
    });

    it("has no line and no caption before the first book", () => {
      render(<Hero {...props({ view: null, history: emptyHistory })} />);
      expect(screen.queryByRole("img")).not.toBeInTheDocument();
      expect(screen.queryByText("Collecting…")).not.toBeInTheDocument();
    });
  });

  it("never puts a style attribute in its markup", () => {
    const { container } = render(<Hero {...props({ history: history(21) })} />);
    expect(container.querySelectorAll("[style]")).toHaveLength(0);
  });
});
