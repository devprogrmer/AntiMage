# Backup

This package implements AntiMage backup export and import.

New exports use the `.ambackup` extension. Import identifies supported backups
from their manifest, so existing archives remain restorable regardless of their
filename. Backups use a portable gzip/tar container and versioned format,
including manifest validation, SQLite backup/restore, MySQL/MariaDB dump and
restore paths, full backup file roots, and safe archive extraction. Export
returns only after tar and gzip finalization and file sync succeed. Import
reads through the bounded tar tail to verify the gzip checksum and trailer
before changing the database or files, so a truncated backup is rejected.

The backup logic is only intended for supported binary installs. HTTP handlers
should use this package rather than shelling out directly.
