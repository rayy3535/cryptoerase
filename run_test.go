// SPDX-License-Identifier: Apache-2.0

package cryptoerase

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rayy3535/cryptoerase/nvme"
	"github.com/rayy3535/cryptoerase/perc"
)

func TestMixedHost(t *testing.T) {
	h := newHost(t)
	n0 := h.addNVMe("nvme0", nvmeSpec{sanicap: 7})
	n1 := h.addNVMe("nvme1", nvmeSpec{sanicap: 2, fna: 4})
	h.addNVMe("nvme2", nvmeSpec{sanicap: 1, behaviour: "noop"})
	n3 := h.addNVMe("nvme3", nvmeSpec{sanicap: 1, behaviour: "fail"})
	sda := &fakeATADisk{words: ataWords("MOCK SATA SSD", "SATA0001", "FW1", true, false), behaviour: "ok"}
	sdb := &fakeATADisk{words: ataWords("MOCK SATA SSD", "SATA0002", "FW1", false, false), behaviour: "ok"}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: sda})
	h.addSCSI("sdb", scsiSpec{vendor: "ATA", driver: "ahci", ata: sdb})
	h.addSCSI("sdc", scsiSpec{vendor: "ATA", driver: "ahci", rotational: 1})
	h.addSCSI("sdd", scsiSpec{vendor: "DELL", model: "PERC H755 Front", driver: "megaraid_sas"})
	h.addSCSI("sde", scsiSpec{vendor: "Linux", driver: "usb-storage", removable: 1, usb: true})
	h.addSCSI("sdf", scsiSpec{vendor: "ATA", driver: "ahci", ata: &fakeATADisk{words: ataWords("M", "S", "F", true, false)}})
	h.mount("sdf")
	o := h.options(ModeErase)
	o.JobID = "T1"
	rep := h.run(o)

	if rep.ExitCode() != 1 || rep.Result != "FAIL" {
		t.Fatalf("exit %d result %s", rep.ExitCode(), rep.Result)
	}
	d0 := drive(t, rep, "nvme0")
	if d0.Result != Pass || n0.called("sanitize 4 ause=true") != 1 {
		t.Fatalf("nvme0: %s %s calls=%v", d0.Result, d0.Reason, n0.calls)
	}
	if d0.Verification.Changed != 16 || d0.Verification.ZeroAfterErase != 16 || d0.DeviceStatus.SSTAT != "0x0101" || !*d0.DeviceStatus.GlobalDataErased {
		t.Errorf("nvme0 evidence: %+v %+v", d0.Verification, d0.DeviceStatus)
	}
	if !strings.Contains(d0.TechniqueDetail, "Crypto Erase") || d0.NISTMethod != "Purge" || d0.Planned != "" {
		t.Errorf("nvme0 technique: %q %q planned=%q", d0.TechniqueDetail, d0.NISTMethod, d0.Planned)
	}
	d1 := drive(t, rep, "nvme1")
	if d1.Result != Fail || !strings.Contains(d1.Reason, "--allow-format") || n1.called("format") != 0 {
		t.Errorf("nvme1: %s %s", d1.Result, d1.Reason)
	}
	if d := drive(t, rep, "nvme2"); d.Result != Fail || !strings.Contains(d.Reason, "16 of 16 markers unchanged") {
		t.Errorf("nvme2 (firmware kept data): %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "nvme3"); d.Result != Fail || !strings.Contains(d.Reason, "status 3") || !n3.exitFail {
		t.Errorf("nvme3: %s %s exitFail=%v", d.Result, d.Reason, n3.exitFail)
	}
	if d := drive(t, rep, "sda"); d.Result != Pass || d.Verification.Changed != 16 || d.Verification.ZeroAfterErase != 0 {
		t.Errorf("sda: %s %s %+v", d.Result, d.Reason, d.Verification)
	}
	if d := drive(t, rep, "sdb"); d.Result != Fail || len(sdb.calls) != 0 {
		t.Errorf("sdb must fail without any sanitize call: %s %v", d.Result, sdb.calls)
	}
	if d := drive(t, rep, "sdc"); d.Result != Unhandled || !strings.Contains(d.Reason, "HDD") {
		t.Errorf("sdc: %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "sdd"); d.Result != Unhandled || d.Attach.Driver != "megaraid_sas" {
		t.Errorf("sdd: %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "sde"); d.Result != Skipped {
		t.Errorf("sde: %s", d.Result)
	}
	if d := drive(t, rep, "sdf"); d.Result != Unhandled || !strings.Contains(d.Reason, "in use") {
		t.Errorf("sdf: %s %s", d.Result, d.Reason)
	}
	if rep.Host.Serial != "TAG1234" || rep.JobID != "T1" || rep.Tool.ATABackend == "" {
		t.Errorf("header: %+v %+v", rep.Host, rep.Tool)
	}
	if rep.Counts[Pass] != 2 || rep.Counts[Fail] != 4 || rep.Counts[Unhandled] != 3 || rep.Counts[Skipped] != 1 {
		t.Errorf("counts %v", rep.Counts)
	}
}

func TestCleanHostPasses(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{sanicap: 7, fna: 4})
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: &fakeATADisk{words: ataWords("M", "S", "F", true, false)}})
	rep := h.run(h.options(ModeErase))
	if rep.ExitCode() != 0 || rep.Counts[Pass] != 2 {
		t.Fatalf("exit %d counts %v", rep.ExitCode(), rep.Counts)
	}
}

