# Dependencies

cryptoerase is a static Go binary with no shared-library or Go-module dependencies. On the server it needs Linux, root, and at most two outside programs, depending on the drives:

| Dependency | Needed when | Without it |
|---|---|---|
| Linux (amd64 or arm64), root, `/sys` and `/proc` mounted | Always | — |
| [hdparm](#hdparm) 9.56 or later | A SATA drive is present, or `--raid-reset` is given | The run stops before changing anything (exit 1, no report) |
| [perccli64 or storcli64](#raid-controller-cli) | A Dell PERC / Broadcom MegaRAID controller (`megaraid_sas`) is present, or `--raid-reset` is given | The run stops before changing anything (exit 1, no report) |

NVMe drives need neither program. SATA drives on AHCI or an HBA such as smartpqi need only hdparm. The check runs before anything is written, and ignores disks that are excluded, in use by the running OS, removable, USB or empty.

## hdparm

[hdparm](https://sourceforge.net/projects/hdparm/) sends the ATA commands to SATA drives. Version 9.56 or later is required, for `--Istdout` in host byte order; the version is not checked, only that `hdparm -V` runs.

Install it from the distribution: `apt install hdparm`, `dnf install hdparm`. It is looked up in `$PATH`; `--hdparm PATH` sets another binary.

Commands run:

| Command | When |
|---|---|
| `hdparm -V` | Before the run, and for the report |
| `hdparm --Istdout /dev/sdX` | Every SATA drive (IDENTIFY DEVICE; read-only) |
| `hdparm --sanitize-status /dev/sdX` | Every SATA drive that hdparm erases, in `--inventory` too (read-only) |
| `hdparm --yes-i-know-what-i-am-doing --sanitize-crypto-scramble /dev/sdX` | Erase mode only |

Behind a PERC/MegaRAID controller that can erase a drive itself, the controller erases it; hdparm only reads IDENTIFY, and SANITIZE STATUS if the drive rejects writes.

## RAID controller CLI

The controller CLI lists the drives behind a PERC/MegaRAID controller, including drives the OS cannot see, and performs the RAID reset and the controller's crypto erase.

| CLI | Controllers | Source | Installs to |
|---|---|---|---|
| `perccli64` (or `perccli`) | Dell PERC only | Dell support site, "PERC CLI" | `/opt/MegaRAID/perccli/` |
| `storcli64` (or `storcli`) | Broadcom, LSI and OEM-branded MegaRAID (Inspur, Supermicro, Lenovo, …) | Broadcom support site, "StorCLI" | `/opt/MegaRAID/storcli/` |

perccli does not see a non-Dell controller (`Controller Count = 0`). The CLIs are looked up in this order: perccli64, perccli, storcli64, storcli, first in `$PATH`, then in `/opt/MegaRAID/perccli` and `/opt/MegaRAID/storcli`. With several installed, the first that sees a controller (`show ctrlcount`) is used. `--raid-cli PATH` sets the binary and skips the search. Seen on the tested servers: perccli64 007.1623 and storcli64 007.2612. MegaCli is not used.

Commands run (`J` asks for JSON output):

| Command | When |
|---|---|
| `show ctrlcount J` | Choosing between several installed CLIs |
| `/call/eall/sall show all J` (or `/call/sall show all J`) | Reading the drives: state, serial number, WWN, crypto erase capability |
| `/call/vall show all J` | Reading the virtual disks |
| `/cN show jbod J` | Reading the controller's JBOD mode |
| `/call/eall/sall show J` (or `/call/sall show J`) | Listing the drives behind a virtual disk |
| `/cN show events type=latest=N` | Reading the result of a controller erase |
| `/cN/eE/sS show erase J` | Erase progress |
| `/cN/eE/sS delete hotsparedrive` | Erase mode, `--raid-reset`: hot spares |
| `/cN/vM delete force` | Erase mode, `--raid-reset`: virtual disks the OS does not use |
| `/cN set jbod=on` | Erase mode, `--raid-reset`, when the controller's JBOD mode is off |
| `/cN/eE/sS set jbod` | Erase mode: exposing drives (`--raid-reset`), and setting each drive back to non-RAID after its controller erase |
| `/cN/eE/sS set good force` | Erase mode: before a controller erase |
| `/cN/eE/sS start erase crypto` | Erase mode: the controller erase |

In `--inventory` only the read-only commands run; the others are listed in the report as planned.

## Kernel interfaces

Everything else goes straight to the kernel:

| Interface | Used for |
|---|---|
| `NVME_IOCTL_ADMIN_CMD` on `/dev/nvmeN` | Identify, Get Log Page, Sanitize, Format NVM, Security Receive (TCG Level 0 Discovery) |
| `NVME_IOCTL_ID`, `NVME_IOCTL_RESCAN` | Namespace ID of a block device; rescanning namespaces after Format NVM |
| `SG_IO` on `/dev/sdX` | MODE SENSE / MODE SELECT of the Control page, only when a controller returns ATA status in fixed-format sense data (D_SENSE; seen on smartpqi) |
| `O_DIRECT` reads and writes, `BLKGETSIZE64`, `BLKFLSBUF` on block devices | The markers |
| `/sys/block`, `/sys/class/{nvme,block,scsi_host,dmi}`, `/sys/dev/block` (read) | Finding drives, drivers, WWNs and serial numbers, host identity |
| `/sys/block/sdX/device/delete`, `/sys/class/scsi_host/hostN/scan` (write) | Behind a PERC/MegaRAID controller, erase mode: removing a disk from the kernel before the controller erase, and finding it again afterwards |
| `/proc/mounts`, `/proc/swaps`, `/proc/self/mountinfo` | Disks the running OS uses, which are never touched |

Root is needed: CAP_SYS_ADMIN for NVMe admin commands, CAP_SYS_RAWIO for SG_IO, and write access to block devices and sysfs. The drivers on the tested servers were `nvme`, `ahci`, `megaraid_sas` and `smartpqi`; the Inspur servers ran kernel 5.15.

## Not needed

nvme-cli, sg3_utils, sdparm, smartmontools, sedutil-cli, udevadm, lsblk, MegaCli, and the Microchip and HPE CLIs (arcconf, ssacli). SATA drives behind smartpqi need only hdparm.

## Library

The library needs the same programs on the server. Their paths are set through the backends:

```go
rep, err := cryptoerase.Run(ctx, cryptoerase.Options{
	ATA:  &ata.Hdparm{Path: "/usr/local/sbin/hdparm"},
	PERC: &perc.Lister{Path: "/opt/MegaRAID/storcli/storcli64"},
})
```

A missing program makes `Run` return an error wrapping `cryptoerase.ErrToolMissing`, before anything is changed. Every backend can also be replaced by your own implementation; see [library.md](library.md). The module has no third-party Go dependencies and builds with `CGO_ENABLED=0`.
