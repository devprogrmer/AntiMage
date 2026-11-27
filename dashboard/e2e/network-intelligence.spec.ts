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

test("keeps selected history readable after presence disappears", async ({ page }) => {
	await setup(page);
	let online = true;
	await page.route("**/api/network-intelligence/presence", (route) =>
		route.fulfill({ json: { observations: online ? [
			{ user_id: 42, username: "traffic-user", node_id: 7, node_name: "isolated-node", vpn_protocol: "wireguard", last_seen_at: new Date().toISOString() },
		] : [], limit: 250 } }),
	);
	await page.route("**/api/network-intelligence/history?**", (route) =>
		route.fulfill({ json: { available: true, storage: "durable", collection: "background", truncated: false, points: [
			{ node_id: 7, user_id: 42, at: "2026-11-27T06:00:00Z", method: "tcp_ack_upload_observation", ack_rtt_ms: 42.5, jitter_ms: null, retransmission_delta: null, findings: [] },
		] } }),
	);
	await page.goto("/dashboard/network-intelligence/");
	const node = page.getByLabel("Node", { exact: true });
	const account = page.getByLabel("History account", { exact: true });
	await node.selectOption("7");
	await account.selectOption("42");
	await expect(page.getByText("Observed samples", { exact: true })).toBeVisible();
	online = false;
	await page.getByRole("button", { name: "Refresh", exact: true }).click();
	await expect(account.locator("option:checked")).toHaveText("Account 42 (no recent presence)");
	await expect(node).toHaveValue("7");
	await expect(account).toHaveValue("42");
	await page.getByText("Observed samples", { exact: true }).click();
	await expect(page.getByRole("cell", { name: "42.5", exact: true })).toBeVisible();
});

test("renders node MTU and interval counter telemetry", async ({ page }) => {
	await setup(page);
	await page.route("**/api/network-intelligence/node?**", (route) =>
		route.fulfill({ json: {
			flows: [],
			node_resources: { memory_used_bytes: 1024, memory_total_bytes: 2048, sampled_at_unix: 1795759200 },
			infrastructure: {
				sampled_at: "2026-11-27T06:00:00Z",
				interval_seconds: 5,
				interfaces: [{ name: "wg-observed", mtu: 1420, oper_state: "up" }],
				counter_rates_per_second: { "softnet.dropped": 0.5 },
			},
		} }),
	);
	await page.goto("/dashboard/network-intelligence/");
	await page.getByLabel("Node", { exact: true }).selectOption("7");
	await expect(page.getByRole("cell", { name: "wg-observed" })).toBeVisible();
	await expect(page.getByRole("cell", { name: "1420" })).toBeVisible();
	await expect(page.getByText("Node memory used / total 1,024 / 2,048 B")).toBeVisible();
	await expect(page.getByRole("row", { name: "wg-observed 1420 up" })).toBeVisible();
	await expect(page.getByRole("row", { name: "softnet.dropped 0.50" })).toBeVisible();
});

test("standard admin cannot view telemetry", async ({ page }) => {
	await setup(page, "standard");
	await page.goto("/dashboard/network-intelligence/");
	await expect(page.getByText("Full-access administrator permission is required.")).toBeVisible();
	await expect(page.getByText("traffic-user", { exact: true })).toHaveCount(0);
});

test("one-click diagnostics displays node evidence without a user identity", async ({ page }) => {
	await setup(page);
	await page.route("**/api/network-intelligence/node?**", (route) => {
		const query = new URL(route.request().url()).searchParams;
		if (query.get("diagnose") !== "true") return route.fulfill({ json: { flows: [] } });
		return route.fulfill({ json: { results: [{ scope: "node", findings: [{
			code: "node_softnet_drops", severity: "warning", confidence: "observed",
			evidence: "Kernel receive backlog drops averaged 1 events/s over 5 s on this node",
			recommendation: "Compare node counters and tunnel observations over the same interval",
		}] }] } });
	});
	await page.goto("/dashboard/network-intelligence/");
	await page.getByLabel("Node", { exact: true }).selectOption("7");
	await page.getByRole("button", { name: "Run network diagnostics" }).click();
	await expect(page.getByText("Node · node_softnet_drops")).toBeVisible();
	await expect(page.getByText("Kernel receive backlog drops averaged 1 events/s over 5 s on this node")).toBeVisible();
});

test("account history preserves unavailable measurements and reports retention limits", async ({ page }) => {
	await setup(page);
	await page.route("**/api/network-intelligence/history?**", async (route) => {
		const query = new URL(route.request().url()).searchParams;
		expect(query.get("node_id")).toBe("7");
		expect(query.get("user_id")).toBe("42");
		await route.fulfill({ json: { available: true, storage: "volatile", collection: "on_telemetry_read", truncated: true, points: [
			{ node_id: 7, user_id: 42, at: "2026-11-27T06:00:00Z", method: "tcp_ack_upload_observation", ack_rtt_ms: 123.4, jitter_ms: null, retransmission_delta: null, findings: [] },
			{ node_id: 7, user_id: 42, at: "2026-11-27T06:00:05Z", method: "tcp_ack_upload_observation", ack_rtt_ms: null, jitter_ms: null, retransmission_delta: 1, findings: [{ code: "observed_tcp_retransmission", severity: "warning", confidence: "observed", evidence: "Repeated TCP sequence spans were observed", recommendation: "Inspect capture completeness and both network directions" }] },
		] } });
	});
	await page.goto("/dashboard/network-intelligence/");
	await page.getByLabel("Node", { exact: true }).selectOption("7");
	await page.getByLabel("History account", { exact: true }).selectOption("42");
	await expect(page.getByText("History is held in memory and clears when the node restarts.")).toBeVisible();
	await expect(page.getByText("The response limit was reached. This account history may be incomplete.")).toBeVisible();
	await expect(page.getByText("Repeated TCP sequence spans were observed", { exact: false })).toBeVisible();
	await page.getByText("Observed samples", { exact: true }).click();
	await expect(page.getByRole("cell", { name: "123.4", exact: true })).toBeVisible();
	await expect(page.getByRole("cell", { name: "N/A", exact: true }).first()).toBeVisible();
});
