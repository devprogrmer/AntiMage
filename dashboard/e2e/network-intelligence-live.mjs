import { chromium, expect } from "@playwright/test";

const origin = process.argv[2];
if (!origin || new URL(origin).hostname !== "127.0.0.1") {
	throw new Error("The history browser fixture requires a loopback panel.");
}

const browser = await chromium.launch({
	executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH,
});
try {
	const page = await browser.newPage();
	await page.addInitScript(() => localStorage.setItem("i18nextLng", "en"));
	const login = await page.request.post(origin + "/api/auth/login", {
		headers: { Origin: origin },
		data: { username: "history-admin", password: "fixture-admin-password" },
	});
	expect(login.ok()).toBe(true);
	await page.goto(origin + "/dashboard/network-intelligence/");
	await expect(page.getByRole("heading", { name: "Network Intelligence" })).toBeVisible();
	await page.getByLabel("Node", { exact: true }).selectOption("7");
	const [response] = await Promise.all([
		page.waitForResponse((res) => {
			const url = new URL(res.url());
			return url.origin === origin && url.pathname === "/api/network-intelligence/history"
				&& url.searchParams.get("node_id") === "7" && url.searchParams.get("user_id") === "42";
		}),
		page.getByLabel("History account", { exact: true }).selectOption("42"),
	]);
	expect(response.status()).toBe(200);
	const sample = await response.json();
	expect(sample.storage).toBe("durable");
	expect(sample.points).toHaveLength(1);
	await page.getByText("Observed samples", { exact: true }).click();
	await expect(page.getByRole("cell", { name: "42.5", exact: true })).toBeVisible();
} finally {
	await browser.close();
}
