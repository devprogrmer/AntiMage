import { Box, Button, Checkbox, HStack, Input, Select, Stack, Text } from "@chakra-ui/react";
import { useState } from "react";
import { useQuery } from "react-query";
import { fetch } from "service/http";

type Node = {id:number;name:string;status:string};
type Child = {operation_id:string;node_id:number;phase:string;running_version?:string;error?:string;rollback_error?:string};
type Target = {version:string;commit:string;sha256:string;size:number;architecture:string;os:string;artifact_name:string};
type View = {operation:{id:string;state:string;phase:string;request_id?:string};rollout:{id:string;resolved_target:Target;mode:string;concurrency:number;canary_count:number;confirmed:boolean;result?:string};children:Child[];summary:Record<string,number>;cannot_cancel?:string[]};

export function NodeRolloutControls() {
 const nodes=useQuery<Node[]>("am-rollout-nodes",()=>fetch("/nodes"),{staleTime:15_000});
 const [selected,setSelected]=useState<number[]>([]);
 const [channel,setChannel]=useState("stable");const [policy,setPolicy]=useState("latest");const [version,setVersion]=useState("");
 const [concurrency,setConcurrency]=useState(2);const [mode,setMode]=useState("bulk");const [canaries,setCanaries]=useState(1);
 const [id,setID]=useState("");const [busy,setBusy]=useState(false);const [error,setError]=useState(false);const [confirm,setConfirm]=useState(false);
 const history=useQuery<{rollouts:Array<{id:string;state:string;phase:string}>}>("am-rollout-history",()=>fetch("/nodes/rollouts"),{staleTime:10_000,refetchInterval:15_000});
 const view=useQuery<View>(["am-rollout",id],()=>fetch(`/nodes/rollouts/${encodeURIComponent(id)}`),{enabled:!!id,refetchInterval:3000,retry:false});
 const data=view.data;
 const terminal=data && ["completed","failed","cancelled","rolled_back"].includes(data.operation.state);
 const act=async(action:"prepare"|"start"|"cancel"|"retry")=>{
  setBusy(true);setError(false);
  try {
   const result=action==="prepare"?await fetch<View>("/nodes/rollouts",{method:"POST",body:{node_ids:selected,channel,policy,version:policy==="pinned"?version:"",concurrency,mode,canary_count:mode==="canary"?canaries:0,confirm:false}}):await fetch<View>(`/nodes/rollouts/${encodeURIComponent(id)}/${action}`,{method:"POST",body:action==="start"?{confirm:true}:{}});
   setID(result.rollout.id);setConfirm(false);if(result.rollout.id===id)await view.refetch();await history.refetch();
  }catch{setError(true);}finally{setBusy(false);}
 };
 return <Stack spacing={4} mt={4}>
  <Text fontWeight="bold">Bulk / Canary rollout</Text>
  {error && <Text role="alert" color="red.400">Rollout request failed. Review Diagnostics for the correlated request error.</Text>}
  {nodes.isError && <Text role="alert">Node inventory unavailable</Text>}
  <Box as="fieldset" border="1px solid" borderColor="var(--am-panel-border)" p={3} borderRadius="md"><legend>Selected nodes</legend><Stack>
   {(nodes.data || []).map(node=><Checkbox key={node.id} isDisabled={busy || (!!data && !terminal)} isChecked={selected.includes(node.id)} onChange={event=>setSelected(ids=>event.target.checked?[...ids,node.id]:ids.filter(id=>id!==node.id))}>{node.name} ({node.status})</Checkbox>)}
  </Stack></Box>
  <HStack flexWrap="wrap">
   <Select aria-label="Rollout channel" value={channel} onChange={event=>setChannel(event.target.value)} maxW="180px"><option value="stable">Stable</option><option value="dev">Dev</option></Select>
   <Select aria-label="Rollout policy" value={policy} onChange={event=>setPolicy(event.target.value)} maxW="180px"><option value="latest">Latest</option><option value="pinned">Pinned</option></Select>
   {policy==="pinned" && <Input aria-label="Pinned rollout version" value={version} onChange={event=>setVersion(event.target.value)} placeholder="v1.2.3 or dev-SHA" maxW="260px"/>}
   <Select aria-label="Rollout concurrency" value={concurrency} onChange={event=>setConcurrency(Number(event.target.value))} maxW="180px">{[1,2,3,4,5].map(n=><option key={n} value={n}>Concurrency: {n}</option>)}</Select>
   <Select aria-label="Rollout mode" value={mode} onChange={event=>setMode(event.target.value)} maxW="180px"><option value="bulk">Bulk</option><option value="canary">Canary</option></Select>
   {mode==="canary" && <Input aria-label="Canary count" type="number" min={1} max={selected.length || 1} value={canaries} onChange={event=>setCanaries(Number(event.target.value))} maxW="120px"/>}
  </HStack>
  <Button alignSelf="start" isDisabled={!selected.length || busy || (!!data && !terminal) || (policy==="pinned" && !version.trim())} onClick={()=>void act("prepare")}>Resolve and review {selected.length} nodes</Button>
  <Select aria-label="Saved rollout" value={id} onChange={event=>{setID(event.target.value);setConfirm(false);}}><option value="">Choose persisted rollout</option>{history.data?.rollouts?.map(item=><option key={item.id} value={item.id}>{item.id} · {item.state} · {item.phase}</option>)}</Select>
  {data && <Box border="1px solid" borderColor="var(--am-panel-border)" p={4} borderRadius="md"><Stack spacing={3}>
   <Text>Target: <span dir="ltr">{data.rollout.resolved_target.version}</span></Text>
   <Text overflowWrap="anywhere">Commit: <span dir="ltr">{data.rollout.resolved_target.commit}</span></Text>
   <Text overflowWrap="anywhere">Artifact: {data.rollout.resolved_target.artifact_name} · {data.rollout.resolved_target.size} bytes · {data.rollout.resolved_target.os}/{data.rollout.resolved_target.architecture}</Text>
   <Text overflowWrap="anywhere">SHA256: <span dir="ltr">{data.rollout.resolved_target.sha256}</span></Text>
   <Text>Mode: {data.rollout.mode} · Canary count: {data.rollout.canary_count} · Concurrency: {data.rollout.concurrency}</Text>
   <Text>State: {data.operation.state} · Phase: {data.operation.phase} · Result: {data.rollout.result || "Pending"}</Text>
   <Text overflowWrap="anywhere">Operation ID: {data.operation.id} · Request ID: {data.operation.request_id || "Unavailable"}</Text>
   <Text>{Object.entries(data.summary).map(([label,count])=>`${label}: ${count}`).join(" · ")}</Text>
   {!data.rollout.confirmed && !terminal && <><Text>Review the exact frozen artifact above. Nodes will restart and temporarily disconnect. No installation has started.</Text><Checkbox isChecked={confirm} onChange={event=>setConfirm(event.target.checked)}>I confirm this target and the selected nodes</Checkbox><Button isDisabled={!confirm || busy} onClick={()=>void act("start")}>Start rollout</Button></>}
   {!terminal && <Button variant="outline" isDisabled={busy} onClick={()=>void act("cancel")}>Cancel queued nodes</Button>}
   {!!data.cannot_cancel?.length && <Text>Cannot cancel active installation/restart: {data.cannot_cancel.join(", ")}. Those operations continue through verification.</Text>}
   {terminal && (data.summary.failed+data.summary.rolled_back)>0 && <Button isDisabled={busy} onClick={()=>void act("retry")}>Retry Failed only</Button>}
   {data.children.map(child=><Box key={child.operation_id} borderTop="1px solid" borderColor="var(--am-panel-border)" pt={2}><Text>Node {child.node_id} · {child.phase} · Running: {child.running_version || "Unknown"}</Text><Text fontSize="sm" overflowWrap="anywhere">{child.operation_id}</Text>{child.error && <Text color="red.400">{child.error}</Text>}{child.rollback_error && <Text role="alert" color="red.400">Rollback failed — manual recovery required: {child.rollback_error}</Text>}</Box>)}
  </Stack></Box>}
 </Stack>;
}
