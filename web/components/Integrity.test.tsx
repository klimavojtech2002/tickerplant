import { act, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { formatClock } from "@/lib/hero";
import { connection, initialConnection, type ConnectionEvent, type ConnectionState } from "@/lib/connection";
import type { Telemetry, TelemetryEvent } from "@/lib/types";

import { Integrity, type IntegrityProps } from "./Integrity";

const fold = (...events: ConnectionEvent[]): ConnectionState => events.reduce(connection, initialConnection);
const open = fold({ type: "open", now: 100_000 }, { type: "view", now: 100_000 }, { type: "telemetry", now: 100_000 });

const ev = (id: number, over: Partial<TelemetryEvent> = {}): TelemetryEvent => ({
  id,
  kind: "gap",
  atUnixMs: new Date(2026, 9, 25, 12, 4, 11).getTime(),
  expectedSeq: 4182,
  gotSeq: 4185,
  reason: "sequence gap",
  durationMs: 38,
  ...over,
});

const telemetry = (over: Partial<Telemetry> = {}): Telemetry => ({
  t: 1,
  seq: 2384,
  gaps: 10,
  resyncs: 18,
  disconnects: 4,
  wouldCrosses: 4,
  checksumMismatches: 0,
  snapshotRejects: 0,
  snapshotErrors: 0,
  rejected: 0,
  consumers: 1,
  events: [],
  ...over,
});

function props(over: Partial<IntegrityProps> = {}): IntegrityProps {
  return { telemetry: telemetry(), events: [ev(18)], viewsChecked: 1908, viewsRefused: 0, conn: open, ...over };
}

describe("Integrity", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(100_000);
  });
  afterEach(() => vi.useRealTimers());

  it("waits for the first update from the server, in a card that is there already", () => {
    render(<Integrity {...props({ telemetry: null, events: [] })} />);
    expect(screen.getByText("Waiting for the first update from the server…")).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "Integrity" })).toBeInTheDocument();
  });

  describe("the headline", () => {
    it("says how many faults were repaired, with the latest and how long it took", () => {
      render(<Integrity {...props()} />);
      expect(screen.getByRole("heading", { level: 2, name: "18 faults repaired" })).toBeInTheDocument();
      expect(
        screen.getByText("Latest: sequence gap at 12:04:11, rebuilt from a snapshot in 38 ms."),
      ).toBeInTheDocument();
    });

    it("says fault in the singular", () => {
      render(<Integrity {...props({ telemetry: telemetry({ resyncs: 1 }) })} />);
      expect(screen.getByRole("heading", { name: "1 fault repaired" })).toBeInTheDocument();
    });

    it("does not say zero faults were repaired, and says the server has not had to rebuild", () => {
      render(<Integrity {...props({ telemetry: telemetry({ resyncs: 0 }), events: [] })} />);
      expect(screen.getByRole("heading", { name: "No rebuilds yet" })).toBeInTheDocument();
      expect(screen.getByText("The server has not had to rebuild the book since it started.")).toBeInTheDocument();
      expect(screen.queryByText(/0 faults repaired/)).not.toBeInTheDocument();
    });

    it("leaves a rebuild that is running out of the count and says so", () => {
      render(<Integrity {...props({ events: [ev(18, { durationMs: -1 })] })} />);
      expect(screen.getByRole("heading", { name: "17 faults repaired" })).toBeInTheDocument();
      expect(screen.getByText(/Latest: sequence gap at 12:04:11, rebuilding…$/)).toBeInTheDocument();
    });

    it("says the first rebuild is in progress when it is the only one", () => {
      render(<Integrity {...props({ telemetry: telemetry({ resyncs: 1 }), events: [ev(1, { durationMs: -1 })] })} />);
      expect(screen.getByRole("heading", { name: "The first rebuild is in progress" })).toBeInTheDocument();
    });

    it("finds the running rebuild behind snapshot problems recorded after it", () => {
      const events = [
        ev(20, { kind: "snapshot-error", durationMs: 0, reason: "snapshot failed" }),
        ev(19, { durationMs: -1 }),
      ];
      render(<Integrity {...props({ events })} />);
      expect(screen.getByRole("heading", { name: "17 faults repaired" })).toBeInTheDocument();
    });

    it("has a headline that is plain text, not a live region", () => {
      const { container } = render(<Integrity {...props()} />);
      expect(container.querySelector("[aria-live]")).toBeNull();
      expect(container.querySelector('[role="status"], [role="alert"]')).toBeNull();
    });
  });

  describe("the check this page made", () => {
    it("is green and says none crossed while the link is open and nothing was refused", () => {
      render(<Integrity {...props()} />);
      const line = document.querySelector('[data-field="check"]')!;
      expect(line).toHaveTextContent("On this page: 1,908 updates checked, none crossed.");
      expect(line).toHaveClass("check-ok");
    });

    it("is amber and counts the crossed updates once any was refused", () => {
      render(<Integrity {...props({ viewsRefused: 3 })} />);
      const line = document.querySelector('[data-field="check"]')!;
      expect(line).toHaveTextContent("On this page: 1,908 updates checked; 3 crossed and refused.");
      expect(line).toHaveClass("check-refused");
      expect(line).not.toHaveTextContent("none crossed");
    });

    it("makes no claim when the link is not open", () => {
      const closed = fold({ type: "open", now: 100_000 }, { type: "transient" });
      render(<Integrity {...props({ conn: closed })} />);
      const line = document.querySelector('[data-field="check"]')!;
      expect(line).toHaveTextContent("On this page: 1,908 updates checked so far.");
      expect(line).toHaveClass("check-idle");
    });

    it("says nothing has been checked before the first update", () => {
      render(<Integrity {...props({ viewsChecked: 0 })} />);
      expect(document.querySelector('[data-field="check"]')).toHaveTextContent("On this page: no updates checked yet.");
    });

    it("keeps its own number apart from the server's", () => {
      render(<Integrity {...props({ viewsChecked: 5, telemetry: telemetry({ wouldCrosses: 4 }) })} />);
      expect(document.querySelector('[data-field="check"]')).toHaveTextContent("5 updates checked");
      expect(
        screen.getByText("The server rejected 4 crossing updates before publishing any of them."),
      ).toBeInTheDocument();
    });

    it("shows the count a few times a second at most, and the latest at the end", () => {
      const { rerender } = render(<Integrity {...props({ viewsChecked: 10 })} />);
      rerender(<Integrity {...props({ viewsChecked: 11 })} />);
      expect(document.querySelector('[data-field="check"]')).toHaveTextContent(" 11 updates");
      rerender(<Integrity {...props({ viewsChecked: 12 })} />);
      rerender(<Integrity {...props({ viewsChecked: 13 })} />);
      expect(document.querySelector('[data-field="check"]')).toHaveTextContent(" 11 updates"); // held
      act(() => vi.advanceTimersByTime(250));
      expect(document.querySelector('[data-field="check"]')).toHaveTextContent(" 13 updates");
    });
  });

  describe("the server's line", () => {
    it.each([
      [0, "The server has rejected no crossing updates."],
      [1, "The server rejected 1 crossing update before publishing any of them."],
      [4, "The server rejected 4 crossing updates before publishing any of them."],
    ])("for %s", (n, text) => {
      render(<Integrity {...props({ telemetry: telemetry({ wouldCrosses: n }) })} />);
      expect(screen.getByText(text)).toBeInTheDocument();
    });
  });

  describe("the counters", () => {
    it("are separate rows, each with its own value and its own definition, since they do not add up", () => {
      const { container } = render(
        <Integrity
          {...props({
            telemetry: telemetry({
              resyncs: 18,
              gaps: 10,
              disconnects: 4,
              wouldCrosses: 3,
              checksumMismatches: 11,
              snapshotRejects: 12,
              snapshotErrors: 13,
              rejected: 14,
            }),
          })}
        />,
      );
      const value = (id: string) => container.querySelector(`[data-row="${id}"] .row-v`)!.textContent;
      expect(value("resyncs")).toBe("18");
      expect(value("gaps")).toBe("10");
      expect(value("disconnects")).toBe("4");
      expect(value("wouldCrosses")).toBe("3");
      expect(value("checksumMismatches")).toBe("11");
      expect(value("snapshotRejects")).toBe("12");
      expect(value("snapshotErrors")).toBe("13");
      expect(value("rejected")).toBe("14");
      expect(container.querySelectorAll(".row")).toHaveLength(8);
      expect(screen.getByText("Rebuilds from a snapshot")).toBeInTheDocument();
      expect(screen.getByText("Each repair rebuilt the book")).toBeInTheDocument();
      expect(screen.getByText("Since the server started")).toBeInTheDocument();
    });

    it("never claim to be a total or a part of one another", () => {
      const { container } = render(<Integrity {...props()} />);
      expect(container.textContent).not.toMatch(/\b(total|in all|of which|sum)\b/i);
    });

    it("group thousands in large counts", () => {
      render(<Integrity {...props({ telemetry: telemetry({ gaps: 12_345 }) })} />);
      expect(document.querySelector('[data-row="gaps"] .row-v')).toHaveTextContent("12,345");
    });
  });

  describe("a connection that has gone quiet", () => {
    const quiet = fold(
      { type: "open", now: 90_000 },
      { type: "view", now: 90_000 },
      { type: "telemetry", now: 90_000 },
    );

    it("says the numbers are the last received, and when", () => {
      render(<Integrity {...props({ conn: quiet })} />);
      act(() => vi.advanceTimersByTime(1000)); // the card's own clock ticks: 10 s without telemetry
      expect(screen.getByText(`These are the last numbers received, at ${formatClock(90_000)}.`)).toBeInTheDocument();
      expect(document.querySelector(".mk-integrity")).toHaveClass("is-stale");
    });

    it("says nothing about staleness while the link is healthy", () => {
      render(<Integrity {...props()} />);
      act(() => vi.advanceTimersByTime(1000));
      expect(screen.queryByText(/last numbers received/)).not.toBeInTheDocument();
    });
  });

  it("puts no style attribute in its markup, loaded or waiting", () => {
    const a = render(<Integrity {...props()} />);
    expect(a.container.querySelectorAll("[style]")).toHaveLength(0);
    a.unmount();
    const b = render(<Integrity {...props({ telemetry: null, events: [] })} />);
    expect(b.container.querySelectorAll("[style]")).toHaveLength(0);
  });

  it("is a labelled region named by its headline", () => {
    render(<Integrity {...props()} />);
    expect(
      within(screen.getByRole("region", { name: "18 faults repaired" })).getByText(/updates checked/),
    ).toBeInTheDocument();
  });
});
