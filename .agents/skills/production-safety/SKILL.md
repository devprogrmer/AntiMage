---
name: production-safety
description: "Review AntiMage master and node changes to deployment, storage, listeners and runtime state before production operations."
---

# Production Safety

Identify Docker and binary paths from `docker-compose.yml`, `docs/scripts/antimage/README.md` and `scripts/antimage/`. Map configuration, certificates and persistent storage separately for master and node. Use disposable data and local validation. Confirm backup restore on a disposable instance and document the SQLite or MySQL migration impact, release compatibility, health gates and rollback decision. Prepare production commands as a reviewable plan and keep production operations within the explicitly authorized scope.
