---
name: secure-release-engineering
description: "Review AntiMage CI, binary and container releases, package names and artifact integrity."
---

# Secure Release Engineering

Read `.github/workflows/binary-build.yml`, `build.yml` and `test-db.yml` in the same directory, plus `scripts/package_release.ps1` and `scripts/verify_release_names.ps1`. Identify which tags and events publish packages. Verify artifact filenames and architecture selection, pin reviewed build inputs, scope publishing credentials to release jobs and validate checksums against the actual package set. Keep existing release download contracts unless changed deliberately. Validate migration compatibility and perform release packaging tests without publishing artifacts.