func TestFormatKeepsCurrentFormat(t *testing.T) {
	h := newHost(t)
	n := h.addNVMe("nvme1", nvmeSpec{sanicap: 2, fna: 4})
	o := h.options(ModeErase)
	o.AllowFormat = true
	rep := h.run(o)
	d := drive(t, rep, "nvme1")
	if d.Result != Pass {
		t.Fatalf("%s %s", d.Result, d.Reason)
	}
	// FLBAS 0x22 -> LBAF 0x12, DPS 1 -> PI type 1; SES=2
	want := nvme.FormatSpec{LBAF: 0x12, PI: 1, SES: nvme.SESCryptoErase}
	if len(n.formats) != 1 || n.formats[0] != want {
		t.Fatalf("format spec %+v, want %+v", n.formats, want)
	}
	if d.DeviceStatus.Format[0].CDW10 != "0x1422" || d.DeviceStatus.Format[0].NSID != 1 {
		t.Errorf("format evidence %+v", d.DeviceStatus.Format)
	}
	if n.called("sanitize ") != 0 {
		t.Errorf("sanitize must not be used: %v", n.calls)
	}
}

func TestInventoryWritesNothing(t *testing.T) {
	h := newHost(t)
	n0 := h.addNVMe("nvme0", nvmeSpec{sanicap: 7})
	h.addNVMe("nvme1", nvmeSpec{sanicap: 2})
	sda := &fakeATADisk{words: ataWords("M", "S", "F", true, false)}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: sda})
	before0, beforeA := h.sum("nvme0n1"), h.sum("sda")
	rep := h.run(h.options(ModeInventory))
	if rep.ExitCode() != 1 {
		t.Errorf("exit %d (nvme1 has no method)", rep.ExitCode())
	}
	if d := drive(t, rep, "nvme0"); d.Result != Planned || d.Planned != "nvme-sanitize-crypto-erase" {
		t.Errorf("nvme0 %s %q", d.Result, d.Planned)
	}
	if d := drive(t, rep, "sda"); d.Result != Planned || d.Planned != "ata-sanitize-crypto-scramble" {
		t.Errorf("sda %s %q", d.Result, d.Planned)
	}
	if h.sum("nvme0n1") != before0 || h.sum("sda") != beforeA {
		t.Error("inventory modified a drive")
	}
	// SANITIZE STATUS EXT is read-only and is the only ATA command allowed.
	if n0.called("sanitize ") != 0 || slices.ContainsFunc(sda.calls, func(c string) bool { return c != "status" }) {
		t.Errorf("erase commands issued: %v %v", n0.calls, sda.calls)
	}
}

