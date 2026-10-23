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

/** Script types a browser runs. Anything else (application/json, importmap, ld+json) is data and is not hashed. */
const JS_TYPES = new Set(["", "module", "text/javascript", "application/javascript", "text/ecmascript"]);

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

function attr(tag, name) {
  const m = new RegExp(`\\s${name}\\s*=\\s*("([^"]*)"|'([^']*)'|([^\\s>]+))`, "i").exec(tag);
  return m ? (m[2] ?? m[3] ?? m[4]) : null;
}

/**
 * parseInline returns the executable inline script bodies and the inline style bodies of one HTML document,
 * and counts what it saw so the caller can check the parse is complete.
 */
export function parseInline(html) {
  const scripts = [];
  const styles = [];
  let scriptTags = 0;
  const open = /<(script|style)(?=[\s>])([^>]*)>/gi;
  let m;
  while ((m = open.exec(html)) !== null) {
    const kind = m[1].toLowerCase();
    const end = html.toLowerCase().indexOf(`</${kind}`, open.lastIndex);
    if (end < 0) {
      throw new Error(`unterminated <${kind}> at offset ${m.index}`);
    }
    const body = html.slice(open.lastIndex, end);
    open.lastIndex = end;
    if (kind === "style") {
      styles.push(body);
      continue;
    }
    scriptTags++;
    const type = (attr(m[2], "type") ?? "").trim().toLowerCase();
    if (attr(m[2], "src") === null && JS_TYPES.has(type)) {
      scripts.push(body);
    }
  }
  const opened = (html.match(/<script(?=[\s>])/gi) ?? []).length;
  if (opened !== scriptTags) {
    throw new Error(`found ${opened} <script> tags but parsed ${scriptTags}`);
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
      if (/\sstyle\s*=/i.test(html) || /\son[a-z]+\s*=\s*["']/i.test(html)) {
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
  if (out.includes("__")) {
    const left = /__[A-Z_]+__/.exec(out);
    if (left) {
      throw new Error(`placeholder left in the output: ${left[0]}`);
    }
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
