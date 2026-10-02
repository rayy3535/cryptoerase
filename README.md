<h1 align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/logo-wordmark-dark.svg">
    <img alt="cryptoerase" src="docs/assets/logo-wordmark-light.svg" width="440">
  </picture>
</h1>

<p align="center">
  <a href="https://github.com/rayy3535/cryptoerase/actions/workflows/ci.yml"><img alt="ci" src="https://github.com/rayy3535/cryptoerase/actions/workflows/ci.yml/badge.svg"></a>
  <a href="https://github.com/rayy3535/cryptoerase/actions/workflows/codeql.yml"><img alt="codeql" src="https://github.com/rayy3535/cryptoerase/actions/workflows/codeql.yml/badge.svg"></a>
  <a href="https://scorecard.dev/viewer/?uri=github.com/rayy3535/cryptoerase"><img alt="OpenSSF Scorecard" src="https://api.scorecard.dev/projects/github.com/rayy3535/cryptoerase/badge"></a>
  <a href="https://pkg.go.dev/github.com/rayy3535/cryptoerase"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/rayy3535/cryptoerase.svg"></a>
</p>

Cryptographic erase for the NVMe and SATA SSDs in a Linux server, with a JSON evidence report per run. Useful wherever servers pass from one user to the next (bare-metal hosting, hardware refresh and resale, lab fleets) and you need to show, drive by drive, how the previous data was destroyed.

It is a Go library (`github.com/rayy3535/cryptoerase`) with a small command-line tool (`cmd/cryptoerase`).

> [!WARNING]
> **This tool destroys all data on the drives it erases.** It is pre-release software. It has been tested against simulated devices only. Run `--inventory` and a test erase on spare hardware of every drive model you use before putting it into production.

## What it does

| Drive | Erase command | Requires |
|---|---|---|
| NVMe | Sanitize, Crypto Erase action (SANACT=100b) | SANICAP bit 0 |
| NVMe, only with `--allow-format` | Format NVM, Secure Erase Setting 010b (Cryptographic Erase), keeping the current LBA format, metadata and protection information | FNA bit 2, and no unallocated NVM capacity |
| SATA SSD | ATA SANITIZE CRYPTO SCRAMBLE EXT | IDENTIFY word 59 bits 12 and 13 |

Design rules:

