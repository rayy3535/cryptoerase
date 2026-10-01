// SPDX-License-Identifier: Apache-2.0

package nvme

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

func TestPassthruLayout(t *testing.T) {
	if s := unsafe.Sizeof(passthruCmd{}); s != 72 {
		t.Fatalf("sizeof(passthruCmd) = %d, want 72", s)
	}
	var c passthruCmd
	offsets := map[string]uintptr{
		"NSID": unsafe.Offsetof(c.NSID), "Metadata": unsafe.Offsetof(c.Metadata),
		"Addr": unsafe.Offsetof(c.Addr), "DataLen": unsafe.Offsetof(c.DataLen),
		"CDW10": unsafe.Offsetof(c.CDW10), "TimeoutMs": unsafe.Offsetof(c.TimeoutMs),
		"Result": unsafe.Offsetof(c.Result),
	}
	want := map[string]uintptr{"NSID": 4, "Metadata": 16, "Addr": 24, "DataLen": 36, "CDW10": 40, "TimeoutMs": 64, "Result": 68}
	for k, v := range want {
		if offsets[k] != v {
			t.Errorf("offset %s = %d, want %d", k, offsets[k], v)
		}
	}
}

func idCtrlBuf() []byte {
	b := make([]byte, 4096)
	binary.LittleEndian.PutUint16(b[0:], 0x144d)
	copy(b[4:24], "EXAMPLESN00000000001")
	copy(b[24:64], "Dell Ent NVMe v2 AGN MU U.2 6.4TB       ")
	copy(b[64:72], "2.3.0   ")
	binary.LittleEndian.PutUint32(b[80:], 0x00010400) // 1.4.0
	binary.LittleEndian.PutUint16(b[256:], 0x0003)    // security + format
	binary.LittleEndian.PutUint64(b[280:], 6401252745216)
	binary.LittleEndian.PutUint64(b[296:], 0)
	binary.LittleEndian.PutUint32(b[328:], 0x60000003) // crypto + block, NDI/NODMMAS bits
	binary.LittleEndian.PutUint32(b[516:], 32)
	b[524] = 0x04
	return b
}

func TestParseIdentifyController(t *testing.T) {
	c, err := ParseIdentifyController(idCtrlBuf())
	if err != nil {
		t.Fatal(err)
	}
	if c.Serial != "EXAMPLESN00000000001" || c.Model != "Dell Ent NVMe v2 AGN MU U.2 6.4TB" || c.Firmware != "2.3.0" {
		t.Fatalf("strings: %+v", c)
	}
	if !c.SanitizeCryptoErase() || !c.SanitizeBlockErase() || c.SanitizeOverwrite() || !c.SanitizeAny() {
		t.Errorf("sanicap decode: %#x", c.SANICAP)
	}
	if !c.FormatCryptoErase() || c.FormatAppliesToAllNamespaces() || c.SecureEraseAppliesToAllNamespaces() {
		t.Errorf("fna decode: %#x", c.FNA)
	}
	if !c.SecuritySendReceive() || !c.FormatNVMSupported() {
		t.Errorf("oacs decode: %#x", c.OACS)
	}
	if c.TNVMCAP.String() != "6401252745216" || !c.UNVMCAP.IsZero() {
		t.Errorf("capacity: %s %s", c.TNVMCAP, c.UNVMCAP)
	}
	if c.VersionString() != "1.4.0" {
		t.Errorf("version %s", c.VersionString())
	}
}

func TestUint128(t *testing.T) {
	u := Uint128{Lo: 1, Hi: 1}
	if u.String() != "18446744073709551617" {
		t.Fatal(u.String())
	}
	j, _ := u.MarshalJSON()
	if string(j) != "18446744073709551617" {
		t.Fatal(string(j))
	}
}

func TestIdentifyNamespaceAndFormatSpec(t *testing.T) {
	b := make([]byte, 4096)
	binary.LittleEndian.PutUint64(b[0:], 1562824368)
	b[25] = 18   // 19 formats
	b[26] = 0x32 // lbaf low=2, metadata extended, upper bits 01 -> index 0x12
	b[29] = 0x09 // PI type 1, PI first
	for i := range 19 {
		b[128+4*i+2] = 12
	}
	ns, err := ParseIdentifyNamespace(b)
	if err != nil {
		t.Fatal(err)
	}
	if ns.CurrentLBAF() != 0x12 || !ns.MetadataExtended() || ns.PIType() != 1 || !ns.PIFirst() {
		t.Fatalf("decode: lbaf=%#x ms=%v pi=%d pil=%v", ns.CurrentLBAF(), ns.MetadataExtended(), ns.PIType(), ns.PIFirst())
	}
	if ns.BlockSize() != 4096 {
		t.Errorf("block size %d", ns.BlockSize())
	}
	spec := KeepCurrentFormat(ns, SESCryptoErase)
	// LBAF[3:0]=2, MSET bit4, PI=1 bits 7:5, PIL bit8, SES=2 bits 11:9, LBAFU=1 bits 13:12
	want := uint32(0x2 | 1<<4 | 1<<5 | 1<<8 | 2<<9 | 1<<12)
	if spec.CDW10() != want {
		t.Errorf("format cdw10 %#x, want %#x", spec.CDW10(), want)
	}
	plain := FormatSpec{LBAF: 0, SES: SESCryptoErase}
	if plain.CDW10() != 0x400 {
		t.Errorf("plain cdw10 %#x", plain.CDW10())
	}
}

func TestSanitizeAndLogCDW(t *testing.T) {
	if v := SanitizeCDW10(SanitizeCryptoErase, true); v != 0xc {
		t.Errorf("sanitize crypto+ause = %#x", v)
	}
	if v := SanitizeCDW10(SanitizeExitFailureMode, false); v != 0x1 {
		t.Errorf("exit failure = %#x", v)
	}
	c10, c11 := GetLogPageCDW(LogSanitizeStatus, 512)
	if c10 != 0x007f0081 || c11 != 0 {
		t.Errorf("get log cdw10=%#x cdw11=%#x", c10, c11)
	}
	if v := SecurityReceiveCDW10(1, 1); v != 0x01000100 {
		t.Errorf("security receive cdw10 %#x", v)
	}
}

func TestSanitizeLogAndSmart(t *testing.T) {
	b := make([]byte, 512)
	binary.LittleEndian.PutUint16(b[0:], 0xffff)
	binary.LittleEndian.PutUint16(b[2:], 0x0101)
	binary.LittleEndian.PutUint32(b[16:], 30)
	l, err := ParseSanitizeLog(b)
	if err != nil {
		t.Fatal(err)
	}
	if l.Status() != SanitizeSucceeded || !l.GlobalDataErased() || l.ETCE != 30 {
		t.Errorf("log %+v", l)
	}
	s := make([]byte, 512)
	s[0], s[3], s[4], s[5] = 0x08, 100, 10, 1
	sm, _ := ParseSmartLog(s)
	if sm.CriticalWarning != 8 || sm.AvailableSpare != 100 || sm.PercentageUsed != 1 {
		t.Errorf("smart %+v", sm)
	}
}

func TestStatusError(t *testing.T) {
	e := &StatusError{Op: "sanitize", Status: 0x4002}
	if e.SCT() != 0 || e.SC() != 2 || !e.DNR() {
		t.Fatalf("decode %v", e)
	}
	if got := e.Error(); got != "nvme sanitize: status 0x4002 (Invalid Field in Command)" {
		t.Errorf("error string %q", got)
	}
	if StatusName(0x10a) != "Invalid Format" || StatusName(0x01d) != "Sanitize In Progress" {
		t.Errorf("names")
	}
}
