# Report format

`cryptoerase` writes one JSON document per run (schema `cryptoerase.report/v1`). For complete examples from a simulated host, see [`examples/report-inventory.json`](../examples/report-inventory.json) and [`examples/report-erase.json`](../examples/report-erase.json).

## Top level

| Field | Meaning |
|---|---|
| `schema` | `cryptoerase.report/v1` |
| `mode` | `inventory` or `erase` |
| `job_id` | From `--job-id` |
| `host` | DMI manufacturer, product and serial, plus hostname and kernel |
| `tool` | Tool version, Go version, NVMe transport, ATA backend version |
| `policy` | `allow_format`, `samples`, firmware rules and exclusions in effect |
| `started_at`, `finished_at` | UTC |
| `result` | `PASS`, `FAIL` or `INCOMPLETE`. These map to exit codes 0, 1 and 2 |
| `counts` | Number of drives per result |
| `raid_reset` | With `--raid-reset`: per controller, the virtual disks deleted and kept, the drives exposed, the commands run (or planned), `executed`, and `skipped` or `error` |
| `drives` | One record per drive. See below |

## Drive record

| Field | Meaning |
|---|---|
| `device` | `/dev/nvmeN` for an NVMe controller, `/dev/sdX` otherwise |
| `namespaces` | NVMe namespace block devices covered by the record |
| `interface`, `media_type` | E.g. `NVMe` / `SSD (NVMe)`, `SATA` / `SSD (SATA)` |
| `model`, `serial`, `firmware`, `capacity_bytes` | Drive identity |
| `attach` | SCSI host driver and SCSI inquiry vendor/model (SCSI-class disks) |
| `nvme` | VER, OACS, SANICAP, FNA, TNVMCAP, UNVMCAP, and the capability bits decoded from them |
| `ata` | Sanitize capabilities from IDENTIFY word 59, security state from word 128, non-rotating flag |
| `tcg` | TCG Level 0 Discovery: SSCs, locking state, `media_encryption` |
| `health` | NVMe SMART: critical warning, available spare, percentage used |
| `perc` | Physical drives behind a RAID virtual disk, if a controller CLI is installed |
| `attach.raid_slot` | Controller slot (`/c0/e64/s1`) of a drive exposed by `--raid-reset` or erased by the controller |
| `firmware_policy` | `pass`, `fail` or `no_rule`, with the matching rule |
| `nist_method`, `technique`, `technique_detail`, `scope`, `command` | How the drive was erased |
| `planned` | Inventory mode only: the method that would be used |
| `device_status` | What the device itself reported. See below |
| `erase_duration_s` | Time from the erase command to completion |
| `verification` | Marker check: `samples`, `changed`, `zero_after_erase`, `unreadable` |
| `result`, `reason` | `PASS`, `FAIL`, `UNHANDLED`, `SKIPPED` or `PLANNED`, with the reason |
| `started_at`, `finished_at` | UTC |

`device_status` contains:

- **NVMe Sanitize:** the Sanitize Status log fields `sstat`, `sprog`, `global_data_erased`, and `estimated_crypto_erase_s`.
- **NVMe Format:** `format`, one entry per namespace with the NSID, LBA format and CDW10 issued.
- **SATA:** `ata_sanitize_status`; or, for a drive erased by its PERC/MegaRAID controller, `perc_erase`: `controller`, `slot`, `did`, `tool`, the `commands` run (planned in inventory mode), the `outcome` (`completed`, `failed` or `aborted`) and the controller log `event` it was read from, `reattached_as` when the disk came back under another name, and `recovery` when the drive rejected writes until a SANITIZE STATUS EXT was sent to it.
- **All NVMe:** `rescan_error`, set if the namespace rescan after the erase failed.

## Mapping to NIST SP 800-88r2 §4.6

| §4.6 field | Report field |
|---|---|
| Manufacturer, model, serial number | `drives[].model`, `serial`, `firmware`, `host.*` |
| Media type, media source | `interface`, `media_type`, `host` |
| Sanitization method | `nist_method` (`Purge`) |
| Sanitization technique | `technique`, `technique_detail`, `scope`, `command` |
| Tool used, including version | `tool.*` |
| Verification method | `verification.*`, `device_status.*` |
| Name, position, date, location, signature | `job_id` and timestamps. The rest belongs in the operator's ticketing system |
