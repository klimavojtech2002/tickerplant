import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";

import type { TelemetryEvent } from "@/lib/types";

import { FaultLog, type FaultLogProps } from "./FaultLog";

const ev = (id: number, over: Partial<TelemetryEvent> = {}): TelemetryEvent => ({
  id,
  kind: "gap",
  atUnixMs: new Date(2026, 9, 25, 12, 0, id).getTime(),
  expectedSeq: 100 + id,
  gotSeq: 103 + id,
  reason: "sequence gap",
  durationMs: 4,
  ...over,
});

// newest first, as the store keeps them
const events = (n: number) => Array.from({ length: n }, (_, i) => ev(n - i));

function props(over: Partial<FaultLogProps> = {}): FaultLogProps {
  return { events: events(3), eventBaseline: 3, missedEvents: 0, known: true, ...over };
}

describe("FaultLog", () => {
  it("names itself and says what it lists and what it includes", () => {
    render(<FaultLog {...props()} />);
    expect(screen.getByRole("heading", { level: 2, name: "Recent faults" })).toBeInTheDocument();
    expect(screen.getByText(/The last 16 the server recorded\./)).toBeInTheDocument();
    expect(screen.getByText(/Includes snapshot problems, which are not counted as repairs\./)).toBeInTheDocument();
  });

  it("lists the entries newest first, each with an absolute time of day", () => {
    render(<FaultLog {...props()} />);
    const times = screen.getAllByRole("listitem").map((li) => li.querySelector("time")!.textContent);
    expect(times).toEqual(["12:00:03", "12:00:02", "12:00:01"]);
    expect(screen.getAllByRole("listitem")[0]!.querySelector("time")).toHaveAttribute("datetime");
    expect(screen.queryByText(/ago/)).not.toBeInTheDocument();
  });

  it("words each entry from its kind, with its sequence numbers and how long the rebuild took", () => {
    render(<FaultLog {...props({ events: [ev(2, { durationMs: 38 })] })} />);
    const item = screen.getByRole("listitem");
    expect(within(item).getByText("Sequence gap")).toBeInTheDocument();
    expect(within(item).getByText("Expected 102, got 105. Rebuilt from a snapshot in 38 ms.")).toBeInTheDocument();
  });

  it("says rebuilding only for the newest rebuild, and not recorded for an older one without a time", () => {
    render(<FaultLog {...props({ events: [ev(2, { durationMs: -1 }), ev(1, { durationMs: -1 })] })} />);
    const items = screen.getAllByRole("listitem");
    expect(items[0]).toHaveTextContent("Rebuilding…");
    expect(items[1]).toHaveTextContent("Rebuild time not recorded.");
  });

  it("shows snapshot problems with the server's fixed reason and no rebuild time", () => {
    render(
      <FaultLog
        {...props({
          events: [ev(1, { kind: "snapshot-reject", reason: "snapshot crossed", durationMs: 0, expectedSeq: 0 })],
        })}
      />,
    );
    const item = screen.getByRole("listitem");
    expect(within(item).getByText("Snapshot refused")).toBeInTheDocument();
    expect(item).toHaveTextContent("Snapshot crossed.");
    expect(item).not.toHaveTextContent(/rebuilt/i);
  });

  it("words a feed disconnect and gives it its own icon", () => {
    const { container } = render(
      <FaultLog {...props({ events: [ev(1, { kind: "disconnect", expectedSeq: 0, gotSeq: 0 })] })} />,
    );
    expect(screen.getByRole("listitem")).toHaveTextContent("Feed disconnected");
    expect(container.querySelector("li svg path")!.getAttribute("d")).toContain("M3 3l12 12");
  });

  it("shows a crossing update without any price, and never a sequence number of zero", () => {
    render(<FaultLog {...props({ events: [ev(1, { kind: "cross", expectedSeq: 0, gotSeq: 9999 })] })} />);
    const item = screen.getByRole("listitem");
    expect(item).toHaveTextContent("It would have put a bid above the ask. Discarded.");
    expect(item.textContent).not.toMatch(/\b9999\b|\b0\b/);
  });

  it("shows the five newest when there are more, not the five oldest", () => {
    render(<FaultLog {...props({ events: events(8), eventBaseline: 8 })} />);
    const times = screen.getAllByRole("listitem").map((li) => li.querySelector("time")!.textContent);
    expect(times).toEqual(["12:00:08", "12:00:07", "12:00:06", "12:00:05", "12:00:04"]);
  });

  it("says no faults yet when the server has recorded none", () => {
    render(<FaultLog {...props({ events: [], eventBaseline: 0 })} />);
    expect(screen.getByText("No faults yet.")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  it("says nothing about faults before the first update, not even that there are none", () => {
    render(<FaultLog {...props({ events: [], eventBaseline: null, known: false })} />);
    expect(screen.queryByText("No faults yet.")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Recent faults" })).toBeInTheDocument();
  });

  describe("what is new", () => {
    it("counts the entries since the page opened, in the singular and the plural", () => {
      const { rerender } = render(<FaultLog {...props({ events: events(4), eventBaseline: 3 })} />);
      expect(screen.getByText("1 new entry since this page opened.")).toBeInTheDocument();
      rerender(<FaultLog {...props({ events: events(6), eventBaseline: 3 })} />);
      expect(screen.getByText("3 new entries since this page opened.")).toBeInTheDocument();
    });

    it("says nothing when nothing is new", () => {
      render(<FaultLog {...props()} />);
      expect(screen.queryByText(/new entr/)).not.toBeInTheDocument();
    });

    it("says how many entries the page missed between updates, only when it missed any", () => {
      const { rerender } = render(<FaultLog {...props({ missedEvents: 1 })} />);
      expect(screen.getByText("1 entry was missed between updates.")).toBeInTheDocument();
      rerender(<FaultLog {...props({ missedEvents: 4 })} />);
      expect(screen.getByText("4 entries were missed between updates.")).toBeInTheDocument();
      rerender(<FaultLog {...props({ missedEvents: 0 })} />);
      expect(screen.queryByText(/missed between updates/)).not.toBeInTheDocument();
    });
  });

  describe("the five newest, and the rest on request", () => {
    it("shows five of sixteen and a button that says how many there are", () => {
      render(<FaultLog {...props({ events: events(16), eventBaseline: 16 })} />);
      expect(screen.getAllByRole("listitem")).toHaveLength(5);
      const button = screen.getByRole("button", { name: "Show all 16" });
      expect(button).toHaveAttribute("aria-expanded", "false");
      expect(button).toHaveAttribute("aria-controls", "fault-list");
    });

    it("opens the rest in place and closes them again", async () => {
      const user = userEvent.setup();
      render(<FaultLog {...props({ events: events(16), eventBaseline: 16 })} />);
      await user.click(screen.getByRole("button", { name: "Show all 16" }));
      expect(screen.getAllByRole("listitem")).toHaveLength(16);
      expect(screen.getByRole("button", { name: "Show fewer" })).toHaveAttribute("aria-expanded", "true");
      await user.click(screen.getByRole("button", { name: "Show fewer" }));
      expect(screen.getAllByRole("listitem")).toHaveLength(5);
    });

    it("has no button when everything fits", () => {
      render(<FaultLog {...props({ events: events(5), eventBaseline: 5 })} />);
      expect(screen.queryByRole("button")).not.toBeInTheDocument();
    });

    it("keeps its choice when a new frame arrives", async () => {
      const user = userEvent.setup();
      const { rerender } = render(<FaultLog {...props({ events: events(8), eventBaseline: 8 })} />);
      await user.click(screen.getByRole("button", { name: "Show all 8" }));
      rerender(<FaultLog {...props({ events: events(9), eventBaseline: 8 })} />);
      expect(screen.getAllByRole("listitem")).toHaveLength(9);
    });
  });

  it("puts no style attribute in its markup", () => {
    const { container } = render(<FaultLog {...props({ events: events(16) })} />);
    expect(container.querySelectorAll("[style]")).toHaveLength(0);
  });
});
