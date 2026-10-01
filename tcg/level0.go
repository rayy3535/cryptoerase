// SPDX-License-Identifier: Apache-2.0

// Package tcg decodes TCG Storage Level 0 Discovery data (Security Protocol
// 01h, ComID 0001h), which reports which Security Subsystem Classes a drive
// implements and the state of its Locking feature, including whether the
// drive encrypts user data (MediaEncryption). Reading it is non-destructive.
package tcg

import (
	"encoding/binary"
	"fmt"
)

// Feature codes from the TCG Storage Architecture Core Spec and SSC specs.
const (
	featureLocking    = 0x0002
	featureEnterprise = 0x0100
	featureOpal1      = 0x0200
	featureOpal2      = 0x0203
	featureOpalite    = 0x0301
	featurePyrite1    = 0x0302
	featurePyrite2    = 0x0303
	featureRuby       = 0x0304
)

// Level0 is the decoded discovery response.
type Level0 struct {
	SSC               []string `json:"ssc,omitempty"`
	HasLockingFeature bool     `json:"has_locking_feature"`
	LockingSupported  bool     `json:"locking_supported"`
	LockingEnabled    bool     `json:"locking_enabled"`
	Locked            bool     `json:"locked"`
	MediaEncryption   bool     `json:"media_encryption"`
	MBREnabled        bool     `json:"mbr_enabled"`
	MBRDone           bool     `json:"mbr_done"`
}

// ParseLevel0 decodes a Level 0 Discovery response: a 48-byte header whose
// first four bytes (big-endian) give the length of the parameter data that
// follows them, then feature descriptors (code, version, length, data).
func ParseLevel0(b []byte) (*Level0, error) {
	if len(b) < 48 {
		return nil, fmt.Errorf("level 0 discovery: %d bytes", len(b))
	}
	total := int(binary.BigEndian.Uint32(b[0:4])) + 4
	if total < 48 {
		return nil, fmt.Errorf("level 0 discovery: header length %d", total)
	}
	if total > len(b) {
		total = len(b)
	}
	d := &Level0{}
	for off := 48; off+4 <= total; {
		code := binary.BigEndian.Uint16(b[off : off+2])
		n := int(b[off+3]) //nolint:gosec // G602: off+4 <= total <= len(b)
		data := b[off+4 : min(off+4+n, total)]
		switch code {
		case featureLocking:
			if len(data) > 0 {
				f := data[0]
				d.HasLockingFeature = true
				d.LockingSupported = f&0x01 != 0
				d.LockingEnabled = f&0x02 != 0
				d.Locked = f&0x04 != 0
				d.MediaEncryption = f&0x08 != 0
				d.MBREnabled = f&0x10 != 0
				d.MBRDone = f&0x20 != 0
			}
		case featureEnterprise:
			d.SSC = append(d.SSC, "Enterprise")
		case featureOpal1:
			d.SSC = append(d.SSC, "Opal 1.0")
		case featureOpal2:
			d.SSC = append(d.SSC, "Opal 2")
		case featureOpalite:
			d.SSC = append(d.SSC, "Opalite")
		case featurePyrite1:
			d.SSC = append(d.SSC, "Pyrite 1")
		case featurePyrite2:
			d.SSC = append(d.SSC, "Pyrite 2")
		case featureRuby:
			d.SSC = append(d.SSC, "Ruby")
		}
		off += 4 + n
	}
	return d, nil
}
