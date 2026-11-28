import {
	Alert,
	AlertIcon,
	Badge,
	Box,
	Button,
	FormControl,
	FormLabel,
	Heading,
	HStack,
	Input,
	Select,
	Spinner,
	Stack,
	Table,
	TableContainer,
	Tbody,
	Td,
	Text,
	Th,
	Thead,
	Tr,
} from "@chakra-ui/react";
import useGetUser from "hooks/useGetUser";
import NetworkQualityHistory from "components/NetworkQualityHistory";
import NetworkOptimizationPanel from "components/NetworkOptimizationPanel";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { AdminRole } from "types/Admin";
import {
	getNetworkDiagnostics,
	getNetworkPresence,
	getNetworkSnapshot,
	type DiagnosticFinding,
	type NetworkPresence,
	type NetworkSnapshot,
} from "service/networkIntelligence";

const milliseconds = (value: number | null | undefined) =>
	value == null || !Number.isFinite(value) ? "N/A" : `${value.toFixed(1)} ms`;
const bytes = (value: number) => new Intl.NumberFormat().format(value);

export default function NetworkIntelligencePage() {
	const { t } = useTranslation();
	const label = useCallback(
		(key: string, fallback: string) =>
			t(`networkIntelligence.${key}`, { defaultValue: fallback }),
		[t],
	);
	const { userData, getUserIsSuccess } = useGetUser();
	const allowed = getUserIsSuccess && userData.role === AdminRole.FullAccess;
	const [presence, setPresence] = useState<NetworkPresence[]>([]);
	const [node, setNode] = useState("");
	const [search, setSearch] = useState("");
	const [snapshot, setSnapshot] = useState<NetworkSnapshot | null>(null);
	const [error, setError] = useState("");
	const [loading, setLoading] = useState(false);
	const [refresh, setRefresh] = useState(0);
	const [diagnostics, setDiagnostics] = useState<
		| {
				scope?: "node";
				user_id?: number;
				destination?: string;
				interface?: string;
				source?: string;
				protocol?: string;
				findings: DiagnosticFinding[];
		  }[]
		| null
	>(null);
	const [diagnosing, setDiagnosing] = useState(false);
	const diagnosticRequest = useRef<AbortController>();
	const diagnose = async () => {
		diagnosticRequest.current?.abort();
		const request = new AbortController();
		diagnosticRequest.current = request;
		setDiagnosing(true);
		try {
			const result = await getNetworkDiagnostics(Number(node), request.signal);
			if (!request.signal.aborted) setDiagnostics(result.results);
		} catch {
			if (!request.signal.aborted)
				setError(
					label("diagnosticError", "Network diagnostics are unavailable."),
				);
		} finally {
			if (!request.signal.aborted) setDiagnosing(false);
		}
	};
	useEffect(() => {
		setDiagnostics(null);
		diagnosticRequest.current?.abort();
		setDiagnosing(false);
		if (!allowed) return;
		let disposed = false;
		let pending: AbortController | undefined;
		let timer: ReturnType<typeof setTimeout>;
		let sequence = refresh;
		const load = async () => {
			if (document.hidden || disposed) return;
			const currentSequence = ++sequence;
			pending = new AbortController();
			const request = pending;
			setLoading(true);
			try {
				const result = await getNetworkPresence(request.signal);
				if (disposed || request.signal.aborted) return;
				setPresence(result.observations);
				const resultSnapshot = node
					? await getNetworkSnapshot(Number(node), request.signal)
					: null;
				if (disposed || request.signal.aborted) return;
				setSnapshot(resultSnapshot);
				setError("");
			} catch (failure: any) {
				if (disposed || request.signal.aborted) return;
				setSnapshot(null);
				setPresence([]);
				setError(
					failure?.statusCode === 404
						? label(
								"disabled",
								"Network Intelligence is disabled on the panel.",
							)
						: label(
								"unavailable",
								"Network telemetry is unavailable. Retry to check the connection.",
							),
				);
			} finally {
				if (!disposed && currentSequence === sequence) {
					setLoading(false);
					clearTimeout(timer);
					timer = setTimeout(load, 5000);
				}
			}
		};
		const visible = () => {
			sequence++;
			clearTimeout(timer);
			pending?.abort();
			setLoading(false);
			if (!document.hidden) void load();
		};
		void load();
		document.addEventListener("visibilitychange", visible);
		return () => {
			disposed = true;
			diagnosticRequest.current?.abort();
			clearTimeout(timer);
			pending?.abort();
			document.removeEventListener("visibilitychange", visible);
		};
	}, [allowed, node, refresh, label]);
	const nodes = useMemo(
		() =>
			Array.from(
				new Map(
					presence.map((item) => [item.node_id, item.node_name]),
				).entries(),
			),
		[presence],
	);
	const users = presence.filter(
		(item) =>
			(!node || String(item.node_id) === node) &&
			`${item.username} ${item.user_id}`
				.toLowerCase()
				.includes(search.toLowerCase()),
	);
	const flows = (snapshot?.flows || []).filter(
		(flow) => !search || users.some((user) => user.user_id === flow.user_id),
	);
	if (!getUserIsSuccess) return <Spinner />;
	if (!allowed)
		return (
			<Alert status="warning">
				<AlertIcon />
				{label(
					"permission",
					"Full-access administrator permission is required.",
				)}
			</Alert>
		);
	return (
		<Stack spacing={5}>
			<HStack justify="space-between">
				<Heading size="lg">{label("title", "Network Intelligence")}</Heading>
				<Button
					onClick={() => setRefresh((value) => value + 1)}
					isLoading={loading}
				>
					{label("refresh", "Refresh")}
				</Button>
			</HStack>
			{error && (
				<Alert status="warning">
					<AlertIcon />
					{error}
				</Alert>
			)}
			<Stack direction={{ base: "column", md: "row" }} spacing={4}>
				<FormControl>
					<FormLabel>{label("user", "User")}</FormLabel>
					<Input
						value={search}
						onChange={(event) => setSearch(event.target.value)}
						placeholder={label("search", "Search username or ID")}
					/>
				</FormControl>
				<FormControl>
					<FormLabel>{label("node", "Node")}</FormLabel>
					<Select
						value={node}
						onChange={(event) => {
							setSnapshot(null);
							setNode(event.target.value);
						}}
					>
						<option value="">
							{label("selectNode", "Select a node to inspect flows")}
						</option>
						{node && !nodes.some(([id]) => String(id) === node) && (
							<option value={node}>
								{label("node", "Node")} {node}
							</option>
						)}
						{nodes.map(([id, name]) => (
							<option key={id} value={id}>
								{name || id}
							</option>
						))}
					</Select>
				</FormControl>
			</Stack>
			<Box borderWidth="1px" borderRadius="xl" overflow="hidden">
				<TableContainer>
					<Table size="sm">
						<Thead>
							<Tr>
								<Th>{label("user", "User")}</Th>
								<Th>{label("node", "Node")}</Th>
								<Th>VPN</Th>
								<Th>{label("lastSeen", "Last observed")}</Th>
							</Tr>
						</Thead>
						<Tbody>
							{users.map((item) => (
								<Tr
									key={`${item.user_id}-${item.node_id}-${item.vpn_protocol}`}
								>
									<Td>{item.username || item.user_id}</Td>
									<Td>{item.node_name || item.node_id}</Td>
									<Td>
										<Badge>{item.vpn_protocol}</Badge>
									</Td>
									<Td>{new Date(item.last_seen_at).toLocaleString()}</Td>
								</Tr>
							))}
						</Tbody>
					</Table>
				</TableContainer>
				{!users.length && (
					<Text p={4}>
						{label("noPresence", "No recent presence observations.")}
					</Text>
				)}
			</Box>
			{node && snapshot?.available === false && (
				<Alert status="info">
					<AlertIcon />
					{label(
						"nodeUnavailable",
						"This node has no Network Intelligence snapshot. Check the node feature flag and supported agent version.",
					)}
				</Alert>
			)}
			{node && (
				<Button alignSelf="start" onClick={diagnose} isLoading={diagnosing}>
					{label("diagnose", "Run network diagnostics")}
				</Button>
			)}
			{diagnostics && (
				<Stack spacing={3}>
					{diagnostics.flatMap((result) =>
						result.findings.map((finding, index) => (
							<Alert
								key={`${result.scope}-${result.user_id}-${result.interface}-${result.source}-${result.destination}-${result.protocol}-${index}`}
								status={finding.severity === "critical" ? "error" : "warning"}
							>
								<AlertIcon />
								<Box>
									<Text fontWeight="semibold">
										{result.scope === "node"
											? label("node", "Node")
											: `${result.user_id} · ${result.destination}`}{" "}
										· {finding.code}
									</Text>
									{result.interface && <Text dir="ltr">{result.interface} · {result.source} · {result.protocol}</Text>}
									<Text>{finding.evidence}</Text>
									<Text>{finding.recommendation}</Text>
									<Text fontSize="sm">{finding.confidence}</Text>
								</Box>
							</Alert>
						)),
					)}
					{diagnostics.every((result) => result.findings.length === 0) && (
						<Text>
							{label(
								"noFindings",
								"No findings from the available measurements.",
							)}
						</Text>
					)}
				</Stack>
			)}
			{node && (
				<Box borderWidth="1px" borderRadius="xl" overflow="hidden">
					<TableContainer>
						<Table size="sm">
							<Thead>
								<Tr>
									<Th>{label("user", "User")}</Th>
									<Th>{label("destination", "Destination")}</Th>
									<Th>{label("application", "Application")}</Th>
									<Th>{label("source", "Assigned source")}</Th>
									<Th>{label("interface", "Interface")}</Th>
									<Th>{label("traffic", "Upload / download bytes")}</Th>
									<Th>{label("packets", "Upload / download packets")}</Th>
									<Th>{label("exchangeDestination", "Node → destination RTT")}</Th>
									<Th>{label("exchangeClient", "Node → client RTT")}</Th>
									<Th>{label("exchangeVariation", "RTT variation (upload / download)")}</Th>
									<Th>
										{label(
											"retransmissions",
											"Observed retransmissions / duplicate ACKs",
										)}
									</Th>
									<Th>{label("loss", "Packet loss")}</Th>
								</Tr>
							</Thead>
							<Tbody>
								{flows.map((flow) => (
									<Tr
										key={`${flow.user_id}-${flow.interface}-${flow.source}-${flow.destination}-${flow.protocol}`}
									>
										<Td>
											{presence.find((user) => user.user_id === flow.user_id)
												?.username || flow.user_id}
										</Td>
										<Td dir="ltr">
											<Text>{flow.destination}</Text>
											<Badge>{flow.protocol}</Badge>
										</Td>
										<Td>
											<Text>{flow.application?.application || "Unknown"}</Text>
											{flow.application && (
												<Badge>{flow.application.confidence}</Badge>
											)}
											{flow.application?.evidence?.length ? (
												<details><summary>{label("detectionEvidence", "Detection evidence")}</summary>
													<Text fontSize="xs">{flow.application.revision}</Text>
													{flow.application.evidence.map((evidence, index) => <Text fontSize="xs" key={`${index}-${evidence}`}>{evidence}</Text>)}
												</details>
											) : null}
											<Text fontSize="xs">{flow.protocol === "tcp" ? "TCP ACK" : flow.udp_quality?.protocol || "N/A"}</Text>
											{flow.udp_quality && <Text fontSize="xs">{label("udpExchangeOnly", "UDP transaction timing; not in-game ping. Packet loss: N/A.")}</Text>}
											{flow.udp_quality?.measurement_at && <Text fontSize="xs" dir="ltr">{flow.udp_quality.measurement_at}</Text>}
											{flow.udp_quality?.timing_unavailable_reason && <Text fontSize="xs">{flow.udp_quality.timing_unavailable_reason}</Text>}
											{flow.udp_quality?.limited && <Badge>{label("samplingLimited", "Sampling limited")}</Badge>}
										</Td>
										<Td dir="ltr">{flow.source}</Td>
										<Td dir="ltr">{flow.interface}</Td>
										<Td>
											{bytes(flow.upload_bytes)} / {bytes(flow.download_bytes)}
										</Td>
										<Td>
											{bytes(flow.upload_packets)} /{" "}
											{bytes(flow.download_packets)}
										</Td>
										<Td>
											{milliseconds(flow.protocol === "tcp" ? flow.tcp_quality?.upload_ack.mean_ms : flow.udp_quality?.upload_exchange.mean_ms)}
											{flow.udp_quality && <Text fontSize="xs">{label("timingSamples", "Samples")}: {flow.udp_quality.upload_exchange.samples}</Text>}
										</Td>
										<Td>
											{milliseconds(flow.protocol === "tcp" ? flow.tcp_quality?.download_ack.mean_ms : flow.udp_quality?.download_exchange.mean_ms)}
										</Td>
										<Td>
											{milliseconds(flow.protocol === "tcp" ? flow.tcp_quality?.upload_ack.jitter_ms : flow.udp_quality?.upload_exchange.jitter_ms)} /{" "}
											{milliseconds(flow.protocol === "tcp" ? flow.tcp_quality?.download_ack.jitter_ms : flow.udp_quality?.download_exchange.jitter_ms)}
										</Td>
										<Td>
											{flow.tcp_quality
												? `${flow.tcp_quality.observed_retransmissions} / ${flow.tcp_quality.observed_duplicate_acks}`
												: "N/A"}
										</Td>
										<Td>N/A</Td>
									</Tr>
								))}
							</Tbody>
						</Table>
					</TableContainer>
					{!flows.length && (
						<Text p={4}>
							{label("noFlows", "No attributed active flows in this snapshot.")}
						</Text>
					)}
				</Box>
			)}
			{snapshot?.truncated && (
				<Alert status="info">
					<AlertIcon />
					{label(
						"truncated",
						"The node snapshot reached its flow limit. Some flows are omitted.",
					)}
				</Alert>
			)}
			{snapshot?.node_resources && (
				<Text fontSize="sm">
					{label("nodeMemory", "Node memory used / total")}{" "}
					{bytes(snapshot.node_resources.memory_used_bytes)} /{" "}
					{bytes(snapshot.node_resources.memory_total_bytes)} B
				</Text>
			)}
			{snapshot?.infrastructure && (
				<Stack spacing={3}>
					<Heading size="md">
						{label("infrastructure", "Node network interfaces")}
					</Heading>
					<Text fontSize="sm">
						{label("infrastructureSample", "Sample time")}{" "}
						{new Date(snapshot.infrastructure.sampled_at).toLocaleString()}
					</Text>
					<TableContainer>
						<Table size="sm">
							<Thead>
								<Tr>
									<Th>{label("interface", "Interface")}</Th>
									<Th>MTU</Th>
									<Th>{label("interfaceState", "Interface state")}</Th>
								</Tr>
							</Thead>
							<Tbody>
								{snapshot.infrastructure.interfaces.map((item) => (
									<Tr key={item.name}>
										<Td>{item.name}</Td>
										<Td>{item.mtu}</Td>
										<Td>{item.oper_state || "N/A"}</Td>
									</Tr>
								))}
							</Tbody>
						</Table>
					</TableContainer>
					<TableContainer>
						<Table size="sm">
							<Thead>
								<Tr>
									<Th>{label("counter", "Counter")}</Th>
									<Th>{label("ratePerSecond", "Rate per second")}</Th>
								</Tr>
							</Thead>
							<Tbody>
								{Object.entries(
									snapshot.infrastructure.counter_rates_per_second ?? {},
								)
									.sort(([a], [b]) => a.localeCompare(b))
									.map(([name, rate]) => (
										<Tr key={name}>
											<Td>{name}</Td>
											<Td>{rate.toFixed(2)}</Td>
										</Tr>
									))}
							</Tbody>
						</Table>
					</TableContainer>
					{!Object.keys(snapshot.infrastructure.counter_rates_per_second ?? {})
						.length && <Text>N/A</Text>}
				</Stack>
			)}
			{node && (
				<NetworkQualityHistory key={`history-${node}`} nodeID={Number(node)} users={presence} />
			)}
			{node && (
				<NetworkOptimizationPanel key={`optimization-${node}`} nodeID={Number(node)} users={presence} />
			)}
			<Text fontSize="sm" color="panel.textMuted">
				{label(
					"measurement",
					"ACK RTT measures timing from the node observation point. In-game latency and UDP packet loss are N/A without a validated measurement source.",
				)}
			</Text>
		</Stack>
	);
}
