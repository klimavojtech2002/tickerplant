// Fails when the JavaScript of the built page is larger than its budget (gzip, the size a visitor downloads).
//
//   node scripts/check-size.mjs out 204800
//
// The page has no runtime dependency beyond React and Next, so growth here is the page's own code. The budget is raised by a
// deliberate edit of the number in the CI workflow, never by accident.
import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { gzipSync } from "node:zlib";

export function jsFiles(dir) {
  const out = [];
  for (const name of readdirSync(dir).sort()) {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      out.push(...jsFiles(path));
    } else if (name.endsWith(".js")) {
      out.push(path);
    }
  }
  return out;
}

export function gzipBytes(files) {
  return files.reduce((sum, f) => sum + gzipSync(readFileSync(f), { level: 6 }).length, 0);
}

const [dir, budgetArg] = process.argv.slice(2);
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const budget = Number(budgetArg);
  if (!dir || !Number.isInteger(budget) || budget <= 0) {
    console.error("usage: check-size.mjs <export dir> <budget in bytes>");
    process.exit(2);
  }
  const staticDir = join(dir, "_next", "static");
  const files = existsSync(staticDir) ? jsFiles(staticDir) : [];
  if (files.length === 0) {
    console.error(`no JavaScript found under ${dir}/_next/static; run the build first`);
    process.exit(1);
  }
  const size = gzipBytes(files);
  const kb = (n) => `${(n / 1024).toFixed(1)} KB`;
  console.log(`JavaScript: ${files.length} files, ${kb(size)} gzip (budget ${kb(budget)})`);
  if (size > budget) {
    console.error(`over budget by ${kb(size - budget)}`);
    process.exit(1);
  }
}
