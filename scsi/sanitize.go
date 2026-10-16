// SPDX-License-Identifier: Apache-2.0

package scsi

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	opRequestSense       = 0x03
	opSanitize           = 0x48
	opMaintenanceIn      = 0xa3
	saReportSupportedOps = 0x0c

	// SanitizeCryptoErase is the SANITIZE service action CRYPTOGRAPHIC
	// ERASE (SBC-4).
	SanitizeCryptoErase = 0x03

	senseBufLen = 252 // REQUEST SENSE allocation length, the maximum

	senseNoSense        = 0x0
	senseRecoveredError = 0x1
	senseIllegalRequest = 0x5
)

// Support says whether a device supports a command, from REPORT SUPPORTED
// OPERATION CODES.
type Support int

// Support values.
const (
	SupportUnknown Support = iota // the device does not say
	SupportNo
	SupportYes
)

func (s Support) String() string {
	switch s {
	case SupportNo:
		return "not supported"
	case SupportYes:
		return "supported"
	}
	return "unknown"
}

// CommandSupported asks the device whether it supports opcode with service
// action sa (REPORT SUPPORTED OPERATION CODES, reporting option 010b). A
// device that does not support the report itself gives SupportUnknown
// without an error.
func CommandSupported(exec Exec, opcode byte, sa uint16) (Support, error) {
	buf := make([]byte, modeBufLen)
	cdb := []byte{opMaintenanceIn, saReportSupportedOps, 0x02, opcode, 0, 0, 0, 0, 0, modeBufLen, 0, 0}
	binary.BigEndian.PutUint16(cdb[4:6], sa)
	if err := exec(cdb, DirFromDevice, buf); err != nil {
		if cc, ok := errors.AsType[*CheckCondition](err); ok {
			// Invalid command operation code or invalid field in CDB: the
			// report itself is not supported.
			if key, asc, _ := SenseKey(cc.Sense); key == senseIllegalRequest && (asc == 0x20 || asc == 0x24) {
				return SupportUnknown, nil
			}
		}
		return SupportUnknown, fmt.Errorf("REPORT SUPPORTED OPERATION CODES: %w", err)
	}
	switch buf[1] & 0x07 {
	case 0x03, 0x05: // supported as in the standard, or in a vendor-specific way
		return SupportYes, nil
	case 0x01:
		return SupportNo, nil
	}
	return SupportUnknown, nil // 000b: not currently available
}

// CryptoEraseSupported asks whether the device supports SANITIZE,
// CRYPTOGRAPHIC ERASE.
func CryptoEraseSupported(exec Exec) (Support, error) {
	return CommandSupported(exec, opSanitize, SanitizeCryptoErase)
}

// StartSanitize sends SANITIZE with service action sa and IMMED set: the
// command returns at once and SanitizeProgress follows the operation. AUSE
// is clear: if the sanitize fails, the device stays in sanitize failure
// mode until a sanitize completes.
func StartSanitize(exec Exec, sa byte) error {
	cdb := []byte{opSanitize, 0x80 | sa&0x1f, 0, 0, 0, 0, 0, 0, 0, 0}
	if err := exec(cdb, DirNone, nil); err != nil {
		return fmt.Errorf("SANITIZE: %w", err)
	}
	return nil
}

// Sanitize states, from the sense data of REQUEST SENSE.
const (
	SanitizeIdle       = "idle"        // no sanitize in progress, no failure
	SanitizeInProgress = "in progress" // NOT READY, SANITIZE IN PROGRESS (04/1B)
	SanitizeFailed     = "failed"      // SANITIZE COMMAND FAILED (31/03)
	SanitizeOther      = "other"       // other sense data, e.g. a unit attention
)

// SanitizeStatus is what REQUEST SENSE says about a sanitize.
type SanitizeStatus struct {
	State string `json:"state"`
	// Progress is the progress indication while in progress, 0 to 65535.
	Progress int `json:"progress,omitempty"`
	// Sense is "KEY/ASC/ASCQ" in hex.
	Sense string `json:"sense"`
}

// InProgress reports whether a sanitize is running.
func (s *SanitizeStatus) InProgress() bool { return s.State == SanitizeInProgress }

// SanitizeProgress reads the device's sense data (REQUEST SENSE, fixed
// format) and decodes the sanitize state.
func SanitizeProgress(exec Exec) (*SanitizeStatus, error) {
	buf := make([]byte, senseBufLen)
	cdb := []byte{opRequestSense, 0, 0, 0, senseBufLen, 0}
	if err := exec(cdb, DirFromDevice, buf); err != nil {
		return nil, fmt.Errorf("REQUEST SENSE: %w", err)
	}
	return ParseSanitizeStatus(buf), nil
}

// ParseSanitizeStatus decodes sense data, fixed or descriptor format.
func ParseSanitizeStatus(s []byte) *SanitizeStatus {
	key, asc, ascq := SenseKey(s)
	st := &SanitizeStatus{Sense: fmt.Sprintf("%x/%02x/%02x", key, asc, ascq)}
	switch {
	case asc == 0x04 && ascq == 0x1b:
		st.State = SanitizeInProgress
		st.Progress, _ = progressIndication(s)
	case asc == 0x31 && ascq == 0x03:
		st.State = SanitizeFailed
	case (key == senseNoSense || key == senseRecoveredError) && len(s) > 0 && (s[0]&0x7e == 0x70 || s[0]&0x7e == 0x72):
		// A valid response code with nothing to report.
		st.State = SanitizeIdle
	default:
		st.State = SanitizeOther
	}
	return st
}

// progressIndication returns the sense-key-specific progress indication
// (SKSV set), from fixed format bytes 15-17 or a descriptor of type 02h.
func progressIndication(s []byte) (int, bool) {
	if len(s) == 0 {
		return 0, false
	}
	switch s[0] & 0x7f {
	case 0x70, 0x71:
		if len(s) >= 18 && s[15]&0x80 != 0 {
			return int(binary.BigEndian.Uint16(s[16:18])), true
		}
	case 0x72, 0x73:
		if len(s) < 8 {
			return 0, false
		}
		end := min(8+int(s[7]), len(s))
		for d := 8; d+2 <= end; d += 2 + int(s[d+1]) {
			if s[d] == 0x02 && d+7 <= end && s[d+4]&0x80 != 0 {
				return int(binary.BigEndian.Uint16(s[d+5 : d+7])), true
			}
		}
	}
	return 0, false
}
