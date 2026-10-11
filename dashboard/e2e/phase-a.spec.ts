import { expect, test, type Page } from "@playwright/test";

const diagnostic = {
	id: "diag-smoke-001",
	source: "node-connectivity",
	resource_type: "node",
	resource_id: "203.0.113.17",
	severity: "critical",
	code: "node.agent_unhealthy",
	summary: "Node agent is not healthy",
	detail: "The node agent did not return healthy runtime metrics.",
	first_seen_at: 1_791_457_365,
	last_seen_at: 1_791_457_365,
	occurrence_count: 1,
	status: "active",
	recommended_action: "Inspect node connectivity and runtime health.",
};

const operation = {
	id: "op-smoke-001",
	operation_type: "node_update",
	target_type: "node",
	target_id: "7",
	requested_by: "smoke-owner",
	request_id: "req-smoke-001",
	state: "running",
	phase: "installing",
	progress: 42,
	created_at: 1_791_457_300,
	started_at: 1_791_457_310,
	updated_at: 1_791_457_365,
	metadata: { update: { requested_version: "v1.2.4+abc123" } },
};

const system = {
	version: "v1.2.3+sha.abcdef12",
	channel: "stable",
	cpu_cores: 4,
	cpu_threads: 8,
	cpu_frequency_hz: 2_100_000_000,
	cpu_usage: 12,
	total_user: 12,
	online_users: 3,
	online_users_usage: 1024,
	online_users_upload_speed: 10,
	online_users_download_speed: 20,
	users_active: 10,
	users_on_hold: 0,
	users_disabled: 1,
	users_expired: 0,
	users_limited: 1,
	incoming_bandwidth: 100,
	outgoing_bandwidth: 200,
	panel_total_bandwidth: 300,
	incoming_bandwidth_speed: 1,
	outgoing_bandwidth_speed: 2,
	memory: { current: 30, total: 100, percent: 30 },
	swap: { current: 0, total: 100, percent: 0 },
	disk: { current: 20, total: 100, percent: 20 },
	load_avg: [0.1, 0.2, 0.3],
	uptime_seconds: 1000,
	panel_uptime_seconds: 500,
	xray_uptime_seconds: 400,
	xray_running: true,
	xray_version: "26.7.11",
	app_memory: 1024,
	app_threads: 8,
	panel_cpu_percent: 2,
	panel_memory_percent: 3,
	cpu_history: [], memory_history: [], swap_history: [], disk_history: [],
	network_history: [], panel_cpu_history: [], panel_memory_history: [],
	personal_usage: { total_users: 12, consumed_bytes: 10, built_bytes: 20, reset_bytes: 10 },
	admin_overview: { total_admins: 1, sudo_admins: 0, full_access_admins: 1, standard_admins: 0, top_admin_usage: 0 },
};

async function mockAPI(page: Page) {
	await page.route("**/api/**", async (route) => {
		const url = new URL(route.request().url());
		const path = url.pathname.replace(/^\/api/, "");
		let body: unknown = {};
		if (path === "/auth/session") {
			body = { state: "active", admin: { id: 1, username: "smoke-owner", role: "full_access", status: "active", permissions: {} }, permissions_version: "1", totp_enabled: false, require_2fa: false };
		} else if (path === "/system") {
			body = system;
		} else if (path === "/maintenance/diagnostics") {
			body = { diagnostics: url.searchParams.get("status") === "resolved" ? [] : [diagnostic], refreshed_at: diagnostic.last_seen_at };
		} else if (path === "/maintenance/operations") {
			body = { operations: [operation] };
		} else if (path === "/maintenance/info") {
			body = { panel: { mode: "binary", running_version: "v1.2.3+sha.abcdef12", tag: "v1.2.3", channel: "stable", update: { available: true, target: "v1.2.4" } }, node_update: { channel: "stable", current: "v1.2.3" } };
		} else if (path === "/maintenance/versions") {
			body = { stable: [{ version: "v1.2.4" }], dev: [{ version: "v1.3.0-dev.1" }] };
		} else if (path === "/nodes") {
			body = [{ id: 7, name: "node-smoke", status: "connected", address: "203.0.113.17", node_service_version: "v1.2.3", node_binary_tag: "v1.2.3", running_version: "v1.2.3", installed_version: "v1.2.3", desired_version: "v1.2.4", node_update_channel: "stable" }];
		} else if (path === "/inbounds") {
			body = [];
		}
		await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(body) });
	});
}

