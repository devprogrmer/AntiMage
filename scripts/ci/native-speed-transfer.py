#!/usr/bin/env python3
import json
import socket
import sys
import time

MODE, ADDRESS, PORT = sys.argv[1:4]
PORT = int(PORT)
EXPECTED = 4 * 1024 * 1024

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
    configured = 4 if DIRECTION == "upload" else 6
    with socket.socket() as listener:
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind((ADDRESS, PORT))
        listener.listen(1)
        conn, _ = listener.accept()
        conn.settimeout(120)
        started = time.monotonic()
        total = 0
        windows = {}
        with conn:
            while total < EXPECTED:
                block = conn.recv(65536)
                if not block:
                    break
                total += len(block)
                second = int(time.monotonic() - started)
                windows[second] = windows.get(second, 0) + len(block)
        elapsed = time.monotonic() - started
    if total != EXPECTED:
        raise SystemExit(f"{DIRECTION} receiver got {total} bytes, want {EXPECTED}")
    average = total * 8 / elapsed / 1_000_000
    peak = max(windows.values()) * 8 / 1_000_000
    result = {"configured_mbps": configured, "bytes": total, "seconds": elapsed,
              "average_mbps": average, "peak_1s_mbps": peak}
    with open(RESULT, "w", encoding="utf-8") as output:
        json.dump(result, output, sort_keys=True)
    print(f"native tunnel {DIRECTION} configured={configured} Mbps bytes={total} "
          f"seconds={elapsed:.2f} average={average:.2f} Mbps peak_1s={peak:.2f} Mbps")
    lower, upper = (2.5, 5.5) if configured == 4 else (4.0, 8.0)
    if not lower <= average <= upper or peak > configured * 2:
        raise SystemExit(f"native tunnel {DIRECTION} outside configured speed tolerance: {result}")
else:
    raise SystemExit(f"unknown speed transfer mode {MODE}")