func TestEraseRequiresConfirm(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	o := h.options(ModeErase)
	o.Confirm = false
	if _, err := Run(context.Background(), o); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("err %v", err)
	}
}

func TestIncompleteExitCode(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	h.addSCSI("sdc", scsiSpec{vendor: "ATA", driver: "ahci", rotational: 1})
	rep := h.run(h.options(ModeErase))
	if rep.ExitCode() != 2 || rep.Result != "INCOMPLETE" {
		t.Fatalf("exit %d %s", rep.ExitCode(), rep.Result)
	}
}

func TestExclude(t *testing.T) {
	h := newHost(t)
	n := h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	o := h.options(ModeErase)
	o.Exclude = []string{"/dev/nvme0"}
	rep := h.run(o)
	if d := drive(t, rep, "nvme0"); d.Result != Unhandled || !strings.Contains(d.Reason, "excluded") || len(n.calls) != 0 {
		t.Fatalf("%s %s %v", d.Result, d.Reason, n.calls)
	}
}

func TestSanitizeStall(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{sanicap: 1, behaviour: "stall"})
	rep := h.run(h.options(ModeErase))
	if d := drive(t, rep, "nvme0"); d.Result != Fail || !strings.Contains(d.Reason, "stalled") {
		t.Fatalf("%s %s", d.Result, d.Reason)
	}
}

func TestSanitizeRejected(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{sanicap: 1, behaviour: "reject"})
	rep := h.run(h.options(ModeErase))
	if d := drive(t, rep, "nvme0"); d.Result != Fail || !strings.Contains(d.Reason, "Invalid Field in Command") {
		t.Fatalf("%s %s", d.Result, d.Reason)
	}
}

