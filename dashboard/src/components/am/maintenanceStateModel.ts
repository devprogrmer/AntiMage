export function canFenceNodeCommands(capabilities?: readonly string[]) {
	return Boolean(capabilities?.includes("shared_fencing_v1") && capabilities.includes("command_idempotency_v1"));
}

export function maintenancePhaseNotice(phase: string): string | null {
	switch (phase) {
		case "outcome_unknown":
			return "Command acknowledgement was lost. The resource remains reserved while actual state is checked. Do not repeat the command.";
		case "reconciling":
			return "Recovery is comparing persisted ownership, files and runtime evidence before continuing a missing step.";
		case "manual_recovery_required":
			return "Recovery could not verify a safe next step. The reservation remains active; inspect Diagnostics before making changes.";
		case "waiting_for_reconnect":
			return "Waiting for the agent to reconnect within the original operation deadline. A reconnect alone does not prove success.";
		case "finalizing":
			return "The requested runtime is verified. Persistent transaction cleanup must complete before success is reported.";
		default:
			return null;
	}
}
