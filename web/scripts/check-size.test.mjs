import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

import { gzipBytes, jsFiles } from "./check-size.mjs";

const script = join(dirname(fileURLToPath(import.meta.url)), "check-size.mjs");

function exportOf(files) {
  const dir = mkdtempSync(join(tmpdir(), "check-size-"));
  for (const [name, body] of Object.entries(files)) {
    const path = join(dir, "_next", "static", name);
    mkdirSync(dirname(path), { recursive: true });
    writeFileSync(path, body);
  }
  return dir;
}
const run = (dir, budget) => {
  try {
    return {
      code: 0,
      out: execFileSync("node", [script, dir, String(budget)], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }),
    };
  } catch (err) {
    return { code: err.status, out: String(err.stdout) + String(err.stderr) };
  }
};

test("finds .js files at any depth and ignores the rest", () => {
  const dir = exportOf({ "chunks/a.js": "a", "b.js": "b", "c.css": "c", "chunks/deep/d.js": "d" });
  assert.equal(jsFiles(join(dir, "_next", "static")).length, 3);
});

test("passes under the budget and fails over it", () => {
  const dir = exportOf({ "a.js": "x".repeat(50_000) + Math.random() });
  const size = gzipBytes(jsFiles(join(dir, "_next", "static")));
  assert.ok(size > 0);
  assert.equal(run(dir, size + 1).code, 0);
  assert.equal(run(dir, size).code, 0);
  const over = run(dir, size - 1);
  assert.equal(over.code, 1);
  assert.match(over.out, /over budget/);
});

test("refuses a missing build and a bad budget", () => {
  const missing = run(join(tmpdir(), "no-such-export"), 1000);
  assert.equal(missing.code, 1);
  assert.match(missing.out, /run the build first/); // the friendly message, not a stack trace
  assert.doesNotMatch(missing.out, /ENOENT/);
  const dir = exportOf({ "a.js": "x" });
  assert.equal(run(dir, "abc").code, 2);
  assert.equal(run(dir, 0).code, 2);
});
