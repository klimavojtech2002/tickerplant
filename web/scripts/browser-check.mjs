// Loads the served dashboard in a real Chrome and checks what a visitor would hit: no horizontal scroll, readable
// text, no console error, no Content-Security-Policy violation, no layout shift while the book updates.
//
//   node scripts/browser-check.mjs http://127.0.0.1:18080
//
// It talks to Chrome over the DevTools Protocol with Node's built-in WebSocket, so it adds no dependency. Chrome is
// found through CHROME_BIN, or a usual install path. The page must be served with its real policy (see check-image.sh)
// and, for the book checks, an edge must be running at the address the page was built for.
//
// Layout is asserted by geometry and computed style, not by pixel comparison: fonts render differently on Windows and on
// the CI runner, so a pixel baseline would either flake or be too loose to catch anything.
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [pageURL] = process.argv.slice(2);
if (!pageURL) {
  console.error("usage: browser-check.mjs <page url>");
  process.exit(2);
}
const outDir = process.env.BROWSER_CHECK_OUT ?? "";
// STRICT_INITIAL_SHIFT=1 also gates the shift while the first view arrives (the page must reserve its space).
const strictInitialShift = process.env.STRICT_INITIAL_SHIFT === "1";
// The most layout shift allowed while the book updates. The target is 0.001; a page that still holds parts not yet
// rebuilt runs with a looser limit set by the caller, which is tightened as those parts are replaced.
const shiftLimit = Number(process.env.SHIFT_LIMIT ?? "0.001");

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
  const lum = (c) => { const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4); }; return 0.2126 * f(c[0]) + 0.7152 * f(c[1]) + 0.0722 * f(c[2]); };
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
  const wide = [...document.querySelectorAll("body *")].filter((e) => { const r = e.getBoundingClientRect(); return r.width > 0 && (r.right > innerWidth + 1 || r.left < -1); }).map((e) => e.tagName.toLowerCase() + "." + e.className).slice(0, 5);
  return { texts, lowContrast, small, hscroll: document.documentElement.scrollWidth > innerWidth, scrollW: document.documentElement.scrollWidth, innerW: innerWidth, wide,
    rows: document.querySelectorAll("table tbody tr").length, cls: window.__cls, csp: window.__csp };
})()`;

const chrome = findChrome();
const profile = mkdtempSync(join(tmpdir(), "browser-check-"));
const port = 9500 + Math.floor(Math.random() * 400);
const proc = spawn(
  chrome,
  [
    `--remote-debugging-port=${port}`,
    `--user-data-dir=${profile}`,
    "--headless=new",
    "--disable-gpu",
    "--no-sandbox",
    "--hide-scrollbars",
    "--no-first-run",
    "about:blank",
  ],
  { stdio: "ignore" },
);

let ws;
try {
  let targets;
  for (let i = 0; i < 60 && !targets; i++) {
    try {
      targets = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
    } catch {
      await sleep(250);
    }
  }
  if (!targets) {
    throw new Error("Chrome did not start");
  }
  const target = targets.find((t) => t.type === "page");
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
  const send = (method, params = {}) =>
    new Promise((resolve) => {
      const i = ++id;
      pending.set(i, resolve);
      ws.send(JSON.stringify({ id: i, method, params }));
    });
  const evaluate = async (expression) =>
    (await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true })).result?.value;
  await send("Runtime.enable");
  await send("Log.enable");
  await send("Page.enable");
  await send("Page.addScriptToEvaluateOnNewDocument", { source: INSTALL });

  const viewports = [
    [320, 700, true],
    [390, 844, true],
    [820, 1100, false],
    [1280, 900, false],
    [1920, 1080, false],
  ];
  for (const [w, h, mobile] of viewports) {
    const tag = `${w}px`;
    consoleErrors = [];
    await send("Emulation.setDeviceMetricsOverride", { width: w, height: h, deviceScaleFactor: 1, mobile });
    await send("Page.navigate", { url: pageURL });
    // wait for the first book, if an edge is running
    let state;
    for (let i = 0; i < 60; i++) {
      await sleep(250);
      state = await evaluate(INSPECT);
      if (state && state.rows > 0) {
        break;
      }
    }
    const withBook = state.rows > 0;
    if (!withBook) {
      notes.push(`${tag}: no order-book rows appeared (no edge running, or the page did not connect)`);
    }
    const clsAtReady = state.cls;
    await sleep(withBook ? 4000 : 500); // let the book update while we watch for layout shift
    state = await evaluate(INSPECT);

    check(
      !state.hscroll,
      `${tag}: horizontal scroll (scrollWidth ${state.scrollW} > ${state.innerW}); wide: ${state.wide.join(", ")}`,
    );
    check(state.wide.length === 0, `${tag}: elements outside the viewport: ${state.wide.join(", ")}`);
    check(state.lowContrast.length === 0, `${tag}: text below contrast: ${state.lowContrast.slice(0, 5).join("; ")}`);
    check(state.small.length === 0, `${tag}: text under 12px: ${state.small.slice(0, 5).join("; ")}`);
    check(state.csp.length === 0, `${tag}: CSP violations: ${state.csp.join("; ")}`);
    check(
      consoleErrors.length === 0,
      `${tag}: console errors: ${consoleErrors
        .slice(0, 3)
        .map((e) => e.slice(0, 200))
        .join(" | ")}`,
    );
    const shiftWhileUpdating = state.cls - clsAtReady;
    check(
      shiftWhileUpdating <= shiftLimit,
      `${tag}: layout shifted ${shiftWhileUpdating.toFixed(4)} while the book updated (limit ${shiftLimit})`,
    );
    if (strictInitialShift) {
      check(
        clsAtReady <= shiftLimit,
        `${tag}: layout shifted ${clsAtReady.toFixed(4)} while loading (limit ${shiftLimit})`,
      );
    } else {
      notes.push(`${tag}: layout shift while loading ${clsAtReady.toFixed(4)} (not gated)`);
    }

    // text at 200%: no horizontal scroll (rem sizing must follow the browser setting)
    await evaluate(`document.documentElement.style.fontSize = "200%"; 0`);
    await sleep(200);
    const big = await evaluate(INSPECT);
    check(!big.hscroll, `${tag} at 200% text: horizontal scroll (${big.scrollW} > ${big.innerW})`);
    await evaluate(`document.documentElement.style.fontSize = ""; 0`);

    if (outDir && failures.some((f) => f.startsWith(tag))) {
      mkdirSync(outDir, { recursive: true });
      const shot = await send("Page.captureScreenshot", { format: "png", captureBeyondViewport: true });
      writeFileSync(join(outDir, `browser-check-${w}.png`), Buffer.from(shot.data, "base64"));
    }
  }
} catch (err) {
  failures.push(String(err.stack ?? err));
} finally {
  try {
    ws?.close();
  } catch {}
  proc.kill();
  await sleep(500);
  rmSync(profile, { recursive: true, force: true });
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
console.log("browser check passed: 5 widths, contrast, text size, overflow, console, CSP, layout shift");
