---
name: docker-deployment-review
description: "Review AntiMage Docker images, Compose isolation and state persistence for master and node development."
---

# Docker Deployment Review

Read `Dockerfile`, `Dockerfile.node`, `docker-compose.yml` and `docs/scripts/antimage/README.md`. Identify master and node runtime requirements individually. Validate Compose interpolation with fixture variables. Check intended privileges, listeners, secrets mounted at runtime and persistence across a container restart. Rehearse build and health checks in an isolated Compose project with disposable SQLite or MySQL data.
