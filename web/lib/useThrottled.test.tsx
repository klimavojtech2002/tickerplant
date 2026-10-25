import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { useThrottled } from "./useThrottled";

describe("useThrottled", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(10_000);
  });
  afterEach(() => vi.useRealTimers());

  it("starts with the value it is given", () => {
    const { result } = renderHook(() => useThrottled(7, 250));
    expect(result.current).toBe(7);
  });

  it("shows the first change at once", () => {
    const { result, rerender } = renderHook(({ v }) => useThrottled(v, 250), { initialProps: { v: 1 } });
    rerender({ v: 2 });
    expect(result.current).toBe(2);
  });

  it("holds changes inside the interval and shows the latest of them when it ends", () => {
    const { result, rerender } = renderHook(({ v }) => useThrottled(v, 250), { initialProps: { v: 1 } });
    rerender({ v: 2 }); // shown at once
    act(() => vi.advanceTimersByTime(50));
    rerender({ v: 3 });
    act(() => vi.advanceTimersByTime(50));
    rerender({ v: 4 });
    expect(result.current).toBe(2); // held
    act(() => vi.advanceTimersByTime(149));
    expect(result.current).toBe(2);
    act(() => vi.advanceTimersByTime(1)); // 250 ms after the last time it was shown
    expect(result.current).toBe(4); // the latest, not 3
  });

  it("shows a change after a quiet spell at once, again", () => {
    const { result, rerender } = renderHook(({ v }) => useThrottled(v, 250), { initialProps: { v: 1 } });
    rerender({ v: 2 });
    act(() => vi.advanceTimersByTime(1000));
    rerender({ v: 5 });
    expect(result.current).toBe(5);
  });

  it("never shows a value more often than once per interval under a steady stream", () => {
    const shown: number[] = [];
    const { result, rerender } = renderHook(({ v }) => useThrottled(v, 250), { initialProps: { v: 0 } });
    for (let i = 1; i <= 100; i++) {
      act(() => vi.advanceTimersByTime(10)); // 1000 ms in all
      rerender({ v: i });
      if (shown[shown.length - 1] !== result.current) shown.push(result.current);
    }
    act(() => vi.advanceTimersByTime(300));
    if (shown[shown.length - 1] !== result.current) shown.push(result.current);
    expect(shown.length).toBeLessThanOrEqual(6); // four a second, plus the first and the last
    expect(result.current).toBe(100);
  });

  it("clears its timer when it goes away", () => {
    const { rerender, unmount } = renderHook(({ v }) => useThrottled(v, 250), { initialProps: { v: 1 } });
    rerender({ v: 2 });
    rerender({ v: 3 });
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
  });
});
