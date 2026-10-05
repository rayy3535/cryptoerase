// SPDX-License-Identifier: Apache-2.0

// Package scsi sends the few SCSI commands the erase workflow needs that no
// installed tool sends for it.
//
// A SCSI-to-ATA translation layer returns the registers of an ATA
// PASS-THROUGH command in the sense data. In fixed format (response code
// 70h) the upper bytes of COUNT and LBA do not fit, so tools that need them,
// hdparm among them, accept only descriptor format (72h). Which format a
// logical unit uses is the D_SENSE bit of its Control mode page (0Ah). Some
// host bus adapters (seen: smartpqi) default to fixed format; setting
// D_SENSE makes them return descriptor format.
package scsi

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Data transfer directions for Exec.
const (
	DirNone = iota
	DirFromDevice
	DirToDevice
)

// Exec sends one CDB, transferring data in direction dir (DirNone,
// DirFromDevice, DirToDevice). A command that ends with CHECK CONDITION
// returns a *CheckCondition carrying the sense data.
type Exec func(cdb []byte, dir int, data []byte) error

// CheckCondition is a command that ended with CHECK CONDITION.
type CheckCondition struct {
	Sense []byte
}

func (e *CheckCondition) Error() string {
	key, asc, ascq := SenseKey(e.Sense)
	return fmt.Sprintf("check condition: sense key %#x, ASC/ASCQ %02x/%02x", key, asc, ascq)
}

// SenseKey decodes the sense key and additional sense code of fixed or
// descriptor format sense data.
func SenseKey(s []byte) (key, asc, ascq byte) {
	if len(s) < 1 {
		return 0, 0, 0
	}
	switch s[0] & 0x7f {
	case 0x70, 0x71:
		if len(s) >= 14 {
			return s[2] & 0x0f, s[12], s[13]
		}
	case 0x72, 0x73:
		if len(s) >= 4 {
			return s[1] & 0x0f, s[2], s[3]
		}
	}
	return 0, 0, 0
}

const (
	opModeSense10  = 0x5a
	opModeSelect10 = 0x55
	pageControl    = 0x0a
	dSenseBit      = 0x04 // byte 2 of the Control mode page
	modeBufLen     = 64   // MODE SENSE allocation length; the page fits in it
)

// SetDescriptorSense sets D_SENSE in the current Control mode page through
// exec, unless it is set already. It reports whether it changed the page.
// The change is not saved: it lasts until the device is reset.
func SetDescriptorSense(exec Exec) (bool, error) {
	buf := make([]byte, modeBufLen)
	// MODE SENSE(10): DBD (no block descriptors), current values, page 0Ah.
	cdb := []byte{opModeSense10, 0x08, pageControl, 0, 0, 0, 0, 0, modeBufLen, 0}
	if err := exec(cdb, DirFromDevice, buf); err != nil {
		return false, fmt.Errorf("MODE SENSE(10), Control page: %w", err)
	}
	page, err := controlPage(buf)
	if err != nil {
		return false, err
	}
	if page[2]&dSenseBit != 0 {
		return false, nil
	}
	param := make([]byte, 8+len(page))
	copy(param[8:], page)
	param[8] &= 0x3f // PS and SPF must be zero in MODE SELECT
	param[8+2] |= dSenseBit
	// MODE SELECT(10): PF=1, SP=0 (do not save).
	cdb = []byte{opModeSelect10, 0x10, 0, 0, 0, 0, 0, 0, byte(len(param)), 0} //nolint:gosec // G115: at most modeBufLen
	if err := exec(cdb, DirToDevice, param); err != nil {
		return false, fmt.Errorf("MODE SELECT(10), D_SENSE=1: %w", err)
	}
	return true, nil
}

// controlPage returns the Control mode page from MODE SENSE(10) data.
func controlPage(b []byte) ([]byte, error) {
	if len(b) < 8 {
		return nil, errors.New("MODE SENSE(10): short header")
	}
	total := int(binary.BigEndian.Uint16(b[0:2])) + 2
	off := 8 + int(binary.BigEndian.Uint16(b[6:8]))
	if total > len(b) {
		total = len(b)
	}
	if off+2 > total {
		return nil, errors.New("MODE SENSE(10): no mode page in reply")
	}
	if b[off]&0x7f != pageControl { // SPF set would be a subpage
		return nil, fmt.Errorf("MODE SENSE(10): got page %#x, want the Control page", b[off]&0x7f)
	}
	n := 2 + int(b[off+1])
	if n < 3 || off+n > total {
		return nil, fmt.Errorf("MODE SENSE(10): Control page truncated (%d bytes)", n)
	}
	return b[off : off+n], nil
}
