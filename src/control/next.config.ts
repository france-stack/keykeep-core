import type { NextConfig } from "next";

/** 管理控制台的基础安全头；CSP 只加不破坏 Next 运行的限制项。 */
const securityHeaders = [
  { key: "X-Frame-Options", value: "DENY" },
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "Referrer-Policy", value: "same-origin" },
  { key: "Content-Security-Policy", value: "frame-ancestors 'none'; object-src 'none'; base-uri 'self'" },
  { key: "Permissions-Policy", value: "camera=(), microphone=(), geolocation=()" },
];

const nextConfig: NextConfig = {
  output: "standalone",
  // dev 服务器在 <distDir>/lock 上做单例锁：同一目录起第二个实例会无视 -p 直接退出。
  // 验收执行器需要同时起「业务控制面（维护关闭）」与「维护控制面（显式开启）」两个
  // 实例，用不同的 distDir 让锁互不干扰；生产构建不设该变量，仍输出到 .next。
  distDir: process.env.KEYKEEP_NEXT_DIST_DIR || ".next",
  experimental: {
    // Next 16 在交互终端里启动 dev 时会先弹「版本有安全公告，要不要升级」并让命令
    // 退出等确认（实测：.next-maint 首次启动停在提示上，3101 永不监听）。
    // 本项目的升级由发布流程决定，不由 dev 启动时的提示决定；关掉这条提示。
    agentUpgrade: false,
  },
  reactStrictMode: true,
  async headers() {
    return [{ source: "/:path*", headers: securityHeaders }];
  },
};

export default nextConfig;
