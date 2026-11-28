import { chromium, expect } from "@playwright/test";

const origin = process.argv[2];
const realWireGuard = process.argv[3] === "--real-wireguard";
if (!origin || new URL(origin).hostname !== "127.0.0.1") {
	throw new Error("The history browser fixture requires a loopback panel.");
}

const browser = await chromium.launch({
	executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH,
});
try {
	const page = await browser.newPage();
	const failedRequests = [];
	const apiRequests = [];
	page.on("request", (request) => {
		if (new URL(request.url()).pathname.startsWith("/api/")) {
			apiRequests.push(`${request.method()} ${request.url()}`);
		}
	});
	page.on("requestfailed", (request) => failedRequests.push(`${request.method()} ${request.url()}: ${request.failure()?.errorText || "failed"}`));
	page.on("pageerror", (error) => failedRequests.push(`page error: ${error.message}`));
	await page.addInitScript(() => localStorage.setItem("i18nextLng", "en"));
	const login = await page.request.post(origin + "/api/auth/login", {
		headers: { Origin: origin },
		data: { username: "history-admin", password: "fixture-admin-password" },
	});
	expect(login.ok()).toBe(true);
	await page.goto(origin + "/dashboard/network-intelligence/");
	await expect(page.getByRole("heading", { name: "Network Intelligence" })).toBeVisible();
	const node = page.getByLabel("Node", { exact: true });
	if (realWireGuard) {
		const [flowResponse] = await Promise.all([
			page.waitForResponse((res) => {
				const url = new URL(res.url());
				return url.origin === origin && url.pathname === "/api/network-intelligence/node"
					&& url.searchParams.get("node_id") === "7" && !url.searchParams.has("diagnose");
			}),
			node.selectOption("7"),
		]);
		expect(flowResponse.status()).toBe(200);
		const { flows } = await flowResponse.json();
		expect(flows.length).toBeGreaterThan(0);
		expect(flows.every((flow) => flow.user_id === 42 && flow.node_id === 7)).toBe(true);
		expect(flows.map((flow) => flow.protocol)).toEqual(expect.arrayContaining(["tcp", "udp"]));
		const cells = page.getByRole("cell");
		await expect(cells.getByText("tcp", { exact: true }).first()).toBeVisible();
		await expect(cells.getByText("udp", { exact: true }).first()).toBeVisible();
	} else {
		await node.selectOption("7");
	}
	let response;
	try {
		[response] = await Promise.all([
			page.waitForResponse((res) => {
				const url = new URL(res.url());
				return url.origin === origin && url.pathname === "/api/network-intelligence/history"
					&& url.searchParams.get("node_id") === "7" && url.searchParams.get("user_id") === "42";
			}),
			page.getByLabel("History account", { exact: true }).selectOption("42"),
		]);
	} catch (error) {
		const historyState = await page.evaluate(() => {
			const select = document.querySelector("#network-history-user");
			return {
				value: select?.value ?? null,
				options: select ? Array.from(select.options, (option) => `${option.value}:${option.text}`) : [],
				pageText: document.body.innerText.slice(-2000),
			};
		});
		throw new Error(`history request was not observed; API requests: ${apiRequests.join(" | ") || "none"}; browser failures: ${failedRequests.join(" | ") || "none"}; history select: ${JSON.stringify(historyState)}; cause: ${error}`);
	}
	expect(response.status()).toBe(200);
	const sample = await response.json();
	expect(sample.storage).toBe("durable");
	expect(sample.points).toHaveLength(1);
	expect(sample.points[0]).toMatchObject({ node_id: 7, user_id: 42 });
	await page.getByText("Observed samples", { exact: true }).click();
	if (realWireGuard) {
		const observedAt = await page.evaluate((at) => new Date(at).toLocaleString(), sample.points[0].at);
		await expect(page.getByRole("cell", { name: observedAt, exact: true })).toBeVisible();
	} else {
		await expect(page.getByRole("cell", { name: "42.5", exact: true })).toBeVisible();
	}
} finally {
	await browser.close();
}
