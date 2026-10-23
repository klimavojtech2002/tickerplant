import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { edgeOrigin, parseInline, renderHeaders, sha256Source } from "./csp-hash.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const TEMPLATE =
  "script-src 'self' __SCRIPT_HASHES__; style-src 'self' __STYLE_HASHES__; connect-src 'self' __CONNECT_ORIGIN__;";

/** A throwaway export directory holding the given files (path -> html). */
function exportOf(files) {
  const dir = mkdtempSync(join(tmpdir(), "csp-hash-"));
  for (const [name, html] of Object.entries(files)) {
    const path = join(dir, name);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, html);
  }
  return dir;
}

const page = (...inner) => `<html><head>${inner.join("")}</head><body></body></html>`;

test("the hash is the SHA-256 of the exact body, checked against openssl", () => {
  const body = 'self.__next_f.push([1,"a\\nb"]) // é';
  const dir = mkdtempSync(join(tmpdir(), "csp-hash-"));
  const file = join(dir, "body.txt");
  writeFileSync(file, body, "utf8");
  let expected;
  try {
    expected = execFileSync("openssl", ["dgst", "-sha256", "-binary", file]).toString("base64");
  } catch {
    expected = null; // no openssl on this machine: the known digest below still pins the value
  }
  if (expected !== null) {
    assert.equal(sha256Source(body), `'sha256-${expected}'`);
  }
  // SHA-256 of the empty string, a published value.
  assert.equal(sha256Source(""), "'sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU='");
});

test("parseInline takes inline scripts and styles, and skips external and data scripts", () => {
  const html = page(
    "<script>one()</script>",
    '<script src="/a.js" async></script>',
    '<script type="application/json">{"a":1}</script>',
    '<script type="importmap">{}</script>',
    '<script type="module">two()</script>',
    '<SCRIPT TYPE="text/javascript">three()</SCRIPT>',
    "<style>a{color:red}</style>",
  );
  const { scripts, styles } = parseInline(html);
  assert.deepEqual(scripts, ["one()", "two()", "three()"]);
  assert.deepEqual(styles, ["a{color:red}"]);
});

test("parseInline keeps the body as it is: no trimming and no entity decoding", () => {
  const { scripts } = parseInline("<script> a &amp; b </script>");
  assert.deepEqual(scripts, [" a &amp; b "]);
});

test("parseInline refuses a script that never ends", () => {
  assert.throws(() => parseInline("<script>x()"), /unterminated/);
});

test("parseInline refuses a page where a script tag appears inside a body", () => {
  assert.throws(() => parseInline('<script>var s = "<script >";</script>'), /found 2 <script> tags but parsed 1/);
});

test("renderHeaders hashes the union of the pages, once each, in a fixed order", () => {
  const dir = exportOf({
    "index.html": page("<script>b()</script>", "<script>a()</script>"),
    "404.html": page("<script>a()</script>", "<style>x{}</style>"),
    "sub/page.html": page("<script>c()</script>"),
  });
  const out = renderHeaders(TEMPLATE, dir, "http://127.0.0.1:8080");
  const hashes = ["a()", "b()", "c()"].map(sha256Source).sort();
  assert.ok(out.includes(`script-src 'self' ${hashes.join(" ")};`), out);
  assert.ok(out.includes(`style-src 'self' ${sha256Source("x{}")};`), out);
  assert.ok(out.includes("connect-src 'self' http://127.0.0.1:8080;"), out);
  assert.ok(!out.includes("__"), "a placeholder is left");
});

