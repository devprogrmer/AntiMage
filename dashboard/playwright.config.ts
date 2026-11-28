import { defineConfig } from "@playwright/test";

export default defineConfig({
	testDir: "./e2e",
	workers: 1,
	use: {
		baseURL: "http://127.0.0.1:3018",
		launchOptions: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
			? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
			: undefined,
	},
	webServer: {
		command: "npm run dev -- --port 3018 --strictPort",
		url: "http://127.0.0.1:3018",
		timeout: 120000,
		reuseExistingServer: false,
	},
});
