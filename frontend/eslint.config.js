// spec-0.2 D3：eslint flat config（typescript-eslint strict + react-hooks + prettier 兜底）。
// 规则变更先修订 spec-0.2；`any` 豁免须行内 disable 并附理由（development-standards §4）。
import js from "@eslint/js";
import prettier from "eslint-config-prettier";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

export default tseslint.config(
  { ignores: ["dist/", "src/api/generated/"] },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    files: ["**/*.{ts,tsx}"],
    plugins: { "react-hooks": reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "@typescript-eslint/no-explicit-any": "error",
    },
  },
  prettier,
);
