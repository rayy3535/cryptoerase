# Changelog

## v0.4.0 (2026-10-03)

- **SAS drives behind a PERC.** Drives a PERC/MegaRAID controller passes through (JBOD, non-RAID) are told apart from virtual disks by the SCSI channel. megaraid_sas puts virtual disks on channel 2 and above. Before, every megaraid_sas disk whose vendor was not "ATA" was taken for a virtual disk. On a PowerEdge with two SAS hard disks (ST1200MM0099), `--raid-reset` exposed them, and the first then reused the name of the deleted virtual disk. The run reported "PERC virtual disk still present after the RAID reset deleted it" and "set to non-RAID but not visible to the OS".
- SAS drives and SATA hard disks behind such a controller are crypto-erased by the controller if it reports them capable (ISE or SED). Otherwise they are `UNHANDLED`, as overwriting is out of scope.
- Drives are matched to their controller slot by WWN, by serial number in the wwid or in VPD page 0x80 (SAS drives pad it), or, with a single megaraid_sas host, by the device ID that the driver encodes in the SCSI target. The same matching decides which drive record an exposed drive has.
- **Drives that reject writes.** Such a drive is erased once by the controller without markers, which made the drives a PERC H730P had left NOT READY usable again, and then erased with markers and verified. Before, it failed with "cannot write markers before erase". `device_status.perc_erase.recovery` records this.

## v0.3.0 (2026-10-03)

- SATA drives behind a PERC/MegaRAID controller are erased by the controller (`set good force`, `start erase crypto`, `set jbod`, result from the event log, markers verified) whenever the controller reports them "Cryptographic Erase Capable". Before, this was done only when ATA pass-through returned no registers. On a PERC H730P Mini (firmware 4.300.00-8366) pass-through SANITIZE was rejected with an I/O error while the drives completed it; the controller then reported them NOT READY and failed all reads. The controller erase worked on the same drives and made them usable again. hdparm is the fallback when the controller cannot erase a drive, and its failure reason says why.
- The erase result is searched among all controller events since the erase started, not the latest 64: the H730P logs an "Unexpected sense" event for every TEST UNIT READY to a drive that is not ready.
## v0.2.1 (2026-10-02)

- Drives behind a PERC/MegaRAID controller that the OS cannot see (state Ready / UGood, hot spares, drives in no virtual disk) were missing from the report. A host whose only other drives passed reported `PASS` while those drives still held data. They now get a record named by their slot (`/c0/e68/s0`):
  - without `--raid-reset`: `UNHANDLED`, with the hint to rerun with `--raid-reset`;
  - with `--raid-reset --inventory`: `PLANNED` (`raid-reset`); before, they appeared only under `raid_reset`, not in the drive table;
  - with `--raid-reset` in erase mode: set to non-RAID and erased, as before.

  If the controller cannot be listed, a `RAID controller hostN` record is `UNHANDLED`.
- The controller CLI is required whenever a megaraid_sas SCSI host exists, even if no disk on it is visible, since only the CLI can list such drives.
- The record of a PERC virtual disk points to `--raid-reset`.

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
