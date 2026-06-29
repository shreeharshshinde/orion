/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  // `npm run lint` is a dedicated CI step; avoid running ESLint a second time
  // inside Next's memory-intensive production build worker.
  eslint: { ignoreDuringBuilds: true },
  // TypeScript is likewise enforced by the explicit `npm run typecheck` step.
  typescript: { ignoreBuildErrors: true }
};

export default nextConfig;
