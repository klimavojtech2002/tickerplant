"use client";

// The dashboard page: one stream subscription, one status poll, and pure renders. The
// client is a view over the backend, not a second implementation: every event is a
// complete book, so there is nothing to merge. What the page does keep is the state of
// its own link (lib/connection.ts), which a table-tested reducer owns.
//
// The page re-renders whenever the store changes (telemetry once a second, and each
// view). The book panel is memoised and skips telemetry-only changes; the clocks live in
// the badge and the health section, so nothing here ticks.

import { useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { BookPanel } from "@/components/BookPanel";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { HealthSection } from "@/components/HealthPanel";
import { NavBar } from "@/components/NavBar";
import { initialPollState, pollStatus, type PollState } from "@/lib/status";
import { initialSnapshot, StreamStore } from "@/lib/store";
import { connectStream } from "@/lib/stream";

const edgeURL = process.env.NEXT_PUBLIC_EDGE_URL ?? "http://127.0.0.1:8080";

export default function Page() {
  const store = useMemo(() => new StreamStore(), []);
  const snap = useSyncExternalStore(store.subscribe, store.getSnapshot, () => initialSnapshot);
  const [poll, setPoll] = useState<PollState>(initialPollState);

  useEffect(() => connectStream(edgeURL, store), [store]);
  useEffect(() => pollStatus(edgeURL, setPoll), []);

  return (
    <>
      <NavBar conn={snap.conn} />
      <main className="shell">
        <div className="legacy">
          <header>
            <h1>
              tickerplant
              {snap.view ? (
                <span className="instrument">
                  {snap.view.symbol} @ {snap.view.venue}
                </span>
              ) : null}
            </h1>
          </header>
          <ErrorBoundary resetKey={snap.view}>
            {snap.view ? <BookPanel view={snap.view} /> : <p>waiting for the first view…</p>}
          </ErrorBoundary>
          <ErrorBoundary resetKey={poll.kind}>
            <HealthSection state={poll} />
          </ErrorBoundary>
          <footer>
            <p>
              {`edge: ${edgeURL} — prices and sizes are the venue's decimal strings end to end; this page never parses them into floats.`}
            </p>
          </footer>
        </div>
      </main>
    </>
  );
}
