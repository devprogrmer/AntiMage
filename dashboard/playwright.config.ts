import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
	testDir: "./e2e",
	fullyParallel: false,
	reporter: "list",
	use: {
		baseURL: "http://127.0.0.1:3000",
		trace: "retain-on-failure",
	},
	workers: 1,
	projects: [
		{
			name: "chromium",
			use: {
				...devices["Desktop Chrome"],
				viewport: { width: 1440, height: 1000 },
				launchOptions: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH
					? { executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH }
					: undefined,
			},
		},
	],
	webServer: {
		command: "npm run dev",
		url: "http://127.0.0.1:3000",
		reuseExistingServer: !process.env.CI,
		timeout: 120_000,
	},
});
