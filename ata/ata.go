// SPDX-License-Identifier: Apache-2.0

// Package ata covers the SATA side of cryptographic erase: decoding ATA
// IDENTIFY DEVICE data and running the ACS SANITIZE feature set.
//
// The transport is the Backend interface. The provided implementation,
// Hdparm, runs hdparm(8), which handles SCSI/ATA Translation and sense-data
// formats across kernels and HBAs. A native SG_IO backend can be added
// behind the same interface without touching callers.
package ata

import (
	"context"
	"encoding/binary"
	"fmt"
)

// Backend is the set of ATA operations the erase workflow needs.
type Backend interface {
	// Identify returns the 512-byte IDENTIFY DEVICE data.
	Identify(ctx context.Context, dev string) (*Identify, error)
	// SanitizeStatus issues SANITIZE STATUS EXT.
	SanitizeStatus(ctx context.Context, dev string) (*SanitizeStatus, error)
	// SanitizeCryptoScramble issues SANITIZE CRYPTO SCRAMBLE EXT. The command
	// returns once the operation has started.
	SanitizeCryptoScramble(ctx context.Context, dev string) error
	// Version describes the backend for the report.
	Version(ctx context.Context) string
}

// Identify is decoded IDENTIFY DEVICE data.
type Identify struct {
	Words [256]uint16 `json:"-"`
}

// ParseIdentify decodes 512 bytes of little-endian IDENTIFY words.
func ParseIdentify(b []byte) (*Identify, error) {
	if len(b) < 512 {
		return nil, fmt.Errorf("identify device: %d bytes, want 512", len(b))
	}
	id := &Identify{}
	for i := range id.Words {
		id.Words[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return id, nil
}

// FromWords builds Identify from host-order words.
func FromWords(w []uint16) (*Identify, error) {
	if len(w) < 256 {
		return nil, fmt.Errorf("identify device: %d words, want 256", len(w))
	}
	id := &Identify{}
	copy(id.Words[:], w)
	return id, nil
}

// str decodes an ATA string: two characters per word, high byte first.
func (id *Identify) str(first, n int) string {
	b := make([]byte, 0, 2*n)
	for i := first; i < first+n; i++ {
		b = append(b, byte(id.Words[i]>>8), byte(id.Words[i]))
	}
	s := string(b)
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == 0) {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == 0) {
		end--
	}
	return s[start:end]
}

// Serial returns words 10-19.
func (id *Identify) Serial() string { return id.str(10, 10) }

// Firmware returns words 23-26.
func (id *Identify) Firmware() string { return id.str(23, 4) }

// Model returns words 27-46.
func (id *Identify) Model() string { return id.str(27, 20) }

// Sectors returns the LBA48 capacity (words 100-103), falling back to the
// 28-bit capacity (words 60-61).
func (id *Identify) Sectors() uint64 {
	if id.Words[83]&0x0400 != 0 {
		return uint64(id.Words[100]) | uint64(id.Words[101])<<16 | uint64(id.Words[102])<<32 | uint64(id.Words[103])<<48
	}
	return uint64(id.Words[60]) | uint64(id.Words[61])<<16
}

// NonRotating reports word 217 == 0001h (solid state).
func (id *Identify) NonRotating() bool { return id.Words[217] == 1 }

// Word 59: sanitize feature set.

// SanitizeSupported reports word 59 bit 12.
func (id *Identify) SanitizeSupported() bool { return id.Words[59]&0x1000 != 0 }

// CryptoScrambleSupported reports word 59 bit 13.
func (id *Identify) CryptoScrambleSupported() bool { return id.Words[59]&0x2000 != 0 }

// OverwriteSupported reports word 59 bit 14.
func (id *Identify) OverwriteSupported() bool { return id.Words[59]&0x4000 != 0 }

// BlockEraseSupported reports word 59 bit 15.
func (id *Identify) BlockEraseSupported() bool { return id.Words[59]&0x8000 != 0 }

// Security is word 128 (Security Mode feature set state).
type Security struct {
	Supported     bool `json:"supported"`
	Enabled       bool `json:"enabled"`
	Locked        bool `json:"locked"`
	Frozen        bool `json:"frozen"`
	CountExpired  bool `json:"count_expired"`
	EnhancedErase bool `json:"enhanced_erase"`
}

// Security decodes word 128.
func (id *Identify) Security() Security {
	w := id.Words[128]
	return Security{
		Supported:     w&0x01 != 0,
		Enabled:       w&0x02 != 0,
		Locked:        w&0x04 != 0,
		Frozen:        w&0x08 != 0,
		CountExpired:  w&0x10 != 0,
		EnhancedErase: w&0x20 != 0,
	}
}

// SanitizeStatus is the decoded result of SANITIZE STATUS EXT.
type SanitizeStatus struct {
	State                 string `json:"state"` // SD0 idle, SD1 frozen, SD2 in progress
	InProgress            bool   `json:"in_progress"`
	Frozen                bool   `json:"frozen"`
	Progress              uint16 `json:"progress"`
	CompletedWithoutError bool   `json:"completed_without_error"`
	Antifreeze            bool   `json:"antifreeze"`
	Raw                   string `json:"raw,omitempty"`
}
