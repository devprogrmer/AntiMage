import { describe, expect, it, vi } from "vitest";
import {
	getNetworkDiagnostics,
	getNetworkHistory,
	getNetworkOptimizations,
	getNetworkPresence,
	getNetworkSnapshot,
	applyNetworkTCPMSS,
	rollbackNetworkOptimization,
} from "./networkIntelligence";
import { fetch } from "./http";

vi.mock("./http", () => ({ fetch: vi.fn().mockResolvedValue({}) }));

describe("network telemetry requests", () => {
	it("passes cancellation and bounded no-retry reads to the authenticated API", async () => {
		const controller = new AbortController();
		await getNetworkPresence(controller.signal);
		expect(fetch).toHaveBeenLastCalledWith("/network-intelligence/presence", {
			signal: controller.signal,
			retry: 0,
		});
		await getNetworkSnapshot(7, controller.signal);
		expect(fetch).toHaveBeenLastCalledWith("/network-intelligence/node", {
			query: { node_id: 7 },
			signal: controller.signal,
			retry: 0,
		});
		await getNetworkDiagnostics(7, controller.signal);
		expect(fetch).toHaveBeenLastCalledWith("/network-intelligence/node", {
			query: { node_id: 7, diagnose: true },
			signal: controller.signal,
			retry: 0,
		});
		await getNetworkHistory(7, 42, controller.signal);
		expect(fetch).toHaveBeenLastCalledWith("/network-intelligence/history", {
			query: { node_id: 7, user_id: 42 },
			signal: controller.signal,
			retry: 0,
		});
		await getNetworkOptimizations(7, 42, controller.signal);
		expect(fetch).toHaveBeenLastCalledWith("/network-intelligence/optimization", {
			query: { node_id: 7, user_id: 42 },
			signal: controller.signal,
			retry: 0,
		});
		await applyNetworkTCPMSS(7, 42, 1340);
		expect(fetch).toHaveBeenLastCalledWith("/network-intelligence/optimization", {
			method: "POST",
			body: { action: "apply_tcp_mss", node_id: 7, user_id: 42, tcp_mss: 1340 },
		});
		await rollbackNetworkOptimization(7, 42, "0123456789abcdef0123456789abcdef");
		expect(fetch).toHaveBeenLastCalledWith("/network-intelligence/optimization", {
			method: "POST",
			body: { action: "rollback", node_id: 7, user_id: 42, operation_id: "0123456789abcdef0123456789abcdef" },
		});
	});
});
