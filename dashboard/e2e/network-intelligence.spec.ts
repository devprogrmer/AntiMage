import { expect, test, type Page } from "@playwright/test";

// These UI fixtures validate rendering and permission handling. Real capture
// and backend attribution are covered by separate Linux traffic tests.
async function setup(page: Page, role = "full_access", disabled = false) {
	await page.addInitScript(() => localStorage.setItem("i18nextLng", "en"));
	await page.route("**/api/**", async (route) => {
		const path = new URL(route.request().url()).pathname;
		let body: unknown = {};
		if (path.endsWith("/auth/session")) body = { state: "active", admin: { id: 1, username: "test-admin", role, status: "active" } };
		if (path.endsWith("/network-intelligence/presence")) {
			if (disabled) { await route.fulfill({ status: 404, json: { detail: "disabled" } }); return; }
			body = { observations: [{ user_id: 42, username: "traffic-user", node_id: 7, node_name: "isolated-node", vpn_protocol: "wireguard", last_seen_at: new Date().toISOString() }], limit: 250 };
		}
		if (path.endsWith("/network-intelligence/node")) body = { available: false };
		await route.fulfill({ json: body });
	});
}

test("full access displays presence and unavailable node telemetry", async ({ page }) => {
	await setup(page);
	await page.goto("/dashboard/network-intelligence/");
	await expect(page.getByRole("heading", { name: "Network Intelligence" })).toBeVisible();
	await expect(page.getByText("traffic-user", { exact: true })).toBeVisible();
	await page.getByLabel("Node", { exact: true }).selectOption("7");
	await expect(page.getByText("This node has no Network Intelligence snapshot.", { exact: false })).toBeVisible();
});

test("disabled panel reports feature flag status", async ({ page }) => {
	await setup(page, "full_access", true);
	await page.goto("/dashboard/network-intelligence/");
	await expect(page.getByText("Network Intelligence is disabled on the panel.")).toBeVisible();
});

test("standard admin cannot view telemetry", async ({ page }) => {
	await setup(page, "standard");
	await page.goto("/dashboard/network-intelligence/");
	await expect(page.getByText("Full-access administrator permission is required.")).toBeVisible();
	await expect(page.getByText("traffic-user", { exact: true })).toHaveCount(0);
});
