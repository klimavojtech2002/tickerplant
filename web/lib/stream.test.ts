import { describe, expect, it } from "vitest";

import { ViewStore } from "./store";
import { connectStream } from "./stream";

// FakeEventSource scripts the whole EventSource lifecycle, so reconnect semantics
// are tested without a socket — the same seam-injection pattern as the Go suite.
class FakeEventSource {
  static last: FakeEventSource | null = null;
  url: string;
  closed = false;
  // Mirrors EventSource's readyState: 0 CONNECTING, 1 OPEN, 2 CLOSED. Tests drive
  // this directly to distinguish a transient error (browser keeps retrying) from a
  // fatal one (browser gave up for good).
  readyState = 0;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  private handlers = new Map<string, (ev: MessageEvent) => void>();

  constructor(url: string) {
    this.url = url;
    FakeEventSource.last = this;
  }
  addEventListener(type: string, fn: (ev: MessageEvent) => void) {
    this.handlers.set(type, fn);
  }
  close() {
    this.closed = true;
  }
  emit(type: string, data: string) {
    this.handlers.get(type)?.({ data } as MessageEvent);
  }
}

const goodView = JSON.stringify({
  venue: "kraken",
  symbol: "BTC/USD",
  seq: 7,
  bids: [["62721.7", "1.35823999"]],
  asks: [["62721.8", "0.00114202"]],
});

describe("connectStream", () => {
  it("drives status through open, error, and reopen — EventSource's own retry loop", () => {
    const store = new ViewStore();
    connectStream("http://edge", store, FakeEventSource);
    const es = FakeEventSource.last!;
    expect(es.url).toBe("http://edge/stream");
    expect(store.getSnapshot().status).toBe("connecting");
    es.onopen!(new Event("open"));
    expect(store.getSnapshot().status).toBe("live");
    es.onerror!(new Event("error"));
    expect(store.getSnapshot().status).toBe("retrying");
    es.onopen!(new Event("open")); // the automatic reconnect succeeded
    expect(store.getSnapshot().status).toBe("live");
    es.emit("view", goodView); // the server's opener rebuilds the picture
    expect(store.getSnapshot().view?.seq).toBe(7);
  });

  it("parses view events into the store and drops malformed ones", () => {
    const store = new ViewStore();
    connectStream("http://edge", store, FakeEventSource);
    const es = FakeEventSource.last!;
    es.emit("view", goodView);
    expect(store.getSnapshot().view?.seq).toBe(7);
    es.emit("view", `{"venue":1}`); // wrong shape: dropped, latest view survives
    es.emit("view", `not json`);
    // decimals BigInt math must never see: dropped at the boundary, no throw
    es.emit("view", goodView.replace("62721.7", "1e5"));
    es.emit("view", goodView.replace("1.35823999", "-3"));
    expect(store.getSnapshot().view?.seq).toBe(7);
  });

  it("reports a fatal close as disconnected, not retrying", () => {
    const store = new ViewStore();
    connectStream("http://edge", store, FakeEventSource);
    const es = FakeEventSource.last!;
    es.readyState = 2; // CLOSED: the browser has given up for good
    es.onerror!(new Event("error"));
    expect(store.getSnapshot().status).toBe("disconnected");
  });

  it("keeps reporting retrying while EventSource is still retrying on its own", () => {
    const store = new ViewStore();
    connectStream("http://edge", store, FakeEventSource);
    const es = FakeEventSource.last!;
    es.readyState = 0; // CONNECTING: mid browser-managed retry, not fatal
    es.onerror!(new Event("error"));
    expect(store.getSnapshot().status).toBe("retrying");
  });

  it("closes the source on cleanup", () => {
    const stop = connectStream("http://edge", new ViewStore(), FakeEventSource);
    const es = FakeEventSource.last!;
    stop();
    expect(es.closed).toBe(true);
  });

  // React Strict Mode (on by default under the App Router) mounts every effect
  // twice in development: connect, clean up, reconnect. The discarded first
  // EventSource's handlers stay wired to the same store, so a stale event
  // reaching them after cleanup must be inert rather than corrupt state the
  // surviving connection already set.
  it("ignores a stale status event from a connection whose cleanup already ran", () => {
    const store = new ViewStore();
    const stop1 = connectStream("http://edge", store, FakeEventSource);
    const esA = FakeEventSource.last!;
    esA.onopen!(new Event("open"));
    stop1();

    connectStream("http://edge", store, FakeEventSource);
    const esB = FakeEventSource.last!;
    esB.onopen!(new Event("open"));
    expect(store.getSnapshot().status).toBe("live");

    esA.onerror!(new Event("error")); // late event from the discarded connection
    expect(store.getSnapshot().status).toBe("live");
  });

  it("ignores a stale view event from a connection whose cleanup already ran", () => {
    const store = new ViewStore();
    const stop1 = connectStream("http://edge", store, FakeEventSource);
    const esA = FakeEventSource.last!;
    stop1();

    connectStream("http://edge", store, FakeEventSource);
    const esB = FakeEventSource.last!;
    esB.emit("view", goodView);
    expect(store.getSnapshot().view?.seq).toBe(7);

    esA.emit("view", goodView.replace('"seq":7', '"seq":1')); // stale, from A
    expect(store.getSnapshot().view?.seq).toBe(7);
  });
});
