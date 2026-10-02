// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayy3535/cryptoerase/perc"
)

// readyHost has a PERC with no disk visible to the OS: a drive in state
// Ready (UGood) and a hot spare. Setting a drive to JBOD exposes it.
func readyHost(t *testing.T) (*testHost, *fakeRAID) {
	h := newHost(t)
	write(t, filepath.Join(h.sys, "class/scsi_host/host9/proc_name"), "megaraid_sas\n")
	f := &fakeRAID{
		h: h,
		ctrls: []perc.Controller{{Index: 0, Tool: "perccli64", Drives: []perc.Drive{
			pd("68:0", 38, "UGood", "-", "EXAMPLESATA0001", "5002538E00000001"),
			pd("68:1", 37, "GHS", "-", "EXAMPLESATA0002", "5002538E00000002"),
		}}},
		fail: map[string]error{},
	}
	f.jbod = map[string]func(){
		"68:0": func() {
			h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "megaraid_sas", wwid: "naa.5002538e00000001",
				ata: &fakeATADisk{words: ataWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESATA0001", "F", true, false)}})
		},
		"68:1": func() {
			h.addSCSI("sdb", scsiSpec{vendor: "ATA", driver: "megaraid_sas", wwid: "naa.5002538e00000002",
				ata: &fakeATADisk{words: ataWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESATA0002", "F", true, false)}})
		},
	}
	return h, f
}

// Drives the OS cannot see are reported, not left out: without
// --raid-reset they are UNHANDLED, so the run does not pass.
func TestHiddenDrivesReported(t *testing.T) {
	h, f := readyHost(t)
	o := h.options(ModeErase)
	o.PERC = f
	rep := h.run(o)
	if len(rep.Drives) != 2 || rep.ExitCode() != 2 || len(f.calls) != 0 {
		t.Fatalf("drives %d exit %d calls %v", len(rep.Drives), rep.ExitCode(), f.calls)
	}
	d := wantResult(t, rep, "s0", Unhandled, "behind the PERC/MegaRAID controller in state UGood, not visible to the OS; rerun with --raid-reset to set it to non-RAID and erase it")
	if d.Device != "/c0/e68/s0" || d.Serial != "EXAMPLESATA0001" || d.Attach.RAIDSlot != "/c0/e68/s0" || len(d.PERC.PhysicalDrives) != 1 {
		t.Errorf("record %+v", d)
	}
	wantResult(t, rep, "s1", Unhandled, "in state GHS")
}

func TestHiddenDrivesWithRAIDReset(t *testing.T) {
	t.Run("inventory plans them", func(t *testing.T) {
		h, f := readyHost(t)
		rep := h.run(raidOptions(h, f, ModeInventory))
		for _, s := range []string{"s0", "s1"} {
			d := wantResult(t, rep, s, Planned, "the RAID reset sets it to non-RAID, then it is checked and erased on its own")
			if d.Planned != "raid-reset" {
				t.Errorf("%s planned %q", s, d.Planned)
			}
		}
		if len(f.calls) != 0 || rep.ExitCode() != 0 {
			t.Fatalf("calls %v exit %d", f.calls, rep.ExitCode())
		}
	})
	t.Run("erase exposes and erases them", func(t *testing.T) {
		h, f := readyHost(t)
		rep := h.run(raidOptions(h, f, ModeErase))
		want := "perccli64 /c0/e68/s1 delete hotsparedrive|perccli64 /c0/e68/s0 set jbod|perccli64 /c0/e68/s1 set jbod"
		if strings.Join(f.calls, "|") != want {
			t.Fatalf("calls %v", f.calls)
		}
		if len(rep.Drives) != 2 || rep.Result != "PASS" {
			t.Fatalf("drives %d result %s", len(rep.Drives), rep.Result)
		}
		wantResult(t, rep, "sda", Pass, "")
		wantResult(t, rep, "sdb", Pass, "")
	})
	t.Run("skipped controller", func(t *testing.T) {
		h, f := readyHost(t)
		f.ctrls[0].Drives = append(f.ctrls[0].Drives, pd("68:2", 36, "UBad", "-", "EXAMPLESATA0003", "5002538E00000003"))
		rep := h.run(raidOptions(h, f, ModeErase))
		if len(f.calls) != 0 || len(rep.Drives) != 3 {
			t.Fatalf("calls %v drives %d", f.calls, len(rep.Drives))
		}
		wantResult(t, rep, "s0", Unhandled, `the RAID reset skipped this controller: drive 68:2 is in state "UBad"`)
		wantResult(t, rep, "s2", Unhandled, "in state UBad")
	})
}

func TestHiddenDrivesListingFails(t *testing.T) {
	h, f := readyHost(t)
	f.ctrlErr = errors.New("perccli64 crashed")
	o := h.options(ModeErase)
	o.PERC = f
	rep := h.run(o)
	d := wantResult(t, rep, "RAID controller host9", Unhandled, "drives behind the controller that the OS does not see could not be listed: perccli64 crashed")
	if d.PERC == nil || d.PERC.Error != "perccli64 crashed" {
		t.Fatalf("%+v", d.PERC)
	}
}

// Without a PERC/MegaRAID controller the CLI is not run at all; drives the
// OS sees are not reported twice.
func TestHiddenDrivesNotLookedFor(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	f := &fakeRAID{h: h}
	o := h.options(ModeErase)
	o.PERC = f
	h.run(o)
	if f.ctrlCalls != 0 {
		t.Fatalf("controller listed %d times", f.ctrlCalls)
	}

	h, f, _ = percHost(t) // a JBOD drive, visible as sda
	rep := h.run(percOptions(h, f, ModeErase))
	if len(rep.Drives) != 1 {
		t.Fatalf("drives %d", len(rep.Drives))
	}
}
