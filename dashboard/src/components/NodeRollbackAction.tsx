import { Button, Checkbox, Modal, ModalBody, ModalCloseButton, ModalContent, ModalFooter, ModalHeader, ModalOverlay, Stack, Text } from "@chakra-ui/react";
import { useState } from "react";
import { useQuery } from "react-query";
import { fetch } from "service/http";

type Backup = { identity:string; version:string; commit:string; created_at:number; valid:boolean; current_running_version?:string };
type Props = { nodeID:string; sourceID:string; onStarted:()=>void };

export function NodeRollbackAction({nodeID,sourceID,onStarted}:Props) {
 const [open,setOpen]=useState(false);
 const [confirmed,setConfirmed]=useState(false);
 const [busy,setBusy]=useState(false);
 const [failed,setFailed]=useState(false);
 const backup=useQuery<Backup>(["node-rollback-backup",nodeID,sourceID],()=>fetch(`/node/${encodeURIComponent(nodeID)}/service/rollback/backup`,{query:{source_operation_id:sourceID}}),{staleTime:0,retry:false,refetchOnWindowFocus:false});
 const target=backup.data;
 if (!target?.valid) return null;
 const rollback=async()=>{
  setBusy(true);setFailed(false);
  try {
   await fetch(`/node/${encodeURIComponent(nodeID)}/service/rollback`,{method:"POST",body:{source_operation_id:sourceID,confirm:true,reason:"Manual rollback from operation history"}});
   setOpen(false);setConfirmed(false);onStarted();
  } catch {setFailed(true);} finally {setBusy(false);}
 };
 return <>
  <Button mt={3} size="sm" variant="outline" onClick={()=>{setConfirmed(false);setFailed(false);setOpen(true);}}>Rollback to {target.version}</Button>
  <Modal isOpen={open} onClose={()=>{if(!busy)setOpen(false);}}>
   <ModalOverlay/><ModalContent><ModalHeader>Confirm Node rollback</ModalHeader><ModalCloseButton isDisabled={busy}/>
    <ModalBody><Stack spacing={3}>
     <Text>Node: {nodeID}</Text><Text>Current running version: {target.current_running_version || "Unknown"}</Text>
     <Text>Rollback target: {target.version}</Text><Text>Commit: {target.commit || "Unavailable"}</Text>
     <Text>Backup created: {new Date(target.created_at*1000).toLocaleString()}</Text>
     <Text>The node service will restart. Expect temporary downtime while it reconnects. Success requires fresh runtime and version verification.</Text>
     <Checkbox isChecked={confirmed} onChange={(event)=>setConfirmed(event.target.checked)}>I confirm this rollback and service restart</Checkbox>
     {failed && <Text role="alert" color="red.400">Rollback request failed. Review Diagnostics for the correlated request error.</Text>}
    </Stack></ModalBody>
    <ModalFooter><Button mr={3} isDisabled={busy} onClick={()=>setOpen(false)}>Cancel</Button><Button colorScheme="orange" isDisabled={!confirmed} isLoading={busy} onClick={()=>void rollback()}>Rollback to {target.version}</Button></ModalFooter>
   </ModalContent>
  </Modal>
 </>;
}