- **Cryptographic erase only, fail closed.** There is no fallback to block erase, overwrite, ATA Security Erase or zero-fill. A drive that cannot be cryptographically erased is reported `FAIL`.
- **Nothing is skipped silently.** Drives the host cannot address directly are reported `UNHANDLED` with the reason: RAID virtual disks, SAS drives, HDDs, disks used by the running OS, and drives excluded by the operator. Removable media and empty slots are reported `SKIPPED`.
- **Firmware floor.** Drive models with published erase-related advisories must run fixed firmware. Older firmware is reported `FAIL`. See [Firmware floor](#firmware-floor).
- **Evidence.** Each drive gets a record of identity, capabilities, the exact command, the status the device reported, and an independent read-back check. The fields follow NIST SP 800-88r2 §4.6. See [docs/report.md](docs/report.md).
- **Concurrency.** Drives are processed in parallel. `--parallel` sets an upper bound.

### Verification

Two independent checks must both pass:

1. **The device's own completion status.**
   - NVMe Sanitize: the Sanitize Status log page, with SSTAT reporting success. The Global Data Erased bit is recorded.
   - NVMe Format: successful command completion.
   - ATA: SANITIZE STATUS EXT reporting "completed without error".
2. **Markers.** Before the erase, 16 random 1 MiB blocks are written with O_DIRECT at evenly spaced offsets of every namespace or disk, and each is read back to confirm it landed. After the erase, every one of them must read back different. This catches firmware that reports success without changing the data.

NIST SP 800-88r2 §4.5.1 asks for completion status, errors and device health, and says elaborate sampling is not necessary after a clear or purge. The markers are cheap extra evidence on top of that, not a replacement for it.

### How it talks to the drives

- **NVMe:** native `NVME_IOCTL_ADMIN_CMD` passthrough on the controller character device (`/dev/nvmeN`). Every command and status code is under the tool's control, and nvme-cli is not needed.
  - Commands used: Identify, Get Log Page (SMART, Sanitize Status), Sanitize, Format NVM, and Security Receive for TCG Level 0 Discovery.
  - TCG Level 0 Discovery records whether the drive reports media encryption and whether a locking range is locked.
- **SATA:** through [hdparm](https://sourceforge.net/projects/hdparm/) (9.56 or later), behind the `ata.Backend` interface. hdparm handles SCSI/ATA Translation and sense-data formats across kernels and HBAs. A native SG_IO backend can be added behind the same interface.
- **Dell PERC / Broadcom MegaRAID:** the OS sees only virtual disks. If `perccli64`, `perccli`, `storcli64` or `storcli` is installed, the record for each virtual disk lists the physical drives behind it, including whether each is a SED. With `--raid-reset` the tool removes the RAID configuration and erases each drive directly; see [RAID controllers](#raid-controllers).

## Requirements

- Linux, amd64 or arm64, running as root (admin passthrough needs `CAP_SYS_ADMIN`).
- `hdparm` 9.56 or later if SATA drives are present.
- Optional: `perccli64` or `storcli64` for RAID controller inventory; required for `--raid-reset`.

The binary is static (`CGO_ENABLED=0`) and has no other runtime dependencies. It is meant to run from a minimal maintenance OS, for example a PXE-booted environment used between deployments.

## Install

Release binaries are static and built by GitHub Actions from the tagged commit, with [SLSA build provenance](https://slsa.dev/spec/v1.0/provenance). Download `cryptoerase-linux-amd64` or `-arm64` and `SHA256SUMS` from the [releases page](https://github.com/rayy3535/cryptoerase/releases), then check them before copying the binary into your maintenance image:

```sh
sha256sum -c SHA256SUMS --ignore-missing
gh attestation verify cryptoerase-linux-amd64 --repo rayy3535/cryptoerase
```

From source (Go 1.27.1 or later):

```sh
go install github.com/rayy3535/cryptoerase/cmd/cryptoerase@latest
# or, in a checkout
make build        # bin/cryptoerase, static
make release      # dist/cryptoerase-linux-{amd64,arm64} + SHA256SUMS
```

`cryptoerase --version` prints the version, the commit it was built from, and the Go version.

## Usage

```sh
# Detect drives and plan the erase. Writes nothing.
cryptoerase --inventory

# Erase.
cryptoerase --yes --job-id RECLAIM-1234 --report /var/tmp/erase.json
```

| Flag | Meaning |
|---|---|
| `--inventory` | Detect and plan only. Cannot be combined with `--yes` |
| `--yes` | Required to erase |
| `--report FILE` | Report path. Default `./cryptoerase-<serial>-<UTC>.json` |
| `--job-id ID` | Copied into the report |
| `--allow-format` | Accept NVMe Format NVM SES=010b on controllers without Sanitize Crypto Erase (typically NVMe 1.2 drives). Off by default, because Format covers less than Sanitize; see [docs/background.md](docs/background.md) |
| `--raid-reset` | PERC/MegaRAID: delete the virtual disks the running OS does not use, set their drives to non-RAID, then erase each drive directly. With `--inventory`, only the plan is reported. See [RAID controllers](#raid-controllers) |
| `--exclude DEV` | Never touch `DEV` (`sda`, `/dev/sda`, `nvme0`). Repeatable. Reported `UNHANDLED` |
| `--fw-policy FILE` | Replace the built-in firmware floor table |
| `--parallel N` | Process at most N drives at once. Default 0 = all |
| `--samples N` | Markers per namespace or disk. Default 16 |
| `--poll-interval`, `--no-progress-timeout`, `--format-timeout` | Sanitize polling and timeouts |
| `--hdparm PATH` | hdparm binary |
| `--log-format text\|json` | stderr log format |
| `--version` | Print version, commit and Go version |

Exit codes:

| Code | Meaning |
|---|---|
| 0 | Every in-scope drive was erased and verified. In inventory mode: every in-scope drive has a method |
| 1 | At least one drive `FAIL`ed, no drive was found, or bad arguments |
| 2 | No failures, but some drives are `UNHANDLED` and need another method before the server is released |

A typical reclamation hook:

```sh
cryptoerase --yes --job-id "$JOB" --report "/tmp/erase-$JOB.json"
case $? in
  0) release_server ;;
  2) handle_unhandled_drives_then_review ;;   # e.g. RAID virtual disks via the controller
  *) quarantine_server ;;
esac
upload "/tmp/erase-$JOB.json"
```

## RAID controllers

On a Dell PERC or Broadcom MegaRAID controller in RAID mode, the OS sees virtual disks, not drives, so nothing behind them can be crypto-erased directly. `--raid-reset` removes that layer first:

1. **Read the configuration** with `perccli64` (or `perccli`, `storcli64`, `storcli`): virtual disks, the drives behind them, hot spares, serial numbers and WWNs.
2. **Decide, without changing anything.** A virtual disk that backs a disk the running OS uses (mounted, swap, through LVM) is kept, with its drives. The whole controller is left unchanged if:
   - it carries a foreign configuration;
   - any drive is in a state other than online, unconfigured good, JBOD or hot spare (failed, rebuilding, ...);
   - the OS uses a disk on such a controller, and a virtual disk cannot be matched to its block device (by the CLI's "OS Drive Name" or the SCSI NAA identifier).
3. **Apply,** in erase mode only:
   - remove hot spares (`delete hotsparedrive`);
   - delete the other virtual disks (`/cN/vM delete force`);
   - set their drives to non-RAID (`set jbod`).
4. **Wait** up to 60 s for each drive to appear as a block device, matched by WWN or serial number.
5. **Erase** each drive like any directly attached drive: markers, Sanitize Crypto Erase / SANITIZE CRYPTO SCRAMBLE, verification. Each drive record names its controller slot (`attach.raid_slot`).

Every command run (or, with `--inventory`, planned) is in the report under `raid_reset`. A drive that was to be exposed but never reached the OS is reported `FAIL`, so its data cannot be left behind unnoticed. Drives stay non-RAID afterwards.

Whether ATA passthrough reaches a SATA drive set to non-RAID depends on the controller and firmware; if IDENTIFY does not get through, the drive is reported `UNHANDLED` as usual.

### SATA drives the controller erases

Some controllers forward ATA commands to a non-RAID SATA drive but not the drive's registers, so the outcome of a sanitize cannot be read back. On a PERC H355 the answer is fixed-format sense with every register field zero, and hdparm reports `bad/missing sense data`. Behind a PERC/MegaRAID controller (`megaraid_sas`), such a drive is erased by the controller instead, if the controller reports it "Cryptographic Erase Capable" (ISE or SED drives):

1. Write the markers through the OS.
2. Remove the disk from the kernel (`/sys/block/sdX/device/delete`). The controller hides the drive in the next step, and reads of the stale disk would fail with I/O errors in the kernel log. The markers' write handle is closed only after the removal: closing it makes udev re-read the partition table, and that read would race with the removal.
3. `set good force`: the drive becomes unconfigured good, which the controller requires for an erase.
4. `start erase crypto`.
5. Wait for the result in the controller event log (`show events`): `Erase completed on PD 26(e0x44/s0)` or `Erase failed ...`. `show erase` cannot tell: it reads `Not in progress` both before and after an erase that takes no time.
6. `set jbod`. This happens after a failed erase too, so the drive is left non-RAID as it was found.
7. Wait for the disk to come back, matched by WWN or serial; its name may change.
8. Verify the markers.

The drive passes only with `Erase completed` in the event log and every marker changed. The commands, the event and the new device name are in the report under `device_status.perc_erase`. In `--inventory` the drive is `PLANNED` with method `perc-crypto-erase`. Drives that do not match exactly one controller drive by serial number or WWN, that are not JBOD, or that the controller does not report as crypto-erase capable are reported `FAIL`, as are such drives behind other controller types.

## Library

```go
rep, err := cryptoerase.Run(ctx, cryptoerase.Options{
	Mode:        cryptoerase.ModeErase,
	Confirm:     true,
	AllowFormat: false,
	Parallel:    4,
	JobID:       "RECLAIM-1234",
})
if err != nil {
	return err // the run could not start; per-drive problems are in rep
}
for _, d := range rep.Drives {
	fmt.Println(d.Device, d.Model, d.Serial, d.Result, d.Reason)
}
os.Exit(rep.ExitCode())
```

Every backend in `Options` can be replaced:

| Option | Default |
|---|---|
| `OpenNVMe` | `nvme.OpenController` |
| `ATA` | `*ata.Hdparm` |
| `PERC` | `*perc.Lister` |
| `OpenBlock` | `blockdev.Open` |

The sysfs, procfs and /dev roots are also configurable. The tests use this to simulate whole servers.

Subpackages are usable on their own:

| Package | Contents |
|---|---|
| `nvme` | Admin passthrough, Identify / log page decoding, Sanitize and Format encoding, status codes |
| `ata` | IDENTIFY DEVICE decoding, SANITIZE status, hdparm backend |
| `tcg` | TCG Storage Level 0 Discovery decoding |
| `blockdev` | Aligned O_DIRECT I/O |
| `perc` | perccli / storcli JSON |

## Firmware floor

Built-in rules (`DefaultFirmwarePolicyText`):

| Model | Firmware line | Minimum | Reference |
|---|---|---|---|
| Intel / Solidigm DC P4510, P4610 | Dell (`VDV1DP*`) | VDV1DP25 | Dell DSA-2022-203 |
| Intel / Solidigm DC P4510, P4610 | Intel (`VDV10*`) | VDV10184 | INTEL-SA-00535, SOLIDIGM-SA-00563 |

Rule format, one per line: `model regex ; firmware regex ; minimum ; reference`. Versions are compared like `sort -V`. Pull requests that add rules should cite a public advisory.

## Not covered

- **SAS drives:** SCSI SANITIZE is not implemented.
- **HDDs:** cryptographic erase is not applicable to drives that do not encrypt; use overwrite.
- **Drives behind RAID virtual disks without `--raid-reset`:** they are listed, not erased. Use `--raid-reset`, the controller's own cryptographic erase for SED/ISE drives, or switch the drives to non-RAID / HBA mode and rerun. RAID controllers other than PERC/MegaRAID are not supported.
- **TCG Opal drives with a locked range, or ATA drives with a user password set:** these need a PSID revert or unlock first. They are reported `FAIL`.
- **NVMe controllers with no namespace attached:** recreate the namespace layout first.
- **Windows and macOS:** not supported.

## Development

Requires Go 1.27.1 or later.

```sh
make              # lint (gofmt, go mod tidy, vet, golangci-lint, actionlint), tests with -race, static build
make cover        # coverage profile; fails below 90%
make fuzz         # fuzz every parser for FUZZTIME (default 30s) each
make vulncheck    # govulncheck
make integration  # needs root: real kernel tests on a loop device, read-only NVMe identify
make examples     # regenerate examples/*.json after an intended report change
```

The tests come in four layers:

- **Scenario tests** build a fake sysfs tree, use sparse files as drives, and swap in fake NVMe, ATA and RAID backends to run whole servers through `Run`. They cover: honest erase; firmware that reports success but keeps the data; failed, stalled, rejected and interrupted sanitize; unreadable device status; format-only NVMe 1.2 controllers; the firmware floor; unallocated NVM capacity; RAID virtual disks, and the RAID reset (planning, in-use virtual disks, foreign and failed drives, hot spares, failed commands, drives that never appear); HDD, USB, removable, virtual and in-use disks (including through LVM, swap and a root filesystem shown as `/dev/root`); native NVMe multipath naming; TCG locking; marker write, read-back and verification faults; cancellation; the `--parallel` bound.
- **Unit tests** per package, including the exact bytes of every NVMe admin command handed to the kernel (through an injectable ioctl), and the polling loops on a fake clock (`testing/synctest`) with the production timeouts.
- **Fuzz tests** for every parser of device or tool output (NVMe Identify and log pages, ATA IDENTIFY and hdparm output, TCG Level 0 Discovery, perccli/storcli JSON, firmware policy and version ordering).
- **Integration tests** (`-tags integration`) against the running kernel: O_DIRECT and block ioctls on a loop device, markers, an inventory of the host, and Identify on any NVMe controller present. They never write to a real drive.

The example reports in `examples/` are golden files: the tests fail if the code would produce something different.

CI runs all of this on linux/amd64 and linux/arm64 for every push and pull request, plus a nightly long fuzz run, CodeQL and OpenSSF Scorecard. The integration job also runs `cryptoerase --inventory` on the runner and shows the report in the job summary; CI never runs an erase.

### Releasing

1. In a pull request, set `Version` in `options.go` to the new version and add a `## vX.Y.Z (YYYY-MM-DD)` section at the top of `CHANGELOG.md`. A version with a `-` (`1.2.0-rc.1`) is published as a pre-release.
2. Merge it. The release workflow sees a version on `main` that has no tag yet, checks that the version and the changelog agree, runs lint and tests, builds both binaries, attests their provenance, and creates the tag `vX.Y.Z` and the GitHub release with the changelog section as notes.

Pushing a tag `vX.Y.Z`, or publishing a release with a new tag in the web UI, runs the same workflow.

## Background

[docs/background.md](docs/background.md) covers:

- what Sanitize is and how it differs from Format NVM;
- the Secure Erase Settings (SES) levels;
- the ATA SANITIZE feature set;
- what NIST SP 800-88r2 expects from cryptographic erase and from sanitization records.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
