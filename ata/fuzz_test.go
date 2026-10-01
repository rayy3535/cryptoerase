// SPDX-License-Identifier: Apache-2.0

package ata

import "testing"

func FuzzParseIstdout(f *testing.F) {
	f.Add(istdout(identifyWords("M", "S", "F", 0x3000, 0x0001, true)))
	f.Add("")
	f.Add("0000 0000 0000 0000 0000 0000 0000 0000\n")
	f.Fuzz(func(t *testing.T, s string) {
		id, err := ParseIstdout(s)
		if err != nil {
			return
		}
		_ = id.Model() + id.Serial() + id.Firmware()
		_ = id.Sectors()
		_ = id.Security()
	})
}

func FuzzParseIdentify(f *testing.F) {
	f.Add(make([]byte, 512))
	f.Add([]byte{1, 2, 3})
	f.Fuzz(func(t *testing.T, b []byte) {
		id, err := ParseIdentify(b)
		if (err == nil) != (len(b) >= 512) {
			t.Fatalf("len %d err %v", len(b), err)
		}
		if err != nil {
			return
		}
		if len(id.Model()) > 40 || len(id.Serial()) > 20 || len(id.Firmware()) > 8 {
			t.Fatal("string longer than its field")
		}
		_ = id.Sectors()
		_ = id.Security()
	})
}

func FuzzParseSanitizeStatus(f *testing.F) {
	f.Add("Sanitize status:\n    State:    SD2 Sanitize operation In Process\n    Progress: 0x8000 (50%)\n")
	f.Add("State: SD0\nLast Sanitize Operation Completed Without Error\n")
	f.Add("State: SD1 Frozen")
	f.Fuzz(func(t *testing.T, s string) {
		st, err := ParseSanitizeStatus(s)
		if err != nil {
			return
		}
		if st.InProgress && st.Frozen {
			t.Fatal("both in progress and frozen")
		}
	})
}
