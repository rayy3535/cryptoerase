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

// smartpqi returns the ATA registers in fixed-format sense data until
// D_SENSE is set. The tool sets it, reads the status again and goes on, in
// inventory mode as well.
func TestSATAFixedSenseSetsDescriptorSense(t *testing.T) {
	for _, mode := range []Mode{ModeInventory, ModeErase} {
		t.Run(mode.String(), func(t *testing.T) {
			h := newHost(t)
			d := &fakeATADisk{words: ataWords("SAMSUNG MZ7LH960HAJR-00005", "S1", "F", true, false), behaviour: "fixedsense"}
			h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "smartpqi", ata: d})
			rep := h.run(h.options(mode))
			r := drive(t, rep, "sda")
			want := Planned
			if mode == ModeErase {
				want = Pass
			}
			if r.Result != want || r.Attach == nil || !r.Attach.DescriptorSense {
				t.Fatalf("%s %q attach=%+v", r.Result, r.Reason, r.Attach)
			}
			if got := strings.Join(d.calls, ","); !strings.HasPrefix(got, "status,dsense,status") {
				t.Fatalf("calls %s", got)
			}
		})
	}
}

// A reset after the sanitize clears D_SENSE again: the status poll sets it
// once more and the erase is confirmed.
func TestSATAFixedSenseResetDuringSanitize(t *testing.T) {
	h := newHost(t)
	d := &fakeATADisk{words: ataWords("M", "S1", "F", true, false), behaviour: "fixedsense", resetDSense: true}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "smartpqi", ata: d})
	rep := h.run(h.options(ModeErase))
	if r := drive(t, rep, "sda"); r.Result != Pass {
		t.Fatalf("%s %q (calls %v)", r.Result, r.Reason, d.calls)
	}
	if n := strings.Count(strings.Join(d.calls, ","), "dsense"); n != 2 {
		t.Fatalf("D_SENSE set %d times: %v", n, d.calls)
	}
}

// D_SENSE that cannot be set, or is set already, leaves the drive failed,
// and the reason says what was tried.
func TestSATANoRegistersDescriptorSenseNotHelping(t *testing.T) {
	h := newHost(t)
	d := &fakeATADisk{words: ataWords("M", "S1", "F", true, false), behaviour: "noregs"}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "smartpqi", ata: d})
	rep := h.run(h.options(ModeErase))
	r := drive(t, rep, "sda")
	if r.Result != Fail || !strings.Contains(r.Reason, "setting D_SENSE (descriptor-format sense data) failed: MODE SENSE(10)") || r.Attach.DescriptorSense {
		t.Fatalf("%s %q", r.Result, r.Reason)
	}

	h = newHost(t)
	// Registers missing although D_SENSE is set.
	d = &fakeATADisk{words: ataWords("M", "S1", "F", true, false), behaviour: "noregs", dsense: true}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "smartpqi", ata: d})
	rep = h.run(h.options(ModeInventory))
	r = drive(t, rep, "sda")
	if r.Result != Fail || !strings.Contains(r.Reason, "D_SENSE (descriptor-format sense data) is already set") {
		t.Fatalf("%s %q", r.Result, r.Reason)
	}
}
