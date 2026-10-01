# Background

## Terminology

**Sanitization** is the general term in NIST SP 800-88 for making data on media infeasible to recover. NIST groups the methods into three levels: Clear, Purge and Destroy.

**Sanitize**, capitalised, is a specific command family that storage standards define for this purpose:

| Interface | Command | Since |
|---|---|---|
| NVMe | Sanitize command, opcode 84h | NVMe 1.3 |
| ATA | SANITIZE DEVICE feature set | ACS-2 |
| SCSI | SANITIZE command | SBC-3 |

Each family offers up to three methods:

| Method | NVMe SANACT | ATA | Effect |
|---|---|---|---|
| Crypto Erase | 100b | CRYPTO SCRAMBLE EXT | Changes the media encryption key, so existing data can no longer be decrypted. Usually completes in seconds |
| Block Erase | 010b | BLOCK ERASE EXT | Physically erases every block of the media |
| Overwrite | 011b | OVERWRITE EXT | Writes a pattern over the whole media, once or several times |

NVMe also defines SANACT 001b, Exit Failure Mode. It is used after a failed sanitize, and only when that sanitize was started with AUSE (Allow Unrestricted Sanitize Exit) set.

## What makes Sanitize different

- **Scope.** A sanitize applies to all user data in the NVM subsystem:
  - every namespace;
  - capacity not allocated to any namespace;
  - caches.

  Overwriting LBAs from the host reaches none of the over-provisioned or remapped areas.
- **Persistence.** A sanitize resumes after a reset or power loss. While it runs, the device rejects most other commands.
- **Status.**
  - NVMe reports progress (SPROG) and the outcome (SSTAT) in the Sanitize Status log page. SSTAT also carries a Global Data Erased bit: no user data has been written since the last successful sanitize.
  - ATA reports the same information through SANITIZE STATUS EXT.
- **Failure mode.** A failed sanitize leaves the device in a restricted state until a sanitize succeeds or, if AUSE was set, the host issues Exit Failure Mode.

## Format NVM and its Secure Erase Settings

Format NVM (opcode 80h) re-initialises a namespace with a chosen LBA format. Its Secure Erase Settings field, SES (CDW10 bits 11:9), optionally erases user data at the same time:

| SES | Name | Meaning | Precondition |
|---|---|---|---|
| 000b | none | No secure erase is requested. Do not rely on it to remove data | — |
| 001b | User Data Erase | All user data shall be erased. Contents afterwards are indeterminate, e.g. all zeros or all ones. If all user data is encrypted, the controller may implement this as a cryptographic erase | — |
| 010b | Cryptographic Erase | All user data shall be erased cryptographically, by deleting the encryption key | Identify Controller FNA bit 2 |
| 011b–111b | reserved | — | — |

Scope:

- **By default, only the namespace being formatted.** FNA bit 0 (format applies to all namespaces) and bit 1 (secure erase applies to all namespaces) widen this to every namespace.
- **Never capacity outside a namespace.** For this reason cryptoerase refuses the Format path when Identify Controller reports a non-zero UNVMCAP (unallocated NVM capacity).

Format leaves no status log behind, which is why cryptoerase:

- prefers Sanitize Crypto Erase;
- uses Format SES=010b only when `--allow-format` is given. This matters for NVMe 1.2 drives, which predate the Sanitize command.

Other fields of CDW10 are LBA format, metadata setting, protection information type and location. nvme-cli defaults them to zero, which silently reformats a namespace that uses another LBA format or PI. cryptoerase reads them from Identify Namespace and keeps them.

## The ATA SANITIZE feature set

IDENTIFY DEVICE word 59 reports support:

| Bit | Feature |
|---|---|
| 12 | SANITIZE feature set |
| 13 | CRYPTO SCRAMBLE EXT |
| 14 | OVERWRITE EXT |
| 15 | BLOCK ERASE EXT |

A sanitize command returns as soon as the operation starts. The host then polls SANITIZE STATUS EXT, which reports:

- one of three states: SD0 idle, SD1 frozen, SD2 in progress;
- the progress;
- whether the last operation completed without error.

A drive is frozen when firmware, typically the BIOS, has issued SANITIZE FREEZE LOCK. It accepts no sanitize command until the next power cycle.

ATA Security Erase (SECURITY ERASE UNIT) is a different, older mechanism. It is not used here. It requires setting a user password, it is blocked when the BIOS freezes the security feature set, and the enhanced variant writes a vendor-defined pattern.

## Cryptographic erase and NIST SP 800-88r2

NIST SP 800-88 Revision 2 (2025) defers the choice of purge techniques to IEEE 2883. It keeps specific guidance for cryptographic erase (CE). Section 3.2 conditions CE on, among other things:

- **Encryption of all sensitive data.** No sensitive data was ever stored on the media unencrypted. Drives that report MediaEncryption in TCG Level 0 Discovery, or that support NVMe/ATA crypto erase, encrypt all user data with a media key.
- **Key strength.** Keys of at least 128-bit security strength. For federal use, the modules must be validated to FIPS 140.
- **No key escrow or backup.** The organisation must have confidence that the media key was not copied elsewhere.

On verification, §4.5.1 asks for checking:

- the tools' completion status;
- errors and anomalies;
- media health.

It also says elaborate sampling is not necessary after clear or purge. §4.6 lists the fields of a sanitization record. [report.md](report.md) maps them to the report.

## Why fail closed

Drives differ in which erase commands they support, and firmware sometimes implements a command poorly. A tool that quietly falls back to overwrite or zero-fill when crypto erase is missing produces a report that looks like success. cryptoerase therefore reports such a drive as `FAIL` and leaves the decision to the operator: upgrade firmware, accept Format SES=010b, or retire the drive.