test("renderHeaders fails closed", () => {
  const ok = { "index.html": page("<script>a()</script>") };
  assert.throws(() => renderHeaders(TEMPLATE, join(tmpdir(), "csp-hash-missing"), "http://x"), /not a directory/);
  assert.throws(
    () => renderHeaders(TEMPLATE, exportOf({ "other.html": page("<script>a()</script>") }), "http://x"),
    /index.html not found/,
  );
  assert.throws(() => renderHeaders(TEMPLATE, exportOf({ "index.html": page() }), "http://x"), /no inline script/);
  assert.throws(
    () =>
      renderHeaders(TEMPLATE, exportOf({ "index.html": page("<script>a()</script>", '<p style="x:y">') }), "http://x"),
    /style attribute/,
  );
  assert.throws(
    () =>
      renderHeaders(
        TEMPLATE,
        exportOf({ "index.html": page("<script>a()</script>", '<a onclick="f()">') }),
        "http://x",
      ),
    /event handler/,
  );
  assert.throws(() => renderHeaders("no placeholders", exportOf(ok), "http://x"), /exactly once/);
  assert.throws(() => renderHeaders(`${TEMPLATE} __SCRIPT_HASHES__`, exportOf(ok), "http://x"), /exactly once/);
});

test("the error pages may hold style attributes: only index.html is held to the strict rule", () => {
  const dir = exportOf({
    "index.html": page("<script>a()</script>"),
    "404.html": page("<script>a()</script>", '<div style="color:red"></div>'),
  });
  assert.doesNotThrow(() => renderHeaders(TEMPLATE, dir, "http://x"));
});

test("edgeOrigin gives scheme, host and port, and refuses everything else", () => {
  assert.equal(edgeOrigin("http://127.0.0.1:8080"), "http://127.0.0.1:8080");
  assert.equal(edgeOrigin("https://edge.example.com/api/x?y=1"), "https://edge.example.com");
  assert.equal(edgeOrigin("http://[::1]:8080/"), "http://[::1]:8080");
  for (const bad of [
    undefined,
    "",
    "not a url",
    "ftp://host",
    "javascript:alert(1)",
    "data:text/html,x",
    "file:///etc",
  ]) {
    assert.throws(() => edgeOrigin(bad), /NEXT_PUBLIC_EDGE_URL/, String(bad));
  }
});

test("an edge URL cannot inject another directive", () => {
  assert.equal(edgeOrigin("http://host/;script-src *"), "http://host");
  assert.throws(() => edgeOrigin("http://host:80;script-src%20*"), /not a URL/);
});

test("the real template has each placeholder exactly once", () => {
  const template = readFileSync(join(here, "..", "security-headers.conf.template"), "utf8");
  for (const p of ["__SCRIPT_HASHES__", "__STYLE_HASHES__", "__CONNECT_ORIGIN__"]) {
    assert.equal(template.split(p).length, 2, p);
  }
  assert.ok(!/unsafe-inline|unsafe-eval/.test(template));
  const adds = template.split("\n").filter((l) => l.startsWith("add_header"));
  assert.ok(adds.length >= 5 && adds.every((l) => l.endsWith("always;")), "every header must use always");
});

test("the real nginx.conf includes the headers wherever it sets its own add_header", () => {
  const conf = readFileSync(join(here, "..", "nginx.conf"), "utf8")
    .split("\n")
    .filter((line) => !line.trim().startsWith("#"))
    .join("\n");
  // Split the server block into the server level and one chunk per location.
  const chunks = conf.split(/\n\s*location /).slice(1);
  assert.ok(chunks.length >= 3);
  for (const chunk of chunks) {
    assert.ok(
      chunk.includes("include /etc/nginx/security-headers.conf;"),
      `a location lacks the shared headers:\n${chunk}`,
    );
  }
  assert.ok(conf.indexOf("include /etc/nginx/security-headers.conf;") < conf.indexOf("location "));
  for (const p of [
    "client_body_temp_path",
    "proxy_temp_path",
    "fastcgi_temp_path",
    "uwsgi_temp_path",
    "scgi_temp_path",
    "pid",
  ]) {
    assert.match(conf, new RegExp(`${p} /tmp`), p);
  }
  assert.ok(!/^\s*user\s/m.test(conf) && !conf.includes("conf.d"));
});
