// Exact arithmetic on the edge's decimal strings via BigInt — the client-side end of
// the no-float rule. All prices in one feed share the venue's price scale and all
// sizes share the size scale (the server renders them with FormatScaled), so within
// one side-by-side comparison the fraction widths always match.

const decimalRe = /^\d+(\.\d+)?$/;

/** isPlainDecimal is the wire grammar FormatScaled emits — the boundary check. */
export function isPlainDecimal(s: string): boolean {
  return decimalRe.test(s);
}

/** toScaled turns "62721.7" into 627217n — the venue's integer ticks, exactly. */
export function toScaled(dec: string): bigint {
  if (!decimalRe.test(dec)) {
    throw new Error(`not a plain decimal: ${JSON.stringify(dec)}`);
  }
  return BigInt(dec.replace(".", ""));
}

/** scaleOf reports the fraction width, so a derived value can be rendered back. */
export function scaleOf(dec: string): number {
  const dot = dec.indexOf(".");
  return dot === -1 ? 0 : dec.length - dot - 1;
}

/** fromScaled renders integer ticks back at the given scale: (12034n, 2) -> "120.34". */
export function fromScaled(v: bigint, scale: number): string {
  const digits = v.toString().padStart(scale + 1, "0");
  if (scale === 0) {
    return digits;
  }
  return `${digits.slice(0, -scale)}.${digits.slice(-scale)}`;
}

/** crossed mirrors the backend invariant: best bid >= best ask is never renderable. */
export function crossed(bestBid: string, bestAsk: string): boolean {
  return toScaled(bestBid) >= toScaled(bestAsk);
}

/** spread = ask - bid, rendered at the prices' own scale. */
export function spread(bestBid: string, bestAsk: string): string {
  return fromScaled(toScaled(bestAsk) - toScaled(bestBid), scaleOf(bestAsk));
}

/**
 * barPct maps a size to an integer 0..100 share of the side's largest size, for a
 * bar width. The division happens in BigInt; only the final small integer becomes a
 * JS number, so no price or size ever touches a float.
 */
export function barPct(size: string, maxSize: string): number {
  const max = toScaled(maxSize);
  if (max === 0n) {
    return 0;
  }
  return Number((toScaled(size) * 100n) / max);
}

/** maxSize returns the largest size string of a side (exact comparison). */
export function maxSize(levels: [string, string][]): string {
  let best = "0";
  let bestV = 0n;
  for (const [, size] of levels) {
    const v = toScaled(size);
    if (v > bestV) {
      bestV = v;
      best = size;
    }
  }
  return best;
}
