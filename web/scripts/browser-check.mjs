// Loads the served dashboard in a real Chrome and checks what a visitor would hit: no horizontal scroll, readable
// text in both colour schemes, no console error, no Content-Security-Policy violation, no layout shift while the book
// updates.
//
//   node scripts/browser-check.mjs http://127.0.0.1:18080
//
// It talks to Chrome over the DevTools Protocol with Node's built-in WebSocket, so it adds no dependency. Chrome is
// found through CHROME_BIN, or a usual install path. The page must be served with its real policy (see check-image.sh)
// and an edge must be running at the address the page was built for.
//
// Layout is asserted by geometry and computed style, not by pixel comparison: fonts render differently on Windows and on
// the CI runner, so a pixel baseline would either flake or be too loose to catch anything.
//
// Settings (environment):
//   SHIFT_LIMIT           most layout shift allowed while the book updates (default 0.001)
//   STRICT_INITIAL_SHIFT  1 = also gate the shift while the first book arrives
//   REQUIRE_TEXT_SCALING  1 = fail if the page does not follow the browser's text size setting
//   REQUIRE_BOOK          1 = fail if no book appears (an edge must be running)
//   REQUIRE_HERO          1 = the hero must show a mid price that equals the one worked out from the order book's best prices
//   MIN_ROWS              table rows that mean "the book arrived" (default 10)
//   BROWSER_CHECK_OUT     directory for screenshots of failed viewports
//
// What the contrast check does not see: opacity, background images and gradients, text drawn inside SVG, and hover or focus
// states. It is a floor, not a proof of accessibility.
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [pageURL] = process.argv.slice(2);
if (!pageURL) {
  console.error("usage: browser-check.mjs <page url>");
  process.exit(2);
}
const outDir = process.env.BROWSER_CHECK_OUT ?? "";
const strictInitialShift = process.env.STRICT_INITIAL_SHIFT === "1";
const requireTextScaling = process.env.REQUIRE_TEXT_SCALING === "1";
const requireBook = process.env.REQUIRE_BOOK === "1";
const requireHero = process.env.REQUIRE_HERO === "1";
const shiftLimit = Number(process.env.SHIFT_LIMIT ?? "0.001");
const minRows = Number(process.env.MIN_ROWS ?? "10");
const base = pageURL.replace(/\/$/, "");

