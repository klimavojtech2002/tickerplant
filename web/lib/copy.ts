// The words the interface uses, in one place. Connection state words live with the state machine in connection.ts and are
// exempt until that file is moved here.

const levels = (n: number) => `${n} ${n === 1 ? "level" : "levels"}`;

export const copy = {
  appName: "tickerplant",
  nav: {
    themeSystem: "Appearance: follows the system. Switch to light.",
    themeLight: "Appearance: light. Switch to dark.",
    themeDark: "Appearance: dark. Switch to follow the system.",
  },
  pill: {
    refused: (n: number) => `${n} ${n === 1 ? "update" : "updates"} refused`,
    refusedHelp: "Updates the page could not use were left out; the book on screen is the last good one.",
  },
  market: {
    title: "Order book",
    ladderLede: (n: number) => `Top ${levels(n)}. Prices and sizes are the exchange's exact text.`,
    price: "Price",
    size: "Size",
    meter: "Share of the largest size",
    bestBid: "best bid",
    bestAsk: "best ask",
    spread: "Spread",
    mid: "Mid",
    empty: "No levels on either side yet.",
    depthTitle: "Market depth",
    depthLede: (n: number, currency: string | null) =>
      `Cumulative size resting at each price, top ${levels(n)}${currency ? `, in ${currency}` : ""}.`,
    legendBids: "Bids",
    legendAsks: "Asks",
    depthLabel: (bid: string | null, ask: string | null, n: number) =>
      `Cumulative depth: best bid ${bid ?? "none"}, best ask ${ask ?? "none"}, up to ${levels(n)} a side. The order book has the exact numbers.`,
    oneSided: "Only one side of the book has levels.",
    cannotDraw: "Depth cannot be drawn for this book. The order book has the numbers.",
    waiting: "Waiting for the first book…",
  },
  hero: {
    midLabel: "Mid price",
    bid: "Best bid",
    ask: "Best ask",
    spread: "Spread",
    ticks: (n: string) => `${n} ${n === "1" ? "tick" : "ticks"}`,
    chipDemo: "Demo feed",
    chipDemoChaos: "Demo feed · faults injected",
    chipUnknown: "Source not confirmed",
    pause: "Pause",
    resume: "Resume",
    pausedTitle: (time: string) => `Paused. The book on screen was received at ${time}.`,
    pausedBody: "Prices and the order book are frozen. The connection and the counters keep updating.",
    staleTitle: "The connection was lost.",
    staleBody: (time: string) =>
      `The numbers are the last book received, at ${time}. They update when the connection returns.`,
    waiting: "Waiting for the first book…",
    collecting: "Collecting…",
    oneSided: "One side of the book is empty, so there is no mid price.",
    noMid: "The best bid and ask are not written to the same number of decimals, so there is no mid price.",
    sparkAria: (change: string, span: string) => `Mid price, ${span} of history, ${change}`,
    sparkAriaFlat: (span: string) => `Mid price, ${span} of history`,
    sparkChange: (change: string, span: string) => `${change} in ${span}`,
  },
} as const;
