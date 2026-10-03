// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rayy3535/cryptoerase/perc"
)

// sasHost follows a PowerEdge with a PERC and two SAS hard disks in a RAID1
// virtual disk (sda, at 0:2:0:0). Once set to JBOD the drives come back on
// channel 0 with their own vendor and model, under names the virtual disk
// used, and with a wwid that is not the WWN perccli reports. With vpd they
// report their serial number in VPD page 0x80, padded as Seagate does.
// Each "set jbod" brings a drive back under a new name.
func sasHost(t *testing.T, vpd bool) (*testHost, *fakeRAID) {
	h := newHost(t)
	const host = "host7"
	h.addSCSI("sda", scsiSpec{vendor: "DELL", model: "PERC H730P Mini", driver: "megaraid_sas", host: host, hctl: "0:2:0:0",
		wwid: "naa.6d0946606c5d80002e000000000000ab"})
	drive := func(slot string, did int, serial string) perc.Drive {
		d := pd(slot, did, "Onln", float64(0), serial, fmt.Sprintf("5000C500A000000%d", did))
		d.Interface, d.Media, d.Model, d.CryptoErase = "SAS", "HDD", "ST1200MM0099", true
		return d
	}
	f := &fakeRAID{
		h: h,
		ctrls: []perc.Controller{{Index: 0, Tool: "perccli64",
			VDs:    []perc.VirtualDisk{{Controller: 0, VD: 0, DG: 0, RAID: "RAID1", State: "Optl", NAA: "6d0946606c5d80002e000000000000ab", OSDevice: "/dev/sda", Drives: []string{"32:0", "32:1"}}},
			Drives: []perc.Drive{drive("32:0", 0, "WFK7J2ZJ"), drive("32:1", 1, "WFK7J2XW")}}},
		vdDisk:  map[int]string{0: "sda"},
		fail:    map[string]error{},
		good:    map[string]string{},
		media:   map[string][]byte{},
		outcome: map[string]string{},
	}
	f.event("Controller requests a host bus rescan", nil)
	names := map[string][]string{"32:0": {"sda", "sdc"}, "32:1": {"sdb", "sdd"}}
	f.jbod = map[string]func(){}
	for slot, serial := range map[string]string{"32:0": "WFK7J2ZJ", "32:1": "WFK7J2XW"} {
		f.jbod[slot] = func() {
			name := names[slot][0]
			names[slot] = names[slot][1:]
			spec := scsiSpec{vendor: "SEAGATE", model: "ST1200MM0099", driver: "megaraid_sas", rotational: 1, host: host,
				hctl: "0:0:" + slot[3:] + ":0", wwid: "naa.5000c500a000000" + slot[3:] + "3"}
			if vpd {
				spec.vpd80 = serial + "0000K9123ABC"
			}
			h.addSCSI(name, spec)
			if b, ok := f.media[slot]; ok {
				must(t, os.WriteFile(filepath.Join(h.dev, name), b, 0o644))
			} else {
				fill(t, filepath.Join(h.dev, name), false)
			}
			f.good[slot] = name
		}
	}
	return h, f
}

func TestSASDrivesBehindPERC(t *testing.T) {
	for _, vpd := range []bool{true, false} { // by VPD serial, or by device ID
		t.Run(fmt.Sprintf("vpd=%v", vpd), func(t *testing.T) {
			h, f := sasHost(t, vpd)
			rep := h.run(raidOptions(h, f, ModeErase))
			if rep.Result != "PASS" || len(rep.Drives) != 2 {
				for _, d := range rep.Drives {
					t.Logf("%s %s %s", d.Device, d.Result, d.Reason)
				}
				t.Fatalf("result %s drives %d", rep.Result, len(rep.Drives))
			}
			for name, slot := range map[string]string{"sda": "/c0/e32/s0", "sdb": "/c0/e32/s1"} {
				d := wantResult(t, rep, name, Pass, "")
				if d.Attach.RAIDSlot != slot || d.Interface != "SAS" || d.MediaType != "HDD (SAS)" || d.Model != "ST1200MM0099" ||
					d.DeviceStatus.PERCErase == nil || d.DeviceStatus.PERCErase.Outcome != "completed" {
					t.Errorf("%s: %+v %+v", name, d, d.DeviceStatus)
				}
			}
			for _, cmd := range []string{"perccli64 /c0/v0 delete force", "perccli64 /c0/e32/s0 start erase crypto", "perccli64 /c0/e32/s1 start erase crypto"} {
				if !strings.Contains(strings.Join(f.calls, "|"), cmd) {
					t.Errorf("missing %q in %v", cmd, f.calls)
				}
			}
		})
	}
}

func TestSASDriveNotCryptoCapable(t *testing.T) {
	h, f := sasHost(t, true)
	for i := range f.ctrls[0].Drives {
		f.ctrls[0].Drives[i].CryptoErase = false
	}
	rep := h.run(raidOptions(h, f, ModeErase))
	wantResult(t, rep, "sda", Unhandled, "HDD (SAS) drive behind the megaraid_sas controller, which cannot crypto-erase it: the controller does not report drive /c0/e32/s0 as cryptographic-erase capable. Overwriting is outside this tool's scope")
	if strings.Contains(strings.Join(f.calls, "|"), "start erase") {
		t.Fatalf("erase started: %v", f.calls)
	}
}

// A virtual disk is told apart from a drive the controller passes through by
// the SCSI channel, not by the vendor.
func TestRAIDVirtualDiskByChannel(t *testing.T) {
	h := newHost(t)
	h.addSCSI("sda", scsiSpec{vendor: "LSI", model: "MR9361-8i", driver: "megaraid_sas", hctl: "0:2:0:0"})
	h.addSCSI("sdb", scsiSpec{vendor: "SEAGATE", model: "ST1200MM0099", driver: "megaraid_sas", hctl: "0:0:5:0"})
	h.addSCSI("sdc", scsiSpec{vendor: "SEAGATE", model: "ST1200MM0099", driver: "mpt3sas", hctl: "0:0:5:0"})
	r := &runner{opts: Options{SysfsRoot: h.sys}}
	for name, want := range map[string]bool{"sda": true, "sdb": false, "sdc": false} {
		if got := r.isRAIDVirtualDisk(name, readTrim(filepath.Join(h.sys, "block", name, "device", "model")), r.scsiDriver(name)); got != want {
			t.Errorf("%s: virtual disk %v", name, got)
		}
	}
	if did, ok := r.megaraidDID("sdb"); !ok || did != 5 {
		t.Errorf("sdb DID %d %v", did, ok)
	}
	for _, name := range []string{"sda", "sdc", "nosuch"} {
		if _, ok := r.megaraidDID(name); ok {
			t.Errorf("%s has a DID", name)
		}
	}
}
