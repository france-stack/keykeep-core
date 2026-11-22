import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    // 只测源码目录：.next（构建产物，含陈旧副本）与 node_modules 不参与发现，
    // 否则 `next build` 之后的测试会跑到上一版编译产物里的旧测试。
    include: ["lib/**/*.test.ts", "app/**/*.test.ts"],
    environment: "node",
  },
});
