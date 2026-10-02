// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rayy3535/cryptoerase/perc"
)

func (f *fakeRAID) event(desc string, did *int) {
	seq := uint32(0x100)
	if n := len(f.events); n > 0 {
		seq = f.events[n-1].Seq + 1
	}
	f.events = append(f.events, perc.Event{Seq: seq, Description: desc, DeviceID: did})
}

func (f *fakeRAID) drive(slot string) perc.Drive {
	for _, c := range f.ctrls {
		for _, d := range c.Drives {
			if d.Slot == slot {
				return d
			}
		}
	}
	f.h.t.Fatalf("no drive %s", slot)
	return perc.Drive{}
}

func pdName(d perc.Drive) string {
	e, s, _ := strings.Cut(d.Slot, ":")
	var eid int
	_, _ = fmt.Sscan(e, &eid)
	return fmt.Sprintf("PD %x(e0x%x/s%s)", *d.DID, eid, s)
}

func (f *fakeRAID) SetGood(_ context.Context, c int, slot string) (string, error) {
	return f.run(fmt.Sprintf("perccli64 %s set good force", perc.Path(c, slot)), func() {
		name := f.good[slot]
		b, err := os.ReadFile(filepath.Join(f.h.dev, name))
		must(f.h.t, err)
		f.media[slot] = b
		f.h.removeSCSI(name)
		f.event("State change on "+pdName(f.drive(slot))+" from JBOD(40) to UNCONFIGURED_GOOD(0)", f.drive(slot).DID)
	})
}

func (f *fakeRAID) StartCryptoErase(_ context.Context, c int, slot string) (string, error) {
	return f.run(fmt.Sprintf("perccli64 %s start erase crypto", perc.Path(c, slot)), func() {
		d := f.drive(slot)
		pd := pdName(d)
		switch f.outcome[slot] {
		case "none", "progress":
		case "failed":
			f.event("Erase started on "+pd, d.DID)
			f.event("Erase failed on "+pd+" (Error 02)", d.DID)
		case "noop":
			f.event("Erase completed on "+pd, d.DID)
		default:
			f.event("Erase started on "+pd, d.DID)
			clear(f.media[slot])
			f.event("Erase completed on "+pd, d.DID)
		}
	})
}

func (f *fakeRAID) EraseStatus(_ context.Context, _ int, slot string) (perc.EraseProgress, error) {
	if f.outcome[slot] == "progress" {
		return perc.EraseProgress{Status: "In progress", Percent: 3}, nil
	}
	return perc.EraseProgress{Status: "Not in progress", Percent: -1}, nil
}

func (f *fakeRAID) Events(_ context.Context, _, n int) ([]perc.Event, error) {
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	evs := f.events[max(0, len(f.events)-n):]
	out := slices.Clone(evs)
	slices.Reverse(out) // newest first, like the CLI
	return out, nil
}

// percHost has one SATA SSD in non-RAID (JBOD) mode behind a PERC: ATA
// commands reach it, but SANITIZE STATUS gets no registers back. "set good"
// hides it from the OS; "set jbod" brings it back as sdc.
func percHost(t *testing.T) (*testHost, *fakeRAID, *fakeATADisk) {
	h := newHost(t)
	words := ataWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESATA0001", "HXT7904Q", true, false)
	d := &fakeATADisk{words: words, behaviour: "noregs"}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", model: "SAMSUNG MZ7LH960", driver: "megaraid_sas", wwid: "naa.5002538e00000001", ata: d})
	fill(t, filepath.Join(h.dev, "sda"), false)
	jbod := pd("68:0", 38, "JBOD", "-", "EXAMPLESATA0001", "5002538E00000001")
	jbod.CryptoErase = true
	f := &fakeRAID{
		h:       h,
		ctrls:   []perc.Controller{{Index: 0, Tool: "perccli64", Drives: []perc.Drive{jbod}}},
		fail:    map[string]error{},
		good:    map[string]string{"68:0": "sda"},
		media:   map[string][]byte{},
		outcome: map[string]string{},
	}
	f.event("Controller requests a host bus rescan", nil)
	f.jbod = map[string]func(){"68:0": func() {
		h.addSCSI("sdc", scsiSpec{vendor: "ATA", model: "SAMSUNG MZ7LH960", driver: "megaraid_sas", wwid: "naa.5002538e00000001",
			ata: &fakeATADisk{words: words, behaviour: "noregs"}})
		must(t, os.WriteFile(filepath.Join(h.dev, "sdc"), f.media["68:0"], 0o644))
	}}
	return h, f, d
}

