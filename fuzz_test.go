// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"strings"
	"testing"
)

func FuzzCompareVersions(f *testing.F) {
	for _, s := range [][2]string{{"VDV1DP25", "VDV1DP23"}, {"1.10", "1.9"}, {"", "0"}, {"a01", "a1"}, {"nvme10", "nvme9"}} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		ab, ba := CompareVersions(a, b), CompareVersions(b, a)
		if ab < -1 || ab > 1 {
			t.Fatalf("CompareVersions(%q, %q) = %d", a, b, ab)
		}
		if ab != -ba {
			t.Fatalf("not antisymmetric: %q %q -> %d %d", a, b, ab, ba)
		}
		if CompareVersions(a, a) != 0 {
			t.Fatalf("CompareVersions(%q, %q) != 0", a, a)
		}
	})
}

func FuzzParseFirmwarePolicy(f *testing.F) {
	f.Add(DefaultFirmwarePolicyText)
	f.Add("^A$ ; . ; 1.0\n# comment\n\n")
	f.Add("( ; x ; 1")
	f.Fuzz(func(t *testing.T, s string) {
		rules, err := ParseFirmwarePolicy(s)
		if err != nil {
			return
		}
		if len(rules) > strings.Count(s, "\n")+1 {
			t.Fatalf("%d rules from %d lines", len(rules), strings.Count(s, "\n")+1)
		}
		for _, r := range rules {
			_ = CheckFirmware([]FirmwareRule{r}, "MODEL", "FW")
			_ = r.String()
		}
	})
}

func FuzzSampleOffsets(f *testing.F) {
	f.Add(int64(64<<20), 16)
	f.Add(int64(3<<20), 16)
	f.Add(int64(1<<20), 2)
	f.Fuzz(func(t *testing.T, size int64, n int) {
		if size < 0 || size > 1<<50 || n < 2 || n > 4096 {
			return
		}
		offs := sampleOffsets(size, n)
		total := size / chunk
		if total == 0 {
			if len(offs) != 0 {
				t.Fatalf("offsets on a device smaller than 1 MiB: %v", offs)
			}
			return
		}
		if len(offs) == 0 || len(offs) > n || int64(len(offs)) > total {
			t.Fatalf("size %d n %d: %d offsets", size, n, len(offs))
		}
		if offs[0] != 0 {
			t.Fatal("first chunk not sampled")
		}
		if total >= 2 && offs[len(offs)-1] != (total-1)*chunk {
			t.Fatalf("last chunk not sampled: %d", offs[len(offs)-1])
		}
		for i, o := range offs {
			if o%chunk != 0 || o+chunk > size {
				t.Fatalf("offset %d out of range or unaligned", o)
			}
			if i > 0 && o <= offs[i-1] {
				t.Fatal("offsets not strictly increasing")
			}
		}
	})
}
