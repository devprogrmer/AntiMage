import { Button } from "@chakra-ui/react";
import { useQuery } from "react-query";
import { Link } from "react-router-dom";
import { AMAlert, AMEmptyState, AMLoading, AMMetric, AMMetricGrid, AMOperationStatus, AMPage, AMPageHeader, AMSection, AMStatusBadge } from "components/am/AMPrimitives";
import { fetch } from "service/http";
import { NodeRolloutControls } from "components/NodeRolloutControls";
import { maintenancePhaseNotice } from "components/am/maintenanceStateModel";
import type { NodeType } from "contexts/NodesContext";

type RuntimeInfo = { running_version?: string; tag?: string | null; channel?: string; update?: { current?: string | null; target?: string | null; available?: boolean; error?: string } };
type MaintenanceInfo = { panel?: RuntimeInfo; node_update?: { current?: string | null; target?: string | null; channel?: string; available?: boolean; error?: string } };
type Catalog = { stable?: Array<{ version: string }>; dev?: Array<{ version: string }> };
type Operation = {operation_type:string;target_type:string;target_id:string;state:string;phase:string;updated_at:number;metadata?:{snapshot?:{desired_version?:string;installed_version?:string;running_version?:string};update?:{desired_version?:string;installed_version?:string;running_version?:string}}};

const show = (value?: string | null) => value?.trim() || "Unknown";

export function UpdatesPage() {
	const info = useQuery<MaintenanceInfo>("am-maintenance-info", () => fetch("/maintenance/info"), { staleTime:30_000 });
	const panelCatalog = useQuery<Catalog>("am-panel-catalog", () => fetch("/maintenance/versions?target=panel"), { staleTime:5*60_000 });
	const nodeCatalog = useQuery<Catalog>("am-node-catalog", () => fetch("/maintenance/versions?target=node"), { staleTime:5*60_000 });
	const operationsQuery = useQuery<{operations:Operation[]}>("am-operations", () => fetch("/maintenance/operations"), { staleTime:15_000, refetchOnWindowFocus:false });
	const nodesQuery = useQuery<NodeType[]>("am-update-nodes", () => fetch("/nodes?include_metrics=1"), { staleTime:15_000, refetchInterval:30_000 });
	if (info.isLoading) return <AMLoading />;
	if (info.isError) return <AMAlert status="error" title="Update information unavailable">The update status request failed. Check Diagnostics for the correlated request error.</AMAlert>;
	const panel = info.data?.panel;
	const node = info.data?.node_update;
	const latest = (catalog?:Catalog, channel:"stable"|"dev"="stable") => catalog?.[channel]?.[0]?.version || "Unavailable";
	const panelOperation = operationsQuery.data?.operations.find((operation)=>operation.target_type==="panel");
	const runtimeOperations = (types: string[]) => (operationsQuery.data?.operations || []).filter((operation) => types.includes(operation.operation_type)).slice(0, 10);
	const runtimeHistory = (types: string[]) => operationsQuery.isLoading ? <AMLoading /> : operationsQuery.isError ? <AMAlert status="error" title="Operation evidence unavailable">The persisted operation feed could not be loaded.</AMAlert> : runtimeOperations(types).length === 0 ? <AMEmptyState title="No operations recorded" detail="No persisted operation evidence is available for this target. Runtime identity remains Unknown." /> : runtimeOperations(types).map((operation) => <div key={`${operation.target_id}-${operation.updated_at}-${operation.operation_type}`}><AMOperationStatus operation={operation.operation_type} target={`${operation.target_type} ${operation.target_id}`} state={operation.state} phase={operation.phase} />{maintenancePhaseNotice(operation.phase) && <AMAlert status="warning" title="Recovery state">{maintenancePhaseNotice(operation.phase)}</AMAlert>}</div>);
	return <AMPage>
		<AMPageHeader title="Update Center" description="Review active runtime reports, installed metadata, desired targets, channels, and update state." actions={<Button as={Link} to="/operations" variant="outline">Operation history</Button>} />
		<AMSection title="Panel" description="Running version comes from the active panel runtime; installed tag and desired target remain separate.">
			<AMMetricGrid>
				<AMMetric label="Running version" value={show(panel?.running_version)} />
				<AMMetric label="Installed tag" value={show(panel?.tag)} />
				<AMMetric label="Desired version" value={show(panelOperation?.metadata?.snapshot?.desired_version || panel?.update?.target)} />
				<AMMetric label="Channel" value={show(panel?.channel)} />
				<AMMetric label="Latest stable" value={latest(panelCatalog.data,"stable")} hint={panelCatalog.isError?"Catalog unavailable":undefined} />
				<AMMetric label="Latest dev" value={latest(panelCatalog.data,"dev")} hint={panelCatalog.isError?"Catalog unavailable":undefined} />
			</AMMetricGrid>
			<div style={{marginTop:16}}><AMStatusBadge state={panelOperation?.state || (panel?.update?.available?"update available":panel?.update?.error?"warning":"current")} /> {panelOperation ? ` · ${panelOperation.phase}` : " · No active operation"}</div>
			{panel?.update?.error && <AMAlert status="warning" title="Release check unavailable">The panel update catalog returned an error. The current reported runtime remains unchanged.</AMAlert>}
		</AMSection>
		<AMSection title="Nodes" description="Runtime version is shown only when reported by the node agent; installed metadata does not fill an unknown runtime value.">
			<AMMetricGrid><AMMetric label="Latest stable" value={latest(nodeCatalog.data,"stable")} hint={nodeCatalog.isError?"Catalog unavailable":undefined} /><AMMetric label="Latest dev" value={latest(nodeCatalog.data,"dev")} hint={nodeCatalog.isError?"Catalog unavailable":undefined} /><AMMetric label="Update catalog channel" value={show(node?.channel)} /></AMMetricGrid>
			{nodesQuery.isLoading ? <AMLoading /> : nodesQuery.isError ? <AMAlert status="error" title="Node runtime evidence unavailable">Node version reports could not be loaded.</AMAlert> : !nodesQuery.data?.length ? <AMEmptyState title="No nodes reported" detail="No node runtime evidence is available." /> : nodesQuery.data.map((item) => {
				const operation = operationsQuery.data?.operations.find((entry) => entry.target_type === "node" && entry.target_id === String(item.id));
				return <AMSection key={item.id} title={item.name}><AMMetricGrid><AMMetric label="Running version" value={show(item.node_service_version)} /><AMMetric label="Installed tag" value={show(item.node_binary_tag)} /><AMMetric label="Desired version" value={show(operation?.metadata?.update?.desired_version)} /></AMMetricGrid>{operation && <AMOperationStatus operation={operation.operation_type} target={`node ${item.id}`} state={operation.state} phase={operation.phase} />}</AMSection>;
			})}
			{node?.error && <AMAlert status="warning" title="Node release check unavailable">The node release catalog returned an error; node reported versions are unchanged.</AMAlert>}
			<Button as={Link} to="/node-settings" mt={4} variant="outline">Manage node updates</Button>
			<NodeRolloutControls />
		</AMSection>
		<AMSection title="Xray" description="Persisted Core update and restart outcomes. A command acknowledgement alone does not prove a healthy runtime.">{runtimeHistory(["core_update", "core_restart"])}</AMSection>
		<AMSection title="GeoIP / GeoSite" description="Persisted dataset update outcomes. Recovery verifies committed files and reload evidence before reporting success.">{runtimeHistory(["geo_update"])}</AMSection>
	</AMPage>;
}
