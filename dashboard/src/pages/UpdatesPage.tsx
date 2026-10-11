import { Button } from "@chakra-ui/react";
import { useQuery } from "react-query";
import { Link } from "react-router-dom";
import { AMAlert, AMEmptyState, AMLoading, AMMetric, AMMetricGrid, AMPage, AMPageHeader, AMSection, AMStatusBadge } from "components/am/AMPrimitives";
import { fetch } from "service/http";

type RuntimeInfo = { running_version?: string; tag?: string | null; channel?: string; update?: { current?: string | null; target?: string | null; available?: boolean; error?: string } };
type MaintenanceInfo = { panel?: RuntimeInfo; node_update?: { current?: string | null; target?: string | null; channel?: string; available?: boolean; error?: string } };
type Catalog = { stable?: Array<{ version: string }>; dev?: Array<{ version: string }> };
type Operation = {operation_type:string;target_type:string;target_id:string;state:string;phase:string;updated_at:number;metadata?:{snapshot?:{desired_version?:string;installed_version?:string;running_version?:string}}};

const show = (value?: string | null) => value?.trim() || "Unknown";
const unavailable = "This target has no update lifecycle exposed by the current API.";

export function UpdatesPage() {
	const info = useQuery<MaintenanceInfo>("am-maintenance-info", () => fetch("/maintenance/info"), { staleTime:30_000 });
	const panelCatalog = useQuery<Catalog>("am-panel-catalog", () => fetch("/maintenance/versions?target=panel"), { staleTime:5*60_000 });
	const nodeCatalog = useQuery<Catalog>("am-node-catalog", () => fetch("/maintenance/versions?target=node"), { staleTime:5*60_000 });
	const operationsQuery = useQuery<{operations:Operation[]}>("am-operations", () => fetch("/maintenance/operations"), { staleTime:15_000, refetchOnWindowFocus:false });
	if (info.isLoading) return <AMLoading />;
	if (info.isError) return <AMAlert status="error" title="Update information unavailable">The update status request failed. Check Diagnostics for the correlated request error.</AMAlert>;
	const panel = info.data?.panel;
	const node = info.data?.node_update;
	const latest = (catalog?:Catalog, channel:"stable"|"dev"="stable") => catalog?.[channel]?.[0]?.version || "Unavailable";
	const panelOperation = operationsQuery.data?.operations.find((operation)=>operation.target_type==="panel");
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
			<AMEmptyState title="Node runtime versions unavailable" detail="The current node runtime API does not expose a verified desired, installed, and running version tuple for every node. The catalog is shown above; node lifecycle actions remain on the existing node page." />
			{node?.error && <AMAlert status="warning" title="Node release check unavailable">The node release catalog returned an error; node reported versions are unchanged.</AMAlert>}
			<Button as={Link} to="/node-settings" mt={4} variant="outline">Manage node updates</Button>
		</AMSection>
		<AMSection title="Xray" description="No independent Xray update lifecycle is exposed by the current API."><AMEmptyState title="Unavailable" detail={unavailable} /></AMSection>
		<AMSection title="GeoIP / GeoSite" description="No asset update state is exposed by the current API."><AMEmptyState title="Unavailable" detail={unavailable} /></AMSection>
	</AMPage>;
}
