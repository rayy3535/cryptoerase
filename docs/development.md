# Development and releasing

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

- **Scenario tests** build a fake sysfs tree, use sparse files as drives, and swap in fake NVMe, ATA and RAID backends to run whole servers through `Run`. They cover: honest erase; firmware that reports success but keeps the data; failed, stalled, rejected and interrupted sanitize; unreadable device status; format-only NVMe 1.2 controllers; the firmware floor; unallocated NVM capacity; RAID virtual disks, and the RAID reset (planning, in-use virtual disks, foreign and failed drives, hot spares, drives in state Ready, failed commands, drives that never appear); the controller erase (SATA and SAS drives, matching by WWN, VPD serial and device ID, failed or missing results, flooded event logs, drives that reject writes, drives that come back under a new name); controllers that return no ATA registers or reject SANITIZE; missing tools; HDD, USB, removable, virtual and in-use disks (including through LVM, swap and a root filesystem shown as `/dev/root`); native NVMe multipath naming; TCG locking; marker write, read-back and verification faults; cancellation; the `--parallel` bound.
- **Unit tests** per package, including the exact bytes of every NVMe admin command handed to the kernel (through an injectable ioctl), and the polling loops on a fake clock (`testing/synctest`) with the production timeouts.
- **Fuzz tests** for every parser of device or tool output (NVMe Identify and log pages, ATA IDENTIFY and hdparm output, TCG Level 0 Discovery, perccli/storcli JSON and event log text, firmware policy and version ordering).
- **Integration tests** (`-tags integration`) against the running kernel: O_DIRECT and block ioctls on a loop device, markers, an inventory of the host, and Identify on any NVMe controller present. They never write to a real drive.

The example reports in `examples/` are golden files: the tests fail if the code would produce something different.

CI runs all of this on linux/amd64 and linux/arm64 for every push and pull request, plus a nightly long fuzz run, CodeQL and OpenSSF Scorecard. The integration job also runs `cryptoerase --inventory` on the runner and shows the report in the job summary; CI never runs an erase.

## Releasing

1. In a pull request, set `Version` in `options.go` to the new version and add a `## vX.Y.Z (YYYY-MM-DD)` section at the top of `CHANGELOG.md`. A version with a `-` (`1.2.0-rc.1`) is published as a pre-release.
2. Merge it. The release workflow sees a version on `main` that has no tag yet, checks that the version and the changelog agree, runs lint and tests, builds both binaries, attests their provenance, and creates the tag `vX.Y.Z` and the GitHub release with the changelog section as notes.

Pushing a tag `vX.Y.Z`, or publishing a release with a new tag in the web UI, runs the same workflow.

