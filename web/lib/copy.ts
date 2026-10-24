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
} as const;
