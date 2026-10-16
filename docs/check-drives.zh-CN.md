# 检查硬盘是否支持加密擦除

[English](check-drives.md) | 简体中文

本指南面向硬盘厂商和二手盘供应商：在自己的机器上，用 cryptoerase 查一批盘能不能被加密擦除（cryptographic erase）。

- **只读检查**：`--inventory` 只读取盘的信息和擦除能力，不写盘、不擦除、不改数据。
- **用时**：准备好 U 盘之后，大约 15 分钟。
- **结果**：屏幕上一张表，加一个 JSON 报告文件，可以直接发给买方。

## 准备

| 需要 | 说明 |
|---|---|
| 一台 x86 电脑或服务器 | 普通台式机即可 |
| 待测盘直接接在主板上 | SATA 盘接主板 SATA 口；NVMe 盘接 M.2 口，或 U.2 转接卡 |
| 8 GB 以上 U 盘一个 | 做 Ubuntu 启动盘 |
| 网络 | 用来下载工具；不能上网见[附录 A](#附录-a不能上网的机器) |

> [!IMPORTANT]
> **不要用 USB 硬盘盒、USB 转接线。** USB 接的盘读不到擦除能力，工具会把它标成 `SKIPPED` 并跳过。
>
> SAS 盘要接在 SAS HBA 卡（如 Broadcom 9300/9400/9500 系列、Dell HBA330/HBA355i）上，或接在 Dell PERC / Broadcom MegaRAID 阵列卡上（见[附录 B](#附录-b阵列卡后面的盘含-sas-盘)）。

做启动盘：从 [ubuntu.com/download/desktop](https://ubuntu.com/download/desktop) 下载 Ubuntu 24.04 LTS 桌面版 ISO，用 [Rufus](https://rufus.ie) 或 [balenaEtcher](https://etcher.balena.io) 写入 U 盘。

## 第 1 步：从 U 盘启动 Ubuntu

1. 关机，接好待测盘，插上 U 盘。
2. 开机时按启动菜单键（常见是 F11、F12、F8 或 Esc），选择 U 盘启动。
3. 选 **Try Ubuntu**（试用，不安装），进入桌面。不要在文件管理器里点开待测盘。
4. 按 `Ctrl` + `Alt` + `T` 打开终端，输入下面这行并回车：

```
sudo -i
```

提示符变成 `root@ubuntu:~#` 就对了。以下命令都在这个终端里输入。

## 第 2 步：准备 hdparm

检查 SATA 盘需要 hdparm（只测 NVMe 盘可以跳过）。复制这一行：

```
hdparm -V || (apt update && apt install -y hdparm && hdparm -V)
```

最后显示 `hdparm v9.xx` 即可。

## 第 3 步：下载 cryptoerase

逐行复制执行：

```
cd ~
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/cryptoerase-linux-amd64
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing
install -m 755 cryptoerase-linux-amd64 cryptoerase
```

第 4 行应显示 `cryptoerase-linux-amd64: OK`，说明文件完整。如果显示 `FAILED`，删掉文件重新下载。

## 第 4 步：检查

```
./cryptoerase --inventory
```

通常一分钟内完成。

## 第 5 步：看结果

输出的最后是一张表和一行汇总，例如：

```
DEVICE      MODEL                       SERIAL    TECHNIQUE                                  RESULT     REASON
/dev/nvme0  Samsung SSD 990 EVO Plus 2TB  XXXXXXXX  NVMe Sanitize, SANACT=100b (Crypto Erase)  PLANNED
/dev/sda    SAMSUNG MZ7LH960HAJR-00005  XXXXXXXX  ATA SANITIZE CRYPTO SCRAMBLE EXT           PLANNED
/dev/sdb    ST2000DM006-2DM164          XXXXXXXX  -                                          UNHANDLED  hard disk without a cryptographic erase ...
/dev/sdc    USB DISK 3.0                -         -                                          SKIPPED    removable or USB device ...
result=INCOMPLETE exit=2 report=cryptoerase-XXXXXXXX-20261011T120000Z.json
```

每一行是一块盘，看 **RESULT** 列：

| RESULT | 含义 | 结论 |
|---|---|---|
| `PLANNED` | 盘支持加密擦除；TECHNIQUE 列是将要使用的方法 | **支持** |
| `FAIL` | 不支持，或暂时擦不了 | 看 REASON，对照下表 |
| `UNHANDLED` | 在这台机器上处理不了这块盘 | 看 REASON，对照下表 |
| `SKIPPED` | U 盘、USB 设备、空光驱 | 不是待测盘，忽略 |

最后一行的 `result=` 是整机汇总：除 `SKIPPED` 外所有盘都是 `PLANNED` 时为 `PASS`。

### 常见 REASON

| REASON 里出现 | 意思 | 怎么办 |
|---|---|---|
| `no ATA SANITIZE CRYPTO SCRAMBLE support` | SATA 固态盘不支持加密擦除 | **不支持** |
| `hard disk without a cryptographic erase` | 普通机械盘，盘内不加密 | **不支持** |
| `no cryptographic erase support (sanicap=…, fna=…)` | NVMe 盘不支持加密擦除 | **不支持** |
| 同上，后面带 `Format NVM SES=010b is supported, rerun with --allow-format` | NVMe 盘只支持 Format 方式的加密擦除，不支持 Sanitize | 运行 `./cryptoerase --inventory --allow-format`，变成 `PLANNED` 记为"仅支持 Format 方式" |
| `sanitize is frozen` | 盘支持，但开机时被主板 BIOS 冻结了 | 换一台电脑或主板再测；或先进入 Ubuntu，再把 SATA 盘接上（主板需支持热插拔） |
| `firmware … is below …; upgrade firmware` | 固件版本有已知擦除缺陷 | 升级固件后再测 |
| `removable or USB device`（`SKIPPED`） | 盘是 USB 接的 | 改接主板 SATA 口或 M.2 口 |
| `in use by the running OS` | 这是当前系统所在的盘，或者在文件管理器里点开过这块盘（被挂载了） | 系统盘忽略即可；待测盘的话，重启后不要点开它，再测一次 |
| `no SCSI SANITIZE CRYPTOGRAPHIC ERASE support` | SAS 固态盘不支持加密擦除 | **不支持** |
| `not a directly attached ATA or SAS drive` | 本工具不处理的 RAID 卡上的逻辑卷，或 SAN 存储的卷 | 把盘接到 SAS HBA、主板接口或 PERC/MegaRAID 阵列卡上 |
| `controller passes ATA commands through but does not return the drive's status` | 盘接在阵列卡或 HBA 卡上，卡不回传盘的状态 | 改接主板 SATA 口 |
| `does not support non-RAID (JBOD) drives` | 阵列卡太老（如 Dell PERC H710P），不能把盘交给系统，也不能加密擦除 | 把盘换到新一些的阵列卡（如 PERC H730P）或主板接口上再测 |
| `rerun with --raid-reset`、`PERC virtual disk` | 盘在阵列卡后面 | 见[附录 B](#附录-b阵列卡后面的盘含-sas-盘) |
| 表里没有待测盘 | 系统没识别到这块盘 | 检查接线和供电；在 BIOS 里确认能看到这块盘 |
| 程序直接退出：`required tool missing` | 缺 hdparm 或阵列卡工具 | 回到第 2 步；阵列卡见附录 B |

## 第 6 步：保存结果

从 U 盘启动的 Ubuntu 是在内存里运行的，**关机后文件会丢失**。报告文件名就是最后一行 `report=` 后面的名字。两种保存方法：

- 直接拍照或截图第 5 步的表；
- 或者把 JSON 报告拷到第二个 U 盘。插上第二个 U 盘，等桌面出现它的图标，然后查看它的名字：

```
ls /media/ubuntu/
```

  显示的名字就是这个 U 盘，例如 `MYUSB`。再执行（把 `MYUSB` 换成你看到的名字）：

```
cp ~/cryptoerase-*.json /media/ubuntu/MYUSB/
sync
```

报告里每块盘都有：型号、序列号、固件版本、容量、支持的擦除方式，以及结论和原因。

## 可选：抽样实擦

`--inventory` 证明的是"盘声称支持"。最有力的证明是挑一两块样盘真擦一次，工具会用两种方法核对：盘报告擦除完成，并且擦除前写入的 16 个随机标记全部变化。

> [!WARNING]
> **这会清空这台机器上所有接入的盘**（启动 U 盘和正在使用的系统盘除外）。只接要实擦的样盘，其他盘全部拔掉。

```
./cryptoerase --yes
```

结果为 `PASS` 表示擦除完成并验证通过。每块盘通常几秒到几分钟。

## 附录 A：不能上网的机器

在另一台能上网的 Windows 电脑上，打开 PowerShell：

```
curl.exe -LO https://github.com/rayy3535/cryptoerase/releases/latest/download/cryptoerase-linux-amd64
curl.exe -LO https://github.com/rayy3535/cryptoerase/releases/latest/download/SHA256SUMS
Get-FileHash cryptoerase-linux-amd64 -Algorithm SHA256
type SHA256SUMS
```

确认 `Get-FileHash` 显示的值和 `SHA256SUMS` 里 `cryptoerase-linux-amd64` 那一行一致（不分大小写），然后把 `cryptoerase-linux-amd64` 拷到 U 盘。在测试机的 Ubuntu 里插上这个 U 盘，然后：

```
cp /media/ubuntu/*/cryptoerase-linux-amd64 ~
cd ~
install -m 755 cryptoerase-linux-amd64 cryptoerase
```

hdparm 也需要网络才能安装。不能上网时，可以改用 [SystemRescue](https://www.system-rescue.org) 启动盘（自带 hdparm），代替 Ubuntu。

## 附录 B：阵列卡后面的盘（含 SAS 盘）

适用于 Dell PERC，以及 Broadcom、LSI、浪潮、超微等品牌的 MegaRAID 阵列卡。

1. 装阵列卡工具：Dell PERC 用 `perccli64`（Dell 官网 "PERC CLI"）；其他 MegaRAID 卡用 `storcli64`（Broadcom 官网 "StorCLI"）。安装后在 `/opt/MegaRAID/` 下，工具会自动找到。
2. 最直观的做法：先在阵列卡 BIOS 或 iDRAC 里把待测盘设为 **Non-RAID / JBOD**，再运行：

```
./cryptoerase --inventory
```

   TECHNIQUE 列显示 `drive cryptographic erase by the PERC/MegaRAID controller`，表示阵列卡可以对这块盘做加密擦除，即**支持**。

3. 盘还在 RAID 里或处于 Ready 状态时，运行：

```
./cryptoerase --inventory --raid-reset
```

   这仍然只读：工具只会列出它将删除哪些 RAID 卷、把哪些盘设为 Non-RAID，不会执行。这种状态下每块盘的结论要等真正擦除时才知道。也可以直接问阵列卡（`perccli64` 同理）：

```
storcli64 /c0/eall/sall show all | grep -E "^Drive /c|Cryptographic Erase Capable"
```

   `Cryptographic Erase Capable = Yes` 表示阵列卡能加密擦除这块盘。

> [!WARNING]
> `--yes --raid-reset` 会**删除 RAID 卷**并擦除其中的盘，只能在可以清空的机器上运行。