func TestATAEdgeCases(t *testing.T) {
	h := newHost(t)
	locked := &fakeATADisk{words: ataWords("M", "LOCKED", "F", true, true)}
	rejected := &fakeATADisk{words: ataWords("M", "REJECT", "F", true, false), behaviour: "reject"}
	frozen := &fakeATADisk{words: ataWords("M", "FROZEN", "F", true, false), frozen: true}
	lying := &fakeATADisk{words: ataWords("M", "NOOP", "F", true, false), behaviour: "noop"}
	h.addSCSI("sda", scsiSpec{vendor: "ATA", driver: "ahci", ata: locked})
	h.addSCSI("sdb", scsiSpec{vendor: "ATA", driver: "ahci", ata: rejected})
	h.addSCSI("sdc", scsiSpec{vendor: "ATA", driver: "megaraid_sas"}) // IDENTIFY not passed through
	h.addSCSI("sdd", scsiSpec{vendor: "ATA", driver: "ahci", ata: frozen})
	h.addSCSI("sde", scsiSpec{vendor: "ATA", driver: "ahci", ata: lying})
	rep := h.run(h.options(ModeErase))
	if d := drive(t, rep, "sda"); d.Result != Fail || !strings.Contains(d.Reason, "locked") || slices.Contains(locked.calls, "crypto-scramble") {
		t.Errorf("locked: %s %s %v", d.Result, d.Reason, locked.calls)
	}
	if d := drive(t, rep, "sdb"); d.Result != Fail || !strings.Contains(d.Reason, "FROZEN") {
		t.Errorf("rejected: %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "sdc"); d.Result != Unhandled || !strings.Contains(d.Reason, "megaraid_sas") {
		t.Errorf("no identify: %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "sdd"); d.Result != Fail || !strings.Contains(d.Reason, "frozen") {
		t.Errorf("frozen: %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "sde"); d.Result != Fail || !strings.Contains(d.Reason, "unchanged") {
		t.Errorf("lying firmware: %s %s", d.Result, d.Reason)
	}
}

func TestNoDrives(t *testing.T) {
	h := newHost(t)
	rep := h.run(h.options(ModeErase))
	if rep.ExitCode() != 1 || len(rep.Drives) != 0 {
		t.Fatalf("exit %d drives %d", rep.ExitCode(), len(rep.Drives))
	}
}

func TestNVMe12FormatOnly(t *testing.T) {
	// PM983 / P4610 class: NVMe 1.2, no Sanitize, Format crypto erase only.
	for _, allow := range []bool{false, true} {
		h := newHost(t)
		a := h.addNVMe("nvme0", nvmeSpec{sanicap: 0, fna: 4, model: "SAMSUNG MZQLB960HAJR-00007", fw: "EDA7202Q"})
		b := h.addNVMe("nvme1", nvmeSpec{sanicap: 0, fna: 4, model: "Dell Express Flash NVMe P4610 6.4TB SFF", fw: "VDV1DP25"})
		o := h.options(ModeErase)
		o.AllowFormat = allow
		rep := h.run(o)
		if !allow {
			if rep.ExitCode() != 1 || !strings.Contains(drive(t, rep, "nvme0").Reason, "--allow-format") {
				t.Fatalf("default: exit %d %s", rep.ExitCode(), drive(t, rep, "nvme0").Reason)
			}
			continue
		}
		if rep.ExitCode() != 0 {
			t.Fatalf("allow-format: exit %d %v", rep.ExitCode(), rep.Drives[0].Reason)
		}
		if a.called("sanitize-log") != 0 || b.called("sanitize-log") != 0 {
			t.Error("sanitize log queried on a controller without sanitize support")
		}
		if fp := drive(t, rep, "nvme1").FirmwarePolicy; fp.Status != "pass" {
			t.Errorf("P4610 firmware policy %+v", fp)
		}
		if fp := drive(t, rep, "nvme0").FirmwarePolicy; fp.Status != "no_rule" {
			t.Errorf("PM983 firmware policy %+v", fp)
		}
	}
}

func TestFirmwareFloor(t *testing.T) {
	h := newHost(t)
	a := h.addNVMe("nvme0", nvmeSpec{fna: 4, model: "Dell Express Flash NVMe P4610 6.4TB SFF", fw: "VDV1DP23"})
	b := h.addNVMe("nvme1", nvmeSpec{fna: 4, model: "INTEL SSDPE2KE064T8", fw: "VDV10170"})
	h.addNVMe("nvme2", nvmeSpec{fna: 4, model: "INTEL SSDPE2KE064T8", fw: "VDV10184"})
	o := h.options(ModeErase)
	o.AllowFormat = true
	rep := h.run(o)
	if d := drive(t, rep, "nvme0"); d.Result != Fail || !strings.Contains(d.Reason, "VDV1DP25") || a.called("format") != 0 {
		t.Errorf("Dell line: %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "nvme1"); d.Result != Fail || !strings.Contains(d.Reason, "VDV10184") || b.called("format") != 0 {
		t.Errorf("Intel line: %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "nvme2"); d.Result != Pass {
		t.Errorf("Intel fixed: %s %s", d.Result, d.Reason)
	}

	rules, err := ParseFirmwarePolicy("P4610 ; ^VDV1DP ; VDV1DP20 ; test override")
	if err != nil {
		t.Fatal(err)
	}
	o = h.options(ModeInventory)
	o.AllowFormat = true
	o.FirmwarePolicy = rules
	if d := drive(t, h.run(o), "nvme0"); d.Result != Planned {
		t.Errorf("override: %s %s", d.Result, d.Reason)
	}
}

func TestUnallocatedCapacity(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{fna: 4, unvmcap: 1 << 30})
	h.addNVMe("nvme1", nvmeSpec{sanicap: 1, fna: 4, unvmcap: 1 << 30})
	o := h.options(ModeErase)
	o.AllowFormat = true
	rep := h.run(o)
	if d := drive(t, rep, "nvme0"); d.Result != Fail || !strings.Contains(d.Reason, "unallocated") {
		t.Errorf("format path: %s %s", d.Result, d.Reason)
	}
	if d := drive(t, rep, "nvme1"); d.Result != Pass {
		t.Errorf("sanitize path: %s %s", d.Result, d.Reason)
	}
}

func TestPERCVirtualDisk(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{sanicap: 7, model: "Dell Ent NVMe v2 AGN MU U.2 6.4TB", fw: "2.3.0"})
	h.addSCSI("sda", scsiSpec{vendor: "DELL", model: "PERC H355 Front", driver: "megaraid_sas"})
	h.addSCSI("sdb", scsiSpec{vendor: "DELL", model: "PERC H355 Front", driver: "megaraid_sas"})
	h.perc.drives = []perc.Drive{
		{Controller: new(0), Slot: "64:0", SED: "N", Model: "SAMSUNG MZ7LH960HAJR-00005", Interface: "SATA", Media: "SSD", Tool: "perccli64"},
		{Controller: new(0), Slot: "64:1", SED: "Y", Model: "SED DRIVE", Interface: "SATA", Media: "SSD", Tool: "perccli64"},
	}
	rep := h.run(h.options(ModeErase))
	if rep.ExitCode() != 2 {
		t.Fatalf("exit %d", rep.ExitCode())
	}
	d := drive(t, rep, "sda")
	if d.Result != Unhandled || len(d.PERC.PhysicalDrives) != 2 || !strings.Contains(d.Reason, "2 physical drive(s) behind the controller, 1 SED") {
		t.Errorf("perc: %s %s", d.Result, d.Reason)
	}
	if h.perc.calls.Load() != 1 {
		t.Errorf("perc CLI called %d times, want 1", h.perc.calls.Load())
	}

	h2 := newHost(t)
	h2.addSCSI("sda", scsiSpec{vendor: "DELL", model: "PERC H355 Front", driver: "megaraid_sas"})
	if d := drive(t, h2.run(h2.options(ModeErase)), "sda"); !strings.Contains(d.Reason, "perccli64") {
		t.Errorf("no CLI hint: %s", d.Reason)
	}
}

func TestMultipathNamespaceMapping(t *testing.T) {
	h := newHost(t)
	n := h.addNVMe("nvme3", nvmeSpec{sanicap: 1, multipath: "nvme5n1"})
	rep := h.run(h.options(ModeErase))
	d := drive(t, rep, "nvme3")
	if d.Result != Pass || len(d.Namespaces) != 1 || !strings.HasSuffix(d.Namespaces[0], "nvme5n1") || n.called("sanitize 4") != 1 {
		t.Fatalf("%s %s %v", d.Result, d.Reason, d.Namespaces)
	}
	if len(rep.Drives) != 1 {
		t.Errorf("namespace head reported separately: %d drives", len(rep.Drives))
	}
}

func TestParallelismBound(t *testing.T) {
	h := newHost(t)
	for i := range 8 {
		h.addNVMe("nvme"+string(rune('0'+i)), nvmeSpec{sanicap: 1})
	}
	o := h.options(ModeErase)
	o.Parallel = 3
	rep := h.run(o)
	if rep.Counts[Pass] != 8 {
		t.Fatalf("counts %v", rep.Counts)
	}
	if p := h.peak.Load(); p > 3 || p < 2 {
		t.Errorf("peak concurrently open controllers %d, want 2..3", p)
	}
	for i, d := range rep.Drives {
		if !strings.HasSuffix(d.Device, "nvme"+string(rune('0'+i))) {
			t.Errorf("report order: %d %s", i, d.Device)
		}
	}
}

func TestCancelledBeforeErase(t *testing.T) {
	h := newHost(t)
	n := h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	before := h.sum("nvme0n1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rep, err := Run(ctx, h.options(ModeErase))
	if err != nil {
		t.Fatal(err)
	}
	d := drive(t, rep, "nvme0")
	if d.Result != Fail || !strings.Contains(d.Reason, "interrupted") || h.sum("nvme0n1") != before || n.called("sanitize 4") != 0 {
		t.Fatalf("%s %s calls=%v", d.Result, d.Reason, n.calls)
	}
}

func TestTCGLevel0(t *testing.T) {
	h := newHost(t)
	h.addNVMe("nvme0", nvmeSpec{sanicap: 1, oacs: 1, level0: level0Locking(0x0b)}) // supported, enabled, media encryption
	h.addNVMe("nvme1", nvmeSpec{sanicap: 1, oacs: 1, level0: level0Locking(0x0f)}) // locked
	rep := h.run(h.options(ModeErase))
	d0 := drive(t, rep, "nvme0")
	if d0.Result != Pass || d0.TCG == nil || d0.TCG.Level0 == nil || !d0.TCG.MediaEncryption || d0.TCG.SSC[0] != "Opal 2" {
		t.Errorf("nvme0: %s %+v", d0.Result, d0.TCG)
	}
	if d := drive(t, rep, "nvme1"); d.Result != Fail || !strings.Contains(d.Reason, "PSID") {
		t.Errorf("nvme1: %s %s", d.Result, d.Reason)
	}
}

func TestNamespaceInUse(t *testing.T) {
	h := newHost(t)
	n := h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	// nvme0n1 partition mounted: build a partition under the namespace.
	write(t, h.sys+"/block/nvme0n1/nvme0n1p1/partition", "1")
	if err := symlink(h.sys+"/block/nvme0n1/nvme0n1p1", h.sys+"/class/block/nvme0n1p1"); err != nil {
		t.Fatal(err)
	}
	appendLine(t, h.proc+"/mounts", "/dev/nvme0n1p1 / ext4 rw 0 0")
	rep := h.run(h.options(ModeErase))
	if d := drive(t, rep, "nvme0"); d.Result != Unhandled || !strings.Contains(d.Reason, "in use") || len(n.calls) != 0 {
		t.Fatalf("%s %s %v", d.Result, d.Reason, n.calls)
	}
}

func TestPollingRespectsTimeouts(t *testing.T) {
	// A sanitize that keeps progressing must not be cut off by NoProgressTimeout.
	h := newHost(t)
	n := h.addNVMe("nvme0", nvmeSpec{sanicap: 1})
	o := h.options(ModeErase)
	o.NoProgressTimeout = 20 * time.Millisecond
	o.PollInterval = 10 * time.Millisecond
	_ = n
	if d := drive(t, h.run(o), "nvme0"); d.Result != Pass {
		t.Fatalf("%s %s", d.Result, d.Reason)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"VDV1DP23", "VDV1DP25", -1},
		{"VDV10184", "VDV10170", 1},
		{"VDV10184", "VDV10184", 0},
		{"nvme2", "nvme10", -1},
		{"2.3.0", "2.10.0", -1},
		{"1.0", "1.0.1", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCheckFirmware(t *testing.T) {
	rules := DefaultFirmwarePolicy()
	cases := []struct{ model, fw, want string }{
		{"Dell Express Flash NVMe P4610 6.4TB SFF", "VDV1DP24", "fail"},
		{"Dell Express Flash NVMe P4610 6.4TB SFF", "VDV1DP25", "pass"},
		{"INTEL SSDPE2KE064T8", "VDV10182", "fail"},
		{"INTEL SSDPE2KE064T8", "VDV10190", "pass"},
		{"SAMSUNG MZ7LH960HAJR-00005", "HXT7404Q", "no_rule"},
	}
	for _, c := range cases {
		if got := CheckFirmware(rules, c.model, c.fw).Status; got != c.want {
			t.Errorf("%s %s: %s, want %s", c.model, c.fw, got, c.want)
		}
	}
	if _, err := ParseFirmwarePolicy("bad line"); err == nil {
		t.Error("expected parse error")
	}
}

func TestSampleOffsets(t *testing.T) {
	offs := sampleOffsets(64<<20, 16)
	if len(offs) != 16 || offs[0] != 0 || offs[15] != 63<<20 {
		t.Fatalf("%v", offs)
	}
	if got := sampleOffsets(3<<20, 16); len(got) != 3 {
		t.Errorf("small device: %v", got)
	}
	if got := sampleOffsets(1000, 16); got != nil {
		t.Errorf("tiny device: %v", got)
	}
}
