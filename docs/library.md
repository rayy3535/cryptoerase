# Library

The package is `github.com/rayy3535/cryptoerase`; API documentation is on [pkg.go.dev](https://pkg.go.dev/github.com/rayy3535/cryptoerase).

A complete program that plans and then erases with the RAID reset is in [examples/library/main.go](../examples/library/main.go); the README walks through it.

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
| `perc` | perccli / storcli: configuration, virtual disks, drive state changes, controller crypto erase, event log |
