import { describe, expect, it, vi } from "vitest";
import {
	getNetworkDiagnostics,
	getNetworkHistory,
	getNetworkPresence,
	getNetworkSnapshot,
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
	});
});
