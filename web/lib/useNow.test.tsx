import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useNow } from "./useNow";

describe("useNow", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(1_700_000_000_000);
  });
  afterEach(() => vi.useRealTimers());

  it("is 0 on the first render, takes its first reading from a timer, then follows the interval", () => {
    const { result } = renderHook(() => useNow(1000));
    expect(result.current).toBe(0);
    act(() => vi.advanceTimersByTime(0));
    expect(result.current).toBe(1_700_000_000_000);
    act(() => vi.advanceTimersByTime(1000));
    expect(result.current).toBe(1_700_000_001_000);
    act(() => vi.advanceTimersByTime(2000));
    expect(result.current).toBe(1_700_000_003_000);
  });

  it("sets no timer and stays 0 while disabled", () => {
    const { result } = renderHook(() => useNow(1000, false));
    expect(vi.getTimerCount()).toBe(0);
    act(() => vi.advanceTimersByTime(5000));
    expect(result.current).toBe(0);
  });

  it("starts and stops its timers when enabled flips, and clears them on unmount", () => {
    const { result, rerender, unmount } = renderHook(({ on }) => useNow(1000, on), { initialProps: { on: false } });
    rerender({ on: true });
    expect(vi.getTimerCount()).toBe(2); // the first reading and the interval
    act(() => vi.advanceTimersByTime(0));
    expect(result.current).toBe(1_700_000_000_000);
    rerender({ on: false });
    expect(vi.getTimerCount()).toBe(0);
    expect(result.current).toBe(0); // disabled reads 0 again, not a stale time
    rerender({ on: true });
    expect(result.current).toBe(0); // and so does the first render after it is switched back on
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