func percOptions(h *testHost, f *fakeRAID, mode Mode) Options {
	o := h.options(mode)
	o.PERC = f
	o.RAIDWait = 20 * time.Millisecond
	return o
}

var percCommands = []string{"perccli64 /c0/e68/s0 set good force", "perccli64 /c0/e68/s0 start erase crypto", "perccli64 /c0/e68/s0 set jbod"}

func TestPERCEraseSATA(t *testing.T) {
	h, f, d := percHost(t)
	rep := h.run(percOptions(h, f, ModeErase))
	r := wantResult(t, rep, "sda", Pass, "")
	if strings.Join(f.calls, "|") != strings.Join(percCommands, "|") {
		t.Fatalf("calls %v", f.calls)
	}
	if slices.Contains(d.calls, "crypto-scramble") {
		t.Fatal("hdparm sanitize sent")
	}
	pe := r.DeviceStatus.PERCErase
	if pe == nil || pe.Outcome != "completed" || pe.Event == nil || pe.Event.Description != "Erase completed on PD 26(e0x44/s0)" ||
		pe.Slot != "/c0/e68/s0" || pe.DID != 38 || filepath.Base(pe.ReattachedAs) != "sdc" || strings.Join(pe.Commands, "|") != strings.Join(percCommands, "|") {
		t.Fatalf("perc erase %+v", pe)
	}
	if r.Technique != "Cryptographic Erase" || r.Command != "perccli64 /c0/e68/s0 start erase crypto" || r.Attach.RAIDSlot != "/c0/e68/s0" ||
		r.Verification == nil || r.Verification.Changed != r.Verification.Samples || r.Verification.ZeroAfterErase != r.Verification.Samples {
		t.Fatalf("record %+v %+v", r, r.Verification)
	}
	// The disk was removed from the kernel before the controller hid it.
	if b, err := os.ReadFile(filepath.Join(h.sys, "devices/pci0000:00", "host"+strconv.Itoa(3*7+int('a')), "target0:0:0", "0:0:0:sda", "delete")); err != nil || string(b) != "1" {
		t.Fatalf("delete: %q %v", b, err)
	}
}

func TestPERCEraseInventory(t *testing.T) {
	h, f, _ := percHost(t)
	before := h.sum("sda")
	rep := h.run(percOptions(h, f, ModeInventory))
	r := wantResult(t, rep, "sda", Planned, "the controller erases it")
	if r.Planned != "perc-crypto-erase" || strings.Join(r.DeviceStatus.PERCErase.Commands, "|") != strings.Join(percCommands, "|") {
		t.Fatalf("%q %+v", r.Planned, r.DeviceStatus.PERCErase)
	}
	if len(f.calls) != 0 || h.sum("sda") != before {
		t.Fatalf("inventory changed something: %v", f.calls)
	}
}

func TestPERCEraseFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(f *fakeRAID)
		reason string
		calls  int // controller commands run
	}{
		{"erase failed", func(f *fakeRAID) { f.outcome["68:0"] = "failed" }, "controller event log: Erase failed on PD 26(e0x44/s0) (Error 02)", 3},
		{"no result logged", func(f *fakeRAID) { f.outcome["68:0"] = "none" }, "logged no result for device ID 38; treat the drive as not erased", 3},
		{"stalled", func(f *fakeRAID) { f.outcome["68:0"] = "progress" }, "stalled at 3% for 50ms", 3},
		{"data unchanged", func(f *fakeRAID) { f.outcome["68:0"] = "noop" }, "unchanged", 3},
		{"start refused", func(f *fakeRAID) {
			f.fail["perccli64 /c0/e68/s0 start erase crypto"] = errors.New("Failure: operation not allowed")
		}, "the controller did not start the crypto erase: Failure: operation not allowed; not erased", 3},
		{"set good refused", func(f *fakeRAID) {
			f.fail["perccli64 /c0/e68/s0 set good force"] = errors.New("Failure")
		}, "could not set the drive unconfigured good: Failure; drive not modified", 1},
		{"set jbod refused", func(f *fakeRAID) {
			f.fail["perccli64 /c0/e68/s0 set jbod"] = errors.New("Failure")
		}, "setting the drive back to non-RAID failed (Failure), so the erase could not be verified", 3},
		{"erase and set jbod refused", func(f *fakeRAID) {
			f.fail["perccli64 /c0/e68/s0 start erase crypto"] = errors.New("Failure A")
			f.fail["perccli64 /c0/e68/s0 set jbod"] = errors.New("Failure B")
		}, "not erased; setting the drive back to non-RAID also failed: Failure B", 3},
		{"never comes back", func(f *fakeRAID) { f.jbod["68:0"] = nil }, "did not come back to the OS within 20ms", 3},
		{"event log unreadable", func(f *fakeRAID) { f.eventsErr = errors.New("perccli64 crashed") }, "cannot read the controller event log, which holds the erase result: perccli64 crashed; drive not modified", 0},
		{"unknown serial", func(f *fakeRAID) { f.ctrls[0].Drives[0].Serial, f.ctrls[0].Drives[0].WWN = "OTHER", "" }, `serial "EXAMPLESATA0001" not found among the controller's drives; not erased`, 0},
		{"two matches", func(f *fakeRAID) { f.ctrls[0].Drives = append(f.ctrls[0].Drives, f.ctrls[0].Drives[0]) }, "matches 2 controller drives", 0},
		{"not crypto capable", func(f *fakeRAID) { f.ctrls[0].Drives[0].CryptoErase = false }, "does not report drive /c0/e68/s0 as cryptographic-erase capable", 0},
		{"not JBOD", func(f *fakeRAID) { f.ctrls[0].Drives[0].State = "UGood" }, `is in state "UGood", not JBOD`, 0},
		{"no device ID", func(f *fakeRAID) { f.ctrls[0].Drives[0].DID = nil }, "reports no device ID", 0},
		{"no CLI", func(f *fakeRAID) { f.ctrls = nil }, "no PERC/MegaRAID CLI (perccli64, storcli64) to erase through the controller", 0},
		{"config unreadable", func(f *fakeRAID) { f.ctrlErr = errors.New("boom") }, "controller configuration unreadable: boom", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f, _ := percHost(t)
			tc.setup(f)
			rep := h.run(percOptions(h, f, ModeErase))
			r := drive(t, rep, "sda")
			if r.Result != Fail || !strings.Contains(r.Reason, tc.reason) {
				t.Fatalf("%s %q", r.Result, r.Reason)
			}
			if len(f.calls) != tc.calls {
				t.Fatalf("calls %v", f.calls)
			}
		})
	}
}

// Without a backend that can erase through the controller, or on another
// controller type, the drive fails with the reason why.
func TestPERCEraseUnavailable(t *testing.T) {
	h, _, _ := percHost(t)
	rep := h.run(h.options(ModeErase)) // fakePERC only lists drives
	wantResult(t, rep, "sda", Fail, "the RAID backend cannot erase through the controller; not erased")

	h = newHost(t)
	d := &fakeATADisk{words: ataWords("M", "S", "F", true, false), behaviour: "noregs"}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "mpt3sas", ata: d})
	rep = h.run(h.options(ModeErase))
	wantResult(t, rep, "sda", Fail, "the mpt3sas controller passes ATA commands through but does not return the drive's status, so a sanitize cannot be confirmed; not erased. Use the controller's own erase")
}

func TestPERCEraseLockedDrive(t *testing.T) {
	h, f, _ := percHost(t)
	h.ata.disks[filepath.Join(h.dev, "sda")].words = ataWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESATA0001", "HXT7904Q", true, true)
	rep := h.run(percOptions(h, f, ModeErase))
	wantResult(t, rep, "sda", Fail, "ATA security is locked")
	if len(f.calls) != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}
