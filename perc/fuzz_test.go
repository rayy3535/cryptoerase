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
	f.Fuzz(func(t *testing.T, b []byte) {
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
