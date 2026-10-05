# Tested hardware

Combinations on which a full erase passed on real servers: every drive erased, the device or controller confirmed it, and every marker changed.

| Controller | Drives | Starting state | Erase path | Passed with |
|---|---|---|---|---|
| None (PCIe, native NVMe) | Dell Ent NVMe v2 AGN MU U.2 6.4TB ×2 | — | NVMe Sanitize, Crypto Erase | 0.1.0-rc.2 |
| None (PCIe, native NVMe) | Samsung SSD 990 EVO Plus 2TB ×2 (client drive) | — | NVMe Sanitize, Crypto Erase | 0.4.1 |
| None (directly attached SATA; host controller not recorded) | Micron 5300 MTFDDAK960TDS, SATA SSD 960 GB ×1 | — | ATA SANITIZE CRYPTO SCRAMBLE (hdparm) | 0.4.1 |
| None (directly attached SATA; same server) | Samsung MZ7LH960HAJR-00005, SATA SSD 960 GB ×1 | — | ATA SANITIZE CRYPTO SCRAMBLE (hdparm) | 0.4.1 |
| smartpqi (Microchip SmartHBA / SmartRAID, model not recorded), Inspur server | Samsung MZ7LH960HAJR-00005, SATA SSD 960 GB ×6 | Passed through to the OS | ATA SANITIZE CRYPTO SCRAMBLE (hdparm), after the tool set D_SENSE | 0.6.0 |
| PERC H355 Front (FW 52.30.0-6347) | Samsung MZ7LH960HAJR-00005, SATA SSD 960 GB ×2 | Non-RAID | Controller crypto erase | 0.1.0-rc.3 |
| PERC H730P Mini (FW 4.300.00-8366) | Samsung MZ7LH480HBHQ0D3, SATA SSD 480 GB ×10 | Non-RAID | Controller crypto erase | 0.4.1 |
| PERC H730P Mini | Intel SSDSC2KB960G8, SATA SSD 960 GB ×6 | One virtual disk over all six, `--raid-reset` | Controller crypto erase | 0.4.1 |
| LSI SAS3108, Broadcom firmware (Inspur server; firmware not recorded), storcli64 007.2612 | Micron 5300 MTFDDAK960TDT, SATA SSD 960 GB ×4 | Ready (UGood), controller JBOD mode off, `--raid-reset` | Controller crypto erase | 0.7.0 |
| PERC (model not recorded) | Seagate ST1200MM0099, SAS HDD 1.2 TB ×2 | Non-RAID, after `--raid-reset` from RAID1 | Controller crypto erase | 0.4.1 |

The PERC H355 machine was a PowerEdge R7525, with the NVMe drives and the H355 in the same server. Per drive, the controller erase took about 1–3 s on SSDs and 6–9 s on the SAS hard disks, mostly for the markers. On the server with the 990 EVO Plus drives, the BMC's virtual media (`Virtual HDisk0` and others) was reported `SKIPPED`, as intended. On the smartpqi server, every drive failed with 0.5.0 (no ATA registers returned). With 0.6.0 the tool set D_SENSE on five of them; the sixth had been set by hand with `sdparm` beforehand. Each erase took 1–10 s. On the SAS3108 server, perccli64 saw no controller, so storcli64 was used; the reset turned JBOD mode on before exposing the drives. The controller reported the drives `Cryptographic Erase Capable = Yes` with `Sanitize Support = Not supported`, and its crypto erase passed in about 2.5 s per drive.

## Controller behaviour seen

| Controller | What happened | How the tool handles it |
|---|---|---|
| PERC H355 | ATA pass-through returns no ATA registers (fixed-format sense, all fields zero; hdparm: `bad/missing sense data`). The result of SANITIZE cannot be read. | The controller erases the drive |
| PERC H730P | SANITIZE through ATA pass-through is rejected (I/O error), but the drive completes it. The controller then reports the drive NOT READY and fails reads and writes. Its own crypto erase of such a drive fails (`Error f0`) and marks it Unconfigured Bad. A power cycle clears it. | The controller erases the drive; hdparm SANITIZE is not sent when the controller can erase. A drive that rejects writes is reported `FAIL` with recovery steps. |
| PERC (H355, H730P) | `show erase` reads `Not in progress` before and after a crypto erase, which takes no time. | The result is read from the event log |
| megaraid_sas | Virtual disks on SCSI channel ≥ 2. Pass-through drives on channel 0/1, with target = controller device ID. | Used to tell drives from virtual disks and to match drives to slots |
| LSI SAS3108 (Inspur, not Dell-branded) | perccli64 reports `Controller Count = 0`. | The tool uses the installed CLI that sees a controller (storcli64) |
| LSI SAS3108 (Inspur, not Dell-branded) | JBOD mode off (`show jbod`: `OFF`; `Support JBOD = Yes`, `Enable JBOD = No`). `set jbod` on a drive fails with `command invalid` (ErrCd 2). | `--raid-reset` turns JBOD mode on first |
| smartpqi (Microchip SmartHBA / SmartRAID, Inspur) | SATA drives are passed through, but ATA pass-through answers in fixed-format sense data (`70 00 01 00 50 40 …`), which hdparm does not read (`bad/missing sense data`). With D_SENSE set (`sdparm --set=D_SENSE=1`) hdparm reads the status. | The tool sets D_SENSE and asks again |

## Adding to this list

Run `cryptoerase --inventory`, then an erase, on spare hardware. Open an issue or pull request with:
- the controller model and firmware (`perccli64 /c0 show | grep -iE "Product Name|FW Version"`);
- the drive models;
- the starting state (RAID, non-RAID, Ready);
- the version;
- the result.

Leave out serial numbers and service tags.
