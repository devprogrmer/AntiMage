import { Badge, Box, Button, HStack, Input, Select, Table, Tbody, Td, Text, Th, Thead, Tr } from "@chakra-ui/react";
import { useMemo, useState, useSyncExternalStore } from "react";
import { useMutation, useQuery, useQueryClient } from "react-query";
import { AMAlert, AMEmptyState, AMLoading, AMPage, AMPageHeader, AMSection } from "components/am/AMPrimitives";
import { clearRequestErrors, getRequestErrors, subscribeRequestErrors } from "service/requestErrors";
import { fetch } from "service/http";

type Diagnostic = { id:string; source:string; resource_type:string; resource_id:string; severity:string; code:string; summary:string; detail:string; first_seen_at:number; last_seen_at:number; occurrence_count:number; status:string; recommended_action:string };
type DiagnosticResponse = { diagnostics: Diagnostic[]; refreshed_at:number };

const resourcePath = (resource: Diagnostic) => resource.resource_type === "node" ? `/node-settings?node=${encodeURIComponent(resource.resource_id)}` : resource.source === "xray" ? "/xray-settings" : resource.source === "migrations" || resource.source === "database" ? "/settings" : null;

export function DiagnosticsPage() {
	const errors = useSyncExternalStore(subscribeRequestErrors, getRequestErrors, getRequestErrors);
	const [search, setSearch] = useState("");
	const [status, setStatus] = useState("active");
	const [severity, setSeverity] = useState("");
	const [source, setSource] = useState("");
	const queryClient = useQueryClient();
	const query = useQuery<DiagnosticResponse>(["am-diagnostics", status, severity, source], () => fetch(`/maintenance/diagnostics?${new URLSearchParams({ status, severity, source, refresh:"1" })}`), { staleTime: 30_000, refetchOnWindowFocus: false });
	const acknowledge = useMutation((id:string) => fetch(`/maintenance/diagnostics/${encodeURIComponent(id)}/acknowledge`, { method:"POST" }), { onSuccess: () => queryClient.invalidateQueries("am-diagnostics") });
	const visible = useMemo(() => (query.data?.diagnostics || []).filter((item) => `${item.source} ${item.resource_type} ${item.resource_id} ${item.code} ${item.summary} ${item.detail}`.toLowerCase().includes(search.toLowerCase())), [query.data, search]);
	return <AMPage>
		<AMPageHeader title="Diagnostics" description="Persistent system findings and sanitized request failures, with filters and resource links." actions={<HStack><Button variant="outline" onClick={() => void query.refetch()} isLoading={query.isFetching}>Refresh</Button><Button variant="outline" onClick={clearRequestErrors} isDisabled={!errors.length}>Clear browser errors</Button></HStack>} />
		{query.isError && <AMAlert status="error" title="Diagnostics could not be refreshed">Check the request ID in the browser errors below. Previously stored diagnostics may remain available after the API recovers.</AMAlert>}
		<AMSection title={`System findings (${query.data?.diagnostics.length ?? 0})`} description="Sources are refreshed on demand and repeated observations are deduplicated.">
			<HStack mb={4} wrap="wrap"><Input aria-label="Search diagnostics" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="Search source, resource, code or detail" maxW="lg" /><Select aria-label="Diagnostic status" value={status} onChange={(event) => setStatus(event.target.value)} maxW="xs"><option value="active">Active</option><option value="acknowledged">Acknowledged</option><option value="resolved">Resolved</option><option value="">All states</option></Select><Select aria-label="Diagnostic severity" value={severity} onChange={(event) => setSeverity(event.target.value)} maxW="xs"><option value="">All severities</option><option value="critical">Critical</option><option value="error">Error</option><option value="warning">Warning</option><option value="info">Info</option></Select><Input aria-label="Diagnostic source filter" value={source} onChange={(event) => setSource(event.target.value)} placeholder="Source" maxW="xs" /></HStack>
			{query.isLoading ? <AMLoading /> : visible.length === 0 ? <AMEmptyState title="No matching diagnostics" detail="No matching persisted findings were returned by the current collectors." /> : <Box overflowX="auto"><Table size="sm"><Thead><Tr><Th>Severity</Th><Th>Resource</Th><Th>Finding</Th><Th>First / last seen</Th><Th>Count</Th><Th>Status</Th><Th>Actions</Th></Tr></Thead><Tbody>{visible.map((item) => <Tr key={item.id}><Td><Badge colorScheme={item.severity === "critical" || item.severity === "error" ? "red" : item.severity === "warning" ? "orange" : "blue"}>{item.severity}</Badge></Td><Td>{item.resource_type}{item.resource_id ? <> · <Text as="span" dir="ltr" fontFamily="mono" sx={{ unicodeBidi:"isolate" }}>{item.resource_id}</Text></> : ""}</Td><Td><b>{item.summary}</b><br />{item.detail}<br /><small>Recommended: {item.recommended_action}</small></Td><Td whiteSpace="nowrap">{new Date(item.first_seen_at*1000).toLocaleString()}<br />{new Date(item.last_seen_at*1000).toLocaleString()}</Td><Td>{item.occurrence_count}</Td><Td>{item.status}</Td><Td><HStack>{resourcePath(item) && <Button as="a" href={resourcePath(item) || undefined} size="xs" variant="outline">Open</Button>}{item.status === "active" && <Button size="xs" onClick={() => acknowledge.mutate(item.id)} isLoading={acknowledge.isLoading}>Acknowledge</Button>}</HStack></Td></Tr>)}</Tbody></Table></Box>}
		</AMSection>
		<AMSection title={`Browser request failures (${errors.length})`} description="Only sanitized metadata is retained in memory for this browser tab; request bodies and credentials are excluded.">
			{errors.length === 0 ? <AMEmptyState title="No captured request errors" detail="Failed API calls appear here with their correlation ID when returned by the server." /> : <Box overflowX="auto"><Table size="sm"><Thead><Tr><Th>Time</Th><Th>Operation</Th><Th>Status</Th><Th>Request ID</Th><Th>Message</Th></Tr></Thead><Tbody>{errors.map((error,index)=><Tr key={`${error.timestamp}-${index}`}><Td whiteSpace="nowrap">{new Date(error.timestamp).toLocaleString()}</Td><Td>{error.operation}</Td><Td>{error.status ?? "—"}</Td><Td dir="ltr" fontFamily="mono">{error.request_id || "Unavailable"}</Td><Td>{error.sanitized_message}</Td></Tr>)}</Tbody></Table></Box>}
		</AMSection>
	</AMPage>;
}
