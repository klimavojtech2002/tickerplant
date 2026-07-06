"use client";

// The dashboard page: one stream subscription, one metrics poll, and pure renders.
// The client is a view over the backend, not a second implementation — every event
// is a complete book, so there is no reducer to get wrong.

import { useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { BookPanel } from "@/components/BookPanel";
import { HealthPanel } from "@/components/HealthPanel";
import { StatusBadge } from "@/components/StatusBadge";
import { pollMetrics } from "@/lib/metrics";
import { ViewStore, type Snapshot } from "@/lib/store";
import { connectStream } from "@/lib/stream";
import type { Metrics } from "@/lib/types";

const edgeURL = process.env.NEXT_PUBLIC_EDGE_URL ?? "http://127.0.0.1:8080";

const serverSnapshot: Snapshot = { status: "connecting", view: null, updatedAt: 0 };

export default function Page() {
  const store = useMemo(() => new ViewStore(), []);
  const snap = useSyncExternalStore(store.subscribe, store.getSnapshot, () => serverSnapshot);
  const [metrics, setMetrics] = useState<Metrics | null | undefined>(undefined);
  const [now, setNow] = useState(0);

  useEffect(() => connectStream(edgeURL, store), [store]);
  useEffect(() => pollMetrics(edgeURL, setMetrics), []);
  useEffect(() => {
    // now starts at 0 (fresh-page state, statusLabel treats it as not stalled) and
    // ticks once a second; no synchronous set, so hydration stays clean.
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, []);

  return (
    <main>
      <header>
        <h1>
          tickerplant
          {snap.view ? (
            <span className="instrument">
              {snap.view.symbol} @ {snap.view.venue}
            </span>
          ) : null}
        </h1>
        <StatusBadge status={snap.status} updatedAt={snap.updatedAt} now={now} />
      </header>
      {snap.view ? <BookPanel view={snap.view} /> : <p>waiting for the first view…</p>}
      <HealthPanel metrics={metrics} />
      <footer>
        <p>
          {`edge: ${edgeURL} — prices and sizes are the venue's decimal strings end to end; this page never parses them into floats.`}
        </p>
      </footer>
    </main>
  );
}
