/**
 * at returns items[i], and fails the test with a clear message if it is missing. With
 * noUncheckedIndexedAccess an index read is `T | undefined`; a test that reads an element it just
 * caused to exist should say so here, not with a `!` that would hide a missing one.
 */
export function at<T>(items: readonly T[], i: number): T {
  const item = items[i];
  if (item === undefined) {
    throw new Error(`expected an element at index ${i}, the list has ${items.length}`);
  }
  return item;
}
