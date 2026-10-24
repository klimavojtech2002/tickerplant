import { defineConfig, globalIgnores } from "eslint/config";
import nextVitals from "eslint-config-next/core-web-vitals";
import nextTs from "eslint-config-next/typescript";

const eslintConfig = defineConfig([
  ...nextVitals,
  ...nextTs,
  // Override default ignores of eslint-config-next.
  globalIgnores([
    // Default ignores of eslint-config-next:
    ".next/**",
    "out/**",
    "coverage/**",
    "build/**",
    "next-env.d.ts",
  ]),
  // Prices and sizes are decimal strings and bigints; a float anywhere near them can round. These forms
  // are how a string or bigint becomes a binary float, so they are refused (lib/noFloat.test.ts holds the rule to this).
  {
    rules: {
      "no-restricted-syntax": [
        "error",
        { selector: "CallExpression[callee.name='parseFloat']", message: "parseFloat makes a binary float" },
        {
          selector: "CallExpression[callee.object.name='Number'][callee.property.name='parseFloat']",
          message: "Number.parseFloat makes a binary float",
        },
        { selector: "CallExpression[callee.property.name='toFixed']", message: "toFixed formats a binary float" },
        {
          selector: "CallExpression[callee.name='Number']",
          message: "Number() can turn a price or size into a binary float; keep decimals as strings and bigints",
        },
      ],
    },
  },
  // The build and check scripts handle layout measurements and version strings, never prices.
  { files: ["scripts/**/*.mjs"], rules: { "no-restricted-syntax": "off" } },
]);

export default eslintConfig;
