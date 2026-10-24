"use client";

// ThemeToggle cycles system → light → dark. Storage is read after mount, never during render, so the server's markup and the
// first client render agree (both "system"); a stored choice was already applied to <html> by the snippet in layout.tsx.

import { useEffect, useState } from "react";

import { copy } from "@/lib/copy";
import { applyTheme, nextTheme, readTheme, writeTheme, type Theme } from "@/lib/theme";

const labels: Record<Theme, string> = {
  system: copy.nav.themeSystem,
  light: copy.nav.themeLight,
  dark: copy.nav.themeDark,
};

/** The three icons, drawn on a 16-unit grid: a half-filled disc (system), a sun (light) and a crescent (dark). */
function Icon({ theme }: { theme: Theme }) {
  if (theme === "system") {
    return (
      <>
        <circle cx="8" cy="8" r="6.2" fill="none" stroke="currentColor" strokeWidth="1.4" />
        <path d="M8 1.8a6.2 6.2 0 0 1 0 12.4z" fill="currentColor" />
      </>
    );
  }
  if (theme === "light") {
    return (
      <g fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round">
        <circle cx="8" cy="8" r="3.2" />
        <path d="M8 .8v1.6M8 13.6v1.6M.8 8h1.6M13.6 8h1.6M2.9 2.9l1.1 1.1M12 12l1.1 1.1M2.9 13.1 4 12M12 4l1.1-1.1" />
      </g>
    );
  }
  return <path d="M13.2 10.3A5.9 5.9 0 0 1 5.7 2.8a6 6 0 1 0 7.5 7.5z" fill="currentColor" />;
}

export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>("system");
  useEffect(() => {
    // a one-time read of the visitor's saved choice, which only the browser can supply
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setTheme(readTheme());
  }, []);
  const toggle = () => {
    const next = nextTheme(theme);
    writeTheme(next);
    applyTheme(next);
    setTheme(next);
  };
  return (
    <button type="button" className="icon-btn" onClick={toggle} aria-label={labels[theme]} title={labels[theme]}>
      <span className="icon-disc" aria-hidden>
        <svg width="15" height="15" viewBox="0 0 16 16">
          <Icon theme={theme} />
        </svg>
      </span>
    </button>
  );
}
