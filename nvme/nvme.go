// SPDX-License-Identifier: Apache-2.0

// Package nvme issues NVMe admin commands through the Linux kernel's
// passthrough interface (NVME_IOCTL_ADMIN_CMD on the controller character
// device, e.g. /dev/nvme0) and decodes the data structures needed for
// cryptographic erase: Identify Controller, Identify Namespace, the SMART /
// Health and Sanitize Status log pages, Sanitize, Format NVM and Security
// Receive.
//
// Field offsets follow the NVM Express Base Specification. Parsing lives in
// this file and has no build constraints, so it can be unit-tested anywhere;
// the ioctl transport is in ioctl_linux.go.
package nvme

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// Uint128 holds a 128-bit little-endian capacity field (TNVMCAP, UNVMCAP).
type Uint128 struct{ Lo, Hi uint64 }

// IsZero reports whether the value is zero.
func (u Uint128) IsZero() bool { return u.Lo == 0 && u.Hi == 0 }

// Big returns the value as a big.Int.
func (u Uint128) Big() *big.Int {
	v := new(big.Int).SetUint64(u.Hi)
	v.Lsh(v, 64)
	return v.Or(v, new(big.Int).SetUint64(u.Lo))
}

// String returns the decimal representation.
func (u Uint128) String() string { return u.Big().String() }

// MarshalJSON encodes the value as a JSON number.
func (u Uint128) MarshalJSON() ([]byte, error) { return []byte(u.String()), nil }

func le128(b []byte) Uint128 {
	return Uint128{Lo: binary.LittleEndian.Uint64(b[0:8]), Hi: binary.LittleEndian.Uint64(b[8:16])}
}

func ascii(b []byte) string {
	return strings.TrimSpace(strings.TrimRight(string(b), "\x00"))
}

// IdentifyController is the subset of the Identify Controller data structure
// (CNS 01h) used by this module.
type IdentifyController struct {
	VID      uint16  `json:"vid"`
	SSVID    uint16  `json:"ssvid"`
	Serial   string  `json:"serial"`
	Model    string  `json:"model"`
	Firmware string  `json:"firmware"`
	Version  uint32  `json:"version"`
	OACS     uint16  `json:"oacs"`
	TNVMCAP  Uint128 `json:"tnvmcap"`
	UNVMCAP  Uint128 `json:"unvmcap"`
	SANICAP  uint32  `json:"sanicap"`
	NN       uint32  `json:"nn"`
	FNA      uint8   `json:"fna"`
}

// ParseIdentifyController decodes a 4096-byte Identify Controller buffer.
func ParseIdentifyController(b []byte) (*IdentifyController, error) {
	if len(b) < 4096 {
		return nil, fmt.Errorf("identify controller: %d bytes, want 4096", len(b))
	}
	return &IdentifyController{
		VID:      binary.LittleEndian.Uint16(b[0:2]),
		SSVID:    binary.LittleEndian.Uint16(b[2:4]),
		Serial:   ascii(b[4:24]),
		Model:    ascii(b[24:64]),
		Firmware: ascii(b[64:72]),
		Version:  binary.LittleEndian.Uint32(b[80:84]),
		OACS:     binary.LittleEndian.Uint16(b[256:258]),
		TNVMCAP:  le128(b[280:296]),
		UNVMCAP:  le128(b[296:312]),
		SANICAP:  binary.LittleEndian.Uint32(b[328:332]),
		NN:       binary.LittleEndian.Uint32(b[516:520]),
		FNA:      b[524],
	}, nil
}

// VersionString renders VER as "major.minor.tertiary"; "unreported" if 0
// (NVMe 1.0 controllers may leave it zero).
func (c *IdentifyController) VersionString() string {
	if c.Version == 0 {
		return "unreported"
	}
	return fmt.Sprintf("%d.%d.%d", c.Version>>16, (c.Version>>8)&0xff, c.Version&0xff)
}

// SecuritySendReceive reports OACS bit 0.
func (c *IdentifyController) SecuritySendReceive() bool { return c.OACS&0x1 != 0 }

