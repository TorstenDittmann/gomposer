import { defineConfig } from "docia";

export default defineConfig({
  srcDir: "book",
  outDir: "dist",
  site: {
    title: "gomposer",
    description:
      "gomposer — a Composer-compatible PHP dependency installer written in Go, with pnpm/bun-style workspaces for monorepos.",
    language: "en",
  },
});
