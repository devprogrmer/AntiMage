FROM node:20-bookworm-slim AS dashboard

WORKDIR /src/dashboard
ARG DASHBOARD_NODE_OPTIONS=--max-old-space-size=2048
ENV NODE_OPTIONS=${DASHBOARD_NODE_OPTIONS}
COPY dashboard/package*.json dashboard/chakra.config.ts ./
RUN npm ci
COPY dashboard/ ./
RUN VITE_BASE_API=/api/ npm run build \
    && cp ./build/index.html ./build/404.html

FROM golang:1.25-bookworm AS builder
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}

RUN apt-get update && apt-get install -y --no-install-recommends \
    bash \
    build-essential \
    ca-certificates \
    git \
    pkg-config \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY . .
COPY --from=dashboard /src/dashboard/build ./dashboard/build
RUN bash scripts/build_binary.sh

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    certbot \
    tzdata \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/antimage
COPY --from=builder /src/dist/antimage-server /usr/local/bin/antimage-server
COPY --from=builder /src/dist/antimage-cli /usr/local/bin/antimage-cli
COPY templates ./templates

ENTRYPOINT ["antimage-server"]
