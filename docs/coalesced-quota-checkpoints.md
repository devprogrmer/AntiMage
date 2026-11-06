# Share coincident checkpoint and quota samples

The local worker previously ran Xray's combined checkpoint/quota function and then ran the separate quota callback again. It also ran WireGuard's checkpoint and quota callbacks separately, each obtaining its own `wg show all dump` snapshot.

When both deadlines are due, the worker now executes exactly one combined operation for Xray or WireGuard. The existing combined functions checkpoint the shared sample durably before enforcing a cutoff. A combined-operation failure does not fall through into another quota callback. Checkpoint-only and quota-only ticks retain their existing paths; all intervals and durable accounting/ACK/reflection formats remain unchanged.

The worker test failed against the previous scheduler because it invoked the extra quota callback. It now verifies the combined dispatch and one WireGuard dump. The real gRPC fixture also runs the actual scheduler and asserts one Xray stats RPC at the coincident deadline, alongside the existing direct combined-operation test. The full node suite, build and vet pass locally; Linux race/native/accounting CI is a merge gate.

This removes duplicate work at coincident deadlines. It does not claim a measured percentage reduction in total server CPU or eliminate independent panel-driven stats requests.
