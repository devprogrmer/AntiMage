#!/usr/bin/env python3
import json
import os
import socket
import sys
import time

MODE, ADDRESS, PORT = sys.argv[1:4]
PORT = int(PORT)
# Keep the shared protocol fixture below the small quota used by the PPP
# lifecycle tests; OpenVPN overrides this with a longer sustained transfer.
EXPECTED = int(os.environ.get("ANTIMAGE_NATIVE_SPEED_TEST_BYTES", str(4 * 1024 * 1024)))
CONFIGURED_MBPS = int(os.environ.get("ANTIMAGE_NATIVE_SPEED_TEST_LIMIT_MBPS", "8"))

if MODE == "send":
    with socket.create_connection((ADDRESS, PORT), timeout=10) as conn:
        conn.settimeout(120)
        block = b"s" * 65536
        sent = 0
        while sent < EXPECTED:
            part = block[:min(len(block), EXPECTED - sent)]
            conn.sendall(part)
            sent += len(part)
        conn.shutdown(socket.SHUT_WR)
elif MODE == "receive":
    RESULT, DIRECTION = sys.argv[4:6]
    configured = CONFIGURED_MBPS
    with socket.socket() as listener:
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind((ADDRESS, PORT))
        listener.listen(1)
        conn, _ = listener.accept()
        conn.settimeout(120)
        started = time.monotonic()
        total = 0
        with conn:
            while total < EXPECTED:
                block = conn.recv(65536)
                if not block:
                    break
                total += len(block)
        elapsed = time.monotonic() - started
    if total != EXPECTED:
        raise SystemExit(f"{DIRECTION} receiver got {total} bytes, want {EXPECTED}")
    average = total * 8 / elapsed / 1_000_000
    burst_allowance = max(256 * 1024, configured * 1_000_000 // 160)
    burst_allowance = min(4 * 1024 * 1024, burst_allowance)
    steady_average = max(0, total - burst_allowance) * 8 / elapsed / 1_000_000
    result = {"configured_mbps": configured, "bytes": total, "seconds": elapsed,
              "average_mbps": average, "burst_allowance_bytes": burst_allowance,
              "steady_average_mbps": steady_average}
    with open(RESULT, "w", encoding="utf-8") as output:
        json.dump(result, output, sort_keys=True)
    print(f"native tunnel {DIRECTION} configured={configured} Mbps bytes={total} "
          f"seconds={elapsed:.2f} average={average:.2f} Mbps "
          f"steady_after_burst={steady_average:.2f} Mbps")
    if not configured * 0.4 <= average or steady_average > configured * 1.02:
        raise SystemExit(f"native tunnel {DIRECTION} outside configured speed tolerance: {result}")
else:
    raise SystemExit(f"unknown speed transfer mode {MODE}")
