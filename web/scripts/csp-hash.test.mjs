import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { edgeOrigin, hasInlineStyleOrHandler, parseInline, renderHeaders, sha256Source } from "./csp-hash.mjs";

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

test("a > inside a quoted attribute value does not end the start tag", () => {
  const { scripts } = parseInline("<script data-x=\"a>b\" id='c>d'>real()</script>");
  assert.deepEqual(scripts, ["real()"]);
});

test("a slash may separate attributes, and <script/> forms are still seen", () => {
  assert.deepEqual(parseInline("<script/>a()</script>").scripts, ["a()"]);
  assert.deepEqual(parseInline("<script/ >b()</script>").scripts, ["b()"]);
  assert.deepEqual(parseInline('<script/type="module">c()</script>').scripts, ["c()"]);
});

test("offsets come from the original text, not a lower-cased copy", () => {
  // "İ" lower-cases to two code units, which would shift every later offset.
  const { scripts } = parseInline("<p>İİİ</p><script>abcdef()</script>");
  assert.deepEqual(scripts, ["abcdef()"]);
});

test("a script ends at </script followed by whitespace, a slash or >, not at </scripts", () => {
  assert.deepEqual(parseInline('<script>a="</scripts>";b()</script>').scripts, ['a="</scripts>";b()']);
  assert.deepEqual(parseInline("<script>x()</script\n>").scripts, ["x()"]);
  assert.deepEqual(parseInline("<script>y()</script/ >").scripts, ["y()"]);
});

test("CRLF and a lone CR become LF before hashing, as a browser reads them", () => {
  assert.deepEqual(parseInline("<script>a\r\nb\rc</script>").scripts, ["a\nb\nc"]);
});

test("legacy JavaScript types run; a type with parameters or an unknown type is refused or treated as data", () => {
  assert.deepEqual(parseInline('<script type="text/jscript">a()</script>').scripts, ["a()"]);
  assert.deepEqual(parseInline('<script type="application/x-javascript">b()</script>').scripts, ["b()"]);
  assert.deepEqual(parseInline('<script type="speculationrules">{}</script>').scripts, []);
  assert.throws(() => parseInline('<script type="text/javascript;charset=utf-8">a()</script>'), /cannot tell/);
  assert.throws(() => parseInline('<script type="text/x-unknown">a()</script>'), /cannot tell/);
});

test("only HTML whitespace separates attributes: a no-break space does not", () => {
  // The browser reads the value of type as "module" + nbsp + "src=x", which is no known type: refused, not guessed.
  assert.throws(() => parseInline("<script type=module\u00a0src=x>a()</script>"), /cannot tell/);
  // "<script" + nbsp is not a script start tag at all.
  assert.deepEqual(parseInline("<script\u00a0>a()</script>").scripts, []);
});

test("a NUL in a body is refused, because a browser reads it as U+FFFD", () => {
  assert.throws(() => parseInline("<script>a\0b</script>"), /NUL/);
  assert.throws(() => parseInline("<style>a\0b</style>"), /NUL/);
});

test("hasInlineStyleOrHandler: comments and script bodies are removed in one pass", () => {
  assert.ok(hasInlineStyleOrHandler('<script>var a = "<!--";</script><p style="x"><!-- end -->'));
  assert.ok(!hasInlineStyleOrHandler('<!-- <p style="x"> --><p>'));
  // An unclosed comment runs to the end, and a long run of them must not take quadratic time.
  const started = Date.now();
  assert.ok(!hasInlineStyleOrHandler("<!--".repeat(200_000)));
  assert.ok(Date.now() - started < 2000, "slow on unclosed comments");
});

test("hasInlineStyleOrHandler finds attributes, not words in script text", () => {
  assert.ok(hasInlineStyleOrHandler('<p style="color:red">'));
  assert.ok(hasInlineStyleOrHandler("<a onclick=f()>"));
  assert.ok(hasInlineStyleOrHandler("<div class='x' ONCLICK = \"f()\">"));
  assert.ok(!hasInlineStyleOrHandler('<script>var one = "1"; var style = 2;</script><p class="x">'));
  assert.ok(!hasInlineStyleOrHandler('<style>a { b: c }</style><!-- <p style="x"> --><p>'));
  assert.ok(!hasInlineStyleOrHandler('<meta name="description" content="one = two">'));
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
