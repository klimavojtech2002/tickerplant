"use client";

// The dashboard page: one stream subscription, one status poll, and pure renders. The
// client is a view over the backend, not a second implementation: every event is a
// complete book, so there is nothing to merge. What the page does keep is the state of
// its own link (lib/connection.ts), which a table-tested reducer owns.
//
// The page re-renders whenever the store changes (telemetry once a second, and each
// view). The book panel is memoised and skips telemetry-only changes; the clocks live in
// the connection pill, the hero and the integrity card, so nothing here ticks.

import { useCallback, useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { ErrorBoundary } from "@/components/ErrorBoundary";
import { FaultLog } from "@/components/FaultLog";
import { Footer } from "@/components/Footer";
import { Hero } from "@/components/Hero";
import { Integrity } from "@/components/Integrity";
import { Market } from "@/components/Market";
import { NavBar } from "@/components/NavBar";
import { System } from "@/components/System";
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
      // viewAt is 0 between a new connection opening and its first book; there is nothing honest to freeze then.
      return now.view && now.conn.viewAt > 0 ? { view: now.view, history: now.history, at: now.conn.viewAt } : null;
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
        <ErrorBoundary resetKey={view}>
          <Market view={view} />
        </ErrorBoundary>
        <ErrorBoundary resetKey={snap.telemetry}>
          <div className="row2">
            <Integrity
              telemetry={snap.telemetry}
              events={snap.events}
              viewsChecked={snap.viewsChecked}
              viewsRefused={snap.viewsRefused}
              conn={snap.conn}
            />
            <FaultLog
              events={snap.events}
              eventBaseline={snap.eventBaseline}
              missedEvents={snap.missedEvents}
              known={snap.telemetry !== null}
            />
          </div>
        </ErrorBoundary>
        <ErrorBoundary resetKey={poll.kind}>
          <System poll={poll} edgeURL={edgeURL} />
        </ErrorBoundary>
        <Footer edgeURL={edgeURL} version={poll.kind === "loading" ? null : poll.status ? poll.status.version : null} />
      </main>
    </>
  );
}
