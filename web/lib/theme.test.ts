// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest";

import { applyTheme, nextTheme, readTheme, themeKey, themeScript, writeTheme } from "./theme";

const memory = (initial: Record<string, string> = {}) => {
  const data = new Map(Object.entries(initial));
  return {
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
    data,
  };
};
const throwing = {
  getItem: () => {
    throw new Error("blocked");
  },
  setItem: () => {
    throw new Error("blocked");
  },
  removeItem: () => {
    throw new Error("blocked");
  },
};

describe("theme storage", () => {
  it("reads an explicit choice and treats everything else as the system setting", () => {
    expect(readTheme(memory({ [themeKey]: "dark" }))).toBe("dark");
    expect(readTheme(memory({ [themeKey]: "light" }))).toBe("light");
    for (const junk of ["", "system", "Dark", "auto", "1", "null"]) {
      expect(readTheme(memory({ [themeKey]: junk }))).toBe("system");
    }
    expect(readTheme(memory())).toBe("system");
  });

  it("survives storage that throws", () => {
    expect(readTheme(throwing)).toBe("system");
    expect(() => writeTheme("dark", throwing)).not.toThrow();
  });

  it("writes an explicit choice and clears it for the system setting", () => {
    const s = memory();
    writeTheme("dark", s);
    expect(s.data.get(themeKey)).toBe("dark");
    writeTheme("system", s);
    expect(s.data.has(themeKey)).toBe(false);
  });

  it("cycles system, light, dark, system", () => {
    expect([nextTheme("system"), nextTheme("light"), nextTheme("dark")]).toEqual(["light", "dark", "system"]);
  });
});

describe("applying a theme", () => {
  it("sets data-theme for an explicit choice and removes it for the system setting", () => {
    const root = document.createElement("html");
    applyTheme("dark", root);
    expect(root.getAttribute("data-theme")).toBe("dark");
    applyTheme("light", root);
    expect(root.getAttribute("data-theme")).toBe("light");
    applyTheme("system", root);
    expect(root.hasAttribute("data-theme")).toBe(false);
  });
});

// The snippet in <head> repeats the rule of readTheme and applyTheme. It is run here against the same inputs.
describe("the snippet that runs before first paint", () => {
  afterEach(() => {
    document.documentElement.removeAttribute("data-theme");
    vi.unstubAllGlobals();
  });
  const run = (storage: unknown) => {
    vi.stubGlobal("localStorage", storage);
    new Function(themeScript)();
    return document.documentElement.getAttribute("data-theme");
  };

  it("applies a stored explicit choice", () => {
    expect(run(memory({ [themeKey]: "dark" }))).toBe("dark");
  });
  it("applies a stored light choice", () => {
    expect(run(memory({ [themeKey]: "light" }))).toBe("light");
  });
  it("ignores junk and a missing key", () => {
    expect(run(memory({ [themeKey]: "purple" }))).toBeNull();
    expect(run(memory())).toBeNull();
  });
  it("does not throw when storage does", () => {
    expect(run(throwing)).toBeNull();
  });
  it("is one constant string with no interpolation left in it", () => {
    expect(themeScript).not.toMatch(/\$\{/);
    expect(themeScript).toContain(themeKey);
  });
});
