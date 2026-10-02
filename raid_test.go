// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rayy3535/cryptoerase/perc"
)

// fakeRAID is a PERC controller: deleting a virtual disk removes its SCSI
// disk from the test host, and setting a drive to JBOD adds one.
type fakeRAID struct {
	h       *testHost
	ctrls   []perc.Controller
	ctrlErr error
	vdDisk  map[int]string    // VD number -> block device it backs
	jbod    map[string]func() // slot -> creates the exposed disk
	fail    map[string]error  // command -> error
	mu      sync.Mutex
	calls   []string

	// Controller erase (PERCEraser).
	good      map[string]string // slot -> disk that "set good" hides from the OS
	media     map[string][]byte // slot -> contents while hidden
	outcome   map[string]string // slot -> completed (default) | failed | none | noop | progress
	events    []perc.Event      // oldest first
	eventsErr error
	onEvents  func() // called on every Events
	ctrlCalls int
}

func (f *fakeRAID) List(context.Context) ([]perc.Drive, error) {
	var all []perc.Drive
	for _, c := range f.ctrls {
		all = append(all, c.Drives...)
	}
	return all, nil
}

func (f *fakeRAID) Controllers(context.Context) ([]perc.Controller, error) {
	f.mu.Lock()
	f.ctrlCalls++
	f.mu.Unlock()
	return f.ctrls, f.ctrlErr
}

func (f *fakeRAID) run(cmd string, effect func()) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cmd)
	f.mu.Unlock()
	if err := f.fail[cmd]; err != nil {
		return cmd, err
	}
	if effect != nil {
		effect()
	}
	return cmd, nil
}

func (f *fakeRAID) DeleteVD(_ context.Context, c, vd int) (string, error) {
	return f.run(fmt.Sprintf("perccli64 /c%d/v%d delete force", c, vd), func() {
		if name := f.vdDisk[vd]; name != "" {
			f.h.removeSCSI(name)
		}
	})
}

func (f *fakeRAID) DeleteHotSpare(_ context.Context, c int, slot string) (string, error) {
	return f.run(fmt.Sprintf("perccli64 %s delete hotsparedrive", perc.Path(c, slot)), nil)
}

func (f *fakeRAID) SetJBOD(_ context.Context, c int, slot string) (string, error) {
	return f.run(fmt.Sprintf("perccli64 %s set jbod", perc.Path(c, slot)), f.jbod[slot])
}

func pd(slot string, did int, state string, dg any, serial, wwn string) perc.Drive {
	return perc.Drive{Controller: new(0), Slot: slot, DID: new(did), State: state, DG: dg, Interface: "SATA", Media: "SSD", SED: "N",
		Model: "SAMSUNG MZ7LH960HAJR-00005", Serial: serial, WWN: wwn, Tool: "perccli64"}
}

const vdModel = "PERC H355 Front"

