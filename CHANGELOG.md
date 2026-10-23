# Changelog

## v0.8.2 (2026-10-22)

- **Intel P4510/P4610 firmware floor lowered from VDV10184 to VDV10182.** A P4510 (SSDPE2KX010T8) on VDV10183 was `FAIL`: "firmware VDV10183 is below VDV10184". The floor now follows INTEL-SA-00535 (CVE-2021-0148, fixed in VDV10182) alone. For unfixed drives Intel's workaround is a block erase, so a cryptographic erase alone is not enough on them. SOLIDIGM-SA-00563 (VDV10184) no longer sets the floor: its CVEs are denial of service, privilege escalation and attacks that need physical access, and the one about erasing (CVE-2021-33082) is in Sanitize Block Erase, for which the advisory recommends a cryptographic erase. The Dell firmware line (VDV1DP25) is unchanged.

## v0.8.1 (2026-10-20)

- **`--raid-reset` on a server without a RAID controller.** On a server whose only drive was on a motherboard SATA port (ahci), with perccli64 and storcli64 installed, `--raid-reset` added an `UNHANDLED` `RAID controller` record: "drives behind the controller that the OS does not see could not be listed … No Controller found". A run of drives that all passed would have ended `INCOMPLETE`. When every installed CLI answers `show ctrlcount` with 0 and no megaraid_sas controller is loaded, there is now no such record, and the RAID reset is skipped ("no PERC/MegaRAID controller"). With a megaraid_sas controller loaded, a CLI that sees nothing still leaves the run `INCOMPLETE`, as drives could be hidden behind it. `perc.ErrNoController` marks the case in the errors of `List` and `Controllers`.
- A CLI installed twice (a copy in `$PATH` and one under `/opt/MegaRAID/`) was named twice in the "no controller seen by …" hint; it is named once.

## v0.8.0 (2026-10-16)

- **SAS drives on an HBA.** SAS drives the OS sees directly were `UNHANDLED`: "not a directly attached ATA device … SAS or other controller". Seen: three Seagate Nytro XS1920LE70134 SAS SSDs on a Dell HBA355i (mpt3sas). They are now erased with SCSI SANITIZE, service action CRYPTOGRAPHIC ERASE, sent through SG_IO by the new `scsi` code, with no outside tool:
  - REPORT SUPPORTED OPERATION CODES tells whether the drive supports it (`scsi.sanitize_crypto_erase`). Without it an SSD is `FAIL` and a hard disk `UNHANDLED`; a drive that does not answer the report is `UNHANDLED`.
  - REQUEST SENSE reads the sanitize state, in `--inventory` too. A drive left in sanitize failure mode is `FAIL`.
  - Markers are written, SANITIZE runs with IMMED, REQUEST SENSE is polled until the drive no longer reports it in progress (`device_status.scsi_sanitize_status`), and the markers must all have changed.
  - Only SAS end devices are sanitized. A RAID logical volume of another controller, or a Fibre Channel or iSCSI LUN, is still `UNHANDLED`.
- **`--raid-reset` with an HBA.** perccli64 sees the HBA355i, but it answers the virtual disk listing with "Un-supported command"; the run reported a `RAID controller` record, "virtual disks: Failure: Un-supported command". That answer now means no virtual disks. The RAID reset also leaves alone any drive the OS already sees, whatever state the CLI shows.
- `Options.SCSI` (`SCSIBackend`) replaces the SCSI backend for tests and library users.

## v0.7.3 (2026-10-16)

- **PERC 11 non-RAID drives.** On a PERC H755 Front, the RAID reset deleted the virtual disk and set the four drives to non-RAID, but the drives were then taken for virtual disks: PERC 11 puts non-RAID drives on SCSI channel 2 (`0:2:0:0` …), where megaraid_sas otherwise puts virtual disks. The first drive, which reused the deleted virtual disk's name, failed with "PERC virtual disk still present after the RAID reset deleted it"; the others were `UNHANDLED`. A megaraid_sas disk that matches one of the controller's drives (by WWN or serial number) is now a drive; the channel decides only for a disk that matches none.
- PERC 11 lists a non-RAID drive as state `Onln`, type `JBOD`, not as state `JBOD`. The drive's type is now read (`perc.Drive.Type`) and counts: the controller erase accepts such a drive, and the RAID reset leaves it alone.

## v0.7.2 (2026-10-16)

