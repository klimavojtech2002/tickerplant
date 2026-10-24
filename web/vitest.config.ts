import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": __dirname },
  },
  test: {
    environment: "jsdom",
    include: ["**/*.test.{ts,tsx}"],
    setupFiles: ["./vitest.setup.ts"],
    globals: true, // lets Testing Library auto-clean the DOM between tests
    coverage: {
      provider: "v8",
      reporter: ["text", "lcov"],
      // Listed on purpose: without include, files no test imports are missing from the report.
      include: ["app/**", "components/**", "lib/**"],
      exclude: ["**/*.test.{ts,tsx}", "lib/testing/**", "**/*.d.ts"],
      // Just under what is measured today, so a drop in coverage fails `pnpm test:coverage`.
      thresholds: { statements: 99.0, lines: 99.4, functions: 98.1, branches: 97.8 },
    },
  },
});
