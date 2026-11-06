# Xray pending reflection quota lookup

The Xray quota tick previously scanned every sample in the pending usage batch
for each policy whose pending batch had been reflected by the panel. With
many users, that lookup grew with policies multiplied by pending samples.

The tick now builds one index keyed by `(user ID, inbound tag)` and reuses its
map on later ticks. Only a matching, reflected pending sample is subtracted
from the unacknowledged logical total. Duplicate samples are summed with
overflow detection; other users and inbounds remain separate. This index is
derived state under the existing usage lock. Durable batches, ACKs, reflection
receipts, and checkpoint timing are unchanged.

On a Windows i5-13400F fixture with 1,000 policies and 1,000 pending samples,
three 200-iteration microbenchmarks measured 0.956–1.036 ms per repeated scan
versus 0.091–0.097 ms per indexed lookup. Both paths allocated 0 B per steady
iteration. This measures only the pending lookup, not total node CPU use.

The nodeagent, nodecontroller and xrayconfig suites ran with the official
Xray 26.10.31 binary; build and vet passed. Linux race and native protocol
tests are required on the pull request before merge.