// FormatNVMSupported reports OACS bit 1.
func (c *IdentifyController) FormatNVMSupported() bool { return c.OACS&0x2 != 0 }

// SanitizeCryptoErase reports SANICAP bit 0.
func (c *IdentifyController) SanitizeCryptoErase() bool { return c.SANICAP&0x1 != 0 }

// SanitizeBlockErase reports SANICAP bit 1.
func (c *IdentifyController) SanitizeBlockErase() bool { return c.SANICAP&0x2 != 0 }

// SanitizeOverwrite reports SANICAP bit 2.
func (c *IdentifyController) SanitizeOverwrite() bool { return c.SANICAP&0x4 != 0 }

// SanitizeAny reports whether any sanitize operation is supported, i.e.
// whether the Sanitize Status log page exists.
func (c *IdentifyController) SanitizeAny() bool { return c.SANICAP&0x7 != 0 }

// FormatAppliesToAllNamespaces reports FNA bit 0.
func (c *IdentifyController) FormatAppliesToAllNamespaces() bool { return c.FNA&0x1 != 0 }

// SecureEraseAppliesToAllNamespaces reports FNA bit 1.
func (c *IdentifyController) SecureEraseAppliesToAllNamespaces() bool { return c.FNA&0x2 != 0 }

// FormatCryptoErase reports FNA bit 2: Format NVM supports SES=010b.
func (c *IdentifyController) FormatCryptoErase() bool { return c.FNA&0x4 != 0 }

// LBAFormat is one entry of the LBA Format table.
type LBAFormat struct {
	MetadataSize  uint16 `json:"ms"`
	DataSizeShift uint8  `json:"lbads"`
	RelPerf       uint8  `json:"rp"`
}

// IdentifyNamespace is the subset of the Identify Namespace data structure
// (CNS 00h) used by this module.
type IdentifyNamespace struct {
	NSZE  uint64      `json:"nsze"`
	NCAP  uint64      `json:"ncap"`
	NUSE  uint64      `json:"nuse"`
	NLBAF uint8       `json:"nlbaf"`
	FLBAS uint8       `json:"flbas"`
	DPS   uint8       `json:"dps"`
	LBAF  []LBAFormat `json:"lbaf"`
}

// ParseIdentifyNamespace decodes a 4096-byte Identify Namespace buffer.
func ParseIdentifyNamespace(b []byte) (*IdentifyNamespace, error) {
	if len(b) < 4096 {
		return nil, fmt.Errorf("identify namespace: %d bytes, want 4096", len(b))
	}
	ns := &IdentifyNamespace{
		NSZE:  binary.LittleEndian.Uint64(b[0:8]),
		NCAP:  binary.LittleEndian.Uint64(b[8:16]),
		NUSE:  binary.LittleEndian.Uint64(b[16:24]),
		NLBAF: b[25],
		FLBAS: b[26],
		DPS:   b[29],
	}
	n := min(int(ns.NLBAF)+1, 64)
	for i := 0; i < n; i++ {
		o := 128 + 4*i
		ns.LBAF = append(ns.LBAF, LBAFormat{
			MetadataSize:  binary.LittleEndian.Uint16(b[o : o+2]),
			DataSizeShift: b[o+2],
			RelPerf:       b[o+3] & 0x3,
		})
	}
	return ns, nil
}

// CurrentLBAF returns the index of the LBA format in use: FLBAS bits 3:0,
// with bits 6:5 as the upper two bits when more than 16 formats exist.
func (n *IdentifyNamespace) CurrentLBAF() uint8 {
	return (n.FLBAS & 0xf) | ((n.FLBAS>>5)&0x3)<<4
}

// MetadataExtended reports FLBAS bit 4 (metadata at the end of each LBA).
func (n *IdentifyNamespace) MetadataExtended() bool { return n.FLBAS&0x10 != 0 }

// PIType returns DPS bits 2:0 (0 = protection information disabled).
func (n *IdentifyNamespace) PIType() uint8 { return n.DPS & 0x7 }

