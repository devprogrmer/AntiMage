import { Badge, Box, Button, HStack, Select, Text } from "@chakra-ui/react";
import { useState } from "react";
import { useQuery } from "react-query";
import { AMAlert, AMEmptyState, AMLoading, AMOperationStatus, AMPage, AMPageHeader, AMSection } from "components/am/AMPrimitives";
import { fetch } from "service/http";

type Operation = { id:string; operation_type:string; target_type:string; target_id:string; requested_by?:string; request_id?:string; state:string; phase:string; progress?:number|null; created_at:number; started_at?:number; updated_at:number; completed_at?:number; error?:string; metadata?:Record<string,unknown> };

export function OperationsPage() {
	const [state, setState] = useState("");
	const query = useQuery<{operations:Operation[]}>("am-operations", () => fetch("/maintenance/operations"), { staleTime:15_000, refetchInterval:30_000, refetchOnWindowFocus:false });
	const operations = query.data?.operations || [];
	const visible = state ? operations.filter((op) => op.state === state) : operations;
	return <AMPage>
		<AMPageHeader title="Operation history" description="Persistent operation records reported by maintenance services, with normalized state and phase." actions={<Button variant="outline" onClick={() => void query.refetch()} isLoading={query.isFetching}>Refresh</Button>} />
		{query.isError && <AMAlert title="Operation history unavailable" status="error">The operation feed could not be loaded. Check Diagnostics for the request correlation ID.</AMAlert>}
		<AMSection title={`Operations (${visible.length})`}>
			<Select aria-label="Operation state" value={state} onChange={(event)=>setState(event.target.value)} maxW="xs" mb={4}><option value="">All states</option>{["queued","running","waiting","completed","failed","cancelled","rolled_back"].map((value)=><option key={value} value={value}>{value}</option>)}</Select>
			{query.isLoading ? <AMLoading /> : visible.length===0 ? <AMEmptyState title="No operations recorded" detail="No persisted operation records are currently available." /> : <Box border="1px solid" borderRadius="lg" overflow="hidden">{visible.map((op)=><Box key={op.id}><AMOperationStatus operation={op.operation_type} target={`${op.target_type} ${op.target_id}`} state={op.state} phase={op.phase} progress={op.progress} /><Box px={4} py={3} borderBottom="1px solid" borderColor="var(--am-panel-border)"><HStack wrap="wrap" fontSize="sm" color="var(--am-panel-text-muted)"><Text>Started: {op.started_at ? new Date(op.started_at*1000).toLocaleString() : "Not started"}</Text><Text>Duration: {duration(op)}</Text>{op.request_id && <Text>Request ID: <code dir="ltr">{op.request_id}</code></Text>}{op.requested_by && <Text>Requested by: {op.requested_by}</Text>}<Badge dir="ltr" sx={{ unicodeBidi:"isolate" }}>{op.id}</Badge></HStack>{op.error && <Text mt={2} color="red.400">{op.error}</Text>}<details><summary>Inspect operation details</summary><Box as="pre" mt={2} fontSize="xs" whiteSpace="pre-wrap" overflowWrap="anywhere">{JSON.stringify(op.metadata || {},null,2)}</Box></details></Box></Box>)}</Box>}
		</AMSection>
	</AMPage>;
}

function duration(op: Operation) {
	const end = op.completed_at || op.updated_at || Math.floor(Date.now()/1000);
	const seconds = Math.max(0,end-(op.started_at||op.created_at));
	return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds/60)}m ${seconds%60}s`;
}
