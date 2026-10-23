// @vitest-environment node
// The status contract: these tests read the same golden files the Go tests compare the
// server's output with, so a renamed or retyped field fails both sides. They run in the
// node environment because jsdom's URL breaks fs, and they need a full checkout (the
// goldens live in ../internal/httpapi/testdata, outside this package).

import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

import { parseStatus, parseTelemetry, parseView } from "./types";

const dir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../internal/httpapi/testdata");

function golden(name: string): string {
  return readFileSync(path.join(dir, name), "utf-8");
}

describe("status contract", () => {
  it("finds every golden file", () => {
    for (const name of ["status.json", "telemetry.json", "view.json", "legacy-metrics.json"]) {
      expect(existsSync(path.join(dir, name)), `missing ${path.join(dir, name)}`).toBe(true);
    }
  });

  it("parses the status golden into exactly what the server wrote", () => {
    const text = golden("status.json");
    expect(parseStatus(text)).toEqual(JSON.parse(text));
  });

  it("parses the telemetry golden into exactly what the server wrote", () => {
    const text = golden("telemetry.json");
    expect(parseTelemetry(text)).toEqual(JSON.parse(text));
  });

  it("parses the view golden into exactly what the server wrote", () => {
    const text = golden("view.json");
    const v = parseView(text);
    expect(v).toEqual(JSON.parse(text));
    expect(v?.bids).toHaveLength(3);
    expect(v?.asks).toHaveLength(3);
  });

  it("refuses the old /metrics body", () => {
    expect(parseStatus(golden("legacy-metrics.json"))).toBeNull();
  });

  it("refuses the status golden under a new schema or with a field removed", () => {
    const doc = JSON.parse(golden("status.json")) as Record<string, unknown>;
    expect(parseStatus(JSON.stringify({ ...doc, schema: 2 }))).toBeNull();
    for (const key of Object.keys(doc)) {
      if (key === "latency") continue; // optional
      const without = { ...doc };
      delete without[key];
      expect(parseStatus(JSON.stringify(without)), `without ${key}`).toBeNull();
    }
    expect(parseStatus(JSON.stringify({ ...doc, addedLater: true }))).not.toBeNull();
  });
});
