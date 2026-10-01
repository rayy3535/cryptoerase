# Changelog

## v0.1.0 (unreleased)

First public version.

- NVMe cryptographic erase through native admin passthrough: Sanitize Crypto Erase, and Format NVM with SES=010b behind `--allow-format`. The Format path keeps the current LBA format, metadata setting and protection information, and refuses when unallocated NVM capacity exists.
- SATA cryptographic erase through hdparm: SANITIZE CRYPTO SCRAMBLE EXT, with status polling.
- Fail-closed policy: no fallback to block erase, overwrite, ATA Security Erase or zero-fill.
- Firmware floor table, with built-in rules for Intel/Solidigm DC P4510 and P4610.
- Marker-based verification, in addition to the device's completion status.
- TCG Level 0 Discovery on NVMe (media encryption, locking state).
- Listing of the physical drives behind Dell PERC / Broadcom MegaRAID virtual disks via perccli or storcli.
- Concurrent processing with an optional bound; cancellation on SIGINT/SIGTERM.
- JSON report with fields that follow NIST SP 800-88r2 §4.6.
