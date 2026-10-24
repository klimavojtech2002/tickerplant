// Thousands separators for the figures that are read, not copied: the mid price and the best bid and ask in the hero, and the
// axis of the depth chart. It is a transform of the string and nothing more (no Intl, no Number), and it is lossless: removing the
// separators gives back exactly the string the venue sent. The order book prints the strings untouched.

/** groupThousands puts a comma between every three digits of the whole part: "62721.75" becomes "62,721.75". */
export function groupThousands(s: string): string {
  const m = /^(-?)(\d+)(\.\d+)?$/.exec(s);
  if (!m) {
    return s;
  }
  const [, sign, whole, fraction = ""] = m;
  let out = "";
  for (let i = 0; i < whole!.length; i++) {
    if (i > 0 && (whole!.length - i) % 3 === 0) {
      out += ",";
    }
    out += whole![i];
  }
  return `${sign}${out}${fraction}`;
}

/** ungroup is the inverse, for tests and for comparing what is on screen with what was sent. */
export function ungroup(s: string): string {
  return s.replaceAll(",", "");
}

/** splitFraction cuts a decimal into its whole part and its fraction (with the point): "62,721.75" -> ["62,721", ".75"]. */
export function splitFraction(s: string): [string, string] {
  const dot = s.indexOf(".");
  return dot === -1 ? [s, ""] : [s.slice(0, dot), s.slice(dot)];
}
