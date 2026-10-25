"use client";

// The foot of the page: the one sentence about exact decimals, where the page is connected, and an About section that opens in
// place. It is closed on screen and open when printed. It is a real button, so Enter and Space work and focus stays on it.

import { useState } from "react";

import { copy } from "@/lib/copy";

export function Footer({ edgeURL, version }: { edgeURL: string; version: string | null }) {
  const [open, setOpen] = useState(false);
  const c = copy.footer;
  return (
    <footer className="foot">
      <div className="foot-row">
        <p>{c.exact}</p>
        <p className="foot-links">
          {version ? <span>{c.version(version)}</span> : null}
          <button
            type="button"
            className="about-btn"
            aria-expanded={open}
            aria-controls="about-panel"
            onClick={() => setOpen((o) => !o)}
          >
            {c.about}
          </button>
        </p>
      </div>
      <div id="about-panel" className="about-panel" hidden={!open}>
        <h2 className="about-title">{c.aboutTitle}</h2>
        {c.aboutBody.map((text) => (
          <p key={text}>{text}</p>
        ))}
      </div>
      <p className="foot-edge">{c.connected(edgeURL)}</p>
    </footer>
  );
}
