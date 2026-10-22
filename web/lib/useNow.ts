"use client";

// useNow returns the current time in ms, refreshed on an interval, for the few places
// that show an age or a countdown. It starts at 0 and takes its first reading from a
// timer, never during render or synchronously in the effect: the server render and the
// first client render must match, and a 0 means "not ticked yet" (every age computed
// from it clamps to 0). Pass enabled = false to run no timer at all.

import { useEffect, useState } from "react";

export function useNow(intervalMs: number, enabled = true): number {
  const [now, setNow] = useState(0);
  useEffect(() => {
    if (!enabled) {
      return;
    }
    const tick = () => setNow(Date.now());
    const first = setTimeout(tick, 0);
    const id = setInterval(tick, intervalMs);
    return () => {
      clearTimeout(first);
      clearInterval(id);
      setNow(0); // a clock that is switched off and on again must not show its old reading
    };
  }, [intervalMs, enabled]);
  return now; // 0 while disabled: the cleanup resets it
}
