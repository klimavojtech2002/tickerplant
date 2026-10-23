// Renders security-headers.conf from its template and the static export.
//
//   NEXT_PUBLIC_EDGE_URL=http://127.0.0.1:8080 node scripts/csp-hash.mjs out security-headers.conf.template > security-headers.conf
//
// The Content-Security-Policy lists the SHA-256 of every inline script and style body in the export, so the
// page runs no inline code the build did not produce and the policy never needs 'unsafe-inline'. The hashes
// come from the export that is shipped: Next writes the chunk file names into the inline scripts, so a list
// copied from another build would be wrong. Every failure here stops the build rather than ship a policy that
// is too loose or blocks the page.
import { createHash } from "node:crypto";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * The script types a browser runs as classic or module code (the HTML standard's list of JavaScript MIME type
 * essences, plus "" and "module"). The comparison is exact: a type with parameters is not on the list.
 */
const JS_TYPES = new Set([
  "",
  "module",
  "application/ecmascript",
  "application/javascript",
  "application/x-ecmascript",
  "application/x-javascript",
  "text/ecmascript",
  "text/javascript",
  "text/javascript1.0",
  "text/javascript1.1",
  "text/javascript1.2",
  "text/javascript1.3",
  "text/javascript1.4",
  "text/javascript1.5",
  "text/jscript",
  "text/livescript",
  "text/x-ecmascript",
  "text/x-javascript",
]);

/** Types a browser never runs; their bodies are data. Any other type is refused: the build cannot tell if it runs. */
const DATA_TYPES = new Set(["application/json", "application/ld+json", "importmap", "speculationrules"]);

/** Every file with the given extension under dir, sorted, so the output does not depend on directory order. */
export function filesUnder(dir, ext) {
  const found = [];
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      found.push(...filesUnder(path, ext));
    } else if (name.endsWith(ext)) {
      found.push(path);
    }
  }
  return found;
}

/**
 * readStartTag reads the attributes of a start tag whose name ends at `from`. It follows the HTML rules that
 * matter here: whitespace and "/" separate attributes, and a ">" inside a quoted value does not end the tag.
 * It returns the lower-cased attribute map and the index just after the closing ">".
 */
function readStartTag(html, from) {
  const attrs = new Map();
  let i = from;
  for (;;) {
    while (i < html.length && /[\s/]/.test(html[i])) {
      i++;
    }
    if (i >= html.length) {
      throw new Error(`unterminated start tag at offset ${from}`);
    }
    if (html[i] === ">") {
      return { attrs, end: i + 1 };
    }
    let j = i;
    while (j < html.length && !/[\s/>=]/.test(html[j])) {
      j++;
    }
    if (j === i) {
      j++; // a stray "=" starts an attribute name, as in the standard
    }
    const name = html.slice(i, j).toLowerCase();
    i = j;
    while (i < html.length && /\s/.test(html[i])) {
      i++;
    }
    let value = "";
    if (html[i] === "=") {
      i++;
      while (i < html.length && /\s/.test(html[i])) {
        i++;
      }
      if (html[i] === '"' || html[i] === "'") {
        const close = html.indexOf(html[i], i + 1);
        if (close < 0) {
          throw new Error(`unterminated attribute value at offset ${i}`);
        }
        value = html.slice(i + 1, close);
        i = close + 1;
      } else {
        let k = i;
        while (k < html.length && !/[\s>]/.test(html[k])) {
          k++;
        }
        value = html.slice(i, k);
        i = k;
      }
    }
    if (!attrs.has(name)) {
      attrs.set(name, value);
    }
  }
}

/** A browser turns CRLF and a lone CR into LF before it tokenises, and the policy hashes the result. */
function normalizeNewlines(text) {
  return text.replace(/\r\n?/g, "\n");
}

/**
 * parseInline returns the executable inline script bodies and the inline style bodies of one HTML document.
 * It throws if it cannot be sure it saw every script: the count of "<script" openings must equal the count of
 * start tags it parsed.
 */
export function parseInline(html) {
  const scripts = [];
  const styles = [];
  const open = /<(script|style)(?=[\s/>])/gi;
  let parsed = 0;
  let m;
  while ((m = open.exec(html)) !== null) {
    const kind = m[1].toLowerCase();
    const tag = readStartTag(html, open.lastIndex);
    // The element ends at "</script" followed by whitespace, "/" or ">" (the standard's rule), not at any "</script".
    const close = new RegExp(`</${kind}(?=[\\s/>])`, "gi");
    close.lastIndex = tag.end;
    const found = close.exec(html);
    if (found === null) {
      throw new Error(`unterminated <${kind}> at offset ${m.index}`);
    }
    const body = normalizeNewlines(html.slice(tag.end, found.index));
    open.lastIndex = found.index;
    if (kind === "style") {
      styles.push(body);
      continue;
    }
    parsed++;
    if (tag.attrs.has("src")) {
      continue;
    }
    const type = (tag.attrs.get("type") ?? "").trim().toLowerCase();
    if (JS_TYPES.has(type)) {
      scripts.push(body);
    } else if (!DATA_TYPES.has(type)) {
      throw new Error(`a script of type ${JSON.stringify(type)} at offset ${m.index}: cannot tell if it runs`);
    }
  }
  const opened = (html.match(/<script(?=[\s/>])/gi) ?? []).length;
  if (opened !== parsed) {
    throw new Error(`found ${opened} <script> tags but parsed ${parsed}`);
  }
  return { scripts, styles };
}

