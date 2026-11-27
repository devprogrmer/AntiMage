import {
	Alert,
	AlertIcon,
	Box,
	FormControl,
	FormLabel,
	Heading,
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
	useColorMode,
} from "@chakra-ui/react";
import { lazy, Suspense, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	getNetworkHistory,
	type NetworkHistory,
	type NetworkPresence,
} from "service/networkIntelligence";

const Chart = lazy(() => import("react-apexcharts"));
const measured = (value: number | null) =>
	value !== null && Number.isFinite(value) && value >= 0 ? value : null;

export default function NetworkQualityHistory({
	nodeID,
	users,
}: {
	nodeID: number;
	users: NetworkPresence[];
}) {
	const { t } = useTranslation();
	const { colorMode } = useColorMode();
	const [userID, setUserID] = useState("");
	const [history, setHistory] = useState<NetworkHistory | null>(null);
	const [failed, setFailed] = useState(false);
	const [loading, setLoading] = useState(false);
	const accounts = useMemo(
		() =>
			Array.from(
				new Map(
					users
						.filter((user) => user.node_id === nodeID)
						.map((user) => [user.user_id, user.username]),
				).entries(),
			),
		[users, nodeID],
	);
	const selected = accounts.some(([id]) => String(id) === userID)
		? Number(userID)
		: 0;
	useEffect(() => {
		setHistory(null);
		setFailed(false);
		setLoading(false);
		if (!selected || nodeID <= 0) return;
		let disposed = false;
		let timer: ReturnType<typeof setTimeout> | undefined;
		let pending: AbortController | undefined;
		const load = async () => {
			if (disposed || document.hidden) return;
			const request = new AbortController();
			pending = request;
			setLoading(true);
			try {
				const result = await getNetworkHistory(
					nodeID,
					selected,
					request.signal,
				);
				if (!disposed && !request.signal.aborted) {
					setHistory(result);
					setFailed(false);
				}
			} catch {
				if (!disposed && !request.signal.aborted) {
					setHistory(null);
					setFailed(true);
				}
			} finally {
				if (!disposed && !request.signal.aborted) {
					setLoading(false);
					timer = setTimeout(load, 5000);
				}
			}
		};
		const visible = () => {
			clearTimeout(timer);
			pending?.abort();
			setLoading(false);
			if (!document.hidden) void load();
		};
		void load();
		document.addEventListener("visibilitychange", visible);
		return () => {
			disposed = true;
			pending?.abort();
			clearTimeout(timer);
			document.removeEventListener("visibilitychange", visible);
		};
	}, [nodeID, selected]);
	const points = (history?.points || [])
		.filter(
			(point) =>
				point.node_id === nodeID &&
				point.user_id === selected &&
				Number.isFinite(Date.parse(point.at)),
		)
		.sort((a, b) => Date.parse(a.at) - Date.parse(b.at));
	const series = [
		{
			name: t("networkIntelligence.historyRTT", {
				defaultValue: "Upload ACK RTT",
			}),
			data: points.map((point) => ({
				x: Date.parse(point.at),
				y: measured(point.ack_rtt_ms),
			})),
		},
		{
			name: t("networkIntelligence.historyJitter", {
				defaultValue: "ACK RTT variation",
			}),
			data: points.map((point) => ({
				x: Date.parse(point.at),
				y: measured(point.jitter_ms),
			})),
		},
	];
	return (
		<Stack spacing={3}>
			<Heading size="md">
				{t("networkIntelligence.historyTitle", {
					defaultValue: "User quality history",
				})}
			</Heading>
			<FormControl maxW="md">
				<FormLabel htmlFor="network-history-user">
					{t("networkIntelligence.historyUser", {
						defaultValue: "History account",
					})}
				</FormLabel>
				<Select
					id="network-history-user"
					value={selected ? userID : ""}
					onChange={(event) => setUserID(event.target.value)}
				>
					<option value="">
						{t("networkIntelligence.historySelect", {
							defaultValue: "Select an account",
						})}
					</option>
					{accounts.map(([id, name]) => (
						<option key={id} value={id}>
							{name || id}
						</option>
					))}
				</Select>
			</FormControl>
			{loading && <Spinner size="sm" />}
			{failed && (
				<Alert status="warning">
					<AlertIcon />
					{t("networkIntelligence.historyError", {
						defaultValue: "Quality history is unavailable.",
					})}
				</Alert>
			)}
			{history && (
				<>
					{history.storage === "volatile" && (
						<Text color="panel.textMuted" fontSize="sm">
							{t("networkIntelligence.historyVolatile", {
								defaultValue:
									"History is held in memory and clears when the node restarts.",
							})}
						</Text>
					)}
					{history.collection === "on_telemetry_read" && (
						<Text color="panel.textMuted" fontSize="sm">
							{t("networkIntelligence.historyPolling", {
								defaultValue:
									"Samples are collected while telemetry is read. Gaps may occur between observations.",
							})}
						</Text>
					)}
					{history.truncated && (
						<Alert status="info">
							<AlertIcon />
							{t("networkIntelligence.historyTruncated", {
								defaultValue:
									"The response limit was reached. This account history may be incomplete.",
							})}
						</Alert>
					)}
					{!history.available || !points.length ? (
						<Text>
							{t("networkIntelligence.historyEmpty", {
								defaultValue:
									"No quality history is available for this account.",
							})}
						</Text>
					) : (
						<>
							<Box
								minH="260px"
								dir="ltr"
								aria-label={t("networkIntelligence.historyChart", {
									defaultValue: "Observed TCP ACK timing history",
								})}
							>
								<Suspense fallback={<Spinner size="sm" />}>
									<Chart
										type="line"
										height={260}
										series={series}
										options={{
											chart: {
												animations: { enabled: false },
												toolbar: { show: false },
												zoom: { enabled: false },
											},
											theme: { mode: colorMode },
											xaxis: { type: "datetime" },
											yaxis: { min: 0, title: { text: "ms" } },
											stroke: { width: 2, curve: "straight" },
											markers: { size: 3 },
											noData: { text: "N/A" },
											tooltip: { x: { format: "yyyy-MM-dd HH:mm:ss" } },
										}}
									/>
								</Suspense>
							</Box>
							<Box as="details">
								<Box as="summary" cursor="pointer">
									{t("networkIntelligence.historySamples", {
										defaultValue: "Observed samples",
									})}
								</Box>
								<TableContainer>
									<Table size="sm">
										<Thead>
											<Tr>
												<Th>
													{t("networkIntelligence.lastSeen", {
														defaultValue: "Last observed",
													})}
												</Th>
												<Th>
													{t("networkIntelligence.historyRTT", {
														defaultValue: "Upload ACK RTT",
													})}{" "}
													(ms)
												</Th>
												<Th>
													{t("networkIntelligence.historyJitter", {
														defaultValue: "ACK RTT variation",
													})}{" "}
													(ms)
												</Th>
											</Tr>
										</Thead>
										<Tbody>
											{points.map((point) => (
												<Tr key={point.at}>
													<Td>{new Date(point.at).toLocaleString()}</Td>
													<Td>
														{measured(point.ack_rtt_ms)?.toFixed(1) ?? "N/A"}
													</Td>
													<Td>
														{measured(point.jitter_ms)?.toFixed(1) ?? "N/A"}
													</Td>
												</Tr>
											))}
										</Tbody>
									</Table>
								</TableContainer>
							</Box>
							{points.flatMap((point) =>
								point.findings.map((finding) => (
									<Alert key={`${point.at}-${finding.code}`} status="warning">
										<AlertIcon />
										<Box>
											<Text fontSize="sm">
												{new Date(point.at).toLocaleString()} ·{" "}
												{finding.evidence}
											</Text>
											<Text fontSize="sm">{finding.recommendation}</Text>
										</Box>
									</Alert>
								)),
							)}
						</>
					)}
				</>
			)}
		</Stack>
	);
}
