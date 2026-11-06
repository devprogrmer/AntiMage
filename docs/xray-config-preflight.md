# Validate Xray configurations before activation

The node checks each changed configuration with the installed target core using `xray run -test -config <candidate>` before publishing the offline runtime policy, replacing the applied config, changing the collector API port, or stopping the running core. A core rejection or cancelled/failed check returns an error. The previous durable policy remains available to offline recovery.

The candidate is staged in a private temporary file in the node data directory and removed when the check finishes. The check uses the same asset directory as the runtime, is bounded to ten seconds by the request context, and retains at most 4 KiB of diagnostics. It runs on config transitions, never on accounting or quota ticks. An identical config with a retained running core reuses its applied state without spawning another check process. Native-only nodes with no Xray binary installed retain their existing behavior.

Accounting still checkpoints the old running core before evaluating the guarded desired policy. Validation failure does not discard pending usage, ACK identities, quota policy, runtime generation or the applied revision. This change introduces no accounting format migration or interval changes.

## Verification and limits

The real-core VLESS test first reproduced the previous failure: an invalid protocol config was accepted and replaced the core. It now rejects that config, keeps both existing streams working with the same PID/generation/revision, then restarts from the prior valid durable policy and preserves the unacknowledged traffic batch. Unit tests exercise target rejection, immutable policy and pending usage across a new node instance, cancellation/temporary-file cleanup, bounded output, and reuse of an unchanged config. The six-protocol live-addition E2E continues to run.

The check constructs the configuration without starting its listeners. It cannot guarantee that a port will still be available during activation, or that a later runtime or native-protocol operation will succeed. Last-known-good activation rollback and isolated active-stream quota cutoff remain separate work; this change must not be described as implementing either guarantee. Production deployment and live-server resource measurements are not part of this change.
