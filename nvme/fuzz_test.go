// SPDX-License-Identifier: Apache-2.0

package nvme

import "testing"

func FuzzParsers(f *testing.F) {
	f.Add(idCtrlBuf())
	f.Add(make([]byte, 4096))
	f.Add([]byte{0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		if c, err := ParseIdentifyController(b); err == nil {
			_ = c.VersionString()
			_ = c.TNVMCAP.String()
			if len(c.Model) > 40 || len(c.Serial) > 20 || len(c.Firmware) > 8 {
				t.Fatal("string longer than its field")
			}
		}
		if ns, err := ParseIdentifyNamespace(b); err == nil {
			if len(ns.LBAF) == 0 || len(ns.LBAF) > 64 {
				t.Fatalf("%d LBA formats", len(ns.LBAF))
			}
			if bs := ns.BlockSize(); bs != 0 && (bs < 512 || bs&(bs-1) != 0) {
				t.Fatalf("block size %d", bs)
			}
			spec := KeepCurrentFormat(ns, SESCryptoErase)
			if spec.LBAF != ns.CurrentLBAF() {
				t.Fatal("format index changed")
			}
		}
		if l, err := ParseSanitizeLog(b); err == nil && l.Status() > 7 {
			t.Fatal("status out of range")
		}
		_, _ = ParseSmartLog(b)
	})
}

// FuzzFormatSpecCDW10 decodes CDW10 back into its fields.
func FuzzFormatSpecCDW10(f *testing.F) {
	f.Add(uint8(0x12), true, uint8(1), true, uint8(2))
	f.Fuzz(func(t *testing.T, lbaf uint8, mset bool, pi uint8, pil bool, ses uint8) {
		s := FormatSpec{LBAF: lbaf & 0x3f, MSET: mset, PI: pi & 7, PIL: pil, SES: SES(ses & 7)}
		v := s.CDW10()
		if v>>14 != 0 {
			t.Fatalf("reserved bits set: %#x", v)
		}
		got := FormatSpec{
			LBAF: uint8(v&0xf) | uint8((v>>12)&3)<<4,
			MSET: v&(1<<4) != 0,
			PI:   uint8(v>>5) & 7,
			PIL:  v&(1<<8) != 0,
			SES:  SES(v>>9) & 7,
		}
		if got != s {
			t.Fatalf("%+v -> %#x -> %+v", s, v, got)
		}
	})
}
