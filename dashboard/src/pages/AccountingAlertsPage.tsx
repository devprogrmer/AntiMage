import { Badge, Box, Button, Heading, HStack, Spinner, Text, VStack } from "@chakra-ui/react";
import { useCallback, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { fetch } from "service/http";

type AlertItem = {
	node_id: number;
	node_name: string;
	protocol: string;
	kind: string;
	opened_at: number;
	resolved_at?: number;
	read_at?: number;
};

const alertDescription: Record<string, string> = {
	checkpoint_overdue: "accountingHealth.checkpointOverdue",
	pending_ack_overdue: "accountingHealth.pendingACKOverdue",
	reflection_overdue: "accountingHealth.reflectionOverdue",
};

export default function AccountingAlertsPage() {
	const { t } = useTranslation();
	const [items, setItems] = useState<AlertItem[]>([]);
	const [loading, setLoading] = useState(true);
	const [error, setError] = useState("");
	const [busy, setBusy] = useState("");
	const load = useCallback(async () => {
		try {
			setItems(await fetch<AlertItem[]>("/nodes/accounting-alerts"));
			setError("");
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : t("accountingInbox.loadFailed"));
		} finally {
			setLoading(false);
		}
	}, [t]);
	useEffect(() => {
		void load();
		const timer = window.setInterval(() => void load(), 30_000);
		return () => window.clearInterval(timer);
	}, [load]);
	const markRead = async (item: AlertItem) => {
		const key = `${item.node_id}:${item.protocol}:${item.kind}`;
		setBusy(key);
		try {
			await fetch("/nodes/accounting-alerts/read", {
				method: "POST",
				body: JSON.stringify({ node_id: item.node_id, protocol: item.protocol, kind: item.kind }),
			});
			await load();
		} catch (cause) {
			setError(cause instanceof Error ? cause.message : t("accountingInbox.readFailed"));
		} finally {
			setBusy("");
		}
	};
	return (
		<VStack align="stretch" spacing={5}>
			<HStack justify="space-between"><Box><Heading size="lg">{t("accountingInbox.title")}</Heading><Text color="panel.textSecondary">{t("accountingInbox.description")}</Text></Box><Button onClick={() => void load()}>{t("accountingInbox.refresh")}</Button></HStack>
			{error && <Text color="red.400" role="alert">{error}</Text>}
			{loading ? <Spinner /> : items.length === 0 ? <Text>{t("accountingInbox.empty")}</Text> : items.map((item) => {
				const key = `${item.node_id}:${item.protocol}:${item.kind}`;
				return <Box key={key} p={4} borderWidth="1px" borderColor="panel.border" borderRadius="lg" bg="panel.elevated">
					<HStack justify="space-between" align="start" flexWrap="wrap"><Box><HStack mb={2}><Text fontWeight="bold">{item.node_name}</Text><Badge>{item.protocol}</Badge><Badge colorScheme={item.resolved_at ? "green" : "orange"}>{t(item.resolved_at ? "accountingInbox.resolved" : "accountingInbox.active")}</Badge>{!item.read_at && <Badge colorScheme="pink">{t("accountingInbox.unread")}</Badge>}</HStack><Text>{t(alertDescription[item.kind] ?? "accountingHealth.failure")}</Text><Text fontSize="sm" color="panel.textSecondary">{new Date(item.opened_at * 1000).toLocaleString()}</Text></Box>{!item.read_at && <Button size="sm" isLoading={busy === key} onClick={() => void markRead(item)}>{t("accountingInbox.markRead")}</Button>}</HStack>
				</Box>;
			})}
		</VStack>
	);
}
