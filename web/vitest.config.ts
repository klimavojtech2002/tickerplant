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
    globals: true, // lets Testing Library auto-clean the DOM between tests
  },
});
