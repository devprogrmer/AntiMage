import {
	Alert,
	AlertIcon,
	Button,
	FormControl,
	FormLabel,
	HStack,
	Modal,
	ModalBody,
	ModalCloseButton,
	ModalContent,
	ModalFooter,
	ModalHeader,
	ModalOverlay,
	Spinner,
	Stack,
	Text,
} from "@chakra-ui/react";
import { PanelSelect } from "components/common/PanelSelect";
import { useEffect, useMemo, useState } from "react";
import { useQuery } from "react-query";
import { useTranslation } from "react-i18next";
import { fetch } from "service/http";
import type { NodeType } from "contexts/NodesContext";
import { canFenceNodeCommands, maintenancePhaseNotice } from "components/am/maintenanceStateModel";

type Build = {
	version: string;
	channel: string;
	commit?: string;
	published_at?: string;
	workflow_run_id?: string;
	artifact_name: string;
	sha256: string;
	arch: string;
};
type Catalog = { stable: Build[]; dev: Build[] };
type UpdateOperation = {
	operation_id: string;
	phase: string;
	requested_channel: string;
	requested_version: string;
	previous_version?: string;
	resolved_version?: string;
	updated_at: string;
	desired_version?: string;
	installed_version?: string;
	running_version?: string;
	error?: string;
	rollback_error?: string;
	started_at: string;
	completed_at?: string;
};

const sameNodeBuild = (installed: string, running: string) => {
	const left = installed.trim().toLowerCase();
	const right = running.trim().toLowerCase();
	if (left === right) return true;
	if (!left.startsWith("dev-") || !right.startsWith("dev-")) return false;
	const leftSha = left.slice(4);
	const rightSha = right.slice(4);
	return (
		(leftSha.length >= 7 && rightSha.startsWith(leftSha)) ||
		(rightSha.length >= 7 && leftSha.startsWith(rightSha))
	);
};

export type NodeUpdateTarget = { channel: "stable" | "dev"; version: string };

