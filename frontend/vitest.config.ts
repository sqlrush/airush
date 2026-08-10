// spec-0.4 D3：前端覆盖率口径（规则 4）——仅统计 .ts 逻辑层，.tsx 组件与生成代码剔除。
// 阈值 70%，随 COVER_ENFORCE 激活（spec-1.1 起 CI 默认开）。
import { defineConfig } from "vitest/config";

const enforce = process.env.COVER_ENFORCE === "1";

export default defineConfig({
  test: {
    include: ["src/**/*.test.ts"],
    coverage: {
      provider: "v8",
      include: ["src/**/*.ts"],
      exclude: ["src/**/*.test.ts", "src/api/generated/**"],
      thresholds: enforce
        ? { lines: 70, functions: 70, branches: 70, statements: 70 }
        : undefined,
    },
  },
});
