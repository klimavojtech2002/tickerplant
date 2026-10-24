"use client";

// LinkPill names the connection state in words, with a dot for glance value: state is never colour alone. The state word sits in
// a polite live region so a screen reader hears "Live" or "Reconnecting" when it changes; the age and the countdown sit outside
// it, because they change every second and would be read out every second. The pill owns its one-second clock so the page does
// not re-render for it.

import { connectionPill, type ConnectionState } from "@/lib/connection";
import { copy } from "@/lib/copy";
import { useNow } from "@/lib/useNow";

export function LinkPill({ conn }: { conn: ConnectionState }) {
  const now = useNow(1000);
  const { kind, word, detail } = connectionPill(conn, now);
  return (
    <span className="pill" data-kind={kind}>
      <span className="pill-state" role="status" aria-live="polite">
        <span className="pill-dot" aria-hidden />
        <span>{word}</span>
      </span>
      {detail ? <span className="pill-age">· {detail}</span> : null}
      {conn.dropped > 0 ? (
        <span className="pill-refused">
          {copy.pill.refused(conn.dropped)}
          <span className="visually-hidden">. {copy.pill.refusedHelp}</span>
        </span>
      ) : null}
    </span>
  );
}
