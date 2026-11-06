# Node accounting health

The node list shows a read-only accounting summary from the Node Agent's
`MetricsResponse`. It is diagnostic: it never changes node connectivity,
quota enforcement, ACK handling, checkpoint scheduling, or durable state.

Each protocol row reports whether its durable usage state has been loaded,
the current pending batch and its raw bytes, the last acknowledged batch,
the last successful checkpoint observed **since this agent started**, and
the most recent worker failure. L2TP and PPTP share the PPP checkpoint
worker and therefore share its checkpoint time and worker error while keeping
their own pending and ACK state. The reflection summary reports durable
batches whose panel-side usage still awaits reconciliation. Byte totals
saturate at `uint64` maximum and explicitly flag overflow; this display never
modifies the stored ledger.

An unloaded state or a zero checkpoint time means **unknown**, not zero usage
or a successful recovery. A pending batch is normal while delivery or panel
reflection is in progress. If a pending batch or reflection count remains
unchanged, check Node Agent delivery errors, panel connectivity and the
corresponding ACK before taking any repair action. A worker error is shown
verbatim so the operator can locate it in the Node Agent journal. The UI does
not infer the cause of a failure from a pending count alone.

The health snapshot reads existing in-memory state under the same locks used
by accounting. It does not open usage files or add any disk writes. Its
checkpoint timestamps are volatile diagnostics; durable usage and ACK files
remain the source of truth after a restart.
