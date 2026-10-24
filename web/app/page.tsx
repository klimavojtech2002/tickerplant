"use client";

// The dashboard page: one stream subscription, one status poll, and pure renders. The
// client is a view over the backend, not a second implementation: every event is a
// complete book, so there is nothing to merge. What the page does keep is the state of
// its own link (lib/connection.ts), which a table-tested reducer owns.
//
// The page re-renders whenever the store changes (telemetry once a second, and each
// view). The book panel is memoised and skips telemetry-only changes; the clocks live in
// the connection pill and the health section, so nothing here ticks.

import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { BookPanel } from "@/components/BookPanel";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { HealthSection } from "@/components/HealthPanel";
import { Hero } from "@/components/Hero";
import { NavBar } from "@/components/NavBar";
import type { History } from "@/lib/history";
import { initialPollState, pollStatus, type PollState } from "@/lib/status";
import { initialSnapshot, StreamStore } from "@/lib/store";
import { connectStream } from "@/lib/stream";
import type { ViewEvent } from "@/lib/types";

const edgeURL = process.env.NEXT_PUBLIC_EDGE_URL ?? "http://127.0.0.1:8080";

/** The book as it was when the visitor pressed Pause, with the time it was received. */
type Frozen = { view: ViewEvent; history: History; at: number };

export default function Page() {
  const store = useMemo(() => new StreamStore(), []);
  const snap = useSyncExternalStore(store.subscribe, store.getSnapshot, () => initialSnapshot);
  const [poll, setPoll] = useState<PollState>(initialPollState);
  const [frozen, setFrozen] = useState<Frozen | null>(null);

  useEffect(() => connectStream(edgeURL, store), [store]);
  useEffect(() => pollStatus(edgeURL, setPoll), []);

  // Pause freezes what is drawn, not what is received: the store keeps taking views, so Resume shows the present at once. It reads
  // the store directly so the handler stays the same function and the hero does not re-render for it.
  const togglePause = useCallback(() => {
    setFrozen((f) => {
      if (f !== null) {
        return null;
      }
      const now = store.getSnapshot();
      return now.view ? { view: now.view, history: now.history, at: now.conn.viewAt } : null;
    });
  }, [store]);

  const view = frozen ? frozen.view : snap.view;
  const history = frozen ? frozen.history : snap.history;

  return (
    <>
      <NavBar conn={snap.conn} />
      <main className="shell">
        <Hero
          view={view}
          history={history}
          conn={snap.conn}
          poll={poll}
          frozenAt={frozen ? frozen.at : null}
          lastViewAt={snap.conn.viewAt}
          onTogglePause={togglePause}
        />
        <div className="legacy">
          <ErrorBoundary resetKey={view}>{view ? <BookPanel view={view} /> : null}</ErrorBoundary>
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
