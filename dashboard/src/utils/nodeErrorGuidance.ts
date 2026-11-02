export type NodeErrorKind =
	| "timeout"
	| "refused"
	| "dns"
	| "unreachable"
	| "tls"
	| "auth"
	| "config"
	| "unknown";

export const classifyNodeError = (message: string): NodeErrorKind => {
	const value = message.toLowerCase();
	if (/deadline exceeded|timed? ?out|timeout|i\/o timeout/.test(value)) {
		return "timeout";
	}
	if (/connection refused|actively refused/.test(value)) return "refused";
	if (/no such host|dns|name resolution/.test(value)) return "dns";
	if (
		/no route to host|network is unreachable|host is unreachable/.test(value)
	) {
		return "unreachable";
	}
	if (/tls|certificate|handshake|x509/.test(value)) return "tls";
	if (
		/unauthenticated|authentication|permission denied|rejected.*auth/.test(
			value,
		)
	) {
		return "auth";
	}
	if (
		/invalid.*config|config.*invalid|failed precondition|revision.*mismatch/.test(
			value,
		)
	) {
		return "config";
	}
	return "unknown";
};

export const getNodeErrorGuidanceKeys = (message: string) => {
	const kind = classifyNodeError(message);
	return {
		title: `nodeDiagnostics.${kind}.title`,
		cause: `nodeDiagnostics.${kind}.cause`,
		steps: [1, 2, 3].map((step) => `nodeDiagnostics.${kind}.step${step}`),
	};
};

