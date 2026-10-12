# System

This package provides system and maintenance services for the panel runtime.

It collects CPU, memory, disk, swap, uptime, load, process, network, and
in-memory history metrics. It also provides maintenance metadata and binary
mode update/restart helpers.

Panel updates persist their requested and resolved build identities and remain
pending across a service restart until the newly started process reports the
requested build. Binary updates keep the previous server, CLI, release metadata,
and channel in `.update-rollback` until verification commits the update. A
failed version check restores that saved set and restarts the panel.

Node service updates require an exact catalog version. The controller verifies
the live node health response and running build after reconnect, then records
completion or restores the verified operation-owned backup as rollback.
The dashboard prepares a durable rollout with a frozen target and node list,
then requests explicit confirmation. Bulk execution uses bounded concurrency;
canary execution verifies its first group before releasing the remaining nodes.
A failed canary cancels unstarted children. Failed-only retry creates new child
operations while retaining the original artifact identity.

Master no longer owns a local Xray runtime, so Xray status is derived from
connected nodes rather than a local process.