- **PERC H755 taken for a controller without non-RAID drives.** 0.7.1 judged that from `/cN show jbod` answering "Controller does not support JBOD". The PERC H755 (FW 5.160.02-3552) answers the same, as it has no controller-wide JBOD switch, yet supports non-RAID drives; `--raid-reset` left it unchanged and its virtual disk `UNHANDLED`. A controller now counts as having no non-RAID mode only if `show all` says `Support JBOD = No`, or if, besides that `show jbod` answer, it reports no personality and `Support Drive Crypto Erase = No`, as the PERC H710P does. A controller with a personality (`Current Personality = RAID-Mode`) is taken to support non-RAID drives. The reason names the evidence.
- A `set jbod` that the controller refuses as `command invalid` now says that the controller may have no non-RAID mode, and that drives not yet set to non-RAID stay unconfigured and still hold their data.

## v0.7.1 (2026-10-12)

- **Controllers without non-RAID support.** On a PowerEdge R630 with a PERC H710P Mini (firmware 3.131.05-3415), `--inventory --raid-reset` reported four SAS hard disks in state Ready as `PLANNED` and the run `PASS`. The erase then failed on the first `set jbod` with `command invalid`: PERC 8 controllers (H710, H710P, H810) cannot pass drives to the OS at all, and `/c0 show jbod` answers "Controller does not support JBOD". The configuration read now includes the controller's model, firmware, JBOD support and "Support Drive Crypto Erase" (`/cN show all`, `/cN show jbod`). A controller without JBOD support is left unchanged by the RAID reset, in the inventory too, and its drives are `UNHANDLED` with the reason, also without `--raid-reset`.
- In the inventory, a drive the OS cannot see that is a SAS drive or a hard disk, and that the controller does not report "Cryptographic Erase Capable", is `UNHANDLED` rather than `PLANNED`: after a RAID reset nothing could erase it.

## v0.7.0 (2026-10-05)

- **`--raid-reset` on Broadcom-branded and OEM MegaRAID controllers.** On an LSI SAS3108 in an Inspur server (storcli64), `set jbod` failed on every drive with `command invalid`: the controller's JBOD mode was off, as these controllers ship (Dell PERCs report it on). The configuration read now includes the JBOD mode (`/cN show jbod`). When it is off and drives are to be exposed, the reset turns it on (`/cN set jbod=on`) before setting drives to JBOD, and `raid_reset.enable_jbod` records it. If the firmware then sets unconfigured drives to JBOD by itself, the reset reads the drive states again and skips them. `--inventory` lists the command in the plan.

## v0.6.0 (2026-10-05)

- **SATA drives behind smartpqi** (Microchip SmartHBA / SmartRAID, seen on an Inspur server with Samsung PM883 drives). The controller passes ATA commands through, but returns the drive's registers in fixed-format sense data, which hdparm does not read (`bad/missing sense data`), so every drive failed with "does not return the drive's status". When that happens, the tool now sets D_SENSE in the disk's Control mode page through SG_IO, so the controller returns descriptor-format sense, and reads the status again. The record shows `attach.descriptor_sense_set`. This is done in `--inventory` too: it changes only the format of error reports, is not saved, and is not changed back. If D_SENSE cannot be set, or is set already, the drive fails as before, and the reason says what was tried.
- **Choice of controller CLI.** perccli64 manages Dell PERC controllers only. On a server with a Broadcom-branded or OEM MegaRAID controller (seen: LSI SAS3108 on Inspur) it found no controller, and the report read `perccli64: list physical drives: %!w(<nil>)`. With several CLIs installed, the tool now uses the first that sees a controller (`show ctrlcount`); an explicit `--raid-cli` is used as given. When the CLI lists no drives, the error gives the CLI's own status, and says if it sees no controller at all.

## v0.5.0 (2026-10-05)

- Directly attached SATA hard disks are no longer rejected as "rotational HDD: outside crypto-erase scope" without a look. IDENTIFY is read as for SSDs:
  - an HDD that supports ATA SANITIZE CRYPTO SCRAMBLE (a self-encrypting drive) is erased and verified like an SSD;
  - an HDD that does not support it is `UNHANDLED`, with "hard disk without a cryptographic erase … overwriting is outside this tool's scope", and its model, serial number and firmware are in the record. Seen on a Seagate ST2000DM006 (BarraCuda).

## v0.4.1 (2026-10-03)

- Removed the recovery erase added in 0.4.0. It was meant for a drive behind a PERC that rejects writes: erase it once through the controller, then again with markers. On the PERC H730P that erase failed on every blocked drive (`Erase failed … (Error f0)`), and the controller then marked them Unconfigured Bad, so they disappeared from the OS. The one drive that had worked again (sdd) had been writable before its erase, so the premise was wrong.
- Such a SATA drive now gets one SANITIZE STATUS EXT (read-only) and a retry. If writes are still rejected, it is reported `FAIL` without any controller command, with the next steps: power-cycle, `set good force` for drives in state UBad, rerun.
- After SANITIZE through hdparm is rejected on a megaraid_sas controller, the drive's sanitize status is read at once, so the drive is not left blocked, and is reported. hdparm is only used there when the controller cannot erase the drive.

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
