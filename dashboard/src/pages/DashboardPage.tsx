import { Statistics } from "../components/Statistics";
import { useQuery } from "react-query";
import useGetUser from "hooks/useGetUser";
import { AdminRole, AdminSudoScope } from "types/Admin";
import type { SystemStats } from "types/System";
import { fetch } from "service/http";
import { AMMetric, AMMetricGrid, AMPage, AMPageHeader, AMSection } from "components/am/AMPrimitives";

const systemKey = "statistics-query-key";

export const DashboardPage = () => {
	const { userData } = useGetUser();
	const system = useQuery<SystemStats>(systemKey, () => fetch("/system"), { staleTime: 30_000 });
	const canSeeMaintenance = userData.role === AdminRole.FullAccess || (userData.role === AdminRole.Sudo && Boolean(userData.permissions.sudo?.[AdminSudoScope.Maintenance]));
	const diagnosticQuery = useQuery<{diagnostics:Array<{severity:string;source:string}>}>(["am-diagnostics","active","",""], () => fetch("/maintenance/diagnostics?status=active&refresh=1"), { enabled:canSeeMaintenance, staleTime:30_000, refetchOnWindowFocus:false });
	const operationQuery = useQuery<{operations:Array<{state:string}>}>("am-command-operations", () => fetch("/maintenance/operations"), { enabled:canSeeMaintenance, staleTime:15_000, refetchOnWindowFocus:false });
	const value = system.data;
	const diagnostics = diagnosticQuery.data?.diagnostics;
	const criticalCount = diagnostics?.filter((item) => item.severity === "critical").length;
	const warningCount = diagnostics?.filter((item) => item.severity === "warning").length;
	const pendingCount = operationQuery.data?.operations.filter((item) => ["queued","running","waiting"].includes(item.state)).length;
	const driftCount = diagnostics?.filter((item) => item.source === "version-drift").length;
	return <AMPage>
		<AMPageHeader title="Command Center" description="Live control-plane overview. Metrics without a reliable source are marked unavailable." />
		<AMSection title="Current service picture" description="Values come from the existing system endpoint; daily and monthly traffic buckets are not currently exposed there.">
			<AMMetricGrid>
				<AMMetric label="Healthy nodes / total" value="Unavailable" hint="No reliable live fleet summary is exposed to this dashboard yet." />
				<AMMetric label="Active users" value={value ? value.users_active.toLocaleString() : system.isLoading ? "Loading" : "Unavailable"} />
				<AMMetric label="Online sessions" value={value ? value.online_users.toLocaleString() : system.isLoading ? "Loading" : "Unavailable"} />
				<AMMetric label="Traffic today" value="Unavailable" hint="Daily aggregate is not exposed by the current API." />
				<AMMetric label="Traffic this month" value="Unavailable" hint="Monthly aggregate is not exposed by the current API." />
				<AMMetric label="Critical diagnostics" value={criticalCount ?? "Unavailable"} hint={diagnosticQuery.isError ? "Diagnostics refresh failed." : canSeeMaintenance ? "Active persisted findings" : "Requires maintenance access."} tone={criticalCount ? "critical" : "neutral"} />
				<AMMetric label="Warning diagnostics" value={warningCount ?? "Unavailable"} hint={diagnosticQuery.isError ? "Diagnostics refresh failed." : canSeeMaintenance ? "Active persisted findings" : "Requires maintenance access."} tone={warningCount ? "warning" : "neutral"} />
				<AMMetric label="Pending operations" value={pendingCount ?? "Unavailable"} hint={operationQuery.isError ? "Operation feed request failed." : canSeeMaintenance ? "Queued, running, or waiting" : "Requires maintenance access."} />
				<AMMetric label="Update drift" value={driftCount ?? "Unavailable"} hint={diagnosticQuery.isError ? "Diagnostics refresh failed." : canSeeMaintenance ? "Reported node version drift" : "Requires maintenance access."} tone={driftCount ? "warning" : "neutral"} />
			</AMMetricGrid>
			{system.isError && <AMMetric label="Control-plane metrics" value="Unavailable" hint="System metrics request failed; see Diagnostics for its request ID." />}
		</AMSection>
		<AMSection title="Existing dashboard" description="Current charts, bandwidth history, backup controls and panel actions remain available below."><Statistics /></AMSection>
	</AMPage>;
};
