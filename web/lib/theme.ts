// The visitor's colour-scheme choice. "system" follows the operating system (CSS alone, no attribute); "light" and "dark" are
// explicit and kept in localStorage. Reading and writing storage can throw (private browsing, blocked site data), so both are
// guarded and the page works without it.

export type Theme = "system" | "light" | "dark";

export const themeKey = "tickerplant-theme";

const isExplicit = (v: unknown): v is "light" | "dark" => v === "light" || v === "dark";

export function readTheme(storage?: Pick<Storage, "getItem">): Theme {
  try {
    const v = (storage ?? window.localStorage).getItem(themeKey);
    return isExplicit(v) ? v : "system";
  } catch {
    return "system";
  }
}

export function writeTheme(theme: Theme, storage?: Pick<Storage, "setItem" | "removeItem">): void {
  try {
    const s = storage ?? window.localStorage;
    if (theme === "system") {
      s.removeItem(themeKey);
    } else {
      s.setItem(themeKey, theme);
    }
  } catch {
    // the choice still applies for this visit; it just is not remembered
  }
}

/** applyTheme sets data-theme for an explicit choice and removes it for "system". */
export function applyTheme(theme: Theme, root: HTMLElement = document.documentElement): void {
  if (theme === "system") {
    root.removeAttribute("data-theme");
  } else {
    root.setAttribute("data-theme", theme);
  }
}

/** nextTheme is the order the toggle cycles through. */
export function nextTheme(theme: Theme): Theme {
  return theme === "system" ? "light" : theme === "light" ? "dark" : "system";
}

/**
 * themeScript runs in <head> before first paint, so a stored explicit choice never flashes the other scheme. It is a constant
 * string with no interpolation: the build hashes it into the page's Content-Security-Policy. It repeats the rule of readTheme
 * and applyTheme, and theme.test.ts runs both against the same inputs.
 */
export const themeScript = `(function(){try{var t=localStorage.getItem("${themeKey}");if(t==="light"||t==="dark"){document.documentElement.setAttribute("data-theme",t)}}catch(e){}})()`;
