#!/usr/bin/env bash
# Copy an L2TP native-run tree into a failure-evidence directory while
# guaranteeing the result only contains entries that can actually be uploaded.
#
# `actions/upload-artifact` streams every path matched by the workflow's
# `antimage-*-failure/**` glob. Reading a FIFO blocks until a writer appears, so
# a single FIFO in that tree stalls the upload step forever; the job then dies
# at its 45-minute timeout with no logs and no artifact.
#
# The driver's private xl2tpd instances create exactly such FIFOs as their
# control pipes (`$ROOT/client.control`, `$ROOT/client-b.control`; the driver
# waits for them with `test -p`). Copying the run tree verbatim therefore turned
# one driver failure into a silent 45-minute job. Prune every entry that is not
# a regular file, directory or resolvable symlink, and bound the copy itself so
# a pathological tree cannot stall teardown either.
set -u

src="${1:?missing source directory}"
dst="${2:?missing destination directory}"
copy_timeout="${ANTIMAGE_L2TP_EVIDENCE_COPY_TIMEOUT:-120s}"

mkdir -p "$dst"
timeout "$copy_timeout" cp -a "$src/." "$dst/" 2>/dev/null || true

find "$dst" \( -type p -o -type s -o -type b -o -type c \) -delete 2>/dev/null || true
find "$dst" -xtype l -delete 2>/dev/null || true

exit 0
