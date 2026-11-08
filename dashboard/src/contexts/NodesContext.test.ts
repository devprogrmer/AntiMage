import { describe, expect, it } from "vitest";
import {
	mergeLiveNodes,
	NodeSchema,
	normalizeNodeUpdateChannel,
} from "./NodesContext";

describe("normalizeNodeUpdateChannel", () => {
	it.each([
		["latest", "stable"],
		["release", "stable"],
		["master", "stable"],
		["stable", "stable"],
		["development", "dev"],
		["dev-builds", "dev"],
		["dev", "dev"],
	])("maps %s to %s", (input, expected) => {
		expect(normalizeNodeUpdateChannel(input)).toBe(expected);
	});
});

describe("node accounting diagnostics", () => {
	const node = {
		id: 1,
		name: "test",
		address: "127.0.0.1",
		port: 62050,
		api_port: 62051,
		usage_coefficient: 1,
	};
	it("accepts old nodes without diagnostics", () => {
		const parsed = NodeSchema.safeParse(node);
		expect(parsed.success).toBe(true);
		if (parsed.success) {
			expect(parsed.data.accounting).toBeUndefined();
		}
	});
	it("applies live diagnostics, preserves them on static updates, and clears them when unavailable", () => {
		const accounting = {
			protocols: [],
			reflection_ledger_loaded: true,
			awaiting_reflection_batches: 1,
			awaiting_reflection_bytes: 42,
			reflection_bytes_overflow: false,
		};
		const updated = mergeLiveNodes([node], [{ ...node, accounting }]);
		expect(updated[0].accounting).toEqual(accounting);
		expect(mergeLiveNodes(updated, [node])[0].accounting).toEqual(accounting);
		expect(
			mergeLiveNodes(updated, [{ ...node, accounting: null }])[0].accounting,
		).toBeNull();
	});
	it("retains pending, ACK and reflection status in the node response", () => {
		const parsed = NodeSchema.safeParse({
			...node,
			accounting: {
				protocols: [{
					protocol: "xray", state_loaded: true, pending_batch_id: "pending",
					pending_bytes: 12, pending_bytes_overflow: false,
					last_acked_batch_id: "acked", last_checkpoint_unix: 100,
					checkpoint_overdue: true, pending_ack_overdue: true,
					failure: "",
				}],
				reflection_ledger_loaded: true,
				awaiting_reflection_batches: 1,
				awaiting_reflection_bytes: 12,
				reflection_bytes_overflow: false,
				oldest_reflection_batch_unix: 100,
				reflection_overdue: true,
			},
		});
		expect(parsed.success).toBe(true);
		if (parsed.success) {
			expect(parsed.data.accounting?.protocols[0].pending_batch_id).toBe("pending");
			expect(parsed.data.accounting?.protocols[0].last_acked_batch_id).toBe("acked");
			expect(parsed.data.accounting?.protocols[0].checkpoint_overdue).toBe(true);
			expect(parsed.data.accounting?.protocols[0].pending_ack_overdue).toBe(true);
			expect(parsed.data.accounting?.awaiting_reflection_batches).toBe(1);
			expect(parsed.data.accounting?.reflection_overdue).toBe(true);
			expect(parsed.data.accounting?.oldest_reflection_batch_unix).toBe(100);
		}
	});
	it("accepts accounting snapshots from older nodes without a reflection timestamp", () => {
		const parsed = NodeSchema.safeParse({
			...node,
			accounting: {
				protocols: [], reflection_ledger_loaded: true,
				awaiting_reflection_batches: 1, awaiting_reflection_bytes: 12,
				reflection_bytes_overflow: false,
			},
		});
		expect(parsed.success).toBe(true);
		if (parsed.success) {
			expect(parsed.data.accounting?.oldest_reflection_batch_unix).toBe(0);
		}
	});
});
