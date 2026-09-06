import { defineConfig } from "vitest/config";
import path from "node:path";
import { fileURLToPath } from "node:url";
import tsconfigPaths from "vite-tsconfig-paths";

const dashboardDir = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
	root: dashboardDir,
	plugins: [tsconfigPaths({ root: dashboardDir })],
	test: {
		include: ["src/**/*.test.{ts,tsx}"],
	},
});
