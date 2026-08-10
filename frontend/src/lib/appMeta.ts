// 前端逻辑层示例（spec-0.4 D5）：可测逻辑一律入 .ts（覆盖率口径 §2.1 的架构约束）。
export function appTitle(version: string): string {
  if (!/^\d+\.\d+\.\d+/.test(version)) {
    throw new Error(`invalid version: ${version}`);
  }
  return `AIRush 控制台 v${version}`;
}
