// Checks a running dashboard container from the outside: the headers on a page, a hashed asset, the
// favicon and a 404, and that every inline script in the served page is allowed by the served policy.
//
//   node scripts/check-served.mjs http://127.0.0.1:18080 http://127.0.0.1:8080
//
// (second argument: the edge URL the page was built for). Run by scripts/check-image.sh.
import assert from "node:assert/strict";

import { edgeOrigin, parseInline, sha256Source } from "./csp-hash.mjs";

const [base, edge] = process.argv.slice(2);
if (!base || !edge) {
  console.error("usage: check-served.mjs <base url> <edge url>");
  process.exit(2);
}

async function get(path, headers = {}) {
  // fetch decompresses the body but keeps the Content-Encoding header, which is what is asserted below.
  const res = await fetch(base + path, { headers, redirect: "manual" });
  return { res, body: await res.text() };
}

function assertSecurityHeaders(res, where) {
  assert.equal(res.headers.get("x-content-type-options"), "nosniff", `${where}: nosniff`);
  assert.equal(res.headers.get("referrer-policy"), "no-referrer", `${where}: referrer-policy`);
  assert.match(res.headers.get("permissions-policy") ?? "", /camera=\(\)/, `${where}: permissions-policy`);
  assert.equal(res.headers.get("cross-origin-opener-policy"), "same-origin", `${where}: COOP`);
  const csp = res.headers.get("content-security-policy") ?? "";
  assert.ok(csp.includes("default-src 'self'"), `${where}: csp default-src`);
  assert.ok(csp.includes("frame-ancestors 'none'"), `${where}: csp frame-ancestors`);
  assert.ok(!/unsafe-inline|unsafe-eval|\*/.test(csp), `${where}: csp must not be loosened: ${csp}`);
  assert.equal(res.headers.get("server"), "nginx", `${where}: server header must not carry a version`);
  return csp;
}

const page = await get("/", { "accept-encoding": "gzip" });
// A style attribute in the prerendered page would be blocked by the policy (a hash cannot cover it), so there must be none.
assert.ok(!/<[a-z][^>]*\sstyle\s*=/i.test(page.body), "the served page contains a style attribute");
assert.equal(page.res.status, 200, "/ status");
const csp = assertSecurityHeaders(page.res, "/");
assert.match(page.res.headers.get("cache-control") ?? "", /no-cache/, "/: cache-control");
assert.equal(page.res.headers.get("content-encoding"), "gzip", "/: gzip");

// Every inline script the page carries must be allowed, and the connect origin must be the edge's.
const { scripts } = parseInline(page.body);
assert.ok(scripts.length > 0, "the served page has inline scripts");
for (const s of scripts) {
  assert.ok(csp.includes(sha256Source(s)), `an inline script of / has no hash in the policy: ${s.slice(0, 60)}`);
}
assert.ok(csp.includes(`connect-src 'self' ${edgeOrigin(edge)}`), `connect-src does not name ${edge}: ${csp}`);

// A hashed asset: long cache, right type, gzip when asked.
const asset = /<script src="(\/_next\/static\/[^"]+\.js)"/.exec(page.body)?.[1];
assert.ok(asset, "the page names a hashed script");
const js = await get(asset, { "accept-encoding": "gzip" });
assert.equal(js.res.status, 200, "asset status");
assertSecurityHeaders(js.res, asset);
assert.equal(js.res.headers.get("cache-control"), "public, max-age=31536000, immutable", "asset cache-control");
assert.match(js.res.headers.get("content-type") ?? "", /javascript/, "asset content-type");
assert.equal(js.res.headers.get("content-encoding"), "gzip", "asset: gzip");

// The edge URL the policy allows must be the one built into the page, or the page would connect to a place
// the policy forbids (or the policy would allow a place the page never uses).
const chunks = [...page.body.matchAll(/<script src="(\/_next\/static\/[^"]+\.js)"/g)].map((m) => m[1]);
let inlined = false;
for (const chunk of chunks) {
  if ((await get(chunk)).body.includes(edge.replace(/\/$/, ""))) {
    inlined = true;
    break;
  }
}
assert.ok(inlined, `no script of the page contains the edge URL ${edge}; the build and the policy disagree`);

// The error page keeps the policy, and an unknown path is a real 404.
const missing = await get("/no-such-page");
assert.equal(missing.res.status, 404, "404 status");
assertSecurityHeaders(missing.res, "404");

const direct = await get("/404.html");
assert.equal(direct.res.status, 404, "the internal error page is not served directly");

const icon = await get("/favicon.ico");
assert.equal(icon.res.status, 200, "favicon status");
assertSecurityHeaders(icon.res, "favicon");

console.log(`served checks passed: ${scripts.length} inline scripts hashed, edge ${edgeOrigin(edge)}`);
