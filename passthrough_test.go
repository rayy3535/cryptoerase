// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"slices"
	"strings"
	"testing"
)

// A RAID controller that passes SATA drives through as non-RAID disks
// (PERC/MegaRAID) forwards ATA commands but not the drive's registers, so
// SANITIZE STATUS EXT reads as idle with no completion flag. The drive must
// fail before anything is written or sent, in inventory mode as well.
func TestSATANoRegistersFailsBeforeErase(t *testing.T) {
	for _, mode := range []Mode{ModeInventory, ModeErase} {
		t.Run(mode.String(), func(t *testing.T) {
			h := newHost(t)
			d := &fakeATADisk{words: ataWords("M", "PERC", "F", true, false), behaviour: "noregs"}
			h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "megaraid_sas", ata: d})
			before := h.sum("sda")
			rep := h.run(h.options(mode))
			r := drive(t, rep, "sda")
			if r.Result != Fail || !strings.Contains(r.Reason, "megaraid_sas controller passes ATA commands through but does not return the drive's status") ||
				!strings.Contains(r.Reason, "bad/missing sense data") || r.Planned != "" {
				t.Fatalf("%s %q planned=%q", r.Result, r.Reason, r.Planned)
			}
			if slices.Contains(d.calls, "crypto-scramble") || h.sum("sda") != before {
				t.Fatalf("drive touched: %v", d.calls)
			}
			if r.Model != "M" || r.ATA == nil || !r.ATA.CryptoScramble {
				t.Fatalf("identify data missing: %+v", r)
			}
		})
	}
}

// If the status reads fine but the SANITIZE command itself gets no
// registers back, the drive is not reported erased.
func TestSATANoRegistersOnScramble(t *testing.T) {
	h := newHost(t)
	d := &fakeATADisk{words: ataWords("M", "PERC", "F", true, false), behaviour: "noregs-scramble"}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "megaraid_sas", ata: d})
	rep := h.run(h.options(ModeErase))
	r := drive(t, rep, "sda")
	if r.Result != Fail || !strings.Contains(r.Reason, "did not come back through the megaraid_sas controller; treat the drive as not erased") {
		t.Fatalf("%s %q", r.Result, r.Reason)
	}
}
