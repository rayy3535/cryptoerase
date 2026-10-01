// SPDX-License-Identifier: Apache-2.0

package nvme

import (
	"strings"
	"testing"
)

func TestParseShortBuffers(t *testing.T) {
	if _, err := ParseIdentifyController(make([]byte, 4095)); err == nil {
		t.Error("identify controller")
	}
	if _, err := ParseIdentifyNamespace(make([]byte, 100)); err == nil {
		t.Error("identify namespace")
	}
	if _, err := ParseSanitizeLog(make([]byte, 19)); err == nil {
		t.Error("sanitize log")
	}
	if _, err := ParseSmartLog(make([]byte, 5)); err == nil {
		t.Error("smart log")
	}
}

func TestVersionString(t *testing.T) {
	for v, want := range map[uint32]string{0: "unreported", 0x00010300: "1.3.0", 0x00020001: "2.0.1"} {
		if got := (&IdentifyController{Version: v}).VersionString(); got != want {
			t.Errorf("%#x: %q", v, got)
		}
	}
}

func TestBlockSizeOutOfTable(t *testing.T) {
	ns := &IdentifyNamespace{FLBAS: 3, LBAF: []LBAFormat{{DataSizeShift: 9}}}
	if ns.BlockSize() != 0 {
		t.Error("index beyond table")
	}
	ns = &IdentifyNamespace{LBAF: []LBAFormat{{DataSizeShift: 0}}}
	if ns.BlockSize() != 0 {
		t.Error("shift 0")
	}
	ns = &IdentifyNamespace{FLBAS: 0x21, LBAF: make([]LBAFormat, 18)} // index 0x11
	ns.LBAF[17].DataSizeShift = 12
	if ns.CurrentLBAF() != 17 || ns.BlockSize() != 4096 {
		t.Errorf("lbaf %d bs %d", ns.CurrentLBAF(), ns.BlockSize())
	}
}

func TestEnumStrings(t *testing.T) {
	for s, want := range map[SES]string{SESNone: "000b", SESUserDataErase: "User Data Erase", SESCryptoErase: "Cryptographic Erase", 5: "101b (reserved)"} {
		if !strings.Contains(s.String(), want) {
			t.Errorf("SES %d: %q", s, s.String())
		}
	}
	for a, want := range map[SanitizeAction]string{
		SanitizeExitFailureMode: "Exit Failure Mode", SanitizeBlockErase: "Block Erase",
		SanitizeOverwrite: "Overwrite", SanitizeCryptoErase: "Crypto Erase", 7: "111b (reserved)",
	} {
		if !strings.Contains(a.String(), want) {
			t.Errorf("SANACT %d: %q", a, a.String())
		}
	}
}

func TestStatusNames(t *testing.T) {
	for st, want := range map[uint32]string{
		0x0000: "Successful Completion", 0x0001: "Invalid Command Opcode", 0x0002: "Invalid Field in Command",
		0x001c: "Sanitize Failed", 0x401d: "Sanitize In Progress", 0x010a: "Invalid Format", 0x0281: "SCT 2 SC 0x81",
	} {
		if got := StatusName(st); got != want {
			t.Errorf("%#x: %q", st, got)
		}
	}
}
