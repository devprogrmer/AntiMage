import { fetch } from "./http";

export type NetworkPresence = {
	user_id: number;
	username: string;
	node_id: number;
	node_name: string;
	vpn_protocol: string;
	last_seen_at: string;
};
export type ACKQuality = {
	samples: number;
	mean_ms: number | null;
	p95_ms: number | null;
	jitter_ms: number | null;
};
export type NetworkFlow = {
	user_id: number;
	node_id: number;
	interface: string;
	source: string;
	destination: string;
	protocol: string;
	upload_packets: number;
	download_packets: number;
	upload_bytes: number;
	download_bytes: number;
	observed_at: string;
	application?: { application: string; confidence: string };
	tcp_quality?: {
		method: string;
		upload_ack: ACKQuality;
		download_ack: ACKQuality;
		observed_retransmissions: number;
		observed_duplicate_acks: number;
	} | null;
};
export type NetworkSnapshot = {
	available?: boolean;
	version?: number;
	observed_at?: string;
	truncated?: boolean;
	flows?: NetworkFlow[];
};
export const getNetworkPresence = (signal: AbortSignal) =>
	fetch<{ observations: NetworkPresence[]; limit: number }>(
		"/network-intelligence/presence",
		{ signal, retry: 0 },
	);
export const getNetworkSnapshot = (nodeID: number, signal: AbortSignal) =>
	fetch<NetworkSnapshot>("/network-intelligence/node", {
		query: { node_id: nodeID },
		signal,
		retry: 0,
	});
export type DiagnosticFinding = {
	code: string;
	severity: string;
	confidence: string;
	evidence: string;
	recommendation: string;
};
export const getNetworkDiagnostics = (nodeID: number, signal: AbortSignal) =>
	fetch<{
		results: {
			user_id: number;
			destination: string;
			findings: DiagnosticFinding[];
		}[];
	}>("/network-intelligence/node", {
		query: { node_id: nodeID, diagnose: true },
		signal,
		retry: 0,
	});
