import type { NextConfig } from "next";

// Standalone output for a small runtime image. No configuration is inlined
// at build time: product and domain identity are read per request.
const nextConfig: NextConfig = {
  output: "standalone",
  poweredByHeader: false,
};

export default nextConfig;
