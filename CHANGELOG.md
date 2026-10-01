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
- Requires Go 1.27.1 to build.
- `--version` also prints the VCS revision and Go version the binary was built with.
- `--inventory` and `--yes` together, an unknown `--log-format`, and negative durations are rejected as bad arguments.
- In-use detection also resolves mounts by device number from `/proc/self/mountinfo`, so a root filesystem shown as `/dev/root`, or a filesystem mounted through a udev symlink or a renamed device-mapper node, still protects its disk.
- hdparm and perccli/storcli calls stop waiting for output pipes 5 s after a timeout kills the tool.
- Release binaries are built in GitHub Actions with SLSA build provenance; CI covers linux/amd64 and linux/arm64 with race-enabled tests, a 90% coverage floor, fuzzing, kernel integration tests, staticcheck, govulncheck, CodeQL and OpenSSF Scorecard.
