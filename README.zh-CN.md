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

<p align="center"><a href="README.md">English</a> | 简体中文</p>

对 Linux 服务器上的每一块盘做加密擦除（cryptographic erase），并生成 JSON 证据报告。支持 NVMe、SATA，以及 Dell PERC 或 Broadcom MegaRAID 阵列卡后面的 SATA/SAS 盘。提供 Go 库和命令行工具。

硬盘厂商和供应商：[检查硬盘是否支持加密擦除](docs/check-drives.zh-CN.md)，分步骤、只读。

> [!WARNING]
> **被擦除的盘上所有数据都会被销毁。** 请先做 inventory（只读检查），并对用到的每种盘型号和阵列卡，在备用机器上先试擦一次。已验证通过的硬件见 [docs/tested-hardware.md](docs/tested-hardware.md)。

## 库

```sh
go get github.com/rayy3535/cryptoerase@latest
```

程序需要以 root 运行；服务器上有 SATA 盘时需要 `hdparm`，有 PERC/MegaRAID 阵列卡时需要 `perccli64` 或 `storcli64`，见[依赖](#依赖)。

先做带 RAID 重置的计划（`--inventory --raid-reset`），再擦除（`--yes --raid-reset`）：

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

完整程序见 [examples/library/main.go](examples/library/main.go)。

| 命令行 | `Options` |
|---|---|
| `--inventory` | `Mode: cryptoerase.ModeInventory`（默认） |
| `--yes` | `Mode: cryptoerase.ModeErase, Confirm: true` |
| `--raid-reset` | `RAIDReset: true` |
| `--exclude DEV`、`--job-id ID`、`--parallel N` | `Exclude`、`JobID`、`Parallel` |

`Run` 只在无法开始运行时（参数错误、缺少必需的工具）返回错误；每块盘的结果都在 `rep.Drives` 里。后端和子包见 [docs/library.md](docs/library.md)。

## 命令行

```sh
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/cryptoerase-linux-amd64
wget https://github.com/rayy3535/cryptoerase/releases/latest/download/SHA256SUMS
sha256sum -c SHA256SUMS --ignore-missing && install -m 755 cryptoerase-linux-amd64 cryptoerase

./cryptoerase --inventory --raid-reset                 # 只做计划，不改任何东西
./cryptoerase --yes --raid-reset --job-id RECLAIM-1234 # 擦除
```

只有盘在 PERC/MegaRAID 阵列卡后面、处于 RAID 或 Ready 状态时才需要 `--raid-reset`；不加的话这些盘会报 `UNHANDLED`。

| 参数 | 含义 |
|---|---|
| `--inventory` | 只查找盘并做计划，不改任何东西 |
| `--yes` | 擦除。任何改动都必须加这个参数 |
| `--raid-reset` | PERC/MegaRAID：删除当前系统没有使用的 RAID 卷，把卷里的盘和 Ready 状态的盘设为 Non-RAID（必要时先打开阵列卡的 JBOD 模式），然后逐块擦除 |
| `--exclude DEV` | 永远不碰 `DEV`（`sda`、`/dev/sda`、`nvme0`），可重复 |
| `--report FILE` | 报告路径，默认 `./cryptoerase-<序列号>-<UTC 时间>.json` |
| `--job-id ID` | 写入报告 |
| `--allow-format` | 不支持 Sanitize Crypto Erase 的 NVMe 盘：接受用带加密擦除的 Format NVM（[为什么需要显式开启](docs/background.md)） |
| `--parallel N` | 同时最多处理 N 块盘，默认 0 表示全部 |
| `--hdparm PATH`、`--raid-cli PATH` | 工具路径（不在 `$PATH` 里时） |
| `--samples N`、`--fw-policy FILE`、`--poll-interval`、`--no-progress-timeout`、`--format-timeout`、`--log-format text\|json`、`--version` | 调优和输出 |

| 退出码 | 含义 |
|---|---|
| 0 | 所有盘都已擦除并验证（inventory：每块盘都有擦除方法） |
| 1 | 有盘 `FAIL`、没找到盘、参数错误，或缺少必需的工具 |
| 2 | 没有失败，但有盘是 `UNHANDLED` |

```sh
./cryptoerase --yes --raid-reset --job-id "$JOB" --report "/tmp/erase-$JOB.json"
case $? in
  0) release_server ;;
  2) review_unhandled_drives ;;
  *) quarantine_server ;;
esac
```

可选的来源校验：`gh attestation verify cryptoerase-linux-amd64 --repo rayy3535/cryptoerase`（[SLSA](https://slsa.dev/spec/v1.0/provenance)）。从源码安装：`go install github.com/rayy3535/cryptoerase/cmd/cryptoerase@latest`。

## 依赖

工具本身是静态二进制文件。在服务器上需要 root，并根据盘的类型，最多需要两个外部程序：

| 程序 | 什么时候需要 | 获取 | 查找方式 |
|---|---|---|---|
| `hdparm` 9.56+ | 有 SATA 盘，或使用 `--raid-reset` | `apt install hdparm` / `dnf install hdparm` | `$PATH`，或 `--hdparm PATH` |
| `perccli64` | Dell PERC 阵列卡 | Dell 官网 "PERC CLI" | `$PATH`、`/opt/MegaRAID/perccli`，或 `--raid-cli PATH` |
| `storcli64` | Broadcom、LSI 或 OEM 品牌的 MegaRAID 阵列卡（perccli 看不到这些卡） | Broadcom 官网 "StorCLI" | `$PATH`、`/opt/MegaRAID/storcli`，或 `--raid-cli PATH` |

- NVMe 盘两个程序都不需要，工具直接通过内核访问。AHCI 或 smartpqi 等 HBA 上的 SATA 盘只需要 hdparm。
- `--raid-reset` 需要 hdparm 和阵列卡工具，因为它要暴露的盘事先在系统里看不到。
- 缺少需要的程序时，运行会在改动任何东西之前停止：退出码 1，不生成报告，并提示缺哪个程序、哪块盘需要它。
- 不需要：nvme-cli、sg3_utils、sdparm、smartmontools、sedutil-cli、MegaCli、arcconf。
- Linux，amd64 或 arm64，需挂载 `/sys` 和 `/proc`。

工具执行的每条命令和用到的内核接口见 [docs/dependencies.md](docs/dependencies.md)（英文）。

## 擦除方式

| 盘 | 擦除方式 |
|---|---|
| NVMe | Sanitize，Crypto Erase（带加密擦除的 Format NVM 需加 `--allow-format`） |
| SATA 固态盘或机械盘，直连或接在 HBA 上（AHCI、smartpqi 等） | ATA SANITIZE CRYPTO SCRAMBLE，前提是盘支持 |
| PERC / MegaRAID 阵列卡后面的 SATA 或 SAS 盘（固态盘或机械盘） | 阵列卡自带的加密擦除，前提是阵列卡报告该盘支持 |

只做加密擦除：不会退回到覆写、块擦除或填零。每块盘只有在盘或阵列卡报告成功，**并且**擦除前写入的 16 个随机标记在擦除后全部改变时才算通过。擦不了的盘报 `UNHANDLED` 或 `FAIL` 并写明原因，不会被悄悄跳过。

不支持：不加密的机械盘、普通 HBA 上的 SAS 盘、PERC/MegaRAID 以外的 RAID 卡、已锁定的盘（TCG Opal 锁定区域、设置了 ATA 用户密码）、没有 namespace 的 NVMe 盘。

## 文档

- [检查硬盘是否支持加密擦除](docs/check-drives.zh-CN.md)：给硬盘厂商和供应商的分步指南
- 以下文档为英文：
  - [Dependencies](docs/dependencies.md)：hdparm、perccli/storcli、执行的命令、内核接口
  - [How it works](docs/how-it-works.md)：擦除方式、验证、RAID 重置、阵列卡擦除、固件下限
  - [Report format](docs/report.md)：报告格式，附[示例](examples/)
  - [Tested hardware](docs/tested-hardware.md)：已验证的硬件和阵列卡的特殊行为
  - [Library](docs/library.md)：后端和子包
  - [Background](docs/background.md)：Sanitize 与 Format、ATA SANITIZE、NIST SP 800-88r2
  - [Development and releasing](docs/development.md)、[Changelog](CHANGELOG.md)

## 许可证

Apache License 2.0，见 [LICENSE](LICENSE) 和 [NOTICE](NOTICE)。