async function openAs(page: Page, language: "en" | "fa", path = "/") {
	await page.addInitScript((lng) => {
		localStorage.setItem("i18nextLng", lng);
		localStorage.setItem("chakra-ui-color-mode", "dark");
		localStorage.setItem("antimage.navigation-mode.v1.smoke-owner", "advanced");
	}, language);
	await mockAPI(page);
	const pageErrors: string[] = [];
	page.on("pageerror", (error) => {
		pageErrors.push(error.stack || error.message);
	});
	await page.goto(path);
	return pageErrors;
}

async function expectNoOverflow(page: Page) {
	const overflow = await page.evaluate(() => ({ viewport: document.documentElement.clientWidth, content: document.documentElement.scrollWidth }));
	expect(overflow.content, `document width ${overflow.content} exceeds viewport ${overflow.viewport}`).toBeLessThanOrEqual(overflow.viewport + 1);
}

test("desktop command center, diagnostics, operation history and update center render in English LTR", async ({ page }) => {
	const pageErrors = await openAs(page, "en");
	await expect(page.getByRole("heading", { name: "Command Center" })).toBeVisible();
	await expect(page.getByRole("navigation", { name: "Primary navigation" })).toBeVisible();
	await expect(page.getByText("Active users")).toBeVisible();
	await page.getByRole("link", { name: "Diagnostics" }).click();
	await expect(page.getByRole("heading", { name: "Diagnostics" })).toBeVisible();
	await expect(page.getByText("203.0.113.17")).toBeVisible();
	await expect(page.getByRole("button", { name: "Acknowledge" })).toBeEnabled();
	const ipDirection = await page.getByText("203.0.113.17").getAttribute("dir");
	expect(ipDirection).toBe("ltr");
	await page.getByRole("link", { name: "Operation history" }).click();
	await expect(page.getByRole("heading", { name: "Operation history" })).toBeVisible();
	await expect(page.locator('[dir="ltr"]').getByText("req-smoke-001")).toBeVisible();
	const operationID = page.getByText("op-smoke-001");
	await expect(operationID).toBeVisible();
	await expect(operationID).toHaveAttribute("dir", "ltr");
	await page.getByRole("link", { name: "Updates" }).click();
	await expect(page.getByRole("heading", { name: "Update Center" })).toBeVisible();
	await expect(page.locator('[dir="ltr"]').getByText("v1.2.3+sha.abcdef12", { exact: true }).first()).toBeVisible();
	await expect(page.getByRole("main").getByRole("link", { name: "Operation history" })).toBeEnabled();
	await expectNoOverflow(page);
	expect(pageErrors).toEqual([]);
});

test("desktop RTL navigation and diagnostics keep technical IDs LTR", async ({ page }) => {
	const pageErrors = await openAs(page, "fa", "/diagnostics");
	await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
	await expect(page.getByRole("navigation")).toBeVisible();
	const ip = page.getByText("203.0.113.17");
	await expect(ip).toBeVisible();
	await expect(ip).toHaveAttribute("dir", "ltr");
	await expect(page.getByRole("button", { name: "Acknowledge" })).toBeEnabled();
	await expectNoOverflow(page);
	expect(pageErrors).toEqual([]);
});

test("mobile navigation drawer opens and closes from the left in LTR", async ({ page }) => {
	await page.setViewportSize({ width: 390, height: 844 });
	const pageErrors = await openAs(page, "en");
	await expect(page.getByRole("heading", { name: "Command Center" })).toBeVisible();
	await expectNoOverflow(page);
	await page.getByRole("button", { name: "Open navigation" }).click();
	const drawer = page.locator(".chakra-modal__content");
	await expect(drawer).toBeVisible();
	const rect = await drawer.evaluate((element) => element.getBoundingClientRect().toJSON());
	expect(rect.left).toBeLessThan(2);
	await expect(drawer.getByRole("navigation")).toBeVisible();
	await drawer.getByRole("link", { name: "Diagnostics" }).click();
	await expect(page.getByRole("heading", { name: "Diagnostics" })).toBeVisible();
	await expect(drawer).toBeHidden();
	await expectNoOverflow(page);
	expect(pageErrors).toEqual([]);
});

test("mobile RTL navigation drawer opens from the right", async ({ page }) => {
	await page.setViewportSize({ width: 390, height: 844 });
	const pageErrors = await openAs(page, "fa", "/");
	await expect(page.locator("html")).toHaveAttribute("dir", "rtl");
	await page.getByRole("button", { name: "باز کردن ناوبری" }).click();
	const drawer = page.locator(".chakra-modal__content");
	await expect(drawer).toBeVisible();
	const rect = await drawer.evaluate((element) => element.getBoundingClientRect().toJSON());
	expect(rect.right).toBeGreaterThan(388);
	await expect(drawer.getByRole("navigation")).toBeVisible();
	await page.keyboard.press("Escape");
	await expectNoOverflow(page);
	expect(pageErrors).toEqual([]);
});