export const NodeServiceUpdateDialog = ({
	isOpen,
	node,
	isSubmitting,
	nodes = [],
	bulkStatuses = {},
	onClose,
	onSubmit,
}: {
	isOpen: boolean;
	node: NodeType | null;
	isSubmitting: boolean;
	nodes?: NodeType[];
	bulkStatuses?: Record<
		number,
		{ state: "pending" | "completed" | "failed"; error?: string }
	>;
	onClose: () => void;
	onSubmit: (target: NodeUpdateTarget) => void;
}) => {
	const { t } = useTranslation();
	const [channel, setChannel] = useState<"stable" | "dev">("stable");
	const [version, setVersion] = useState("");
	const nodeId = node?.id;
	const isBulk = nodes.length > 1;
	const unsupportedNodes = (isBulk ? nodes : node ? [node] : []).filter((item) => !canFenceNodeCommands(item.capabilities));
	const catalog = useQuery<Catalog>(
		["maintenance-versions", "node", channel],
		() =>
			fetch<Catalog>("/maintenance/versions", {
				query: { target: "node", refresh: "1" },
				timeout: 20_000,
			}),
		{ enabled: isOpen, staleTime: 10 * 60 * 1000, retry: false },
	);
	const history = useQuery<{ updates: UpdateOperation[] }>(
		["node-service-update-history", nodeId],
		() =>
			fetch(`/node/${nodeId}/service/update/history`, { query: { limit: 10 } }),
		{
			enabled: isOpen && nodeId != null,
			refetchInterval: isSubmitting ? 1500 : false,
			retry: false,
		},
	);
	const builds =
		channel === "dev"
			? (catalog.data?.dev ?? [])
			: (catalog.data?.stable ?? []);
	const selectedBuild = builds.find((build) => build.version === version);
	const latestOperation = history.data?.updates?.[0];
	const runningVersion = node?.node_service_version || "-";
	const unknownVersion = t("nodes.versionUnknown");
	const installedVersion =
		latestOperation?.installed_version || unknownVersion;
	const desiredVersion =
		latestOperation?.desired_version ||
		latestOperation?.resolved_version ||
		latestOperation?.requested_version ||
		unknownVersion;
	const drift =
		installedVersion !== unknownVersion &&
		runningVersion !== "-" &&
		!sameNodeBuild(installedVersion, runningVersion);

	useEffect(() => {
		if (!isOpen) return;
		setChannel(node?.node_update_channel === "dev" ? "dev" : "stable");
		setVersion("");
	}, [isOpen, node?.node_update_channel]);
	useEffect(() => {
		if (!version && builds.length > 0) setVersion(builds[0].version);
	}, [version, builds]);

	const versionOptions = useMemo(
		() =>
			builds.map((build) => ({
				value: build.version,
				label: `${build.version}${build.commit ? ` · ${build.commit.slice(0, 7)}` : ""}${build.published_at ? ` · ${new Date(build.published_at).toLocaleDateString()}` : ""}`,
				searchLabel: `${build.version} ${build.commit ?? ""} ${build.artifact_name}`,
			})),
		[builds],
	);

	return (
		<Modal isOpen={isOpen} onClose={onClose} size="lg">
			<ModalOverlay />
			<ModalContent>
				<ModalHeader>
					{isBulk
						? t("nodes.serviceUpdateDialog.bulkTitle", { count: nodes.length })
						: t("nodes.serviceUpdateDialog.title", {
								name: node?.name ?? node?.address ?? t("nodes.unnamedNode"),
							})}
				</ModalHeader>
				<ModalCloseButton />
				<ModalBody>
					<Stack spacing={4}>
						{unsupportedNodes.length > 0 && <Alert status="warning"><AlertIcon />Agent upgrade required for shared fencing and command idempotency. Read-only connection remains available. Updates are disabled for this selection.</Alert>}
						{latestOperation && maintenancePhaseNotice(latestOperation.phase) && <Alert status="warning"><AlertIcon />{maintenancePhaseNotice(latestOperation.phase)}</Alert>}
						{!isBulk && (
							<Stack spacing={1}>
								<Text>
									{t("nodes.serviceUpdateDialog.running", {
										version: runningVersion,
									})}
								</Text>
								<Text>
									{t("nodes.serviceUpdateDialog.installed", {
										version: installedVersion,
									})}
								</Text>
								<Text>
									{t("nodes.serviceUpdateDialog.desired", {
										version: desiredVersion,
									})}
								</Text>
								{drift && (
									<Alert status="warning">
										<AlertIcon />
										{t("nodes.serviceUpdateDialog.drift")}
									</Alert>
								)}
							</Stack>
						)}
						<FormControl>
							<FormLabel>{t("nodes.serviceUpdateDialog.channel")}</FormLabel>
							<PanelSelect
								value={channel}
								onValueChange={(value) => {
									if (typeof value === "string") {
										setChannel(value as "stable" | "dev");
										setVersion("");
									}
								}}
								options={[
									{
										value: "stable",
										label: t("nodes.serviceUpdateDialog.stable"),
									},
									{ value: "dev", label: t("nodes.serviceUpdateDialog.dev") },
								]}
							/>
						</FormControl>
						<FormControl>
							<FormLabel>{t("nodes.serviceUpdateDialog.version")}</FormLabel>
							<Button
								size="xs"
								variant="link"
								onClick={() => void catalog.refetch()}
								isLoading={catalog.isFetching}
							>
								{t("nodes.serviceUpdateDialog.refresh")}
							</Button>
							{catalog.isLoading ? (
								<HStack>
									<Spinner size="sm" />
									<Text>{t("nodes.serviceUpdateDialog.loading")}</Text>
								</HStack>
							) : (
								<PanelSelect
									value={version}
									onValueChange={(value) =>
										typeof value === "string" && setVersion(value)
									}
									options={versionOptions}
									placeholder={t("nodes.serviceUpdateDialog.chooseVersion")}
								/>
							)}
						</FormControl>
						{catalog.isError && (
							<Alert status="error">
								<AlertIcon />
								{t("nodes.serviceUpdateDialog.catalogError")}
							</Alert>
						)}
						{selectedBuild && (
							<Text fontSize="sm" color="fg.muted">
								{selectedBuild.artifact_name}
								{selectedBuild.commit ? ` · ${selectedBuild.commit}` : ""}
								{selectedBuild.published_at
									? ` · ${new Date(selectedBuild.published_at).toLocaleString()}`
									: ""}
							</Text>
						)}
						{!isBulk && latestOperation && (
							<Text fontSize="sm">
								{t("nodes.serviceUpdateDialog.lastOperation", {
									phase: latestOperation.phase,
									version:
										latestOperation.running_version ||
										latestOperation.requested_version,
								})}
								{latestOperation.error ? ` — ${latestOperation.error}` : ""}
								{latestOperation.rollback_error
									? ` — ${latestOperation.rollback_error}`
									: ""}
							</Text>
						)}
						{!isBulk && (history.data?.updates?.length ?? 0) > 0 && (
							<Stack spacing={1} maxH="220px" overflowY="auto">
								<Text fontWeight="semibold">
									{t("nodes.serviceUpdateDialog.history")}
								</Text>
								{history.data?.updates.map((item) => {
									const end = new Date(
										item.completed_at || item.updated_at,
									).getTime();
									const start = new Date(item.started_at).getTime();
									const seconds = Number.isFinite(end - start)
										? Math.max(0, Math.round((end - start) / 1000))
										: 0;
									const target =
										item.running_version ||
										item.resolved_version ||
										item.requested_version ||
										"-";
									return (
										<Text key={item.operation_id} fontSize="sm">
											{new Date(item.started_at).toLocaleString()} ·{" "}
											{item.previous_version || "-"} → {target} ·{" "}
											{item.requested_channel} · {item.phase} · {seconds}s
											{item.rollback_error
												? ` · rollback: ${item.rollback_error}`
												: ""}
											{item.error ? ` · ${item.error}` : ""}
										</Text>
									);
								})}
							</Stack>
						)}
						{isBulk && (
							<Stack spacing={1} maxH="220px" overflowY="auto">
								{nodes.map((target) => {
									const status =
										target.id == null ? undefined : bulkStatuses[target.id];
									return (
										<Text key={target.id ?? target.name} fontSize="sm">
											{target.name ?? target.address}:{" "}
											{status?.state ?? t("nodes.serviceUpdateDialog.pending")}
											{status?.error ? ` — ${status.error}` : ""}
										</Text>
									);
								})}
							</Stack>
						)}
					</Stack>
				</ModalBody>
				<ModalFooter>
					<Button
						variant="ghost"
						mr={3}
						onClick={onClose}
						isDisabled={isSubmitting}
					>
						{t("cancel")}
					</Button>
					<Button
						colorScheme="blue"
						onClick={() => onSubmit({ channel, version })}
						isLoading={isSubmitting}
						isDisabled={
							!nodeId || !selectedBuild || catalog.isFetching || catalog.isError || unsupportedNodes.length > 0
						}
					>
						{t("nodes.serviceUpdateDialog.submit")}
					</Button>
				</ModalFooter>
			</ModalContent>
		</Modal>
	);
};
