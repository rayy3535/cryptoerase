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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rayy3535/cryptoerase/ata"
	"github.com/rayy3535/cryptoerase/blockdev"
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
		f.setState(slot, "UGood")
		f.event("State change on "+pdName(f.drive(slot))+" from JBOD(40) to UNCONFIGURED_GOOD(0)", f.drive(slot).DID)
	})
}

func (f *fakeRAID) StartCryptoErase(_ context.Context, c int, slot string) (string, error) {
	return f.run(fmt.Sprintf("perccli64 %s start erase crypto", perc.Path(c, slot)), func() {
		if f.onErase != nil {
			f.onErase(slot)
		}
		d := f.drive(slot)
		pd := pdName(d)
		switch f.outcome[slot] {
		case "none", "progress":
		case "failed":
			f.event("Erase started on "+pd, d.DID)
			f.event("Erase failed on "+pd+" (Error 02)", d.DID)
		case "noop":
			f.event("Erase completed on "+pd, d.DID)
		case "flood": // PERC H730P: a drive that is not ready floods the log
			f.event("Erase started on "+pd, d.DID)
			clear(f.media[slot])
			f.event("Erase completed on "+pd, d.DID)
			for range 300 {
				f.event("Unexpected sense: "+pd+" Path 500056b328dea5c3, CDB: 00 00 00 00 00 00, Sense: 2/05/00", nil)
			}
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
	if f.onEvents != nil {
		f.onEvents()
	}
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
	names := []string{"sdc", "sdd"}
	f.jbod = map[string]func(){"68:0": func() {
		name := names[0]
		names = names[1:]
		h.addSCSI(name, scsiSpec{vendor: "ATA", model: "SAMSUNG MZ7LH960", driver: "megaraid_sas", wwid: "naa.5002538e00000001",
			ata: &fakeATADisk{words: words, behaviour: "noregs"}})
		must(t, os.WriteFile(filepath.Join(h.dev, name), f.media["68:0"], 0o644))
		f.good["68:0"] = name
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
	if b, err := os.ReadFile(sdaDeleteFile(h)); err != nil || string(b) != "1" {
		t.Fatalf("delete: %q %v", b, err)
	}
}

// The erase result is found however many events follow it.
func TestPERCEraseEventFlood(t *testing.T) {
	h, f, _ := percHost(t)
	f.outcome["68:0"] = "flood"
	rep := h.run(percOptions(h, f, ModeErase))
	r := wantResult(t, rep, "sda", Pass, "")
	if r.DeviceStatus.PERCErase.Event.Description != "Erase completed on PD 26(e0x44/s0)" {
		t.Fatalf("%+v", r.DeviceStatus.PERCErase.Event)
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
		{"unknown serial", func(f *fakeRAID) { f.ctrls[0].Drives[0].Serial, f.ctrls[0].Drives[0].WWN = "OTHER", "" }, `; erase through the controller not possible: serial "EXAMPLESATA0001" (and sda) not found among the controller's drives`, 0},
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
	wantResult(t, rep, "sda", Fail, "erase through the controller not possible: the RAID backend cannot erase through the controller")

	h = newHost(t)
	d := &fakeATADisk{words: ataWords("M", "S", "F", true, false), behaviour: "noregs"}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "mpt3sas", ata: d})
	rep = h.run(h.options(ModeErase))
	r := wantResult(t, rep, "sda", Fail, "the mpt3sas controller passes ATA commands through but does not return the drive's status, so a sanitize cannot be confirmed; not erased (hdparm --sanitize-status")
	if strings.Contains(r.Reason, "through the controller not possible") {
		t.Fatalf("controller erase considered for mpt3sas: %q", r.Reason)
	}
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

// closeHook calls onClose before closing the device.
type closeHook struct {
	blockdev.Device
	onClose func()
}

func (c closeHook) Close() error {
	c.onClose()
	return c.Device.Close()
}

// The markers' write handle must stay open until the disk is gone from the
// kernel: closing it makes udev re-read the partition table, and on a PERC
// H355 that re-read racing with the removal filled the kernel log with I/O
// errors.
func TestPERCEraseClosesWriteHandleAfterRemoval(t *testing.T) {
	h, f, _ := percHost(t)
	deleteFile := sdaDeleteFile(h)
	o := percOptions(h, f, ModeErase)
	var closes []bool // per write-handle close: was the disk removed already?
	o.OpenBlock = func(p string, direct, write bool) (blockdev.Device, error) {
		if !write {
			return blockdev.OpenReadOnly(p, direct)
		}
		d, err := blockdev.Open(p, direct)
		if err != nil {
			return nil, err
		}
		return closeHook{d, func() {
			_, err := os.Stat(deleteFile)
			closes = append(closes, err == nil)
		}}, nil
	}
	rep := h.run(o)
	wantResult(t, rep, "sda", Pass, "")
	if len(closes) != 1 || !closes[0] {
		t.Fatalf("write handle closes (removed first?): %v", closes)
	}
}

// If the disk cannot be removed from the kernel, the handle is closed and
// udev gets time to re-read the partition table before the controller hides
// the drive.
func TestPERCEraseRemovalFails(t *testing.T) {
	old := udevSettle
	udevSettle = time.Millisecond
	t.Cleanup(func() { udevSettle = old })
	h, f, _ := percHost(t)
	must(t, os.Mkdir(sdaDeleteFile(h), 0o755)) // writing to it fails
	rep := h.run(percOptions(h, f, ModeErase))
	wantResult(t, rep, "sda", Pass, "")
	if strings.Join(f.calls, "|") != strings.Join(percCommands, "|") {
		t.Fatalf("calls %v", f.calls)
	}

	// Interrupted while waiting for udev: nothing was changed on the
	// controller.
	udevSettle = time.Hour
	h, f, _ = percHost(t)
	must(t, os.Mkdir(sdaDeleteFile(h), 0o755))
	ctx, cancel := context.WithCancel(context.Background())
	f.onEvents = cancel
	rep, err := Run(ctx, percOptions(h, f, ModeErase))
	must(t, err)
	wantResult(t, rep, "sda", Fail, "interrupted before erase")
	if len(f.calls) != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

// sdaDeleteFile is the sysfs file that removes percHost's sda from the
// kernel.
func sdaDeleteFile(h *testHost) string {
	return filepath.Join(h.sys, "devices/pci0000:00", "host"+strconv.Itoa(3*7+int('a')), "target0:0:0", "0:0:0:sda", "delete")
}

// Behind a PERC the controller erases the drive even when ATA pass-through
// looks usable: another PERC rejected SANITIZE sent through it.
func TestPERCErasePreferredOverHdparm(t *testing.T) {
	h, f, d := percHost(t)
	d.behaviour = "ok"
	rep := h.run(percOptions(h, f, ModeErase))
	r := wantResult(t, rep, "sda", Pass, "")
	if strings.Join(f.calls, "|") != strings.Join(percCommands, "|") || len(d.calls) != 0 {
		t.Fatalf("controller %v, hdparm %v", f.calls, d.calls)
	}
	if r.DeviceStatus.PERCErase == nil || r.TechniqueDetail != "drive cryptographic erase by the PERC/MegaRAID controller (start erase crypto); result from the controller event log" {
		t.Fatalf("%+v", r)
	}
}

// If the controller cannot erase the drive, hdparm is tried, and its failure
// says why the controller was not used.
func TestPERCEraseFallbackToHdparm(t *testing.T) {
	h, f, d := percHost(t)
	d.behaviour = "reject"
	f.ctrls[0].Drives[0].CryptoErase = false
	rep := h.run(percOptions(h, f, ModeErase))
	wantResult(t, rep, "sda", Fail, "SANITIZE CRYPTO SCRAMBLE rejected: hdparm ")
	r := wantResult(t, rep, "sda", Fail, "; erase through the controller not possible: the controller does not report drive /c0/e68/s0 as cryptographic-erase capable")
	if !strings.Contains(r.Reason, "; the drive's sanitize status afterwards: SD0") || r.DeviceStatus == nil || r.DeviceStatus.ATASanitize == nil {
		t.Fatalf("%q %+v", r.Reason, r.DeviceStatus)
	}
	if len(f.calls) != 0 {
		t.Fatalf("controller commands %v", f.calls)
	}

	h, f, d = percHost(t)
	d.behaviour = "ok"
	f.ctrls[0].Drives[0].State = "Onln"
	rep = h.run(percOptions(h, f, ModeErase))
	wantResult(t, rep, "sda", Pass, "")
	if !slices.Contains(d.calls, "crypto-scramble") {
		t.Fatalf("hdparm not used: %v", d.calls)
	}
}

// stuckDev fails every write with EIO, like a drive a PERC H730P reports
// NOT READY.
type stuckDev struct {
	blockdev.Device
	path string
}

func (d stuckDev) WriteAt([]byte, int64) (int, error) {
	return 0, &os.PathError{Op: "write", Path: d.path, Err: syscall.EIO}
}

// stuckOptions makes writes fail while stuck is set.
func stuckOptions(o Options, stuck *atomic.Bool) Options {
	o.OpenBlock = func(p string, direct, write bool) (blockdev.Device, error) {
		if !write {
			return blockdev.OpenReadOnly(p, direct)
		}
		d, err := blockdev.Open(p, direct)
		if err != nil || !stuck.Load() {
			return d, err
		}
		return stuckDev{d, p}, nil
	}
	return o
}

// A drive that rejects writes is erased once by the controller to make it
// usable, then erased again with markers.
func TestPERCEraseRecoversStuckDrive(t *testing.T) {
	h, f, _ := percHost(t)
	var stuck atomic.Bool
	stuck.Store(true)
	f.onErase = func(string) { stuck.Store(false) }
	rep := h.run(stuckOptions(percOptions(h, f, ModeErase), &stuck))
	r := wantResult(t, rep, "sda", Pass, "")
	pe := r.DeviceStatus.PERCErase
	want := strings.Join(append(slices.Clone(percCommands), percCommands...), "|")
	if strings.Join(f.calls, "|") != want || strings.Join(pe.Commands, "|") != want {
		t.Fatalf("calls %v, recorded %v", f.calls, pe.Commands)
	}
	if !strings.Contains(pe.Recovery, "input/output error") || !strings.Contains(pe.Recovery, `a first controller erase ("Erase completed on PD 26(e0x44/s0)") made it writable again`) ||
		pe.Outcome != "completed" || filepath.Base(pe.ReattachedAs) != "sdd" {
		t.Fatalf("%+v", pe)
	}
	if r.Verification.Changed != r.Verification.Samples {
		t.Fatalf("verification %+v", r.Verification)
	}
}

func TestPERCEraseRecoveryFails(t *testing.T) {
	t.Run("still rejects writes", func(t *testing.T) {
		h, f, _ := percHost(t)
		var stuck atomic.Bool
		stuck.Store(true)
		rep := h.run(stuckOptions(percOptions(h, f, ModeErase), &stuck))
		wantResult(t, rep, "sda", Fail, "cannot write markers before erase")
		if len(f.calls) != 3 {
			t.Fatalf("calls %v", f.calls)
		}
	})
	t.Run("recovery erase fails", func(t *testing.T) {
		h, f, _ := percHost(t)
		var stuck atomic.Bool
		stuck.Store(true)
		f.outcome["68:0"] = "failed"
		rep := h.run(stuckOptions(percOptions(h, f, ModeErase), &stuck))
		wantResult(t, rep, "sda", Fail, "and a controller erase to make it usable again failed: controller event log: Erase failed on PD 26(e0x44/s0) (Error 02); treat the drive as not erased")
	})
}

// statusHook calls onStatus on every SANITIZE STATUS.
type statusHook struct {
	*fakeATA
	onStatus func()
}

func (s statusHook) SanitizeStatus(ctx context.Context, dev string) (*ata.SanitizeStatus, error) {
	s.onStatus()
	return s.fakeATA.SanitizeStatus(ctx, dev)
}

// A PERC H730P keeps a SATA drive blocked after a pass-through SANITIZE
// until it sees SANITIZE STATUS report it over; then one erase is enough.
func TestPERCEraseUnblockedBySanitizeStatus(t *testing.T) {
	old := percStatusSettle
	percStatusSettle = time.Millisecond
	t.Cleanup(func() { percStatusSettle = old })
	h, f, d := percHost(t)
	d.behaviour = "ok"
	var stuck atomic.Bool
	stuck.Store(true)
	o := stuckOptions(percOptions(h, f, ModeErase), &stuck)
	o.ATA = statusHook{h.ata, func() { stuck.Store(false) }}
	rep := h.run(o)
	r := wantResult(t, rep, "sda", Pass, "")
	pe := r.DeviceStatus.PERCErase
	if strings.Join(f.calls, "|") != strings.Join(percCommands, "|") || !strings.Contains(pe.Recovery, "until a SANITIZE STATUS EXT reported SD0") {
		t.Fatalf("calls %v recovery %q", f.calls, pe.Recovery)
	}
	if slices.Contains(d.calls, "crypto-scramble") {
		t.Fatal("SANITIZE sent through hdparm")
	}
}
