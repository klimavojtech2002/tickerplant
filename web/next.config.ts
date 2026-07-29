import type { NextConfig } from "next";

// The page is fully client-side (one "use client" component, no API routes, no
// server actions) — a static export ships as plain files, so the runtime image needs
// no Node server at all.
const nextConfig: NextConfig = {
  output: "export",
};

export default nextConfig;
