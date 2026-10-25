"use client";

// useThrottled follows a value that changes often (a counter that moves with every update) but lets it reach the screen at most once
// per interval. A change after a quiet spell shows at once; changes inside the interval are held, and the latest of them is shown
// when the interval ends (a newer change replaces the timer), so the screen never stays behind. The shown value is state, so only it touches the DOM.

import { useEffect, useRef, useState } from "react";

export function useThrottled<T>(value: T, intervalMs: number): T {
  const [shown, setShown] = useState(value);
  const lastShownAt = useRef(Number.NEGATIVE_INFINITY);
  useEffect(() => {
    if (Object.is(value, shown)) {
      return;
    }
    const wait = lastShownAt.current + intervalMs - Date.now();
    if (wait <= 0) {
      lastShownAt.current = Date.now();
      setShown(value);
      return;
    }
    const timer = setTimeout(() => {
      lastShownAt.current = Date.now();
      setShown(value);
    }, wait);
    return () => clearTimeout(timer);
  }, [value, shown, intervalMs]);

  return shown;
}
