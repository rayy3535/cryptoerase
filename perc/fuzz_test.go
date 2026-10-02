// SPDX-License-Identifier: Apache-2.0

package perc

import (
	"fmt"
	"os"
	"testing"
)

func FuzzParse(f *testing.F) {
	if b, err := os.ReadFile("testdata/perccli_2xsata.json"); err == nil {
		f.Add(b)
	}
	f.Add([]byte(`{"Controllers":[{"Command Status":{"Controller":0},"Response Data":{"x":[{"EID:Slt":"64:0"}],"y":[{"EID:Slt":"64:0"}]}}]}`))
	f.Add([]byte(`{`))
	if b, err := os.ReadFile("testdata/synthetic_vall_showall.json"); err == nil {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if vds, err := ParseVirtualDisks(b); err == nil {
			for _, v := range vds {
				if v.VD < 0 && v.Controller < 0 {
					t.Fatal("negative identifiers")
				}
			}
		}
		_, _ = ParseConfig(b, b, "storcli64")
		d, err := Parse(b, "storcli64")
		if err != nil {
			return
		}
		// Every physical drive is reported once.
		seen := map[string]bool{}
		for _, x := range d {
			k := "-/" + x.Slot
			if x.Controller != nil {
				k = fmt.Sprintf("%d/%s", *x.Controller, x.Slot)
			}
			if seen[k] {
				t.Fatalf("drive %s reported twice", k)
			}
			seen[k] = true
		}
	})
}

func FuzzParseEvents(f *testing.F) {
	if b, err := os.ReadFile("testdata/events_latest.txt"); err == nil {
		f.Add(string(b))
	}
	f.Add("seqNum: 0x1\nEvent Description: Erase completed on PD 26(e0x44/s0)\nDevice ID: 38\n")
	f.Fuzz(func(t *testing.T, s string) {
		evs, err := ParseEvents(s)
		if err != nil {
			return
		}
		if len(evs) == 0 {
			t.Fatal("no events without error")
		}
		o, ev := EraseOutcome(evs, 0, 38)
		if (o == "") != (ev == nil) {
			t.Fatalf("outcome %q with event %+v", o, ev)
		}
	})
}
