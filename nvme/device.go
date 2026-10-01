// SPDX-License-Identifier: Apache-2.0

package nvme

import "time"

// Device is the set of admin operations the erase workflow needs. *Controller
// implements it on Linux; tests substitute a fake.
type Device interface {
	IdentifyController() (*IdentifyController, error)
	IdentifyNamespace(nsid uint32) (*IdentifyNamespace, error)
	SanitizeLog() (*SanitizeLog, error)
	SmartLog() (*SmartLog, error)
	Sanitize(action SanitizeAction, ause bool) error
	Format(nsid uint32, spec FormatSpec, timeout time.Duration) error
	SecurityReceive(secp uint8, spsp uint16, size int) ([]byte, error)
	Rescan() error
	Close() error
}

// Log identifiers and the broadcast namespace ID.
const (
	LogSmartHealth    = 0x02
	LogSanitizeStatus = 0x81
	NSIDAll           = 0xffffffff
)

// GetLogPageCDW returns CDW10 and CDW11 for a Get Log Page of size bytes.
func GetLogPageCDW(lid uint8, size int) (cdw10, cdw11 uint32) {
	numd := uint32(size/4) - 1
	return uint32(lid) | (numd&0xffff)<<16, numd >> 16
}

// SecurityReceiveCDW10 encodes SECP and SPSP for Security Receive.
func SecurityReceiveCDW10(secp uint8, spsp uint16) uint32 {
	return uint32(secp)<<24 | uint32(spsp)<<8
}
