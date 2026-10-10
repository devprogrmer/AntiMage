import { describe, expect, it } from "vitest";
import { canFenceNodeCommands, maintenancePhaseNotice } from "./maintenanceStateModel";

describe("maintenance recovery presentation", () => {
	it("does not grant destructive access to absent or partial old-agent capabilities", () => {
		expect(canFenceNodeCommands()).toBe(false);
		expect(canFenceNodeCommands([])).toBe(false);
		expect(canFenceNodeCommands(["runtime_evidence_v1", "shared_fencing_v1"])).toBe(false);
		expect(canFenceNodeCommands(["shared_fencing_v1", "command_idempotency_v1"])).toBe(true);
	});
	it.each(["outcome_unknown", "reconciling", "manual_recovery_required", "waiting_for_reconnect", "finalizing"])("explains %s without claiming completion", (phase) => {
		expect(maintenancePhaseNotice(phase)).toBeTruthy();
	});
	it("does not invent evidence for an unknown phase", () => {
		expect(maintenancePhaseNotice("future_agent_phase")).toBeNull();
	});
});
