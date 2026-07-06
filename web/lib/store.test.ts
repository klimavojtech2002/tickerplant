import { describe, expect, it, vi } from "vitest";

import { ViewStore } from "./store";
import type { ViewEvent } from "./types";

function view(seq: number, bid = "100.1", ask = "100.2"): ViewEvent {
  return { venue: "v", symbol: "s", seq, bids: [[bid, "1"]], asks: [[ask, "1"]] };
}

describe("ViewStore", () => {
  it("keeps only the latest view (client-side latest-wins)", () => {
    const st = new ViewStore();
    st.push(view(1), 10);
    st.push(view(2), 20);
    expect(st.getSnapshot().view?.seq).toBe(2);
    expect(st.getSnapshot().updatedAt).toBe(20);
  });

  it("notifies subscribers with a fresh snapshot object each change", () => {
    const st = new ViewStore();
    const before = st.getSnapshot();
    let calls = 0;
    const unsub = st.subscribe(() => calls++);
    st.push(view(1));
    expect(calls).toBe(1);
    expect(st.getSnapshot()).not.toBe(before); // useSyncExternalStore needs identity change
    unsub();
    st.push(view(2));
    expect(calls).toBe(1);
  });

  it("deduplicates status transitions", () => {
    const st = new ViewStore();
    let calls = 0;
    st.subscribe(() => calls++);
    st.setStatus("live");
    st.setStatus("live");
    expect(calls).toBe(1);
    expect(st.getSnapshot().status).toBe("live");
  });

  it("never renders a crossed book: the frame is refused, loudly", () => {
    const st = new ViewStore();
    st.push(view(1));
    // dev/test builds throw (the render guard mirroring never-crosses)
    expect(() => st.push(view(2, "100.2", "100.2"))).toThrow(/crossed/);
    expect(st.getSnapshot().view?.seq).toBe(1);
  });

  it("drops crossed frames with an error log in production builds", () => {
    const prev = process.env.NODE_ENV;
    vi.stubEnv("NODE_ENV", "production");
    const errs = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      const st = new ViewStore();
      st.push(view(1));
      st.push(view(2, "100.2", "100.2"));
      expect(st.getSnapshot().view?.seq).toBe(1);
      expect(errs).toHaveBeenCalledOnce();
    } finally {
      errs.mockRestore();
      vi.stubEnv("NODE_ENV", prev ?? "test");
    }
  });

  it("accepts one-sided books without a cross check", () => {
    const st = new ViewStore();
    st.push({ venue: "v", symbol: "s", seq: 3, bids: [], asks: [["1", "1"]] });
    expect(st.getSnapshot().view?.seq).toBe(3);
  });
});
