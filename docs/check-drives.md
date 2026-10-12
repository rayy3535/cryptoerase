# Check whether drives support cryptographic erase

English | [简体中文](check-drives.zh-CN.md)

This guide is for drive makers and used-drive suppliers who want to check, on their own machines, whether a batch of drives can be cryptographically erased with cryptoerase.

- **Read-only.** `--inventory` reads each drive's identity and erase capabilities. It writes nothing, erases nothing and changes no data.
- **Time.** About 15 minutes once the USB stick is ready.
- **Output.** A table on screen, plus a JSON report you can send to the buyer.

## What you need

| Item | Notes |
|---|---|
| An x86 PC or server | An ordinary desktop is fine |
| The drives, attached directly to the motherboard | SATA drives on a motherboard SATA port; NVMe drives in an M.2 slot or on a U.2 adapter |
| A USB stick, 8 GB or more | For an Ubuntu live system |
| Internet access | To download the tool; without it, see [Appendix A](#appendix-a-no-internet-on-the-test-machine) |

> [!IMPORTANT]
> **Do not use USB enclosures or USB-to-SATA cables.** The tool cannot read a drive's erase capabilities over USB; such drives are reported `SKIPPED`.
>
> SAS drives must sit behind a Dell PERC or Broadcom MegaRAID controller; see [Appendix B](#appendix-b-drives-behind-a-raid-controller-including-sas). SAS drives on a plain SAS HBA are not supported.

Making the USB stick: download the Ubuntu 24.04 LTS Desktop ISO from [ubuntu.com/download/desktop](https://ubuntu.com/download/desktop) and write it to the stick with [Rufus](https://rufus.ie) or [balenaEtcher](https://etcher.balena.io).

## Step 1: Boot Ubuntu from the USB stick

1. Power off, attach the drives, and plug in the USB stick.
2. At power-on, press the boot menu key (often F11, F12, F8 or Esc) and pick the USB stick.
3. Choose **Try Ubuntu** (do not install). Do not open the drives under test in the file manager.
4. Press `Ctrl` + `Alt` + `T` to open a terminal, type this line and press Enter:

```
sudo -i
```

The prompt changes to `root@ubuntu:~#`. Type every following command in this terminal.

## Step 2: Make sure hdparm is there

SATA drives need hdparm (skip this if you test NVMe drives only). Copy this line:

```
hdparm -V || (apt update && apt install -y hdparm && hdparm -V)
```

It should end by printing `hdparm v9.xx`.

## Step 3: Download cryptoerase

Copy these lines one by one:

```
cd ~
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/cryptoerase-linux-amd64
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
install -m 755 cryptoerase-linux-amd64 cryptoerase
```

The fourth line must print `cryptoerase-linux-amd64: OK`, which means the file is intact. If it prints `FAILED`, delete the files and download again.

## Step 4: Check

```
./cryptoerase --inventory
```

This usually takes less than a minute.

## Step 5: Read the result

The output ends with a table and a summary line, for example:

```
DEVICE      MODEL                       SERIAL    TECHNIQUE                                  RESULT     REASON
/dev/nvme0  Samsung SSD 990 EVO Plus 2TB  XXXXXXXX  NVMe Sanitize, SANACT=100b (Crypto Erase)  PLANNED
/dev/sda    SAMSUNG MZ7LH960HAJR-00005  XXXXXXXX  ATA SANITIZE CRYPTO SCRAMBLE EXT           PLANNED
/dev/sdb    ST2000DM006-2DM164          XXXXXXXX  -                                          UNHANDLED  hard disk without a cryptographic erase ...
/dev/sdc    USB DISK 3.0                -         -                                          SKIPPED    removable or USB device ...
result=INCOMPLETE exit=2 report=cryptoerase-XXXXXXXX-20261011T120000Z.json
```

Each row is one drive. Look at the **RESULT** column:

| RESULT | Meaning | Verdict |
|---|---|---|
| `PLANNED` | The drive supports cryptographic erase; TECHNIQUE shows the method | **Supported** |
| `FAIL` | Not supported, or cannot be erased right now | See REASON and the table below |
| `UNHANDLED` | This machine cannot handle the drive | See REASON and the table below |
| `SKIPPED` | USB stick, USB device, empty optical drive | Not a drive under test; ignore |

The `result=` on the last line sums up the machine: `PASS` when every drive other than `SKIPPED` ones is `PLANNED`.

### Common reasons

| REASON contains | Meaning | What to do |
|---|---|---|
| `no ATA SANITIZE CRYPTO SCRAMBLE support` | The SATA SSD has no cryptographic erase | **Not supported** |
| `hard disk without a cryptographic erase` | A hard disk that does not encrypt | **Not supported** |
| `no cryptographic erase support (sanicap=…, fna=…)` | The NVMe drive has no cryptographic erase | **Not supported** |
| The same, followed by `Format NVM SES=010b is supported, rerun with --allow-format` | The NVMe drive supports cryptographic erase through Format NVM only, not Sanitize | Run `./cryptoerase --inventory --allow-format`; if it shows `PLANNED`, record "Format only" |
| `sanitize is frozen` | The drive supports it, but the motherboard firmware froze it at boot | Try another PC or motherboard, or attach the SATA drive after Ubuntu has started (the port must support hot-plug) |
| `firmware … is below …; upgrade firmware` | This firmware has a known erase defect | Update the firmware, then check again |
| `removable or USB device` (`SKIPPED`) | The drive is attached over USB | Attach it to a motherboard SATA port or M.2 slot |
| `in use by the running OS` | The system disk, or a drive opened in the file manager (mounted) | Ignore it for the system disk; for a drive under test, reboot, do not open it, and check again |
| `not a directly attached ATA device` | A SAS drive, or a controller the tool does not know | SAS drives: see [Appendix B](#appendix-b-drives-behind-a-raid-controller-including-sas) |
| `controller passes ATA commands through but does not return the drive's status` | The drive is on a RAID or HBA card that does not pass the drive's status back | Attach it to a motherboard SATA port |
| `rerun with --raid-reset`, `PERC virtual disk` | The drive is behind a RAID controller | See [Appendix B](#appendix-b-drives-behind-a-raid-controller-including-sas) |
| A drive under test is not in the table | The system does not see the drive | Check the cables and power; check that the BIOS sees the drive |
| The program stops with `required tool missing` | hdparm or the RAID controller tool is missing | Go back to step 2; for RAID controllers see Appendix B |

## Step 6: Keep the result

The Ubuntu live system runs in memory: **files are lost at power-off**. The report's file name is shown after `report=` on the last line. Either:

- take a photo or screenshot of the table from step 5, or
- copy the JSON report to a second USB stick. Plug it in, wait for its icon on the desktop, then look up its name:

```
ls /media/ubuntu/
```

  The name shown is the stick, for example `MYUSB`. Then (replace `MYUSB` with the name you see):

```
cp ~/cryptoerase-*.json /media/ubuntu/MYUSB/
sync
```

For each drive the report holds the model, serial number, firmware, capacity, supported erase methods, the verdict and the reason.

## Optional: erase a sample

`--inventory` shows what the drive claims to support. The strongest proof is to really erase one or two sample drives. The tool then checks two things: the drive reports the erase complete, and 16 random markers written before the erase have all changed.

> [!WARNING]
> **This wipes every drive attached to the machine**, except the USB boot stick and a system disk in use. Attach only the sample drives and unplug every other drive.

```
./cryptoerase --yes
```

`PASS` means the drive was erased and the erase verified. Each drive takes from a few seconds to a few minutes.

## Appendix A: No internet on the test machine

On another Windows PC with internet access, open PowerShell:

```
curl.exe -LO https://github.com/rayy3535/cryptoerase/releases/latest/download/cryptoerase-linux-amd64
curl.exe -LO https://github.com/rayy3535/cryptoerase/releases/latest/download/SHA256SUMS
Get-FileHash cryptoerase-linux-amd64 -Algorithm SHA256
type SHA256SUMS
```

Check that the hash from `Get-FileHash` matches the `cryptoerase-linux-amd64` line in `SHA256SUMS` (case does not matter), and copy `cryptoerase-linux-amd64` to a USB stick. On the test machine, plug that stick into the running Ubuntu, then:

```
cp /media/ubuntu/*/cryptoerase-linux-amd64 ~
cd ~
install -m 755 cryptoerase-linux-amd64 cryptoerase
```

Installing hdparm also needs internet access. Without it, boot from a [SystemRescue](https://www.system-rescue.org) stick instead of Ubuntu; it includes hdparm.

## Appendix B: Drives behind a RAID controller (including SAS)

For Dell PERC controllers, and MegaRAID controllers sold under Broadcom, LSI, Inspur, Supermicro and other brands.

1. Install the controller tool: `perccli64` for Dell PERC (Dell support site, "PERC CLI"), `storcli64` for other MegaRAID controllers (Broadcom support site, "StorCLI"). They install under `/opt/MegaRAID/`, where the tool finds them.
2. The simplest way: in the controller BIOS or iDRAC, set the drives under test to **Non-RAID / JBOD**, then run:

```
./cryptoerase --inventory
```

   A TECHNIQUE of `drive cryptographic erase by the PERC/MegaRAID controller` means the controller can cryptographically erase the drive: **supported**.

3. If the drives are still in a RAID volume or in state Ready, run:

```
./cryptoerase --inventory --raid-reset
```

   This is still read-only: it lists which RAID volumes it would delete and which drives it would set to Non-RAID, and does neither. In this state the verdict for each drive only comes with a real erase. You can also ask the controller directly (the same works with `perccli64`):

```
storcli64 /c0/eall/sall show all | grep -E "^Drive /c|Cryptographic Erase Capable"
```

   `Cryptographic Erase Capable = Yes` means the controller can cryptographically erase that drive.

> [!WARNING]
> `--yes --raid-reset` **deletes RAID volumes** and erases their drives. Run it only on machines that may be wiped.
