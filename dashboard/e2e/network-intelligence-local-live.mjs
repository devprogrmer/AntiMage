import { chromium, expect } from "@playwright/test";

// Real local HTTP/API/mTLS Node Agent responses. No page.route or request mocks.
const origin = process.argv[2];
if (!origin || new URL(origin).hostname !== "127.0.0.1") throw new Error("Local loopback panel required");
const browser = await chromium.launch({ executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH });
try {
	const page = await browser.newPage();
	const errors = [];
	page.on("pageerror", error => errors.push(error.message));
	await page.addInitScript(() => localStorage.setItem("i18nextLng", "en"));
	for (const suffix of ["node?node_id=7", "node?node_id=7&diagnose=true", "presence"]) {
		const res = await page.request.get(`${origin}/api/network-intelligence/${suffix}`);
		expect(res.status()).toBe(401);
	}
	const login = await page.request.post(`${origin}/api/auth/login`, {
		headers: { Origin: origin }, data: { username: "local-admin", password: "local-fixture-password" },
	});
	expect(login.status()).toBe(200);
	await page.goto(`${origin}/dashboard/network-intelligence/`);
	await expect(page.getByRole("heading", { name: "Network Intelligence" })).toBeVisible();
	const [response] = await Promise.all([
		page.waitForResponse(res => new URL(res.url()).pathname.endsWith("/network-intelligence/node") && !new URL(res.url()).searchParams.has("diagnose")),
		page.getByLabel("Node", { exact: true }).selectOption("7"),
	]);
	expect(response.status()).toBe(200);
	expect(response.headers()["cache-control"]).toContain("no-store");
	const snapshot = await response.json();
	expect(snapshot.flows).toHaveLength(4);
	const slow = snapshot.flows.find(flow => flow.interface === "lab-session-a");
	const fast = snapshot.flows.find(flow => flow.interface === "lab-session-b" && flow.protocol === "udp");
	const unknown = snapshot.flows.find(flow => flow.interface === "lab-session-c");
	const tcp = snapshot.flows.find(flow => flow.protocol === "tcp");
	expect(slow.destination).toBe(fast.destination);
	expect(fast.destination).toBe(unknown.destination);
	for (const flow of [slow, fast]) {
		expect(flow.user_id).toBe(42);
		expect(flow.udp_quality.upload_exchange.samples).toBe(2);
		expect(flow.udp_quality.packet_loss).toBeNull();
		expect(flow.application.application).toBe("STUN connectivity checks");
		expect(flow.application.scope).toBe("protocol");
		expect(flow.upload_packets).toBe(2);
		expect(flow.download_packets).toBe(2);
	}
	expect(slow.udp_quality.upload_exchange.mean_ms).toBeGreaterThanOrEqual(250);
	expect(slow.udp_quality.upload_exchange.jitter_ms).toBeGreaterThan(30);
	expect(unknown.user_id).toBe(43);
	expect(unknown.application.application).toBe("Unknown");
	expect(unknown.udp_quality).toBeNull();
	expect(tcp.user_id).toBe(42);
	expect(tcp.tcp_quality.upload_ack.mean_ms).toBeNull();
	for (const secret of ["local-private-session", "unclassified-private-body", "private-tcp-body"]) {
		expect(JSON.stringify(snapshot)).not.toContain(secret);
	}
	const rows = page.getByRole("row");
	const slowRow = rows.filter({ hasText: slow.destination }).filter({ hasText: slow.interface });
	await expect(slowRow.getByText("lab-session-a", { exact: true })).toBeVisible();
	await expect(slowRow.getByRole("cell", { name: `${slow.udp_quality.upload_exchange.mean_ms.toFixed(1)} ms Samples: 2`, exact: true })).toBeVisible();
	await slowRow.getByText("Detection evidence", { exact: true }).click();
	await expect(slowRow.getByText("udp-protocols-v1", { exact: true })).toBeVisible();
	await expect(rows.filter({ hasText: unknown.destination }).filter({ hasText: unknown.interface }).getByText("Unknown", { exact: true })).toBeVisible();
	await expect(rows.filter({ hasText: tcp.destination }).getByText("TCP ACK", { exact: true })).toBeVisible();
	const [diagnosticResponse] = await Promise.all([
		page.waitForResponse(res => new URL(res.url()).searchParams.get("diagnose") === "true"),
		page.getByRole("button", { name: "Run network diagnostics" }).click(),
	]);
	expect(diagnosticResponse.status()).toBe(200);
	const diagnostics = await diagnosticResponse.json();
	const slowResult = diagnostics.results.find(result => result.destination === slow.destination && result.interface === slow.interface && result.protocol === "udp");
	expect(slowResult.user_id).toBe(42);
	expect(slowResult.findings.map(f => f.code)).toEqual(["high_udp_transaction_rtt", "variable_udp_transaction_rtt"]);
	for (const result of diagnostics.results.filter(result => result !== slowResult)) {
		expect(result.findings).toEqual([]);
	}
	for (const finding of slowResult.findings) {
		expect(finding.confidence).toBe("observed");
		await expect(page.getByText(finding.evidence, { exact: true })).toBeVisible();
		await expect(page.getByText(finding.recommendation, { exact: true })).toBeVisible();
	}
	await expect(page.getByText(`${slowResult.interface} · ${slowResult.source} · udp`, { exact: true })).toHaveCount(2);
	expect(JSON.stringify(diagnostics)).not.toMatch(/packet_loss|game_server_instability|mtu_mismatch/);
	expect(errors).toEqual([]);
	const restrictedLogin=await page.request.post(`${origin}/api/auth/login`,{headers:{Origin:origin},data:{username:"local-restricted",password:"local-fixture-password"}});
	expect(restrictedLogin.status()).toBe(200);
	for (const suffix of ["node?node_id=7", "node?node_id=7&diagnose=true", "presence"]) {
		const denied=await page.request.get(`${origin}/api/network-intelligence/${suffix}`);
		expect(denied.status()).toBe(403);
		expect(await denied.text()).not.toContain(slow.destination);
	}
	console.log(JSON.stringify({ result: "PASS", transport: "real local sockets → session adapter → Node Agent → mTLS gRPC → authenticated API → browser", flows: snapshot.flows.length, users: 2, sessions: 3, mean_udp_ms: slow.udp_quality.upload_exchange.mean_ms, udp_variation_ms: slow.udp_quality.upload_exchange.jitter_ms, diagnostics: slowResult.findings.map(f => f.code), raw_capture: false, wireguard: false }));
} finally {
	await browser.close();
}
