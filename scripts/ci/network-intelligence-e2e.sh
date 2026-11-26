#!/usr/bin/env bash
set -euo pipefail

# Run from a development checkout on a Docker host. Every network mutation
# takes place inside a disposable container, with no host networking or mounts.
repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
image="antimage-network-intelligence-lab"
docker build -t "$image" - <<'DOCKERFILE'
FROM golang:1.26-bookworm
RUN sed -i s,http://deb.debian.org,https://deb.debian.org,g /etc/apt/sources.list.d/debian.sources \
 && apt-get -o Acquire::Retries=1 -o Acquire::https::Timeout=30 update \
 && apt-get install -y --no-install-recommends iproute2 wireguard-tools python3 \
 && rm -rf /var/lib/apt/lists/*
DOCKERFILE
cache="antimage-ni-modules-$$"
docker volume create "$cache" >/dev/null
trap 'docker volume rm "$cache" >/dev/null' EXIT
docker run --rm -e GOMODCACHE=/modules \
  --mount "type=volume,source=$cache,target=/modules" \
  --mount "type=bind,source=$repo,target=/src,readonly" \
  -w /src "$image" go mod download
docker run --rm --network none --cap-add NET_ADMIN --cap-add NET_RAW \
  --cap-add SYS_ADMIN --cpus 2 --memory 2g \
  -e ANTIMAGE_WIREGUARD_NI_E2E=1 -e ANTIMAGE_CAPTURE_E2E=1 \
  -e GOMODCACHE=/modules \
  --mount "type=volume,source=$cache,target=/modules" \
  --mount "type=bind,source=$repo,target=/checkout,readonly" \
  "$image" bash -c 'cp -a /checkout /src; cd /src; go test -race -v ./internal/app/networkintelligence'
