import {
	Alert,
	AlertIcon,
	Badge,
	Box,
	Button,
	FormControl,
	FormLabel,
	HStack,
	Input,
	Select,
	Spinner,
	Stack,
	Text,
} from "@chakra-ui/react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
	applyNetworkTCPMSS,
	getNetworkOptimizations,
	rollbackNetworkOptimization,
	type NetworkOptimizationOperation,
	type NetworkPresence,
} from "service/networkIntelligence";

export default function NetworkOptimizationPanel({
	nodeID,
	users,
}: {
	nodeID: number;
	users: NetworkPresence[];
}) {
	const { t } = useTranslation();
	const label = useCallback(
		(key: string, fallback: string) =>
			t(`networkIntelligence.${key}`, { defaultValue: fallback }),
		[t],
	);
	const accounts = useMemo(
		() =>
			Array.from(
				new Map(
					users
						.filter((user) => user.node_id === nodeID && user.vpn_protocol === "wireguard")
						.map((user) => [user.user_id, user.username]),
				).entries(),
			),
		[users, nodeID],
	);
	const [userID, setUserID] = useState("");
	const [mss, setMss] = useState("1340");
	const [operations, setOperations] = useState<NetworkOptimizationOperation[]>([]);
	const [loading, setLoading] = useState(false);
	const [saving, setSaving] = useState(false);
	const [unavailable, setUnavailable] = useState(false);
	const [error, setError] = useState("");
	const [message, setMessage] = useState("");

	useEffect(() => {
		setOperations([]);
		setUnavailable(false);
		setError("");
		setMessage("");
		if (!nodeID || !userID) return;
		const request = new AbortController();
		setLoading(true);
		void getNetworkOptimizations(nodeID, Number(userID), request.signal)
			.then((result) => setOperations(result.operations || []))
			.catch((failure: any) => {
				if (request.signal.aborted) return;
				if (failure?.statusCode === 404) setUnavailable(true);
				else setError(label("optimizationLoadError", "Optimization state is unavailable on this node."));
			})
			.finally(() => {
				if (!request.signal.aborted) setLoading(false);
			});
		return () => request.abort();
	}, [nodeID, userID, label]);

	const refresh = async () => {
		if (!userID) return;
		try {
			const result = await getNetworkOptimizations(nodeID, Number(userID), new AbortController().signal);
			setOperations(result.operations || []);
			setUnavailable(false);
		} catch (failure: any) {
			if (failure?.statusCode === 404) setUnavailable(true);
			else setError(label("optimizationLoadError", "Optimization state is unavailable on this node."));
		}
	};

	const apply = async () => {
		const tcpMSS = Number(mss);
		if (tcpMSS < 536 || tcpMSS > 1340 || !Number.isInteger(tcpMSS)) return;
		if (!window.confirm(label("optimizationApplyConfirm", "Apply this TCP MSS setting to the selected user's active WireGuard peers?"))) return;
		setSaving(true);
		setError("");
		setMessage("");
		try {
			const result = await applyNetworkTCPMSS(nodeID, Number(userID), tcpMSS);
			setMessage(label("optimizationApplied", "Applied. Operation ") + result.operation_id);
			await refresh();
		} catch (failure: any) {
			setError(failure?.data?.detail || label("optimizationApplyError", "The node refused the optimization or could not verify the active peer."));
			await refresh();
		} finally {
			setSaving(false);
		}
	};

	const rollback = async (operation: NetworkOptimizationOperation) => {
		if (!window.confirm(label("optimizationRollbackConfirm", "Remove this user's scoped TCP MSS optimization?"))) return;
		setSaving(true);
		setError("");
		setMessage("");
		try {
			await rollbackNetworkOptimization(nodeID, Number(userID), operation.operation_id);
			setMessage(label("optimizationRolledBack", "Optimization rolled back."));
			await refresh();
		} catch (failure: any) {
			setError(failure?.data?.detail || label("optimizationRollbackError", "Rollback failed or the rule changed after application."));
			await refresh();
		} finally {
			setSaving(false);
		}
	};

	return (
		<Box borderWidth="1px" borderRadius="xl" p={4}>
			<Stack spacing={4}>
				<Box>
					<Text fontWeight="semibold">{label("optimizationTitle", "Managed per-user optimization")}</Text>
					<Text fontSize="sm" color="panel.textMuted">
						{label("optimizationScope", "TCP MSS is applied only to authenticated WireGuard peer addresses. Xray and other users are not targeted; no routing or TC filter is changed.")}
					</Text>
				</Box>
				<FormControl>
					<FormLabel>{label("optimizationUser", "WireGuard user")}</FormLabel>
					<Select value={userID} onChange={(event) => setUserID(event.target.value)}>
						<option value="">{label("optimizationSelectUser", "Select an active WireGuard user")}</option>
						{accounts.map(([id, username]) => (
							<option key={id} value={id}>{username || id}</option>
						))}
					</Select>
				</FormControl>
				{userID && !unavailable && (
					<>
						<HStack align="end" spacing={3}>
							<FormControl maxW="180px">
								<FormLabel>{label("optimizationMSS", "TCP MSS")}</FormLabel>
								<Input type="number" min={536} max={1340} step={1} value={mss} onChange={(event) => setMss(event.target.value)} />
							</FormControl>
							<Button onClick={apply} isLoading={saving} isDisabled={loading || !Number.isInteger(Number(mss)) || Number(mss) < 536 || Number(mss) > 1340 || operations.some((item) => item.state === "active")}>
								{label("optimizationApply", "Apply TCP MSS")}
							</Button>
						</HStack>
						{loading && <Spinner size="sm" />}
						{operations.map((operation) => (
							<HStack key={operation.operation_id} justify="space-between" borderWidth="1px" borderRadius="md" p={3} flexWrap="wrap">
								<Box>
									<Text>{label("optimizationMSS", "TCP MSS")}: {operation.tcp_mss}</Text>
									<Badge>{operation.state}</Badge>
									<Text fontSize="xs" dir="ltr">{operation.operation_id}</Text>
								</Box>
								{operation.state === "active" && <Button size="sm" variant="outline" colorScheme="red" isLoading={saving} onClick={() => void rollback(operation)}>{label("optimizationRollback", "Rollback")}</Button>}
							</HStack>
						))}
					</>
				)}
				{unavailable && <Alert status="info"><AlertIcon />{label("optimizationDisabled", "Network optimization is disabled or unsupported on this node. No changes were made.")}</Alert>}
				{error && <Alert status="warning"><AlertIcon />{error}</Alert>}
				{message && <Alert status="success"><AlertIcon />{message}</Alert>}
			</Stack>
		</Box>
	);
}