// raidHost has one RAID1 virtual disk (sda) over two SATA SSDs. Setting a
// drive to JBOD exposes it as sdb / sdc: sdb is found by its NAA WWN, sdc
// (as SATA disks often are) by the serial in its t10 wwid.
func raidHost(t *testing.T) (*testHost, *fakeRAID) {
	h := newHost(t)
	h.addSCSI("sda", scsiSpec{vendor: "DELL", model: vdModel, driver: "megaraid_sas", wwid: "naa.6f4ee0804f0feb002e000000000000ab"})
	f := &fakeRAID{
		h: h,
		ctrls: []perc.Controller{{Index: 0, Tool: "perccli64",
			VDs: []perc.VirtualDisk{{Controller: 0, VD: 0, DG: 0, RAID: "RAID1", State: "Optl", NAA: "6f4ee0804f0feb002e000000000000ab", Drives: []string{"64:0", "64:1"}}},
			Drives: []perc.Drive{
				pd("64:0", 0, "Onln", float64(0), "EXAMPLESATA0001", "5002538E00000001"),
				pd("64:1", 1, "Onln", float64(0), "EXAMPLESATA0002", "5002538E00000002"),
			}}},
		vdDisk: map[int]string{0: "sda"},
		fail:   map[string]error{},
	}
	f.jbod = map[string]func(){
		"64:0": func() {
			h.addSCSI("sdb", scsiSpec{vendor: "ATA", model: "SAMSUNG MZ7LH960", driver: "megaraid_sas", wwid: "naa.5002538e00000001",
				ata: &fakeATADisk{words: ataWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESATA0001", "HXT7904Q", true, false)}})
		},
		"64:1": func() {
			h.addSCSI("sdc", scsiSpec{vendor: "ATA", model: "SAMSUNG MZ7LH960", driver: "megaraid_sas", wwid: "t10.ATA     SAMSUNG MZ7LH960HAJR-00005              EXAMPLESATA0002",
				ata: &fakeATADisk{words: ataWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESATA0002", "HXT7904Q", true, false)}})
		},
	}
	return h, f
}

func raidOptions(h *testHost, f *fakeRAID, mode Mode) Options {
	o := h.options(mode)
	o.PERC = f
	o.RAIDReset = true
	o.RAIDWait = 10 * time.Millisecond
	return o
}

func TestRAIDResetErasesExposedDrives(t *testing.T) {
	h, f := raidHost(t)
	rep := h.run(raidOptions(h, f, ModeErase))

	want := []string{"perccli64 /c0/v0 delete force", "perccli64 /c0/e64/s0 set jbod", "perccli64 /c0/e64/s1 set jbod"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls %v", f.calls)
	}
	if len(rep.RAIDReset) != 1 {
		t.Fatalf("raid reset %+v", rep.RAIDReset)
	}
	rr := rep.RAIDReset[0]
	if !rr.Executed || rr.Error != "" || rr.Skipped != "" || strings.Join(rr.Commands, "|") != strings.Join(want, "|") || len(rr.DeleteVDs) != 1 || len(rr.Expose) != 2 {
		t.Errorf("raid reset %+v", rr)
	}
	if len(rep.Drives) != 2 {
		t.Fatalf("drives %d", len(rep.Drives))
	}
	for name, slot := range map[string]string{"sdb": "/c0/e64/s0", "sdc": "/c0/e64/s1"} {
		d := wantResult(t, rep, name, Pass, "")
		if d.Attach == nil || d.Attach.RAIDSlot != slot {
			t.Errorf("%s attach %+v", name, d.Attach)
		}
	}
	if rep.Result != "PASS" || !rep.Policy.RAIDReset {
		t.Errorf("result %s policy %+v", rep.Result, rep.Policy)
	}
}

func TestRAIDResetInventoryOnlyPlans(t *testing.T) {
	h, f := raidHost(t)
	f.ctrls[0].VDs[0].NAA = ""
	f.ctrls[0].VDs[0].OSDevice = "/dev/sda" // as reported by the CLI
	rep := h.run(raidOptions(h, f, ModeInventory))
	if len(f.calls) != 0 {
		t.Fatalf("inventory changed the controller: %v", f.calls)
	}
	rr := rep.RAIDReset[0]
	if rr.Executed || len(rr.Commands) != 3 || rr.Commands[0] != "perccli64 /c0/v0 delete force" {
		t.Errorf("plan %+v", rr)
	}
	d := wantResult(t, rep, "sda", Planned, "deletes it and sets its 2 drive(s) to non-RAID")
	if d.Planned != "raid-reset" {
		t.Errorf("planned %q", d.Planned)
	}
}

func TestRAIDResetKeepsVirtualDiskInUse(t *testing.T) {
	h, f := raidHost(t)
	h.mount("sda") // the OS runs from VD 0
	h.addSCSI("sdd", scsiSpec{vendor: "DELL", model: vdModel, driver: "megaraid_sas", wwid: "naa.6f4ee0804f0feb002e000000000000cd"})
	c := &f.ctrls[0]
	c.VDs = append(c.VDs, perc.VirtualDisk{Controller: 0, VD: 1, DG: 1, RAID: "RAID0", NAA: "6f4ee0804f0feb002e000000000000cd", Drives: []string{"64:2"}})
	c.Drives = append(c.Drives, pd("64:2", 2, "Onln", float64(1), "EXAMPLESATA0003", "5002538E00000003"))
	f.vdDisk[1] = "sdd"
	f.jbod["64:2"] = func() {
		h.addSCSI("sde", scsiSpec{vendor: "ATA", driver: "megaraid_sas", wwid: "naa.5002538e00000003",
			ata: &fakeATADisk{words: ataWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESATA0003", "F", true, false)}})
	}
	rep := h.run(raidOptions(h, f, ModeErase))
	if strings.Join(f.calls, "|") != "perccli64 /c0/v1 delete force|perccli64 /c0/e64/s2 set jbod" {
		t.Fatalf("calls %v", f.calls)
	}
	rr := rep.RAIDReset[0]
	if len(rr.KeepVDs) != 1 || rr.KeepVDs[0].VD != 0 || len(rr.DeleteVDs) != 1 || rr.DeleteVDs[0].VD != 1 {
		t.Errorf("plan %+v", rr)
	}
	wantResult(t, rep, "sda", Unhandled, "in use")
	wantResult(t, rep, "sde", Pass, "")
}

func TestRAIDResetSkipsUnsafeControllers(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(h *testHost, c *perc.Controller)
		reason string
	}{
		{"unknown VD while the OS is on the controller", func(h *testHost, c *perc.Controller) {
			c.VDs[0].NAA = "ffff" // matches no block device
			h.addSCSI("sdz", scsiSpec{vendor: "DELL", model: vdModel, driver: "megaraid_sas"})
			h.mount("sdz")
		}, "cannot tell whether virtual disk 0 holds the running OS"},
		{"foreign configuration", func(_ *testHost, c *perc.Controller) { c.Drives[1].DG = "F" }, "foreign configuration"},
		{"failed drive", func(_ *testHost, c *perc.Controller) { c.Drives[1].State = "UBad" }, `state "UBad"`},
		{"online drive outside every VD", func(_ *testHost, c *perc.Controller) { c.Drives[1].DG = float64(7) }, "drive group 7, which has no virtual disk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, f := raidHost(t)
			tc.mutate(h, &f.ctrls[0])
			rep := h.run(raidOptions(h, f, ModeErase))
			if len(f.calls) != 0 {
				t.Fatalf("controller changed: %v", f.calls)
			}
			if rr := rep.RAIDReset[0]; !strings.Contains(rr.Skipped, tc.reason) || rr.Executed {
				t.Fatalf("raid reset %+v", rr)
			}
			if tc.name != "unknown VD while the OS is on the controller" {
				wantResult(t, rep, "sda", Unhandled, "RAID reset skipped this controller")
			}
		})
	}
}

func TestRAIDResetHotSpareAndJBOD(t *testing.T) {
	h, f := raidHost(t)
	c := &f.ctrls[0]
	c.Drives = append(c.Drives,
		pd("64:2", 2, "DHS", "-", "EXAMPLESATA0003", "5002538E00000003"),
		pd("64:3", 3, "JBOD", "-", "EXAMPLESATA0004", "5002538E00000004"), // already visible as sde: left alone
		pd("64:4", 4, "UGood", "-", "EXAMPLESATA0005", "5002538E00000005"))
	h.addSCSI("sde", scsiSpec{vendor: "ATA", driver: "megaraid_sas", wwid: "naa.5002538e00000004",
		ata: &fakeATADisk{words: ataWords("SAMSUNG MZ7LH960HAJR-00005", "EXAMPLESATA0004", "F", true, false)}})
	for i, slot := range []string{"64:2", "64:4"} {
		name, serial, wwn := fmt.Sprintf("sd%c", 'f'+i), fmt.Sprintf("EXAMPLESATA000%d", 3+2*i), fmt.Sprintf("naa.5002538e0000000%d", 3+2*i)
		f.jbod[slot] = func() {
			h.addSCSI(name, scsiSpec{vendor: "ATA", driver: "megaraid_sas", wwid: wwn,
				ata: &fakeATADisk{words: ataWords("SAMSUNG MZ7LH960HAJR-00005", serial, "F", true, false)}})
		}
	}
	rep := h.run(raidOptions(h, f, ModeErase))
	want := "perccli64 /c0/e64/s2 delete hotsparedrive|perccli64 /c0/v0 delete force|perccli64 /c0/e64/s0 set jbod|perccli64 /c0/e64/s1 set jbod|perccli64 /c0/e64/s2 set jbod|perccli64 /c0/e64/s4 set jbod"
	if strings.Join(f.calls, "|") != want {
		t.Fatalf("calls %v", f.calls)
	}
	if rep.Result != "PASS" || len(rep.Drives) != 5 {
		t.Fatalf("result %s drives %d", rep.Result, len(rep.Drives))
	}
}

func TestRAIDResetFailuresAreReported(t *testing.T) {
	t.Run("command fails", func(t *testing.T) {
		h, f := raidHost(t)
		f.fail["perccli64 /c0/e64/s1 set jbod"] = errors.New("Set PD JBOD Failed")
		rep := h.run(raidOptions(h, f, ModeErase))
		rr := rep.RAIDReset[0]
		if !strings.Contains(rr.Error, "Set PD JBOD Failed") || len(rr.Commands) != 3 {
			t.Fatalf("raid reset %+v", rr)
		}
		wantResult(t, rep, "sdb", Pass, "")
		d := wantResult(t, rep, "s1", Fail, "RAID reset failed (Set PD JBOD Failed)")
		if d.Serial != "EXAMPLESATA0002" || d.Attach.RAIDSlot != "/c0/e64/s1" {
			t.Errorf("record %+v", d)
		}
		if rep.Result != "FAIL" {
			t.Errorf("result %s", rep.Result)
		}
	})
	t.Run("drive never appears", func(t *testing.T) {
		h, f := raidHost(t)
		delete(f.jbod, "64:1")
		rep := h.run(raidOptions(h, f, ModeErase))
		wantResult(t, rep, "sdb", Pass, "")
		wantResult(t, rep, "s1", Fail, "not visible to the OS; not erased")
	})
	t.Run("exposed drive missing, later command fails", func(t *testing.T) {
		// s0 was set to non-RAID before s1 failed: it counts as exposed
		// (and missing), not as part of the failure.
		h, f := raidHost(t)
		delete(f.jbod, "64:0")
		f.fail["perccli64 /c0/e64/s1 set jbod"] = errors.New("Set PD JBOD Failed")
		rep := h.run(raidOptions(h, f, ModeErase))
		wantResult(t, rep, "s0", Fail, "not visible to the OS")
		wantResult(t, rep, "s1", Fail, "RAID reset failed")
	})
	t.Run("virtual disk survives deletion", func(t *testing.T) {
		h, f := raidHost(t)
		delete(f.vdDisk, 0) // the controller says deleted, the OS still shows sda
		rep := h.run(raidOptions(h, f, ModeErase))
		wantResult(t, rep, "sda", Fail, "still present after the RAID reset deleted it")
	})
}

func TestRAIDResetWithoutController(t *testing.T) {
	t.Run("backend cannot reset", func(t *testing.T) {
		h := newHost(t)
		h.addSCSI("sda", scsiSpec{vendor: "DELL", model: vdModel, driver: "megaraid_sas"})
		o := h.options(ModeErase)
		o.RAIDReset = true
		rep := h.run(o)
		if rr := rep.RAIDReset[0]; rr.Controller != -1 || !strings.Contains(rr.Error, "cannot change controller configuration") {
			t.Fatalf("%+v", rr)
		}
		wantResult(t, rep, "sda", Unhandled, "PERC virtual disk")
	})
	t.Run("no CLI installed", func(t *testing.T) {
		h, f := raidHost(t)
		f.ctrls = nil
		rep := h.run(raidOptions(h, f, ModeErase))
		if rr := rep.RAIDReset[0]; !strings.Contains(rr.Skipped, "no PERC/MegaRAID CLI") {
			t.Fatalf("%+v", rr)
		}
	})
	t.Run("listing fails", func(t *testing.T) {
		h, f := raidHost(t)
		f.ctrlErr = errors.New("perccli64: exit status 1")
		rep := h.run(raidOptions(h, f, ModeErase))
		if rr := rep.RAIDReset[0]; !strings.Contains(rr.Error, "exit status 1") || len(f.calls) != 0 {
			t.Fatalf("%+v %v", rr, f.calls)
		}
	})
	t.Run("nothing to do", func(t *testing.T) {
		h, f := raidHost(t)
		h.removeSCSI("sda")
		f.ctrls[0].VDs = nil
		for i := range f.ctrls[0].Drives {
			f.ctrls[0].Drives[i].State, f.ctrls[0].Drives[i].DG = "JBOD", "-"
		}
		rep := h.run(raidOptions(h, f, ModeErase))
		if len(rep.RAIDReset) != 0 || len(f.calls) != 0 {
			t.Fatalf("%+v %v", rep.RAIDReset, f.calls)
		}
	})
}

func TestWWIDMatching(t *testing.T) {
	d := perc.Drive{Serial: "S4ABC", WWN: "5002538E00000001"}
	for wwid, want := range map[string]bool{
		"naa.5002538e00000001":                           true,
		"0x5002538E00000001":                             true,
		"t10.ATA     SAMSUNG MZ7LH960HAJR-00005  S4ABC":  true,
		"t10.ATA     SAMSUNG MZ7LH960HAJR-00005  S4ABCD": false,
		"naa.5002538e00000002":                           false,
		"":                                               false,
	} {
		if got := wwidMatches(wwid, d); got != want {
			t.Errorf("%q: %v", wwid, got)
		}
	}
}