// PIFirst reports DPS bit 3 (protection information in the first bytes of
// metadata).
func (n *IdentifyNamespace) PIFirst() bool { return n.DPS&0x8 != 0 }

// BlockSize returns the logical block size of the current format, or 0 if
// the table does not cover it.
func (n *IdentifyNamespace) BlockSize() uint32 {
	i := int(n.CurrentLBAF())
	if i >= len(n.LBAF) || n.LBAF[i].DataSizeShift < 9 || n.LBAF[i].DataSizeShift > 31 {
		return 0
	}
	return 1 << n.LBAF[i].DataSizeShift
}

// SES is the Secure Erase Settings field of Format NVM (CDW10 bits 11:9).
type SES uint8

const (
	SESNone          SES = 0 // no secure erase requested
	SESUserDataErase SES = 1 // all user data erased; contents afterwards indeterminate
	SESCryptoErase   SES = 2 // user data erased by deleting the encryption key
)

func (s SES) String() string {
	switch s {
	case SESNone:
		return "000b (no secure erase)"
	case SESUserDataErase:
		return "001b (User Data Erase)"
	case SESCryptoErase:
		return "010b (Cryptographic Erase)"
	}
	return fmt.Sprintf("%03bb (reserved)", uint8(s)&7)
}

// FormatSpec is the full CDW10 of a Format NVM command.
type FormatSpec struct {
	LBAF uint8 // 6-bit LBA format index
	MSET bool  // metadata settings: extended LBA
	PI   uint8 // protection information type
	PIL  bool  // protection information location: first bytes of metadata
	SES  SES
}

// CDW10 encodes the spec.
func (f FormatSpec) CDW10() uint32 {
	v := uint32(f.LBAF&0xf) | uint32(f.PI&0x7)<<5 | uint32(f.SES&0x7)<<9 | uint32((f.LBAF>>4)&0x3)<<12
	if f.MSET {
		v |= 1 << 4
	}
	if f.PIL {
		v |= 1 << 8
	}
	return v
}

// KeepCurrentFormat returns a FormatSpec that leaves the namespace's LBA
// format, metadata setting and protection information exactly as they are,
// changing only the secure erase setting. (nvme-cli defaults these fields to
// 0, which silently reformats a namespace that uses another LBA format or PI.)
func KeepCurrentFormat(ns *IdentifyNamespace, ses SES) FormatSpec {
	return FormatSpec{
		LBAF: ns.CurrentLBAF(),
		MSET: ns.MetadataExtended(),
		PI:   ns.PIType(),
		PIL:  ns.PIFirst(),
		SES:  ses,
	}
}

// SanitizeAction is the SANACT field of the Sanitize command.
type SanitizeAction uint8

const (
	SanitizeExitFailureMode SanitizeAction = 1
	SanitizeBlockErase      SanitizeAction = 2
	SanitizeOverwrite       SanitizeAction = 3
	SanitizeCryptoErase     SanitizeAction = 4
)

func (a SanitizeAction) String() string {
	switch a {
	case SanitizeExitFailureMode:
		return "001b (Exit Failure Mode)"
	case SanitizeBlockErase:
		return "010b (Block Erase)"
	case SanitizeOverwrite:
		return "011b (Overwrite)"
	case SanitizeCryptoErase:
		return "100b (Crypto Erase)"
	}
	return fmt.Sprintf("%03bb (reserved)", uint8(a)&7)
}

// SanitizeCDW10 encodes SANACT and AUSE (Allow Unrestricted Sanitize Exit).
func SanitizeCDW10(a SanitizeAction, ause bool) uint32 {
	v := uint32(a) & 0x7
	if ause {
		v |= 1 << 3
	}
	return v
}

// Sanitize operation status (SSTAT bits 2:0).
const (
	SanitizeNeverSanitized     = 0
	SanitizeSucceeded          = 1
	SanitizeInProgress         = 2
	SanitizeFailed             = 3
	SanitizeSucceededNoDealloc = 4
)

