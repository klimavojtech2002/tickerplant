// @vitest-environment node
import { ESLint } from "eslint";
import { beforeAll, describe, expect, it } from "vitest";

// The no-float rule lives in eslint.config.mjs. These tests lint snippets with that real config, and the
// real price/size modules, so removing or weakening the rule makes a test fail.
const eslint = new ESLint({ overrideConfigFile: "eslint.config.mjs" });

async function messages(code: string, filePath = "lib/snippet.ts"): Promise<string[]> {
  const [result] = await eslint.lintText(code, { filePath });
  return (result?.messages ?? []).filter((m) => m.ruleId === "no-restricted-syntax").map((m) => m.message);
}

describe("no-float rule", () => {
  // Loading the Next config and its parser takes several seconds the first time.
  beforeAll(async () => {
    await eslint.calculateConfigForFile("lib/snippet.ts");
  }, 60_000);

  const refused: [string, string][] = [
    ["parseFloat", 'const x = parseFloat("1.5");'],
    ["Number.parseFloat", 'const x = Number.parseFloat("1.5");'],
    ["toFixed", "const x = (1.5).toFixed(2);"],
    ["toFixed on a name", "declare const n: number; const x = n.toFixed(1);"],
    ["Number() of a string", 'const x = Number("1.5");'],
    ["Number() of a bigint", "const x = Number(5n);"],
  ];
  it.each(refused)("refuses %s", async (_name, code) => {
    expect(await messages(code)).toHaveLength(1);
  });

  const allowed: [string, string][] = [
    ["Number.isSafeInteger", "const x = Number.isSafeInteger(3);"],
    ["Number.NEGATIVE_INFINITY", "const x = Number.NEGATIVE_INFINITY;"],
    ["Number.MAX_SAFE_INTEGER", "const x = Number.MAX_SAFE_INTEGER;"],
    ["Math.floor", "const x = Math.floor(7 / 2);"],
    ["BigInt", 'const x = BigInt("12");'],
  ];
  it.each(allowed)("allows %s", async (_name, code) => {
    expect(await messages(code)).toEqual([]);
  });

  it("holds the real price and size modules to the rule", async () => {
    for (const file of ["lib/format.ts", "lib/types.ts", "lib/ticks.ts", "lib/store.ts"]) {
      const [result] = await eslint.lintFiles([file]);
      expect(result?.messages.map((m) => `${file}:${m.line} ${m.message}`)).toEqual([]);
    }
  });
});
