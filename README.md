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

Cryptographic erase for every drive in a Linux server, with a JSON evidence report. NVMe, SATA, and SATA/SAS behind Dell PERC or Broadcom MegaRAID controllers. A Go library plus a command-line tool.

> [!WARNING]
> **This destroys all data on the drives it erases.** Run an inventory first, and a test erase on spare hardware for each drive model and controller you use. What it has passed on: [docs/tested-hardware.md](docs/tested-hardware.md).

## Library

```sh
go get github.com/rayy3535/cryptoerase@latest
```

Plan first, with the RAID reset (`--inventory --raid-reset`), then erase (`--yes --raid-reset`):

```go
ctx := context.Background()

// --inventory --raid-reset: find the drives and plan the erase,
// including the RAID reset. Changes nothing.
plan, err := cryptoerase.Run(ctx, cryptoerase.Options{
	Mode:      cryptoerase.ModeInventory,
	RAIDReset: true,
})
if err != nil {
	log.Fatal(err) // the run could not start, e.g. hdparm or storcli64 missing
}
for _, d := range plan.Drives {
	fmt.Println(d.Device, d.Model, d.Result, d.Reason) // PLANNED, UNHANDLED or FAIL
}
if plan.Result != "PASS" {
	log.Fatalf("not every drive can be erased: %s", plan.Result)
}

// --yes --raid-reset: delete the RAID virtual disks, then erase and
// verify every drive.
rep, err := cryptoerase.Run(ctx, cryptoerase.Options{
	Mode:      cryptoerase.ModeErase,
	Confirm:   true, // --yes
	RAIDReset: true,
	JobID:     "RECLAIM-1234",
})
if err != nil {
	log.Fatal(err)
}
evidence, err := json.MarshalIndent(rep, "", "  ")
if err != nil {
	log.Fatal(err)
}
if err := os.WriteFile("erase-report.json", evidence, 0o600); err != nil {
	log.Fatal(err)
}
os.Exit(rep.ExitCode()) // 0 PASS, 1 FAIL, 2 INCOMPLETE (some drives UNHANDLED)
```

The full program is [examples/library/main.go](examples/library/main.go).

| CLI | `Options` |
|---|---|
| `--inventory` | `Mode: cryptoerase.ModeInventory` (the default) |
| `--yes` | `Mode: cryptoerase.ModeErase, Confirm: true` |
| `--raid-reset` | `RAIDReset: true` |
| `--exclude DEV`, `--job-id ID`, `--parallel N` | `Exclude`, `JobID`, `Parallel` |

`Run` returns an error only when the run cannot start (bad options, a required tool missing); every drive's outcome is in `rep.Drives`. Backends and subpackages: [docs/library.md](docs/library.md).

## Command line

```sh
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/cryptoerase-linux-amd64
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing && install -m 755 cryptoerase-linux-amd64 cryptoerase

./cryptoerase --inventory --raid-reset                 # plan; changes nothing
./cryptoerase --yes --raid-reset --job-id RECLAIM-1234 # erase
```

`--raid-reset` is only needed when drives sit behind a PERC/MegaRAID controller in RAID or in state Ready; without it such drives are reported `UNHANDLED`.

| Flag | Meaning |
|---|---|
| `--inventory` | Find drives and plan only. Changes nothing |
| `--yes` | Erase. Required for any change |
| `--raid-reset` | PERC/MegaRAID: delete the virtual disks the running OS does not use, set their drives and drives in state Ready to non-RAID (turning the controller's JBOD mode on if needed), then erase each drive |
| `--exclude DEV` | Never touch `DEV` (`sda`, `/dev/sda`, `nvme0`). Repeatable |
| `--report FILE` | Report path. Default `./cryptoerase-<serial>-<UTC>.json` |
| `--job-id ID` | Copied into the report |
| `--allow-format` | NVMe without Sanitize Crypto Erase: accept Format NVM with Cryptographic Erase ([why it is opt-in](docs/background.md)) |
| `--parallel N` | At most N drives at once. Default 0 = all |
| `--hdparm PATH`, `--raid-cli PATH` | Tool binaries, if not in `$PATH` |
| `--samples N`, `--fw-policy FILE`, `--poll-interval`, `--no-progress-timeout`, `--format-timeout`, `--log-format text\|json`, `--version` | Tuning and output |

| Exit code | Meaning |
|---|---|
| 0 | Every drive erased and verified (inventory: every drive has a method) |
| 1 | A drive `FAIL`ed, no drive found, bad arguments, or a required tool missing |
| 2 | No failures, but some drives are `UNHANDLED` |

```sh
./cryptoerase --yes --raid-reset --job-id "$JOB" --report "/tmp/erase-$JOB.json"
case $? in
  0) release_server ;;
  2) review_unhandled_drives ;;
  *) quarantine_server ;;
esac
```

Optional provenance check: `gh attestation verify cryptoerase-linux-amd64 --repo rayy3535/cryptoerase` ([SLSA](https://slsa.dev/spec/v1.0/provenance)). From source: `go install github.com/rayy3535/cryptoerase/cmd/cryptoerase@latest`.

## Requirements

- Linux, amd64 or arm64, as root.
- `hdparm` 9.56 or later, if SATA drives are present.
- `perccli64` (Dell PERC) or `storcli64` (other MegaRAID), if such a controller is present. Searched in `$PATH` and `/opt/MegaRAID`.

A missing tool stops the run before anything is changed (exit code 1). The binary is static.

## What it erases

| Drive | Erase |
|---|---|
| NVMe | Sanitize, Crypto Erase (Format NVM with Cryptographic Erase only with `--allow-format`) |
| SATA SSD or HDD, directly attached or behind an HBA (AHCI, smartpqi, …) | ATA SANITIZE CRYPTO SCRAMBLE, if the drive supports it |
| SATA or SAS, SSD or HDD, behind a PERC / MegaRAID | The controller's cryptographic erase, for drives it reports capable |

Cryptographic erase only: no overwrite, block erase or zero-fill fallback. Each drive passes only if the device or controller reports success **and** 16 random markers written before the erase all read back changed. Drives it cannot erase are reported `UNHANDLED` or `FAIL` with the reason, never skipped.

Not covered: HDDs without encryption, SAS drives on a plain HBA, RAID controllers other than PERC/MegaRAID, locked drives (TCG Opal locked range, ATA user password), NVMe controllers with no namespace.

## Documentation

- [How it works](docs/how-it-works.md): erase methods, verification, RAID reset, controller erase, firmware floor
- [Report format](docs/report.md), with [examples](examples/)
- [Tested hardware](docs/tested-hardware.md) and controller quirks
- [Library](docs/library.md): backends and subpackages
- [Background](docs/background.md): Sanitize vs Format, ATA SANITIZE, NIST SP 800-88r2
- [Development and releasing](docs/development.md), [Changelog](CHANGELOG.md)

## License

Apache License 2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
