import type { NextConfig } from "next";

const nextConfig: NextConfig = {
  output: "standalone",
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
