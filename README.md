# cryptoerase

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
- **Dell PERC / Broadcom MegaRAID:** the OS sees only virtual disks. If `perccli64`, `perccli`, `storcli64` or `storcli` is installed, the record for each virtual disk lists the physical drives behind it, including whether each is a SED.

## Requirements

- Linux, amd64 or arm64, running as root (admin passthrough needs `CAP_SYS_ADMIN`).
- `hdparm` 9.56 or later if SATA drives are present.
- Optional: `perccli64` or `storcli64` for RAID controller inventory.

The binary is static (`CGO_ENABLED=0`) and has no other runtime dependencies. It is meant to run from a minimal maintenance OS, for example a PXE-booted environment used between deployments.

## Install

```sh
go install github.com/rayy3535/cryptoerase/cmd/cryptoerase@latest
# or
make build        # bin/cryptoerase, static
make release      # dist/cryptoerase-linux-{amd64,arm64} + SHA256SUMS
```

## Usage

```sh
# Detect drives and plan the erase. Writes nothing.
cryptoerase --inventory

# Erase.
cryptoerase --yes --job-id RECLAIM-1234 --report /var/tmp/erase.json
```

| Flag | Meaning |
|---|---|
| `--inventory` | Detect and plan only |
| `--yes` | Required to erase |
| `--report FILE` | Report path. Default `./cryptoerase-<serial>-<UTC>.json` |
| `--job-id ID` | Copied into the report |
| `--allow-format` | Accept NVMe Format NVM SES=010b on controllers without Sanitize Crypto Erase (typically NVMe 1.2 drives). Off by default, because Format covers less than Sanitize; see [docs/background.md](docs/background.md) |
| `--exclude DEV` | Never touch `DEV` (`sda`, `/dev/sda`, `nvme0`). Repeatable. Reported `UNHANDLED` |
| `--fw-policy FILE` | Replace the built-in firmware floor table |
| `--parallel N` | Process at most N drives at once. Default 0 = all |
| `--samples N` | Markers per namespace or disk. Default 16 |
| `--poll-interval`, `--no-progress-timeout`, `--format-timeout` | Sanitize polling and timeouts |
| `--hdparm PATH` | hdparm binary |
| `--log-format text\|json` | stderr log format |

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
- **Drives behind RAID virtual disks:** use the controller's own cryptographic erase for SED/ISE drives, or switch the drives to non-RAID / HBA mode and rerun.
- **TCG Opal drives with a locked range, or ATA drives with a user password set:** these need a PSID revert or unlock first. They are reported `FAIL`.
- **NVMe controllers with no namespace attached:** recreate the namespace layout first.
- **Windows and macOS:** not supported.

## Development

```sh
make            # gofmt check, vet, tests with -race, static build
make examples   # regenerate examples/*.json from a simulated host
```

The tests build a fake sysfs tree, use sparse files as drives, and swap in fake NVMe, ATA and RAID backends. Scenarios covered:

- Honest erase
- Firmware that reports success but keeps the data
- Failed and stalled sanitize
- Rejected commands
- Format-only NVMe 1.2 controllers
- Firmware floor
- Unallocated NVM capacity
- RAID virtual disks
- HDD, USB and in-use disks
- Native NVMe multipath naming
- TCG locking
- Cancellation
- The `--parallel` bound

Hardware reports are very welcome. Please include the drive model, firmware and the `--inventory` report; see [CONTRIBUTING.md](CONTRIBUTING.md).

## Background

[docs/background.md](docs/background.md) covers:

- what Sanitize is and how it differs from Format NVM;
- the Secure Erase Settings (SES) levels;
- the ATA SANITIZE feature set;
- what NIST SP 800-88r2 expects from cryptographic erase and from sanitization records.

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
