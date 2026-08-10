// spec-0.4 D5：vitest 范本——用例覆盖正常与异常路径，命名表达行为。
import { describe, expect, it } from "vitest";
import { appTitle } from "./appMeta";

describe("appTitle", () => {
  it("renders semver version into title", () => {
    expect(appTitle("1.2.3")).toBe("AIRush 控制台 v1.2.3");
  });

  it("accepts prerelease suffix", () => {
    expect(appTitle("0.1.0-rc.1")).toBe("AIRush 控制台 v0.1.0-rc.1");
  });

  it("throws on non-semver input", () => {
    expect(() => appTitle("abc")).toThrow("invalid version");
  });
});