function findChrome() {
  const candidates = [
    process.env.CHROME_BIN,
    "C:/Program Files/Google/Chrome/Application/chrome.exe",
    "C:/Program Files (x86)/Google/Chrome/Application/chrome.exe",
    "/usr/bin/google-chrome",
    "/usr/bin/google-chrome-stable",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
  ].filter(Boolean);
  const found = candidates.find((c) => existsSync(c));
  if (!found) {
    throw new Error("no Chrome found; set CHROME_BIN");
  }
  return found;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const failures = [];
const notes = [];
function check(ok, what) {
  if (!ok) {
    failures.push(what);
  }
}

// Runs in the page before any of its scripts: collects policy violations and layout shift.
const INSTALL = `
  window.__csp = []; window.__cls = 0;
  document.addEventListener("securitypolicyviolation", (e) => window.__csp.push(e.violatedDirective + " " + e.blockedURI));
  try { new PerformanceObserver((l) => { for (const e of l.getEntries()) { if (!e.hadRecentInput) window.__cls += e.value; } }).observe({ type: "layout-shift", buffered: true }); } catch {}
`;

// Runs in the page: contrast of every visible text node against the colours actually painted, small text, overflow.
const INSPECT = `(() => {
  const cv = document.createElement("canvas"); cv.width = cv.height = 1; const cx = cv.getContext("2d", { willReadFrequently: true });
  const rgba = (c) => { cx.clearRect(0, 0, 1, 1); cx.fillStyle = "#000"; cx.fillStyle = c; cx.fillRect(0, 0, 1, 1); const d = cx.getImageData(0, 0, 1, 1).data; return [d[0], d[1], d[2], d[3] / 255]; };
  const over = (t, b) => { const a = t[3]; return [t[0] * a + b[0] * (1 - a), t[1] * a + b[1] * (1 - a), t[2] * a + b[2] * (1 - a), 1]; };
  const lum = (c) => { const f = (v) => { v /= 255; return v <= 0.04045 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]); };
  const ratio = (a, b) => { const x = lum(a), y = lum(b); return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05); };
  const bgOf = (el) => { const layers = []; for (let e = el; e; e = e.parentElement) { const c = rgba(getComputedStyle(e).backgroundColor); if (c[3] > 0) layers.push(c); if (c[3] === 1) break; } let base = [255, 255, 255, 1]; for (const l of layers.reverse()) base = over(l, base); return base; };
  const lowContrast = [], small = []; let texts = 0;
  const w = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  while (w.nextNode()) {
    const n = w.currentNode; if (!n.textContent.trim()) continue;
    const el = n.parentElement, cs = getComputedStyle(el); if (cs.display === "none" || cs.visibility === "hidden") continue;
    const r = el.getBoundingClientRect(); if (r.width === 0 || r.height === 0) continue;
    texts++;
    const bg = bgOf(el), fg = over(rgba(cs.color), bg), cr = ratio(fg, bg);
    const px = parseFloat(cs.fontSize), large = px >= 24 || (px >= 18.66 && parseInt(cs.fontWeight) >= 700);
    if (cr < (large ? 3 : 4.5)) lowContrast.push(n.textContent.trim().slice(0, 30) + " " + cr.toFixed(2));
    if (px < 12) small.push(n.textContent.trim().slice(0, 30) + " " + px + "px");
  }
  return { texts, lowContrast, small, hscroll: document.documentElement.scrollWidth > innerWidth, scrollW: document.documentElement.scrollWidth, innerW: innerWidth,
    rows: document.querySelectorAll("table tbody tr").length, cls: window.__cls, csp: window.__csp, href: location.href, ready: document.readyState,
    bodyFont: parseFloat(getComputedStyle(document.body).fontSize) };
})()`;

// Reads the hero and the first row of each order-book table in one synchronous call, so both come from the same rendered frame.
const FEATURE = `(() => {
  const text = (s) => document.querySelector(s)?.textContent ?? null;
  const price = (n) => document.querySelector('table[aria-label="' + n + '"] tbody tr:first-child td.price')?.textContent ?? null;
  const mid = document.querySelector('[data-field="mid"]');
  const r = mid ? mid.getBoundingClientRect() : null;
  return { mid: text('[data-field="mid"]'), bid: text('[data-field="bid"]'), ask: text('[data-field="ask"]'), spread: text('[data-field="spread"]'),
    tableBid: price("bids"), tableAsk: price("asks"), midBottom: r ? r.bottom : null, viewport: innerHeight };
})()`;

/** The mid of two decimal strings, exactly: (bid + ask) * 5 at one more digit than the prices. */
function expectedMid(bid, ask) {
  const scale = bid.includes(".") ? bid.length - bid.indexOf(".") - 1 : 0;
  const sum = (BigInt(bid.replace(".", "")) + BigInt(ask.replace(".", ""))) * 5n;
  const digits = sum.toString().padStart(scale + 2, "0");
  return `${digits.slice(0, -(scale + 1))}.${digits.slice(-(scale + 1))}`;
}

const chrome = findChrome();
const profile = mkdtempSync(join(tmpdir(), "browser-check-"));
const proc = spawn(
  chrome,
  [
    "--remote-debugging-port=0",
    `--user-data-dir=${profile}`,
    "--headless=new",
    "--disable-gpu",
    "--no-sandbox",
    "--disable-dev-shm-usage",
    "--hide-scrollbars",
    "--no-first-run",
    "about:blank",
  ],
  { stdio: "ignore" },
);
let chromeExited = false;
const exited = new Promise((resolve) => {
  proc.on("exit", () => {
    chromeExited = true;
    resolve();
  });
});

async function cleanup(ws) {
  try {
    ws?.close();
  } catch {}
  proc.kill();
  await Promise.race([exited, sleep(5000)]);
  try {
    rmSync(profile, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  } catch (err) {
    console.error("note: could not remove", profile, String(err));
  }
}

let ws;
try {
  // Chrome writes its debugging port into the profile directory once it listens.
  let port = 0;
  for (let i = 0; i < 120 && !port; i++) {
    if (chromeExited) {
      throw new Error("Chrome exited before it started listening");
    }
    try {
      port = Number(readFileSync(join(profile, "DevToolsActivePort"), "utf8").split("\n")[0]);
    } catch {
      await sleep(250);
    }
  }
  if (!port) {
    throw new Error("Chrome did not report a debugging port");
  }
  const targets = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
  const target = targets.find((t) => t.type === "page");
  if (!target) {
    throw new Error("Chrome has no page to drive");
  }
  ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    ws.onopen = resolve;
    ws.onerror = () => reject(new Error("could not connect to Chrome"));
  });
  let id = 0;
  const pending = new Map();
  let consoleErrors = [];
  ws.onmessage = (m) => {
    const d = JSON.parse(m.data);
    if (d.id && pending.has(d.id)) {
      pending.get(d.id)(d.result ?? d.error);
      pending.delete(d.id);
    }
    if (d.method === "Runtime.exceptionThrown") {
      consoleErrors.push(
        "exception: " + (d.params.exceptionDetails.exception?.description ?? d.params.exceptionDetails.text),
      );
    } else if (d.method === "Runtime.consoleAPICalled" && d.params.type === "error") {
      consoleErrors.push("console.error: " + d.params.args.map((a) => a.value ?? a.description).join(" "));
    } else if (d.method === "Log.entryAdded" && d.params.entry.level === "error") {
      consoleErrors.push("log: " + d.params.entry.text + (d.params.entry.url ? " " + d.params.entry.url : ""));
    }
  };
  // A command that Chrome never answers (it crashed, or the page hung) fails after 30 s instead of hanging the job.
  const send = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const i = ++id;
      const timer = setTimeout(() => {
        pending.delete(i);
        reject(new Error(`Chrome did not answer ${method} within 30 s`));
      }, 30_000);
      pending.set(i, (v) => {
        clearTimeout(timer);
        resolve(v);
      });
      ws.send(JSON.stringify({ id: i, method, params }));
    });
  ws.onclose = () => {
    for (const resolve of pending.values()) {
      resolve({ error: "the connection to Chrome closed" });
    }
    pending.clear();
  };
  // evaluate returns the value, or undefined if the page context went away (a navigation); callers retry.
  const evaluate = async (expression) =>
    (await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true })).result?.value;
  const inspect = async () => {
    for (let i = 0; i < 40; i++) {
      const v = await evaluate(INSPECT);
      if (v && v.ready !== "loading" && v.href.startsWith(base)) {
        return v;
      }
      await sleep(250);
    }
    throw new Error("the page did not settle enough to be inspected");
  };
  await send("Runtime.enable");
  await send("Log.enable");
  await send("Page.enable");
  await send("Page.addScriptToEvaluateOnNewDocument", { source: INSTALL });

  // [width, height, mobile, how long to watch the book update]. The long watches span a fault of the injected schedule
  // (the first lands 6 to 9 s after the edge starts, then more until about 43 s).
  const viewports = [
    [320, 700, true, 4000],
    [390, 844, true, 20000],
    [820, 1100, false, 4000],
    [1280, 900, false, 20000],
    [1920, 1080, false, 4000],
  ];
  for (const [w, h, mobile, watchMs] of viewports) {
    const tag = `${w}px`;
    await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }] });
    await send("Emulation.setDeviceMetricsOverride", { width: w, height: h, deviceScaleFactor: 1, mobile });
    consoleErrors = [];
    await send("Page.navigate", { url: pageURL });
    let state;
    for (let i = 0; i < 80; i++) {
      await sleep(250);
      state = await evaluate(INSPECT);
      if (state && state.href.startsWith(base) && state.rows >= minRows) {
        break;
      }
    }
    const withBook = Boolean(state && state.href.startsWith(base) && state.rows >= minRows);
    if (requireBook) {
      check(
        withBook,
        `${tag}: the book never appeared (${state ? state.rows : 0} rows, wanted ${minRows}): the page did not reach the edge`,
      );
    } else if (!withBook) {
      notes.push(`${tag}: no book appeared (no edge running?)`);
    }
    state = await inspect();
    const clsAtReady = state.cls;
    await sleep(withBook ? watchMs : 500);
    state = await inspect();

    check(!state.hscroll, `${tag}: horizontal scroll (scrollWidth ${state.scrollW} > ${state.innerW})`);
    check(
      state.lowContrast.length === 0,
      `${tag}: text below contrast (light): ${state.lowContrast.slice(0, 5).join("; ")}`,
    );
    check(state.small.length === 0, `${tag}: text under 12px: ${state.small.slice(0, 5).join("; ")}`);
    check(state.csp.length === 0, `${tag}: CSP violations: ${state.csp.join("; ")}`);
    const shiftWhileUpdating = state.cls - clsAtReady;
    check(
      shiftWhileUpdating <= shiftLimit,
      `${tag}: layout shifted ${shiftWhileUpdating.toFixed(4)} over ${watchMs / 1000}s of updates (limit ${shiftLimit})`,
    );
    if (strictInitialShift) {
      check(
        clsAtReady <= shiftLimit,
        `${tag}: layout shifted ${clsAtReady.toFixed(4)} while loading (limit ${shiftLimit})`,
      );
    } else {
      notes.push(`${tag}: layout shift while loading ${clsAtReady.toFixed(4)} (not gated)`);
    }

    // the hero: its mid price must be exactly the mid of the best prices in the order book, and it must be above the fold
    const feature = await evaluate(FEATURE);
    if (feature && feature.mid !== null) {
      const strip = (s) => s.replaceAll(",", "");
      if (requireHero && (feature.tableBid === null || feature.tableAsk === null)) {
        check(false, `${tag}: the order book has no best bid and ask to compare the hero with`);
      }
      if (feature.tableBid !== null && feature.tableAsk !== null) {
        check(
          strip(feature.bid) === feature.tableBid,
          `${tag}: hero best bid ${feature.bid} is not the order book's ${feature.tableBid}`,
        );
        check(
          strip(feature.ask) === feature.tableAsk,
          `${tag}: hero best ask ${feature.ask} is not the order book's ${feature.tableAsk}`,
        );
        check(
          strip(feature.mid) === expectedMid(feature.tableBid, feature.tableAsk),
          `${tag}: hero mid ${feature.mid} is not (${feature.tableBid} + ${feature.tableAsk}) / 2 = ${expectedMid(feature.tableBid, feature.tableAsk)}`,
        );
      }
      check(
        feature.midBottom <= feature.viewport,
        `${tag}: the mid price is below the fold (bottom ${Math.round(feature.midBottom)} > ${feature.viewport})`,
      );
    } else if (requireHero) {
      check(false, `${tag}: no hero mid price on the page`);
    } else {
      notes.push(`${tag}: no hero on the page (not required)`);
    }

    // dark scheme: the same text and overflow rules
    await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "dark" }] });
    await sleep(300);
    const dark = await inspect();
    check(
      dark.lowContrast.length === 0,
      `${tag}: text below contrast (dark): ${dark.lowContrast.slice(0, 5).join("; ")}`,
    );
    check(!dark.hscroll, `${tag} (dark): horizontal scroll`);
    await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }] });

    // text size at 200 %: the page must follow the browser setting, and then must not scroll sideways
    const before = state.bodyFont;
    await evaluate(`document.documentElement.style.fontSize = "200%"; 0`);
    await sleep(300);
    const big = await inspect();
    const follows = big.bodyFont >= before * 1.5;
    if (requireTextScaling) {
      check(
        follows,
        `${tag}: the page does not follow the browser's text size (body ${before}px -> ${big.bodyFont}px at 200%)`,
      );
    } else if (!follows) {
      notes.push(`${tag}: the page does not follow the browser's text size (not gated yet)`);
    }
    if (follows && requireTextScaling) {
      check(!big.hscroll, `${tag} at 200% text: horizontal scroll (${big.scrollW} > ${big.innerW})`);
    } else if (follows && big.hscroll) {
      notes.push(`${tag} at 200% text: horizontal scroll (${big.scrollW} > ${big.innerW}) (not gated yet)`);
    }
    await evaluate(`document.documentElement.style.fontSize = ""; 0`);
    // read last, so errors raised while the scheme and the text size were switched count too
    check(
      consoleErrors.length === 0,
      `${tag}: console errors: ${consoleErrors
        .slice(0, 3)
        .map((e) => e.slice(0, 200))
        .join(" | ")}`,
    );

    if (outDir && failures.some((f) => f.startsWith(tag))) {
      mkdirSync(outDir, { recursive: true });
      await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: "light" }] });
      const shot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true });
      writeFileSync(join(outDir, `browser-check-${w}.png`), Buffer.from(shot.data, "base64"));
    }
  }
} catch (err) {
  failures.push(String(err.stack ?? err));
} finally {
  await cleanup(ws);
}

for (const n of notes) {
  console.log("note:", n);
}
if (failures.length) {
  console.error(`browser check FAILED (${failures.length}):`);
  for (const f of failures) {
    console.error(" -", f);
  }
  process.exit(1);
}
console.log(
  "browser check passed: 5 widths, contrast in both schemes, text size, overflow, console, CSP, layout shift",
);
