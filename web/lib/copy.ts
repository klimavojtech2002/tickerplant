// The words the interface uses, in one place. Connection state words live with the state machine in connection.ts and are
// exempt until that file is moved here.

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
    sparkAria: (change: string, span: string) => `Mid price over the last ${span}, ${change}`,
    sparkAriaFlat: (span: string) => `Mid price over the last ${span}`,
    sparkStart: (span: string) => `${span} ago`,
    sparkNow: "now",
    sparkChange: (change: string, span: string) => `${change} in ${span}`,
  },
} as const;
