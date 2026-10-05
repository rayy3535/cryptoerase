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

Cryptographic erase for the drives in a Linux server, with a JSON evidence report per run. It handles NVMe drives, SATA SSDs, and SATA or SAS drives (SSD or HDD) behind a Dell PERC / Broadcom MegaRAID controller. It is meant for wherever servers pass from one user to the next (bare-metal hosting, hardware refresh and resale, lab fleets) and you need to show, drive by drive, how the previous data was destroyed.

It is a Go library (`github.com/rayy3535/cryptoerase`) with a small command-line tool (`cmd/cryptoerase`).

> [!WARNING]
> **This tool destroys all data on the drives it erases.** It has passed on the servers listed in [docs/tested-hardware.md](docs/tested-hardware.md). Before using it in production, run `--inventory` and a test erase on spare hardware for every drive model and controller you use.

## What it does

| Drive | Erase |
|---|---|
| NVMe | Sanitize, Crypto Erase. Format NVM with Cryptographic Erase only with `--allow-format` |
| SATA SSD or HDD, directly attached or behind an HBA (AHCI, smartpqi, …) | ATA SANITIZE CRYPTO SCRAMBLE, if the drive supports it (SSDs and self-encrypting HDDs) |
| SATA or SAS, SSD or HDD, behind a Dell PERC / Broadcom MegaRAID | The controller's cryptographic erase, for drives it reports "Cryptographic Erase Capable" (ISE or SED). With `--raid-reset`, virtual disks are deleted first and their drives erased one by one |

- **Cryptographic erase only, fail closed.** There is no fallback to block erase, overwrite or zero-fill. A drive without a cryptographic erase method is reported `FAIL` or `UNHANDLED`.
- **Nothing is skipped silently.** Drives the tool cannot erase are reported `UNHANDLED` with the reason:
  - RAID virtual disks without `--raid-reset`;
  - drives behind the controller that the OS cannot see;
  - HDDs without a cryptographic erase (most desktop HDDs);
  - SAS drives not behind a PERC/MegaRAID controller that can crypto-erase them;
  - disks the running OS uses, and exclusions.
- **Two checks per drive.** The device's or controller's completion status, plus 16 random markers written before the erase that must all read back changed afterwards.
- **Evidence.** One JSON record per drive: identity, capabilities, the exact commands, the reported status, and the marker check. The fields follow NIST SP 800-88r2 §4.6.
- **Firmware floor.** Drive models with erase-related advisories must run fixed firmware.

How each of these works, including the RAID reset and the controller erase: [docs/how-it-works.md](docs/how-it-works.md).

## Requirements

- Linux, amd64 or arm64, as root.
- `hdparm` 9.56 or later, if SATA drives are present.
- `perccli64` (Dell PERC) or `storcli64` (other MegaRAID), if such a controller is present. They are searched in `$PATH` and `/opt/MegaRAID`, and the first that sees a controller is used; `--raid-cli` sets one.

If a needed tool is missing, the tool exits with code 1 before writing anything. The binary is static, with no other dependencies, and is meant to run from a minimal maintenance OS such as a PXE-booted environment.

## Install

Release binaries are built by GitHub Actions from the tagged commit, with [SLSA build provenance](https://slsa.dev/spec/v1.0/provenance):

```sh
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/cryptoerase-linux-amd64
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
gh attestation verify cryptoerase-linux-amd64 --repo rayy3535/cryptoerase   # optional
```

From source (Go 1.27.1 or later): `go install github.com/rayy3535/cryptoerase/cmd/cryptoerase@latest`.

## Usage

```sh
cryptoerase --inventory                  # detect drives and plan; writes nothing
cryptoerase --yes --job-id RECLAIM-1234  # erase
cryptoerase --yes --raid-reset           # also delete RAID virtual disks and erase their drives
```

| Flag | Meaning |
|---|---|
| `--inventory` | Detect and plan only |
| `--yes` | Required to erase |
| `--raid-reset` | PERC/MegaRAID: delete the virtual disks the running OS does not use, set their drives and any drives in state Ready to non-RAID (turning the controller's JBOD mode on if it is off), then erase each drive |
| `--allow-format` | NVMe: accept Format NVM with Cryptographic Erase on controllers without Sanitize Crypto Erase. Format covers less than Sanitize; see [docs/background.md](docs/background.md) |
| `--exclude DEV` | Never touch `DEV` (`sda`, `/dev/sda`, `nvme0`). Repeatable |
| `--report FILE` | Report path. Default `./cryptoerase-<serial>-<UTC>.json` |
| `--job-id ID` | Copied into the report |
| `--parallel N` | Process at most N drives at once. Default 0 = all |
| `--samples N` | Markers per namespace or disk. Default 16 |
| `--fw-policy FILE` | Replace the built-in firmware floor table |
| `--hdparm PATH`, `--raid-cli PATH` | Tool binaries |
| `--poll-interval`, `--no-progress-timeout`, `--format-timeout` | Sanitize polling and timeouts |
| `--log-format text\|json` | stderr log format |
| `--version` | Print version, commit and Go version |

| Exit code | Meaning |
|---|---|
| 0 | Every in-scope drive was erased and verified (inventory: every drive has a method) |
| 1 | A drive `FAIL`ed, no drive was found, bad arguments, or a required tool is missing |
| 2 | No failures, but some drives are `UNHANDLED` and need another method |

```sh
cryptoerase --yes --raid-reset --job-id "$JOB" --report "/tmp/erase-$JOB.json"
case $? in
  0) release_server ;;
  2) review_unhandled_drives ;;
  *) quarantine_server ;;
esac
```

The report format is described in [docs/report.md](docs/report.md), with examples in [examples/](examples/).

## Not covered

- **Drives without a cryptographic erase:** HDDs that do not encrypt (no ATA SANITIZE CRYPTO SCRAMBLE, and no controller crypto erase); most desktop HDDs are like this. Overwriting is out of scope.
- **SAS drives on a plain HBA:** SCSI SANITIZE is not implemented; behind a PERC/MegaRAID controller they are erased by the controller.
- **RAID controllers other than PERC/MegaRAID.**
- **Locked drives:** TCG Opal drives with a locked range, and ATA drives with a user password. They need a PSID revert or unlock first.
- **NVMe controllers with no namespace attached:** recreate the namespace layout first.
- **Windows and macOS.**

## Library

```go
rep, err := cryptoerase.Run(ctx, cryptoerase.Options{Mode: cryptoerase.ModeErase, Confirm: true, RAIDReset: true})
if err != nil {
	return err // the run could not start; per-drive problems are in rep
}
os.Exit(rep.ExitCode())
```

Every backend can be replaced, and the subpackages (`nvme`, `ata`, `tcg`, `blockdev`, `perc`) are usable on their own; see [docs/library.md](docs/library.md).

## Documentation

- [How it works](docs/how-it-works.md): erase methods, verification, RAID reset, controller erase, firmware floor
- [Tested hardware](docs/tested-hardware.md): controllers and drives it has passed on, and controller quirks seen
- [Report format](docs/report.md)
- [Background](docs/background.md): Sanitize vs Format, Secure Erase Settings, ATA SANITIZE, NIST SP 800-88r2
- [Development and releasing](docs/development.md)
- [Changelog](CHANGELOG.md)

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
