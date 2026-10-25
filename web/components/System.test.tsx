import { render, screen, within } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { formatClock } from "@/lib/hero";
import type { PollState } from "@/lib/status";
import type { Status } from "@/lib/types";

import { System } from "./System";

const long = new Date(2020, 0, 2, 3, 4, 5).getTime();

const status = (over: Partial<Status> = {}): Status => ({
  schema: 1,
  mode: "synthetic",
  chaos: true,
  version: "v1.0.1-0.20261025165459-fa812e180c7c+dirty",
  venue: "synthetic",
  symbol: "DEMO",
  startedAtUnixMs: long,
  depth: 10,
  lastEventAgeMs: 56,
  delivery: { consumers: 2, delivered: 12_345, dropped: 7, published: 9_876_543 },
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
  latency: { count: 1_908, p50Ns: 500, p99Ns: 50_118, p999Ns: 74_400, maxNs: 50_000 },
  ...over,
});

const ok = (over: Partial<Status> = {}): PollState => ({ kind: "ok", status: status(over), lastOkAt: 1 });
const row = (id: string) => document.querySelector(`[data-row="${id}"]`)!;
const value = (id: string) => row(id).querySelector(".row-v")!.textContent;

describe("System", () => {
  it("says it is checking before the first answer, in a card that is already there", () => {
    render(<System poll={{ kind: "loading", failures: 0 }} edgeURL="http://edge" />);
    expect(screen.getByRole("heading", { level: 2, name: "System" })).toBeInTheDocument();
    expect(screen.getByText("Checking the server…")).toBeInTheDocument();
    expect(document.querySelector(".sys2")).toBeNull();
  });

  it("keeps checking, not failing, for a server that has missed one or two polls and never answered", () => {
    render(<System poll={{ kind: "loading", failures: 2 }} edgeURL="http://edge" />);
    expect(screen.getByText("Checking the server…")).toBeInTheDocument();
  });

  it("names the source, the instrument and the server version, and when it started", () => {
    render(<System poll={ok()} edgeURL="http://edge" />);
    expect(
      screen.getByText(
        "Synthetic source · synthetic · DEMO · Server version v1.0.1-0.20261025165459-fa812e180c7c+dirty",
      ),
    ).toBeInTheDocument();
    expect(value("started")).toBe("2 Jan 03:04:05");
  });

  it("leaves the start out when the server did not give one", () => {
    render(<System poll={ok({ startedAtUnixMs: 0 })} edgeURL="http://edge" />);
    expect(document.querySelector('[data-row="started"]')).toBeNull();
  });

  it.each([
    [56, "56 ms before the last check"],
    [1500, "1.5 s before the last check"],
    [-1, "No event yet"],
  ])("words the engine's last event age %s", (ms, text) => {
    render(<System poll={ok({ lastEventAgeMs: ms })} edgeURL="http://edge" />);
    expect(value("lastEvent")).toBe(text);
  });

  it("shows each delivery counter on its own row, with thousands grouped and its own definition", () => {
    render(<System poll={ok()} edgeURL="http://edge" />);
    expect(value("consumers")).toBe("2");
    expect(value("published")).toBe("9,876,543");
    expect(value("delivered")).toBe("12,345");
    expect(value("dropped")).toBe("7");
    expect(screen.getByText("Replaced before a viewer took the previous one, summed over viewers")).toBeInTheDocument();
    expect(screen.getByText("Placed in a viewer's slot while it was empty, summed over viewers")).toBeInTheDocument();
    expect(document.querySelector(".sys2")!.textContent).not.toMatch(/\b(total|in all|sum of)\b/i);
  });

  describe("processing time", () => {
    it("reads under a microsecond as <1 µs, never as nanoseconds", () => {
      render(
        <System
          poll={ok({ latency: { count: 5, p50Ns: 1, p99Ns: 999, p999Ns: 0, maxNs: 5_000 } })}
          edgeURL="http://edge"
        />,
      );
      expect(document.querySelector('[data-latency="p50"]')).toHaveTextContent("<1 µs");
      expect(document.querySelector('[data-latency="p99"]')).toHaveTextContent("<1 µs");
      expect(document.querySelector('[data-latency="p999"]')).toHaveTextContent("<1 µs");
      expect(document.querySelector('[data-latency="max"]')).toHaveTextContent("5.0 µs");
      expect(document.querySelector(".sys2")!.textContent).not.toMatch(/\bns\b/);
    });

    it("shows the four values, the number of updates measured, and the caption of what is measured", () => {
      render(<System poll={ok()} edgeURL="http://edge" />);
      expect(document.querySelector('[data-latency="p99"]')).toHaveTextContent("50.1 µs");
      expect(document.querySelector('[data-latency="max"]')).toHaveTextContent("50.0 µs");
      expect(document.querySelector('[data-field="measured"]')).toHaveTextContent("1,908");
      expect(screen.getByText(/before it is sent to anyone/)).toBeInTheDocument();
      expect(screen.getByText(/not a recent window/)).toBeInTheDocument();
      expect(screen.getByText(/Quantiles are estimates within 2 %/)).toBeInTheDocument();
    });

    it("scales every bar to the largest of the four values and says so", () => {
      render(<System poll={ok()} edgeURL="http://edge" />);
      const widths = [...document.querySelectorAll<HTMLElement>(".p-track i")].map((i) => i.style.width);
      expect(widths).toEqual(["0%", "67%", "100%", "67%"]); // 500, 50118, 74400, 50000 of 74400
      expect(screen.getByText(/Bars share one scale, up to the largest value: 74\.4 µs\./)).toBeInTheDocument();
    });

    it("keeps the bars out of the accessibility tree, since the values are text", () => {
      render(<System poll={ok()} edgeURL="http://edge" />);
      for (const track of document.querySelectorAll(".p-track")) {
        expect(track).toHaveAttribute("aria-hidden", "true");
      }
    });

    it("says the server does not measure it when the status has no latency", () => {
      render(<System poll={ok({ latency: undefined })} edgeURL="http://edge" />);
      expect(screen.getByText("This server does not measure processing time.")).toBeInTheDocument();
      expect(document.querySelector(".p-row")).toBeNull();
    });

    it("draws empty bars, not a failure, for a server that has measured nothing yet", () => {
      render(
        <System poll={ok({ latency: { count: 0, p50Ns: 0, p99Ns: 0, p999Ns: 0, maxNs: 0 } })} edgeURL="http://edge" />,
      );
      expect([...document.querySelectorAll<HTMLElement>(".p-track i")].map((i) => i.style.width)).toEqual([
        "0%",
        "0%",
        "0%",
        "0%",
      ]);
      expect(document.querySelector('[data-field="measured"]')).toHaveTextContent("0");
    });
  });

  describe("when the server cannot be reached", () => {
    it("keeps the last numbers, at full strength, with a line that says when they were received", () => {
      const poll: PollState = {
        kind: "unavailable",
        status: status(),
        failures: 3,
        lastOkAt: new Date(2026, 9, 25, 12, 4, 11).getTime(),
      };
      render(<System poll={poll} edgeURL="http://edge" />);
      expect(
        screen.getByText(
          `Can't reach the server. Trying again every 2 seconds. These are the last numbers received, at ${formatClock(poll.lastOkAt!)}.`,
        ),
      ).toBeInTheDocument();
      expect(value("delivered")).toBe("12,345");
    });

    it("says it plainly, with the address, when it never answered", () => {
      render(
        <System
          poll={{ kind: "unavailable", status: null, failures: 3, lastOkAt: null }}
          edgeURL="http://127.0.0.1:8080"
        />,
      );
      expect(screen.getByRole("heading", { level: 2, name: "Can't reach the server" })).toBeInTheDocument();
      expect(
        screen.getByText("http://127.0.0.1:8080 did not answer. Trying again every 2 seconds."),
      ).toBeInTheDocument();
      expect(screen.queryByRole("button")).not.toBeInTheDocument(); // the poll already retries; a button would say it does not
      expect(document.querySelector(".sys2")).toBeNull();
    });

    it("shows nothing for a poll or two behind, and nothing is announced", () => {
      const poll: PollState = { kind: "degraded", status: status(), failures: 2, lastOkAt: 1 };
      render(<System poll={poll} edgeURL="http://edge" />);
      expect(value("delivered")).toBe("12,345");
      expect(screen.queryByText(/Can't reach/)).not.toBeInTheDocument();
    });

    it("keeps its announcement region in the page whether or not there is anything to announce", () => {
      const { rerender } = render(<System poll={ok()} edgeURL="http://edge" />);
      const region = screen.getByRole("status");
      expect(region).toBeEmptyDOMElement();
      rerender(
        <System poll={{ kind: "unavailable", status: status(), failures: 3, lastOkAt: 1 }} edgeURL="http://edge" />,
      );
      expect(screen.getByRole("status")).toBe(region);
      expect(within(region).getByText(/Can't reach the server/)).toBeInTheDocument();
    });
  });

  it("renders no style attribute where it is prerendered, before the first answer", () => {
    const html = renderToString(<System poll={{ kind: "loading", failures: 0 }} edgeURL="http://edge" />);
    expect(html).not.toContain("style=");
  });
});
