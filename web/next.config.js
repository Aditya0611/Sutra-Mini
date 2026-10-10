/** @type {import('next').NextConfig} */
const nextConfig = {
  async redirects() {
    return [{ source: "/", destination: "/sync", permanent: false }];
  },
  async rewrites() {
    const api = process.env.API_URL || "http://127.0.0.1:8080";
    return [{ source: "/backend/:path*", destination: `${api}/:path*` }];
  },
};

module.exports = nextConfig;
