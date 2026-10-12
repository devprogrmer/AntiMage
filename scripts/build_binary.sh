#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

build_version="${ANTIMAGE_BUILD_VERSION:-}"
if [[ -z "$build_version" ]]; then
    build_version="$(git describe --tags --exact-match HEAD 2>/dev/null || true)"
fi
if [[ -z "$build_version" ]]; then
    build_sha="$(git rev-parse --short=7 HEAD 2>/dev/null || true)"
    if [[ -n "$build_sha" ]]; then
        build_version="dev-${build_sha}"
    else
        build_version="0.1.3"
    fi
fi
build_commit="${ANTIMAGE_BUILD_COMMIT:-}"
if [[ -z "$build_commit" ]]; then
    build_commit="$(git rev-parse HEAD 2>/dev/null || true)"
fi
if [[ -z "$build_commit" ]]; then
    build_commit="unknown"
fi
build_channel="stable"
if [[ "$build_version" == dev-* ]]; then
    build_channel="dev"
fi
build_ldflags="-X github.com/antimage/antimage/internal/app/system.BuildVersion=${build_version} -X github.com/antimage/antimage/internal/app/nodeagent.BuildVersion=${build_version} -X github.com/antimage/antimage/internal/app/nodeagent.BuildCommit=${build_commit} -X github.com/antimage/antimage/internal/app/nodeagent.BuildChannel=${build_channel}"

if [ ! -f "dashboard/build/index.html" ] && [ ! -f "dashboard/dist/index.html" ]; then
    echo "Dashboard build is missing. Build dashboard/build or dashboard/dist before creating binaries." >&2
    exit 1
fi

prepare_go_dashboard_embed() {
    local source_dir="dashboard/build"
    local target_dir="internal/gateway/static/dashboard/build"
    if [[ ! -f "$source_dir/index.html" && -f "dashboard/dist/index.html" ]]; then
        source_dir="dashboard/dist"
    fi
    if [[ ! -f "$source_dir/index.html" ]]; then
        echo "Dashboard build is missing. Expected dashboard/build/index.html or dashboard/dist/index.html." >&2
        exit 1
    fi
    rm -rf "$target_dir"
    mkdir -p "$target_dir"
    cp -R "$source_dir"/. "$target_dir/"
    touch "$target_dir/.gitkeep"
}

gateway_output="$ROOT_DIR/dist/antimage-server"
node_output="$ROOT_DIR/dist/antimage-node"
mkdir -p "$ROOT_DIR/dist"
rm -rf "$ROOT_DIR/dist/templates"
if [[ -d "$ROOT_DIR/templates" ]]; then
    cp -R "$ROOT_DIR/templates" "$ROOT_DIR/dist/templates"
fi
if [[ "${OS:-}" == "Windows_NT" ]]; then
    gateway_output="$ROOT_DIR/dist/antimage-server.exe"
    node_output="$ROOT_DIR/dist/antimage-node.exe"
fi

(
    prepare_go_dashboard_embed
    cd "$ROOT_DIR"
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$build_ldflags" -o "$gateway_output" ./cmd/antimage_gateway
    CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "$build_ldflags" -o "$node_output" ./cmd/antimage_node
)

echo "AntiMage Go gateway built at $gateway_output"
echo "AntiMage node agent built at $node_output"

bash scripts/build_go_cli.sh
