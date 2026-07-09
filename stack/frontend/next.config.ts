import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
  // pdf.js (via react-pdf) has an optional Node-only `canvas` dependency that
  // Turbopack tries to resolve in the browser bundle and fails on. Aliasing it
  // to an empty stub keeps the text-extraction and viewer paths - which never
  // use the Node canvas - resolvable. The worker is served from public/ (copied
  // by scripts/copy-pdf-worker.mjs), not via the bundler's asset-URL pattern.
  turbopack: {
    resolveAlias: {
      canvas: "./scripts/empty-module.js",
    },
  },
  // Same-origin proxy to the Go backend so its HttpOnly SameSite=Strict
  // session cookie is first-party, for the API and the demo media the player
  // streams. This is the local-dev path only: deployed environments route
  // /api/* to the backend at the load balancer, and the standalone build
  // freezes rewrites() at build time, so a runtime BACKEND_URL cannot
  // reconfigure it there.
  async rewrites() {
    const backend = process.env.BACKEND_URL || "http://localhost:8080";
    return [
      {
        source: "/api/:path*",
        destination: `${backend}/api/:path*`,
      },
      {
        source: "/demo/:path*",
        destination: `${backend}/demo/:path*`,
      },
    ];
  },
};

export default nextConfig;
