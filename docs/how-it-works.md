# How it works

- [Erase methods](#erase-methods)
- [Verification](#verification)
- [How it talks to the drives](#how-it-talks-to-the-drives)
- [Required tools](#required-tools)
- [RAID controllers](#raid-controllers)
- [Drives the controller erases](#drives-the-controller-erases)
- [Firmware floor](#firmware-floor)

## Erase methods

| Drive | Erase command | Requires |
|---|---|---|
| NVMe | Sanitize, Crypto Erase action (SANACT=100b) | SANICAP bit 0 |
| NVMe, only with `--allow-format` | Format NVM, Secure Erase Setting 010b (Cryptographic Erase), keeping the current LBA format, metadata and protection information | FNA bit 2, and no unallocated NVM capacity |
| SATA SSD or HDD, directly attached | ATA SANITIZE CRYPTO SCRAMBLE EXT | IDENTIFY word 59 bits 12 and 13. An SSD without it is `FAIL`; an HDD without it is `UNHANDLED`, as most HDDs do not encrypt |
| SATA or SAS, SSD or HDD, behind a Dell PERC / Broadcom MegaRAID | The controller's cryptographic erase (`start erase crypto`) | The controller reports the drive "Cryptographic Erase Capable" (ISE or SED drives) |

There is no fallback to block erase, overwrite, ATA Security Erase or zero-fill. A drive without a cryptographic erase method is reported `FAIL` or `UNHANDLED`, never skipped silently.

## Verification

Two independent checks must both pass.

1. **The device's own completion status.**
   - NVMe Sanitize: the Sanitize Status log page, with SSTAT reporting success. The Global Data Erased bit is recorded.
   - NVMe Format: successful command completion.
   - ATA: SANITIZE STATUS EXT reporting "completed without error".
   - Controller erase: `Erase completed on PD …` for that drive in the controller event log.
2. **Markers.** Before the erase, 16 random 1 MiB blocks are written with O_DIRECT at evenly spaced offsets of every namespace or disk. Each is read back to confirm it landed. After the erase, every one of them must read back different. This catches firmware that reports success without changing the data.

NIST SP 800-88r2 §4.5.1 asks for completion status, errors and device health, and says elaborate sampling is not necessary after a clear or purge. The markers are cheap extra evidence on top of that, not a replacement.

The write handle used for the markers stays open until the erase command has finished. Closing a disk that was opened for writing makes udev re-read its partition table (`60-block.rules`, `OPTIONS+="watch"`). A drive that is sanitizing, or that the controller is hiding, rejects that read, and the kernel logs I/O errors.

## How it talks to the drives

- **NVMe:** native `NVME_IOCTL_ADMIN_CMD` passthrough on the controller character device (`/dev/nvmeN`), so nvme-cli is not needed. The commands used are:
  - Identify
  - Get Log Page (SMART, Sanitize Status)
  - Sanitize
  - Format NVM
  - Security Receive, for TCG Level 0 Discovery: whether the drive reports media encryption, and whether a locking range is locked.
- **SATA:** through [hdparm](https://sourceforge.net/projects/hdparm/) (9.56 or later), behind the `ata.Backend` interface. hdparm handles SCSI/ATA Translation and sense-data formats across kernels and HBAs.
  - When hdparm gets no ATA registers back (`bad/missing sense data`), the controller may be using fixed-format sense data, which has no room for all the registers. The tool then sets D_SENSE in the disk's Control mode page (MODE SENSE / MODE SELECT through SG_IO, not saved), so the controller uses descriptor format, and asks again. This made SATA drives behind smartpqi readable. If the registers still do not come back, the outcome of a command cannot be read, and the tool treats it as not done.
  - SANITIZE STATUS EXT (read-only) runs before anything is written, in `--inventory` too.
- **Dell PERC / Broadcom MegaRAID:** through `perccli64`, `perccli`, `storcli64` or `storcli`, using JSON output. The exception is the event log, which the CLI prints as text even when asked for JSON.
  - perccli manages Dell PERC controllers only; Broadcom-branded and OEM controllers (Inspur, Supermicro, Lenovo, …) need storcli. With several installed, the tool uses the first that sees a controller (`show ctrlcount`). `--raid-cli` overrides the search.

## Required tools

Before anything is written, the tool checks that it has what this host needs:
- hdparm, when SATA drives are present;
- the controller CLI, when a PERC/MegaRAID controller is present or `--raid-reset` is given.

Excluded, in-use, removable and USB disks do not count. If a tool is missing, the run stops with exit code 1, naming the tool and the disk that needs it, and writes no report.

## RAID controllers

On a PERC or MegaRAID controller in RAID mode, the OS sees virtual disks, not drives. Without `--raid-reset`, every virtual disk and every drive the OS cannot see is reported `UNHANDLED`, listing the drives behind the controller, so a run cannot pass while they hold data. That includes drives in state Ready (UGood) and hot spares.

`--raid-reset` removes the RAID layer first:

1. **Read the configuration:** virtual disks, the drives behind them, hot spares, serial numbers, WWNs.
2. **Decide, without changing anything.** A virtual disk that backs a disk the running OS uses (mounted, swap, through LVM) is kept, with its drives. The whole controller is left unchanged if:
   - it carries a foreign configuration;
   - any drive is in a state other than online, unconfigured good, JBOD or hot spare (failed, rebuilding, Unconfigured Bad, …);
   - the OS uses a disk on such a controller, and a virtual disk cannot be matched to its block device (by the CLI's "OS Drive Name" or the SCSI NAA identifier).
3. **Apply,** in erase mode only:
   - remove hot spares (`delete hotsparedrive`);
   - delete the other virtual disks (`/cN/vM delete force`);
   - set their drives and any drives in state Ready to non-RAID (`set jbod`).
4. **Wait** up to 60 s (`Options.RAIDWait`) for each drive to appear as a block device.
5. **Erase** each drive on its own. Behind the controller, this is the controller erase described below.

The report lists every command run (or, with `--inventory`, planned) under `raid_reset`. A drive that was to be exposed but never reached the OS is reported `FAIL`. Drives stay non-RAID afterwards.

## Drives the controller erases

### Telling drives from virtual disks

megaraid_sas puts virtual disks on SCSI channel 2 and above (`0:2:0:0`), with the controller as their model (`PERC H730P Mini`). A drive the controller passes through (JBOD, non-RAID) is on channel 0 or 1, with its own vendor and model, and its SCSI target is its controller device ID (DID).

A block device is matched to its controller slot in this order:
1. by WWN;
2. by serial number, in the wwid or in VPD page 0x80 (SAS drives pad it, e.g. `WFK7J2ZJ0000K9…` for `WFK7J2ZJ`);
3. with a single megaraid_sas host, by DID = channel × 128 + target.

### Why the controller, not ATA pass-through

Behind a PERC, SANITIZE sent through ATA pass-through is not dependable.

- **PERC H355:** forwards ATA commands but returns none of the drive's registers. The answer is fixed-format sense with every register field zero, so the outcome of a sanitize cannot be read.
- **PERC H730P:** rejects SANITIZE with an I/O error, while the drive completes it. The controller then reports the drive NOT READY (`Unexpected sense … Sense: 2/05/00` in its event log) and fails every read and write until the server is power-cycled.

SAS drives and SATA hard disks cannot be sanitized through hdparm at all.

So a drive behind the controller is erased by the controller whenever the controller reports it "Cryptographic Erase Capable". Otherwise:
- a SATA SSD falls back to SANITIZE through hdparm; its failure reason also says why the controller could not be used;
- SAS drives and SATA hard disks are `UNHANDLED`, because overwriting is outside this tool's scope.

### Steps

1. Write the markers through the OS.
2. Remove the disk from the kernel (`/sys/block/sdX/device/delete`) while the markers' write handle is still open. The controller hides the drive in the next step, and any read of the stale disk would fail with I/O errors in the kernel log.
3. `set good force`. The controller only erases unconfigured drives.
4. `start erase crypto`.
5. Wait for `Erase completed on PD 26(e0x44/s0)` or `Erase failed …` in the controller event log (`show events`). PD numbers there are hexadecimal: PD 26 is DID 38. Every event since the erase started is read, however many others are logged in between; a drive that is not ready floods the H730P log with "Unexpected sense". `show erase` cannot tell: it reads `Not in progress` both before and after an erase that takes no time.
6. `set jbod`. This happens after a failed erase too, so the drive is left non-RAID as it was found.
7. Wait for the disk to come back. Its name may change.
8. Verify the markers.

The drive passes only with `Erase completed` in the event log and every marker changed. The report records the commands, the event and any new device name under `device_status.perc_erase`. In `--inventory` the drive is `PLANNED` with method `perc-crypto-erase`.

### Drives that reject writes

A drive that rejects the marker writes with an I/O error cannot be verified. The H730P blocked drives this way after a pass-through SANITIZE. For such a SATA drive the tool sends one SANITIZE STATUS EXT (read-only) and retries. If writes are still rejected, the drive is reported `FAIL` and the controller is not asked to erase it. On the H730P that erase failed (`Error f0`) and the controller then marked the drives Unconfigured Bad.

To recover such drives:
1. Power-cycle the server.
2. Set any drive in state UBad good: `perccli64 /cN/eE/sS set good force`.
3. Rerun.

## Firmware floor

Drive models with published erase-related advisories must run fixed firmware. Older firmware is reported `FAIL`. Built-in rules (`DefaultFirmwarePolicyText`):

| Model | Firmware line | Minimum | Reference |
|---|---|---|---|
| Intel / Solidigm DC P4510, P4610 | Dell (`VDV1DP*`) | VDV1DP25 | Dell DSA-2022-203 |
| Intel / Solidigm DC P4510, P4610 | Intel (`VDV10*`) | VDV10184 | INTEL-SA-00535, SOLIDIGM-SA-00563 |

`--fw-policy FILE` replaces the table. The format is one rule per line: `model regex ; firmware regex ; minimum ; reference`. Versions are compared like `sort -V`. Pull requests that add rules should cite a public advisory.