export function sha256Source(body) {
  return `'sha256-${createHash("sha256").update(Buffer.from(body, "utf8")).digest("base64")}'`;
}

/** The origin of the edge URL, or an error: the build must not guess where the page may connect. */
export function edgeOrigin(edgeUrl) {
  if (typeof edgeUrl !== "string" || edgeUrl === "") {
    throw new Error("NEXT_PUBLIC_EDGE_URL is not set");
  }
  let url;
  try {
    url = new URL(edgeUrl);
  } catch {
    throw new Error(`NEXT_PUBLIC_EDGE_URL is not a URL: ${JSON.stringify(edgeUrl)}`);
  }
  if ((url.protocol !== "http:" && url.protocol !== "https:") || url.origin === "null") {
    throw new Error(`NEXT_PUBLIC_EDGE_URL must be an http or https URL: ${JSON.stringify(edgeUrl)}`);
  }
  return url.origin;
}

function replaceOnce(text, placeholder, value) {
  if (text.split(placeholder).length !== 2) {
    throw new Error(`the template must contain ${placeholder} exactly once`);
  }
  return text.replace(placeholder, () => value);
}

/**
 * hasInlineStyleOrHandler reports a style attribute or an event-handler attribute on any tag outside script and
 * style bodies and comments. A hash cannot cover either, so the build should say so.
 */
export function hasInlineStyleOrHandler(html) {
  const bare = html.replace(/<!--[\s\S]*?-->/g, "").replace(/<(script|style)(?=[\s/>])[\s\S]*?<\/\1(?=[\s/>])/gi, "");
  const tag = /<[a-zA-Z][^\s/>]*/g;
  let m;
  while ((m = tag.exec(bare)) !== null) {
    const { attrs, end } = readStartTag(bare, tag.lastIndex);
    if ([...attrs.keys()].some((name) => name === "style" || /^on[a-z]+$/.test(name))) {
      return true;
    }
    tag.lastIndex = end;
  }
  return false;
}

/** renderHeaders fills the template from the files in outDir. */
export function renderHeaders(template, outDir, edgeUrl) {
  const origin = edgeOrigin(edgeUrl);
  if (!statSync(outDir, { throwIfNoEntry: false })?.isDirectory()) {
    throw new Error(`${outDir} is not a directory; run the build first`);
  }
  const scripts = new Set();
  const styles = new Set();
  const pages = filesUnder(outDir, ".html");
  let indexSeen = false;
  for (const file of pages) {
    const html = readFileSync(file, "utf8");
    const found = parseInline(html);
    found.scripts.forEach((s) => scripts.add(sha256Source(s)));
    found.styles.forEach((s) => styles.add(sha256Source(s)));
    if (file === join(outDir, "index.html")) {
      indexSeen = true;
      if (found.scripts.length === 0) {
        throw new Error("index.html has no inline script; the parse is wrong or the export changed");
      }
      // A style attribute or an inline handler cannot be covered by a hash; React sets styles through the
      // CSSOM, which the policy does not block, so the page itself has none. If one appears, say so.
      if (hasInlineStyleOrHandler(html)) {
        throw new Error("index.html has a style attribute or an inline event handler, which the policy would block");
      }
    }
  }
  if (!indexSeen) {
    throw new Error(`${outDir}/index.html not found`);
  }
  let out = replaceOnce(template, "__SCRIPT_HASHES__", [...scripts].sort().join(" "));
  out = replaceOnce(out, "__STYLE_HASHES__", [...styles].sort().join(" "));
  out = replaceOnce(out, "__CONNECT_ORIGIN__", origin);
  const left = /__[A-Z_]+__/.exec(out);
  if (left) {
    throw new Error(`placeholder left in the output: ${left[0]}`);
  }
  return out;
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [outDir, templatePath] = process.argv.slice(2);
  if (!outDir || !templatePath) {
    console.error("usage: csp-hash.mjs <export dir> <template>   (edge URL in NEXT_PUBLIC_EDGE_URL)");
    process.exit(2);
  }
  try {
    process.stdout.write(renderHeaders(readFileSync(templatePath, "utf8"), outDir, process.env.NEXT_PUBLIC_EDGE_URL));
  } catch (err) {
    console.error(`csp-hash: ${err.message}`);
    process.exit(1);
  }
}
