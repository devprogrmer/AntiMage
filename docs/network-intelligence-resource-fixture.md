# Network Intelligence socket-load observations

These measurements exercise actual UDP echo sockets and the SessionCapture adapter on Linux loopback. They do not estimate supported VPN users. All clients use one explicit fixture account. The container has a 2 CPU and 2 GiB memory limit. The host CPU reports Intel Core i5-13400F. The loopback path produces multiple observation points, so tracked flow count and received packet counts cannot establish forwarding counts.

The unpaced run below uses 100 sequential echoes per socket with 256 payload bytes in each direction. Every echo completed. It measures one short trial per configuration, in increasing socket-count order, with no confidence interval or production traffic. Mean echo time includes userspace scheduling and socket operations. Process CPU includes echo generation, echo server and capture. Heap measurements are process Go heap, not collector-only RSS. Snapshot size is serialized in-process fixture metadata, not measured gRPC bandwidth. No database operation is exercised.

| Socket clients | Echoes | Off echoes/s | On echoes/s | Off mean echo µs | On mean echo µs | Off CPU ms | On CPU ms | On heap after bytes | Tracked flows | Kernel observation drops |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 10 | 1000 | 182116 | 143124 | 5.491 | 6.987 | 5.796 | 14.182 | 847832 | 20 | 0 |
| 50 | 5000 | 187083 | 169672 | 5.345 | 5.894 | 26.888 | 56.637 | 2249176 | 100 | 9124 |
| 100 | 10000 | 184329 | 162296 | 5.425 | 6.162 | 54.386 | 120.781 | 3226808 | 200 | 26043 |
| 250 | 25000 | 185103 | 168009 | 5.402 | 5.952 | 135.853 | 298.118 | 1962400 | 500 | 85376 |
| 500 | 50000 | 185325 | 159374 | 5.396 | 6.275 | 270.327 | 629.094 | 3419640 | 954 | 179625 |

At high offered rates the collector misses many observations. Kernel observation drops do not measure VPN packet loss. Zero echo timeouts does not prove zero packet loss on a VPN path. Resource controls and sampling need further validation. Production forwarding overhead, per-core CPU, kernel memory, TCP throughput, database load, real multi-user WireGuard scaling and network latency remain unmeasured by this fixture.

Run the opt-in test in an isolated container with NET_RAW and `ANTIMAGE_NI_RESOURCE_E2E=1`. Setting `ANTIMAGE_NI_RESOURCE_PACED=1` reduces each client to 20 echoes and inserts a one-millisecond delay before each echo across the entire generator. The paced metric therefore includes deliberate generator delay and must not be presented as network RTT. Race-enabled execution also passed, but its timings are excluded from the table.

The non-race paced run passed all configurations with zero kernel observation drops and zero echo timeouts. Offered traffic was approximately 750–888 echoes/s, corresponding to approximately 3000–3550 loopback observation events/s. At 500 sockets, the off trial completed 10000 echoes in 11.699 seconds with 1242.079 ms process CPU and 1125104 bytes ending heap. The on trial completed 10000 echoes in 11.264 seconds with 2064.735 ms process CPU and 3015960 bytes ending heap. Capture received 39997 packets, retained 1000 loopback flow tuples and produced a 447884-byte internal fixture snapshot. Those figures describe this generator, adapter and echo server together. They cannot establish a monitored-user limit or gRPC serialization cost for the production snapshot.
