# Vendored AmneziaWG kernel module

This directory contains a pinned production copy of the AmneziaWG Linux
kernel module used by AntiMage's lazy DKMS provisioner.

- Upstream: https://github.com/amnezia-vpn/amneziawg-linux-kernel-module
- Protocol generation: AmneziaWG 1.0
- Vendored package version: 1.0.0
- License: GPL-2.0 (SPDX identifiers are retained in the source files)

`awgsrc.go` embeds the source tree and extracts it under `/usr/src` only when
the first AmneziaWG inbound is applied and the module is not already loaded.
