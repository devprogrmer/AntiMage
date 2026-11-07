import {
	Box,
	Popover,
	PopoverArrow,
	PopoverBody,
	PopoverContent,
	PopoverTrigger,
	Stack,
	Tag,
	Text,
} from "@chakra-ui/react";
import type { NodeType } from "contexts/NodesContext";
import dayjs from "dayjs";
import { useTranslation } from "react-i18next";
import { formatBytes } from "utils/formatByte";

type Accounting = NonNullable<NodeType["accounting"]>;

export function NodeAccountingHealth({ accounting }: { accounting?: Accounting | null }) {
	const { t } = useTranslation();
	if (!accounting) {
		return <Text color="panel.textMuted">-</Text>;
	}
	const failures = accounting.protocols.filter((item) => item.failure);
	const pending = accounting.protocols.filter((item) => item.pending_batch_id);
	const incomplete = !accounting.reflection_ledger_loaded || accounting.protocols.some((item) => !item.state_loaded);
	const hasTimingWarning = accounting.protocols.some(
		(item) => item.checkpoint_overdue || item.pending_ack_overdue,
	);
	const state = failures.length > 0 ? "degraded" : hasTimingWarning ? "warning" : pending.length > 0 || accounting.awaiting_reflection_batches > 0 ? "pending" : incomplete ? "unknown" : "healthy";
	const color = state === "degraded" ? "red" : state === "warning" ? "orange" : state === "pending" ? "yellow" : state === "healthy" ? "green" : "gray";
	return (
		<Popover trigger="click" placement="auto" isLazy>
			<PopoverTrigger>
				<Tag
					as="button"
					type="button"
					size="sm"
					colorScheme={color}
					onClick={(event) => event.stopPropagation()}
					aria-label={t("accountingHealth.open")}
				>
					{t(`accountingHealth.${state}`)}
				</Tag>
			</PopoverTrigger>
			<PopoverContent maxW="540px" p={0} fontSize="sm" onClick={(event) => event.stopPropagation()}>
				<PopoverArrow />
				<PopoverBody p={4}>
					<Stack spacing={3} maxH="60vh" overflowY="auto">
						<Text fontWeight="bold">{t("accountingHealth.title")}</Text>
						<Text color="panel.textSecondary">
							{t("accountingHealth.reflection")}: {accounting.reflection_ledger_loaded
								? `${accounting.awaiting_reflection_batches} · ${accounting.reflection_bytes_overflow ? "≥" : ""}${formatBytes(accounting.awaiting_reflection_bytes)}`
								: t("accountingHealth.unknown")}
						</Text>
						{accounting.protocols.map((item) => (
							<Box key={item.protocol} borderTopWidth="1px" borderColor="panel.border" pt={2}>
								<Text fontWeight="semibold">{item.protocol.toUpperCase()}</Text>
								<Text color="panel.textSecondary">
									{t("accountingHealth.checkpoint")}: {item.last_checkpoint_unix > 0
										? dayjs.unix(item.last_checkpoint_unix).format("YYYY-MM-DD HH:mm:ss")
										: t("accountingHealth.unknown")}
								</Text>
								<Text color="panel.textSecondary" overflowWrap="anywhere">
									{t("accountingHealth.pending")}: {item.state_loaded
										? item.pending_batch_id
											? `${item.pending_batch_id} · ${item.pending_bytes_overflow ? "≥" : ""}${formatBytes(item.pending_bytes)}`
											: t("accountingHealth.none")
										: t("accountingHealth.unknown")}
								</Text>
								<Text color="panel.textSecondary" overflowWrap="anywhere">
									{t("accountingHealth.lastAck")}: {item.state_loaded
										? item.last_acked_batch_id || t("accountingHealth.none")
										: t("accountingHealth.unknown")}
								</Text>
								{item.checkpoint_overdue && (
									<Text color="orange.400">{t("accountingHealth.checkpointOverdue")}</Text>
								)}
								{item.pending_ack_overdue && (
									<Text color="orange.400">{t("accountingHealth.pendingACKOverdue")}</Text>
								)}
								{item.failure && (
									<Text color="red.400" overflowWrap="anywhere">
										{t("accountingHealth.failure")}: {item.failure}
									</Text>
								)}
							</Box>
						))}
					</Stack>
				</PopoverBody>
			</PopoverContent>
		</Popover>
	);
}
