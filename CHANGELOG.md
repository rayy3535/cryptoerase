# Changelog

## v0.2.0 (2026-10-02)

First release with RAID controller support: `--raid-reset`, and cryptographic erase by the controller for SATA drives behind a Dell PERC / Broadcom MegaRAID whose ATA status does not come back. Both were added in 0.1.0-rc.3 (below) and tested on a PowerEdge R7525 with a PERC H355.

- Missing tools stop the run before anything is written: hdparm when SATA drives are present, perccli/storcli when PERC/MegaRAID disks are present or `--raid-reset` is given (excluded, in-use, removable and USB disks do not count). `Run` returns `ErrToolMissing`, and the CLI exits with code 1 and no report. Before, each drive that needed the tool was reported `FAIL` or `UNHANDLED`. Backends can implement `Checker`; `ata.Hdparm` and `perc.Lister` do.
- `--raid-cli PATH` (`perc.Lister.Path`) sets the perccli/storcli binary. Without it the CLI is searched in `$PATH`, then in `/opt/MegaRAID/perccli` and `/opt/MegaRAID/storcli`, where the Dell and Broadcom packages install it.
- Controller erase of SATA drives behind a PERC: the markers' write handle stays open until the disk has been removed from the kernel. Closing it first made udev re-read the partition table, and that read raced with the removal: on a PERC H355 the kernel logged `I/O error, dev sda, sector 0`, `Buffer I/O error` and `unable to read RDB block 0` for the first drive erased. Erase results were not affected. If the disk cannot be removed, the handle is closed and udev gets 2 s before the controller hides the drive.

## v0.1.0-rc.3 (2026-10-02)

- `--raid-reset` (`Options.RAIDReset`): on Dell PERC / Broadcom MegaRAID controllers, delete the virtual disks the running OS does not use, remove hot spares, set the drives to non-RAID, wait for them to appear, and erase each drive directly. Controllers with foreign configurations, failed or rebuilding drives, or a virtual disk that cannot be matched to its block device while the OS uses the controller are left unchanged. Exposed drives that never reach the OS are reported `FAIL`. With `--inventory`, only the plan is reported.
- `perc.Lister` reads the full controller configuration (`Controllers`) and can delete virtual disks, remove hot spares and set drives to JBOD. Drives carry serial numbers and WWNs.
- Drive records include the SCSI `wwid` under `attach`.
- SATA: SANITIZE STATUS EXT (read-only) runs first, in `--inventory` too, so a frozen sanitize and the case below show up before anything is written. When the controller in front of the drive passes ATA commands through but returns no ATA registers (hdparm: `bad/missing sense data`; seen on Dell PERC with the drive in non-RAID mode), SANITIZE is no longer sent through hdparm. Before, it was sent and the drive failed with "sanitize did not report 'Completed Without Error'", and whether the drive had been erased was unknown. Behind controllers other than PERC/MegaRAID such a drive is reported `FAIL`.
- `ata.ErrNoRegisters`; the hdparm backend returns it from `SanitizeStatus` and `SanitizeCryptoScramble`.
- Such SATA drives behind a PERC/MegaRAID controller are crypto-erased by the controller: the disk is removed from the kernel, set unconfigured good, `start erase crypto` is run, the result is read from the controller event log, the drive is set back to JBOD, and the markers are verified once the disk is back. The controller commands and the event log format were checked by hand on a PERC H355 (perccli 007.1623) with SATA SSDs in non-RAID mode. Report: `device_status.perc_erase`; inventory method `perc-crypto-erase`.
- `perc.Lister`: `SetGood`, `StartCryptoErase`, `EraseStatus`, `Events`, `ParseEvents`, `EraseOutcome`; drives carry `crypto_erase_capable` and `sanitize_support`.

## v0.1.0-rc.2 (2026-10-01)

- The read-write handle used to write the verification markers stays open until the erase command has finished, and verification reopens the device read-only. Closing the handle earlier made udev re-read the partition table while the drive was sanitizing; the drive rejected the read, and the kernel logged `Buffer I/O error ... logical block 0` and `unable to read RDB block 0`. Erase results were not affected.
- `Options.OpenBlock` takes a `write` argument; new `blockdev.OpenReadOnly`.
- Releases are published automatically when a commit on `main` sets a new version.

## v0.1.0-rc.1 (2026-10-01)

First public version, published as a pre-release for testing on real hardware.

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
- Release binaries are built in GitHub Actions with SLSA build provenance; CI covers linux/amd64 and linux/arm64 with race-enabled tests, a 90% coverage floor, fuzzing, kernel integration tests, golangci-lint (including staticcheck, errcheck, gosec and revive), govulncheck, CodeQL and OpenSSF Scorecard.
- The Format NVM timeout saturates at the 32-bit millisecond limit (about 49.7 days) instead of wrapping around to a short timeout, and Get Log Page requests smaller than one dword no longer wrap the dword count.
- Marker errors wrap the underlying I/O error, so callers can test it with `errors.Is`.