// SanitizeLog is the Sanitize Status log page (Log Identifier 81h).
type SanitizeLog struct {
	SPROG  uint16 `json:"sprog"`
	SSTAT  uint16 `json:"sstat"`
	SCDW10 uint32 `json:"scdw10"`
	ETO    uint32 `json:"eto"`
	ETBE   uint32 `json:"etbe"`
	ETCE   uint32 `json:"etce"`
}

// ParseSanitizeLog decodes the first 20 bytes of the log page.
func ParseSanitizeLog(b []byte) (*SanitizeLog, error) {
	if len(b) < 20 {
		return nil, fmt.Errorf("sanitize log: %d bytes, want >= 20", len(b))
	}
	return &SanitizeLog{
		SPROG:  binary.LittleEndian.Uint16(b[0:2]),
		SSTAT:  binary.LittleEndian.Uint16(b[2:4]),
		SCDW10: binary.LittleEndian.Uint32(b[4:8]),
		ETO:    binary.LittleEndian.Uint32(b[8:12]),
		ETBE:   binary.LittleEndian.Uint32(b[12:16]),
		ETCE:   binary.LittleEndian.Uint32(b[16:20]),
	}, nil
}

// Status returns SSTAT bits 2:0.
func (l *SanitizeLog) Status() uint8 { return uint8(l.SSTAT & 0x7) }

// GlobalDataErased reports SSTAT bit 8: no user data has been written since
// the last successful sanitize.
func (l *SanitizeLog) GlobalDataErased() bool { return l.SSTAT&0x100 != 0 }

// SmartLog is the subset of the SMART / Health Information log page (02h)
// recorded as health evidence.
type SmartLog struct {
	CriticalWarning         uint8 `json:"critical_warning"`
	AvailableSpare          uint8 `json:"available_spare"`
	AvailableSpareThreshold uint8 `json:"available_spare_threshold"`
	PercentageUsed          uint8 `json:"percentage_used"`
}

// ParseSmartLog decodes the first bytes of the SMART / Health log page.
func ParseSmartLog(b []byte) (*SmartLog, error) {
	if len(b) < 6 {
		return nil, fmt.Errorf("smart log: %d bytes", len(b))
	}
	return &SmartLog{
		CriticalWarning:         b[0],
		AvailableSpare:          b[3],
		AvailableSpareThreshold: b[4],
		PercentageUsed:          b[5],
	}, nil
}

// StatusError is a non-zero NVMe completion status returned by the kernel
// for a passthrough command: bits 7:0 Status Code, 10:8 Status Code Type,
// 13 More, 14 Do Not Retry.
type StatusError struct {
	Op     string
	Status uint32
}

// SCT returns the Status Code Type.
func (e *StatusError) SCT() uint8 { return uint8((e.Status >> 8) & 0x7) }

// SC returns the Status Code.
func (e *StatusError) SC() uint8 { return uint8(e.Status & 0xff) }

// DNR reports the Do Not Retry bit.
func (e *StatusError) DNR() bool { return e.Status&0x4000 != 0 }

func (e *StatusError) Error() string {
	return fmt.Sprintf("nvme %s: status 0x%04x (%s)", e.Op, e.Status, StatusName(e.Status))
}

// StatusName names the status codes this module can run into; others are
// rendered as SCT/SC.
func StatusName(status uint32) string {
	sct, sc := (status>>8)&0x7, status&0xff
	switch {
	case sct == 0 && sc == 0x00:
		return "Successful Completion"
	case sct == 0 && sc == 0x01:
		return "Invalid Command Opcode"
	case sct == 0 && sc == 0x02:
		return "Invalid Field in Command"
	case sct == 0 && sc == 0x1c:
		return "Sanitize Failed"
	case sct == 0 && sc == 0x1d:
		return "Sanitize In Progress"
	case sct == 1 && sc == 0x0a:
		return "Invalid Format"
	}
	return fmt.Sprintf("SCT %d SC 0x%02x", sct, sc)
}
